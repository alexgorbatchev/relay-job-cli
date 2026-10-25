package job

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (w *Worker) cleanup(ctx context.Context, now time.Time) (cleanupResult, error) {
	var result cleanupResult
	records, err := w.Store.List()
	if err != nil {
		return result, err
	}
	var failures cleanupErrors
	for _, r := range records {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if !r.Terminal() {
			if _, err = w.Status(ctx, r.ID); err != nil {
				failures.add(fmt.Errorf("reconcile %s: %w", r.ID, err))
			}
			continue
		}
		if !r.LogsCollected || !expired(r.Updated, now, w.Retention.Logs) {
			continue
		}
		guard, lockErr := w.Store.WorkspaceLock(r.Source)
		if lockBusy(lockErr) {
			continue
		}
		if lockErr != nil {
			failures.add(lockErr)
			continue
		}
		removed, pruneErr := w.Store.expireLogs(r.ID, now, w.Retention.Logs, true)
		guard.Close()
		if pruneErr != nil {
			failures.add(fmt.Errorf("expire %s: %w", r.ID, pruneErr))
		}
		if removed {
			result.logs++
		}
	}
	if !w.Local && w.Retention.Workspaces > 0 {
		var workspaceErr error
		result.workspaces, workspaceErr = w.cleanWorkspaces(ctx, now)
		failures.add(workspaceErr)
	}
	return result, failures.err()
}

type workspaceUse struct {
	source string
	last   time.Time
	active bool
}

func (w *Worker) detachWorkspaces(ctx context.Context, root *os.Root, now time.Time) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	// Refresh under the same mutex as Prepare. Neither a reservation nor a held
	// native execution/rsync lock may be superseded by a stale cleanup snapshot.
	records, err := w.Store.List()
	if err != nil {
		return err
	}
	used := map[string]workspaceUse{}
	for _, r := range records {
		if err = r.Request.Validate(); err != nil {
			return fmt.Errorf("workspace receipt %s: %w", r.ID, err)
		}
		if r.Workspace != w.workspace(r.Request) {
			return fmt.Errorf("workspace receipt %s has an unexpected path", r.ID)
		}
		key := WorkspaceKey(r.Source)
		u := used[key]
		u.source = r.Source
		u.active = u.active || !r.Terminal()
		if r.Updated.After(u.last) {
			u.last = r.Updated
		}
		used[key] = u
	}
	for key, u := range used {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if u.active || !expired(u.last, now, w.Retention.Workspaces) {
			continue
		}
		guard, err := w.Store.WorkspaceLock(u.source)
		if lockBusy(err) {
			continue
		}
		if err != nil {
			return err
		}
		err = detachWorkspace(root, key)
		guard.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func detachWorkspace(root *os.Root, key string) error {
	info, err := root.Lstat(key)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("refuse cleanup of non-directory workspace %s", key)
	}
	// Rename is short and runs under the reservation mutex and native lock.
	// Recursive deletion runs afterwards, so large caches don't stall the API.
	return root.Rename(key, key+".gc-"+NewID())
}

func (w *Worker) cleanWorkspaces(ctx context.Context, now time.Time) (int, error) {
	root, err := os.OpenRoot(filepath.Join(w.Cache, "workspaces"))
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer root.Close()
	if err = w.detachWorkspaces(ctx, root, now); err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(root.Name())
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if ctx.Err() != nil {
			return removed, ctx.Err()
		}
		key, id, ok := strings.Cut(e.Name(), ".gc-")
		if !ok || !validID.MatchString(key) || !validID.MatchString(id) || !e.IsDir() {
			continue
		}
		// Root anchors recursive removal, including symlinks inside the cache.
		// Leftover detached trees from a crash are safe to finish next startup.
		if err = root.RemoveAll(e.Name()); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
