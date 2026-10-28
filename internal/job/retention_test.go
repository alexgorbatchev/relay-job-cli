package job

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func retainedJob(t *testing.T, w *Worker, state string, age time.Duration) Record {
	t.Helper()
	r := Record{Request: request(t, "true"), State: state, ExitCode: 0, LogsCollected: true}
	r.Workspace = w.workspace(r.Request)
	r.Created = time.Now().UTC().Add(-age)
	r.Updated = r.Created
	r.LeaseUntil = time.Now().Add(time.Hour)
	if err := os.MkdirAll(r.Workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(w.Store.Path(r.ID, "record.json"), r); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"stdout", "stderr", "started", "job.lock"} {
		if err := os.WriteFile(w.Store.Path(r.ID, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func exists(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Lstat(path)
	if (err == nil) != want || (err != nil && !os.IsNotExist(err)) {
		t.Fatalf("%s: exists=%v, want %v: %v", path, err == nil, want, err)
	}
}

func TestRetentionWorkerSafety(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		age                         time.Duration
		collected, locked, disabled bool
		wantLogs, wantWorkspace     bool
	}{
		{"expired", 40 * 24 * time.Hour, true, false, false, false, false},
		{"recent", time.Hour, true, false, false, true, true},
		{"uncollected", 40 * 24 * time.Hour, false, false, false, true, false},
		{"native lock", 40 * 24 * time.Hour, true, true, false, true, true},
		{"disabled", 40 * 24 * time.Hour, true, false, true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := worker(t, 1)
			w.Retention = DefaultRetention()
			if tc.disabled {
				w.Retention.Logs, w.Retention.Workspaces = 0, 0
			}
			r := retainedJob(t, w, stateSucceeded, tc.age)
			r.LogsCollected = tc.collected
			if err := WriteJSON(w.Store.Path(r.ID, "record.json"), r); err != nil {
				t.Fatal(err)
			}
			if tc.locked {
				guard, err := w.Store.WorkspaceLock(r.Source)
				if err != nil {
					t.Fatal(err)
				}
				defer guard.Close()
			}
			if _, err := w.cleanup(context.Background(), time.Now()); err != nil {
				t.Fatal(err)
			}
			exists(t, r.Workspace, tc.wantWorkspace)
			for _, name := range []string{"stdout", "stderr"} {
				exists(t, w.Store.Path(r.ID, name), tc.wantLogs)
			}
			for _, name := range []string{"started", "record.json", "job.lock"} {
				exists(t, w.Store.Path(r.ID, name), true)
			}
			rec, err := w.Store.Get(r.ID)
			if err != nil || rec.LogsExpired == tc.wantLogs || !rec.Updated.Equal(r.Updated) {
				t.Fatalf("expiry receipt changed completion time or lost tombstone: %+v %v", rec, err)
			}
			if !tc.wantLogs {
				response := httptest.NewRecorder()
				w.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/jobs/"+r.ID+"/log?stream=stdout&offset=0", nil))
				if response.Code != http.StatusGone {
					t.Fatal(response.Code)
				}
				// Old IDs must stay terminal after payloads and workspaces are gone.
				if got, err := w.Prepare(r.Request); err != nil || got.State != stateSucceeded {
					t.Fatalf("old ID became runnable: %+v %v", got, err)
				}
				if got, err := w.Start(context.Background(), r.ID); err != nil || !got.Terminal() {
					t.Fatal(got, err)
				}
				if _, err := w.cleanup(context.Background(), time.Now()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestRetentionProtectsWorkspaceReceipts(t *testing.T) {
	for _, state := range []string{stateQueued, statePrepared, stateSyncing, stateStarting, stateRunning, stateCanceling, "unknown"} {
		t.Run(state, func(t *testing.T) {
			w := worker(t, 1)
			w.Retention = DefaultRetention()
			r := retainedJob(t, w, state, 40*24*time.Hour)
			if _, err := w.cleanWorkspaces(context.Background(), time.Now()); err != nil {
				t.Fatal(err)
			}
			exists(t, r.Workspace, true)
		})
	}
	w := worker(t, 1)
	w.Retention = DefaultRetention()
	r := retainedJob(t, w, stateSucceeded, 40*24*time.Hour)
	recent := r
	recent.ID = NewID()
	recent.Updated = time.Now()
	if err := WriteJSON(w.Store.Path(recent.ID, "record.json"), recent); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cleanup(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	exists(t, r.Workspace, true)
	// A reader's lock protects both streams from partial expiry.
	lock, err := recordLock(w.Store.Path(r.ID, "job.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := w.Store.expireLogs(r.ID, time.Now(), time.Hour, false); err != nil || removed {
		t.Fatal(removed, err)
	}
	lock.Close()
}

func TestRetentionCollectionAndRecovery(t *testing.T) {
	w := worker(t, 1)
	w.Retention = DefaultRetention()
	r := retainedJob(t, w, stateSucceeded, 8*24*time.Hour)
	r.LogsCollected = false
	if err := WriteJSON(w.Store.Path(r.ID, "record.json"), r); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("\x00\xff\n"), maxLogBatch/3+1)
	if err := os.WriteFile(w.Store.Path(r.ID, "stdout"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cleanup(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	exists(t, w.Store.Path(r.ID, "stdout"), true)
	config := Config{Workers: []WorkerConfig{{Name: "worker", Socket: endpoint(t, w.Handler())}}}
	paths := Paths{State: t.TempDir()}
	c := NewCoordinator(paths, config, testBinary)
	defer c.Close()
	r.Worker, r.State, r.Submitted = "worker", stateRunning, true
	r.Created = r.Created.Add(-time.Hour)
	submitted := r.Created
	if err := c.Store.Put(r); err != nil {
		t.Fatal(err)
	}
	if done, err := c.observe(context.Background(), &r); done || err != nil {
		t.Fatal(done, err)
	}
	got, err := w.Store.Get(r.ID)
	if err != nil || got.LogsCollected {
		t.Fatal("acknowledged incomplete output", got, err)
	}
	// Rebuild the coordinator from disk while output collection is incomplete.
	c.Close()
	c = NewCoordinator(paths, config, testBinary)
	defer c.Close()
	r, err = c.Store.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done, err := c.observe(context.Background(), &r); !done || err != nil {
		t.Fatal(done, err)
	}
	persisted, err := c.Store.Get(r.ID)
	if err != nil || !persisted.Created.Equal(submitted) {
		t.Fatal("worker receipt replaced coordinator submission time", persisted.Created, submitted, err)
	}
	got, err = w.Store.Get(r.ID)
	if err != nil || !got.LogsCollected {
		t.Fatal("missing durable acknowledgment", got, err)
	}
	copy, err := os.ReadFile(c.Store.Path(r.ID, "stdout"))
	if err != nil || !bytes.Equal(copy, data) {
		t.Fatal("durable output differs", err)
	}
	if _, err := w.cleanup(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	exists(t, w.Store.Path(r.ID, "stdout"), false)
	exists(t, c.Store.Path(r.ID, "stdout"), true)
	future := time.Now().Add(8 * 24 * time.Hour)
	if _, err := c.cleanup(context.Background(), future); err != nil {
		t.Fatal(err)
	}
	exists(t, c.Store.Path(r.ID, "stdout"), false)
	if old, err := c.Submit(context.Background(), r.Request); err != nil || !old.Terminal() || !old.LogsExpired {
		t.Fatal(old, err)
	}
	if _, err := c.cleanup(context.Background(), future); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionWorkspaceBoundaries(t *testing.T) {
	w := worker(t, 1)
	w.Retention = DefaultRetention()
	r := retainedJob(t, w, stateSucceeded, 40*24*time.Hour)
	outside := filepath.Join(t.TempDir(), "keep")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), filepath.Join(r.Workspace, "external")); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(w.Cache, "workspaces", NewID())
	trash := filepath.Join(w.Cache, "workspaces", NewID()+".gc-"+NewID())
	for _, dir := range []string{unknown, trash} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := w.cleanWorkspaces(context.Background(), time.Now()); err != nil || n != 2 {
		t.Fatal(n, err)
	}
	exists(t, outside, true)
	exists(t, unknown, true)
	exists(t, trash, false)
	exists(t, r.Workspace, false)
	if err := os.Symlink(filepath.Dir(outside), r.Workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cleanWorkspaces(context.Background(), time.Now()); err == nil {
		t.Fatal("accepted workspace symlink")
	}
	exists(t, outside, true)
	local := worker(t, 1)
	local.Retention = DefaultRetention()
	local.Local = true
	lr := retainedJob(t, local, stateSucceeded, 40*24*time.Hour)
	if _, err := local.cleanup(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	exists(t, lr.Source, true)
}

func TestRetentionFailsClosedAndRetries(t *testing.T) {
	w := worker(t, 1)
	w.Retention = DefaultRetention()
	r := retainedJob(t, w, stateSucceeded, 40*24*time.Hour)
	// The tombstone must survive a failed unlink, with deletion retried later.
	if err := os.Remove(w.Store.Path(r.ID, "stderr")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(w.Store.Path(r.ID, "stderr"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.Store.Path(r.ID, "stderr/keep"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cleanup(context.Background(), time.Now()); err == nil {
		t.Fatal("ignored deletion failure")
	}
	rec, err := w.Store.Get(r.ID)
	if err != nil || !rec.LogsExpired {
		t.Fatal("missing tombstone after partial deletion", rec, err)
	}
	if err := os.RemoveAll(w.Store.Path(r.ID, "stderr")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cleanup(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	exists(t, w.Store.Path(r.ID, "record.json"), true)
	bad := NewID()
	if err := os.MkdirAll(w.Store.Path(bad, ""), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(w.Store.Path(bad, "started"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cleanup(context.Background(), time.Now()); err == nil {
		t.Fatal("ignored marker without receipt")
	}
	exists(t, w.Store.Path(bad, "started"), true)
	for _, id := range []string{"bad", NewID()} {
		out := httptest.NewRecorder()
		w.Handler().ServeHTTP(out, httptest.NewRequest("POST", "/jobs/"+id+"/collected", nil))
		if out.Code < 400 {
			t.Fatal("acknowledged invalid or missing job")
		}
	}
	activeWorker := worker(t, 1)
	active := retainedJob(t, activeWorker, statePrepared, time.Hour)
	if _, err := activeWorker.collectLogs(active.ID); err == nil {
		t.Fatal("acknowledged active job")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := activeWorker.cleanup(ctx, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestRetentionExpiredSyncLock(t *testing.T) {
	w := worker(t, 1)
	w.Retention = DefaultRetention()
	r, err := w.Prepare(request(t, "true"))
	if err != nil {
		t.Fatal(err)
	}
	guard, _, err := syncGuard(w.Store, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	r.LeaseUntil = time.Now().Add(-time.Second)
	if err := w.Store.Put(r); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(40 * 24 * time.Hour)
	if _, err := w.cleanup(context.Background(), future); err != nil {
		t.Fatal(err)
	}
	exists(t, r.Workspace, true)
	got, err := w.Store.Get(r.ID)
	if err != nil || got.State != stateCanceled {
		t.Fatal(got, err)
	}
	guard.Close()
	if _, err := w.cleanup(context.Background(), future); err != nil {
		t.Fatal(err)
	}
	exists(t, r.Workspace, false)
}
