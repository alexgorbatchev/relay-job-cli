package job

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"syscall"
	"time"
)

// Acknowledgment is only sent after the coordinator's terminal receipt and both
// output streams are durable. An offline coordinator must never lose worker logs.
func (w *Worker) collectLogs(id string) (Record, error) {
	if !validID.MatchString(id) {
		return Record{}, &APIError{400, "invalid job ID"}
	}
	lock, err := recordLock(w.Store.Path(id, "job.lock"))
	if err != nil {
		return Record{}, err
	}
	defer lock.Close()
	r, err := w.Store.Get(id)
	if err != nil {
		return r, err
	}
	if !r.Terminal() {
		return r, &APIError{409, "job is still active"}
	}
	if !r.LogsCollected {
		r.LogsCollected = true
		err = WriteJSON(w.Store.Path(id, "record.json"), r)
	}
	return r, err
}

func (c *Coordinator) acknowledgeLogs(ctx context.Context, r Record) error {
	if r.LogsCollected || r.Worker == "" {
		return nil
	}
	var err error
	if r.Worker == "local" {
		_, err = c.Local.collectLogs(r.ID)
	} else if w := c.worker(r.Worker); w != nil {
		err = w.Call(ctx, http.MethodPost, "/jobs/"+r.ID+"/collected", nil, nil)
	} else {
		err = fmt.Errorf("assigned worker %q is absent from configuration", r.Worker)
	}
	if err != nil {
		return err
	}
	lock, err := recordLock(c.Store.Path(r.ID, "job.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()
	r, err = c.Store.Get(r.ID)
	if err == nil {
		r.LogsCollected = true
		err = WriteJSON(c.Store.Path(r.ID, "record.json"), r)
	}
	return err
}

func lockBusy(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

func (s Store) expireLogs(id string, now time.Time, age time.Duration, requireCollected bool) (bool, error) {
	if age <= 0 {
		return false, nil
	}
	lock, err := Lock(s.Path(id, "job.lock"))
	if lockBusy(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer lock.Close()
	r, err := s.Get(id)
	if err != nil {
		return false, err
	}
	if !r.Terminal() || (requireCollected && !r.LogsCollected) || !expired(r.Updated, now, age) {
		return false, nil
	}
	// Persist the tombstone first. Interrupted deletion is retried; a missing file
	// is never misreported as an empty successful log. Preserve job timestamps.
	changed := !r.LogsExpired
	if changed {
		r.LogsExpired = true
		if err = WriteJSON(s.Path(id, "record.json"), r); err != nil {
			return false, err
		}
	}
	for _, name := range []string{"stdout", "stderr"} {
		if err = os.Remove(s.Path(id, name)); err != nil && !os.IsNotExist(err) {
			return false, err
		}
	}
	return changed, nil
}

func (c *Coordinator) cleanup(ctx context.Context, now time.Time) (cleanupResult, error) {
	var result cleanupResult
	records, err := c.Store.List()
	if err != nil {
		return result, err
	}
	var failures cleanupErrors
	for _, r := range records {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if !r.Terminal() {
			continue
		}
		if err = c.acknowledgeLogs(ctx, r); err != nil {
			failures.add(fmt.Errorf("acknowledge %s: %w", r.ID, err))
		}
		removed, pruneErr := c.Store.expireLogs(r.ID, now, c.Retention.Logs, false)
		if pruneErr != nil {
			failures.add(fmt.Errorf("expire %s: %w", r.ID, pruneErr))
		}
		if removed {
			result.logs++
		}
	}
	local, err := c.Local.cleanup(ctx, now)
	result.logs += local.logs
	failures.add(err)
	return result, failures.err()
}
