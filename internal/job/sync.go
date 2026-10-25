package job

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"time"
)

const leaseRefresh = 30 * time.Second

func syncWorkspace(ctx context.Context, t *Transport, r Record, refresh time.Duration) error {
	renew := func() error {
		var rec Record
		if err := t.Call(ctx, "POST", "/jobs", r.Request, &rec); err != nil {
			return fmt.Errorf("renew source-sync reservation: %w", err)
		}
		if rec.State != statePrepared {
			return fmt.Errorf("source-sync reservation is %s", rec.State)
		}
		return nil
	}
	if err := renew(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(refresh)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				done <- nil
				return
			case <-ticker.C:
				if err := renew(); err != nil {
					if ctx.Err() != nil {
						done <- nil
						return
					}
					done <- err
					cancel()
					return
				}
			}
		}
	}()
	err := t.Sync(ctx, r)
	cancel()
	leaseErr := <-done
	if leaseErr != nil {
		return leaseErr
	}
	return err
}

// SyncReceive binds the rsync receiver to its reserved workspace, holding a native
// file lock through the entire transfer even if the API reservation expires.
func syncGuard(s Store, id string) (*os.File, Record, error) {
	r, err := s.Get(id)
	if err != nil {
		return nil, r, err
	}
	guard, err := s.WorkspaceLock(r.Source)
	if err != nil {
		return nil, r, err
	}
	meta, err := recordLock(s.Path(id, "job.lock"))
	if err != nil {
		guard.Close()
		return nil, r, err
	}
	r, err = s.Get(id)
	meta.Close()
	if err != nil {
		guard.Close()
		return nil, r, err
	}
	if r.State != statePrepared || time.Now().After(r.LeaseUntil) {
		guard.Close()
		return nil, r, fmt.Errorf("source-sync reservation is not active")
	}
	return guard, r, nil
}
func SyncReceive(ctx context.Context, s Store, id string, args []string, in io.Reader, out, stderr io.Writer) error {
	guard, r, err := syncGuard(s, id)
	if err != nil {
		return err
	}
	defer guard.Close()
	// With --protect-args, rsync sends destination paths inside its protocol.
	// The authenticated coordinator supplies that destination from this reservation.
	if len(args) < 2 || args[0] != "--server" || slices.Contains(args, "--sender") {
		return fmt.Errorf("reserved sync requires an rsync receiver")
	}
	cmd := exec.CommandContext(ctx, "rsync", args...)
	cmd.Stdin = in
	cmd.Dir = r.Workspace
	cmd.Stdout = out
	cmd.Stderr = stderr
	cmd.ExtraFiles = []*os.File{guard}
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("rsync receiver: %w", err)
	}
	return nil
}
