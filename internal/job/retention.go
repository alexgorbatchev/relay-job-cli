package job

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Retention bounds disposable data. Receipts and execution/lock files never expire.
type Retention struct {
	Logs, Workspaces, Interval time.Duration
}

func DefaultRetention() Retention {
	return Retention{Logs: 7 * 24 * time.Hour, Workspaces: 30 * 24 * time.Hour, Interval: time.Hour}
}

func (p Retention) Validate() error {
	if p.Logs < 0 || p.Workspaces < 0 || p.Interval <= 0 {
		return fmt.Errorf("retention periods must be nonnegative and cleanup interval must be positive")
	}
	return nil
}

type cleanupResult struct{ logs, workspaces int }

type cleanupErrors struct {
	count int
	first error
}

func (e *cleanupErrors) add(err error) {
	if err == nil {
		return
	}
	if e.first == nil {
		e.first = err
	}
	e.count++
}

func (e cleanupErrors) err() error {
	if e.count == 0 {
		return nil
	}
	return fmt.Errorf("%d cleanup failures; first: %w", e.count, e.first)
}

func ServeWorker(ctx context.Context, socket string, w *Worker) error {
	if err := w.Retention.Validate(); err != nil {
		return err
	}
	return serveMaintained(ctx, socket, w.Retention, w.Handler(), nil, w.cleanup)
}

func serveMaintained(ctx context.Context, socket string, policy Retention, handler http.Handler, start func() error, cleanup func(context.Context, time.Time) (cleanupResult, error)) error {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	return serve(ctx, socket, handler, func() error {
		if start != nil {
			if err := start(); err != nil {
				return err
			}
		}
		if policy.Interval > 0 {
			wg.Go(func() { maintain(ctx, policy.Interval, cleanup) })
		}
		return nil
	})
}

func maintain(ctx context.Context, interval time.Duration, cleanup func(context.Context, time.Time) (cleanupResult, error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		result, err := cleanup(ctx, time.Now().UTC())
		if err != nil && ctx.Err() == nil {
			slog.Error("retention cleanup", "error", err)
		}
		if result.logs > 0 || result.workspaces > 0 {
			slog.Info("retention cleanup", "logs", result.logs, "workspaces", result.workspaces)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func expired(updated, now time.Time, age time.Duration) bool {
	return age > 0 && !updated.IsZero() && !updated.After(now.Add(-age))
}
