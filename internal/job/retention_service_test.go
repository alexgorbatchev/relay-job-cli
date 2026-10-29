package job

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetentionLiveExecution(t *testing.T) {
	nativeManager(t)
	w := worker(t, 1)
	w.Retention = DefaultRetention()
	req := request(t, "/bin/sh", "-c", "printf live; sleep 30")
	r := start(t, w, req)
	wait(t, func() (Record, error) { return w.Status(context.Background(), r.ID) }, stateRunning)
	future := time.Now().Add(40 * 24 * time.Hour)
	if _, err := w.cleanup(context.Background(), future); err != nil {
		t.Fatal(err)
	}
	exists(t, r.Workspace, true)
	exists(t, w.Store.Path(r.ID, "started"), true)
	if _, err := w.collectLogs(r.ID); err == nil {
		t.Fatal("active command acknowledged")
	}
	if _, err := w.Cancel(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.collectLogs(r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cleanup(context.Background(), future); err != nil {
		t.Fatal(err)
	}
	exists(t, r.Workspace, false)
	exists(t, w.Store.Path(r.ID, "started"), true)
	if err := Execute(context.Background(), w.Store, r.ID); err != nil {
		t.Fatal(err)
	}
	got, err := w.Store.Get(r.ID)
	if err != nil || got.State != stateCanceled || !got.LogsExpired {
		t.Fatal(got, err)
	}
}

func TestRetentionAutomaticServices(t *testing.T) {
	for _, role := range []string{"worker", "coordinator"} {
		t.Run(role, func(t *testing.T) {
			w := worker(t, 1)
			w.Retention = Retention{Logs: time.Hour, Workspaces: time.Hour, Interval: 10 * time.Millisecond}
			old := retainedJob(t, w, stateSucceeded, 2*time.Hour)
			c := NewCoordinator(Paths{State: t.TempDir()}, Config{}, testBinary)
			defer c.Close()
			c.Retention = w.Retention
			store := w.Store
			if role == "coordinator" {
				old.Worker = ""
				if err := WriteJSON(c.Store.Path(old.ID, "record.json"), old); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(c.Store.Path(old.ID, "stdout"), []byte("coordinator"), 0600); err != nil {
					t.Fatal(err)
				}
				store = c.Store
			}
			dir, err := os.MkdirTemp("", "rjg-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "api")
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				if role == "coordinator" {
					done <- ServeCoordinator(ctx, socket, c)
				} else {
					done <- ServeWorker(ctx, socket, w)
				}
			}()
			t.Cleanup(func() {
				cancel()
				if err := <-done; err != nil {
					t.Error(err)
				}
			})
			waitFor(t, func() bool { r, err := store.Get(old.ID); return err == nil && r.LogsExpired })
			// A job added after startup must expire through a later timer tick.
			later := retainedJob(t, w, stateSucceeded, 2*time.Hour)
			if role == "coordinator" {
				later.Worker = ""
				if err := WriteJSON(store.Path(later.ID, "record.json"), later); err != nil {
					t.Fatal(err)
				}
			}
			waitFor(t, func() bool { r, err := store.Get(later.ID); return err == nil && r.LogsExpired })
			rpc := NewRPC(socket)
			defer rpc.Close()
			for _, log := range []func() error{
				func() error { _, err := rpc.LogSize(ctx, old.ID, "stdout"); return err },
				func() error { _, err := rpc.Log(ctx, old.ID, "stdout", 0, io.Discard); return err },
			} {
				if err := log(); err == nil || !strings.Contains(err.Error(), "expired") {
					t.Fatal(err)
				}
			}
		})
	}
}

func waitFor(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("automatic cleanup did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRetentionAcknowledgmentRetries(t *testing.T) {
	w := worker(t, 1)
	r := retainedJob(t, w, stateSucceeded, time.Hour)
	r.LogsCollected = false
	if err := WriteJSON(w.Store.Path(r.ID, "record.json"), r); err != nil {
		t.Fatal(err)
	}
	c := NewCoordinator(Paths{State: t.TempDir()}, Config{}, testBinary)
	defer c.Close()
	r.Worker = "missing"
	if err := c.Store.Put(r); err != nil {
		t.Fatal(err)
	}
	if _, err := c.cleanup(context.Background(), time.Now()); err == nil {
		t.Fatal("ignored missing worker acknowledgment")
	}
	c.Workers = []*Transport{{Config: WorkerConfig{Name: "missing", Socket: endpoint(t, w.Handler())}}}
	if _, err := c.cleanup(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err := w.Store.Get(r.ID)
	if err != nil || !got.LogsCollected {
		t.Fatal(got, err)
	}
	// The local fallback's store is acknowledged and pruned without deleting source.
	local := retainedJob(t, c.Local, stateSucceeded, 40*24*time.Hour)
	local.Worker, local.LogsCollected = "local", false
	if err := WriteJSON(c.Store.Path(local.ID, "record.json"), local); err != nil {
		t.Fatal(err)
	}
	if _, err := c.cleanup(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	exists(t, local.Source, true)
	exists(t, c.Local.Store.Path(local.ID, "stdout"), false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.cleanup(ctx, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRetentionAcknowledgmentFailurePreservesResult(t *testing.T) {
	w := worker(t, 1)
	r := retainedJob(t, w, stateSucceeded, time.Hour)
	r.LogsCollected = false
	if err := WriteJSON(w.Store.Path(r.ID, "record.json"), r); err != nil {
		t.Fatal(err)
	}
	var unavailable atomic.Bool
	unavailable.Store(true)
	handler := w.Handler()
	socket := endpoint(t, http.HandlerFunc(func(out http.ResponseWriter, req *http.Request) {
		if unavailable.Load() && strings.HasSuffix(req.URL.Path, "/collected") {
			http.Error(out, "collection acknowledgment unavailable", http.StatusServiceUnavailable)
			return
		}
		handler.ServeHTTP(out, req)
	}))
	c := NewCoordinator(Paths{State: t.TempDir()}, Config{Workers: []WorkerConfig{{Name: "worker", Socket: socket}}}, testBinary)
	defer c.Close()
	r.State, r.Worker, r.Submitted = stateRunning, "worker", true
	if err := c.Store.Put(r); err != nil {
		t.Fatal(err)
	}
	if err := c.run(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	got, err := c.Store.Get(r.ID)
	if err != nil || got.State != stateSucceeded || got.ExitCode != 0 || got.Error != "" || got.LogsCollected {
		t.Fatal(got, err)
	}
	updated := got.Updated
	unavailable.Store(false)
	if _, err := c.cleanup(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err = c.Store.Get(r.ID)
	if err != nil || !got.LogsCollected || !got.Updated.Equal(updated) {
		t.Fatal(got, err)
	}
}
