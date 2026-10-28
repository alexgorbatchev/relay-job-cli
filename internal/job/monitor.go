package job

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

func (c *Coordinator) run(ctx context.Context, r Record) error {
	if r.State == stateSyncing {
		r.State = stateFailed
		r.ExitCode = 125
		r.Error = "coordinator interrupted source sync; command was not submitted"
		if w := c.worker(r.Worker); w != nil {
			if err := w.Call(ctx, "POST", "/jobs/"+r.ID+"/cancel", nil, nil); err != nil {
				r.Error += "; reservation expires automatically: " + err.Error()
			}
		}
		return c.Store.Put(r)
	}
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		switch r.State {
		case stateQueued:
			if err := c.assign(ctx, &r); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				r.State = stateFailed
				r.ExitCode = 125
				r.Error = err.Error()
				return c.Store.Put(r)
			}
		case stateCanceling:
			err := c.cancelWorker(ctx, r)
			if err == nil {
				r.State = stateCanceled
				r.ExitCode = 130
				r.Error = ""
				return c.Store.Put(r)
			}
			if !errors.Is(err, errLogsPending) {
				if err = c.saveError(&r, "cancellation unconfirmed: "+err.Error()); err != nil {
					return err
				}
			}
		default:
			done, err := c.observe(ctx, &r)
			if err != nil {
				if err = c.saveError(&r, err.Error()); err != nil {
					return err
				}
			}
			if done {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
	return nil
}

func (c *Coordinator) observe(ctx context.Context, r *Record) (bool, error) {
	rec, err := c.status(ctx, *r)
	if err != nil {
		return false, fmt.Errorf("worker status unavailable; job will not be replayed: %w", err)
	}
	if rec.State == statePrepared {
		if r.Worker == "local" {
			_, err = c.Local.Start(ctx, r.ID)
		} else {
			err = c.worker(r.Worker).Call(ctx, "POST", "/jobs/"+r.ID+"/start", nil, nil)
		}
		return false, err
	}
	if err = c.copyLogs(ctx, *r); errors.Is(err, errLogsPending) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("log retrieval: %w", err)
	}
	rec.Worker = r.Worker
	rec.Submitted = r.Submitted
	// The coordinator owns submission time; the worker creates its receipt later.
	rec.Created = r.Created
	changed := rec.State != r.State || rec.ExitCode != r.ExitCode || rec.Error != r.Error
	*r = rec
	if changed {
		if err = c.Store.Put(rec); err != nil {
			return false, err
		}
	}
	if rec.Terminal() {
		// Failure is retried by maintenance using the durable terminal receipt.
		// Output collection has already completed before any acknowledgment.
		if err = c.acknowledgeLogs(ctx, rec); err != nil {
			slog.Warn("output acknowledgment pending", "id", rec.ID, "error", err)
		}
	}
	return rec.Terminal(), nil
}
func (c *Coordinator) saveError(r *Record, message string) error {
	if r.Error == message {
		return nil
	}
	r.Error = message
	return c.Store.Put(*r)
}
