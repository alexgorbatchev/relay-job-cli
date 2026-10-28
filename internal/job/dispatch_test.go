package job

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDispatchFailureBoundaries(t *testing.T) {
	systemd(t)
	for _, mode := range []string{"reserve rejected", "sync rejected", "cleanup reply lost", "reserve connection lost", "local launch rejected"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			w := worker(t, 1)
			h := w.Handler()
			req := request(t, "printf", "selected")
			socket := endpoint(t, http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" && r.URL.Path == "/jobs" {
					if mode == "reserve rejected" {
						os.Chmod(w.Cache, 0500)
						defer os.Chmod(w.Cache, 0700)
					}
					if mode == "reserve connection lost" {
						conn, _, err := out.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						conn.Close()
						return
					}
				}
				if strings.HasSuffix(r.URL.Path, "/cancel") && mode == "cleanup reply lost" {
					conn, _, err := out.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					conn.Close()
					return
				}
				h.ServeHTTP(out, r)
			}))
			config := Config{Workers: []WorkerConfig{{Name: "first", Socket: socket}}}
			if mode == "reserve connection lost" {
				second := worker(t, 1)
				config.Workers = append(config.Workers, WorkerConfig{Name: "second", Socket: endpoint(t, second.Handler())})
			}
			if mode == "local launch rejected" {
				config.Workers[0].Socket = "/missing/relay-jobs.sock"
				req.Spec.Unavailable = "local"
			}
			c := NewCoordinator(Paths{State: t.TempDir()}, config, testBinary)
			defer c.Close()
			if mode == "local launch rejected" {
				c.Local.Binary = "/nonexistent/relay-jobs"
			}
			if mode == "sync rejected" || mode == "cleanup reply lost" {
				os.Chmod(req.Source, 0000)
				defer os.Chmod(req.Source, 0700)
				req.Spec.Unavailable = "local"
			}
			if _, err := c.Submit(ctx, req); err != nil {
				t.Fatal(err)
			}
			expected := "failed"
			if mode == "reserve connection lost" {
				expected = "succeeded"
			}
			if mode == "local launch rejected" {
				expected = "lost"
			}
			got := wait(t, func() (Record, error) { return c.Store.Get(req.ID) }, expected)
			if mode == "reserve connection lost" && got.Worker != "second" {
				t.Fatal("second healthy worker was not tried")
			}
			if mode == "cleanup reply lost" {
				if !strings.Contains(got.Error, "reservation cleanup") {
					t.Fatal(got)
				}
				w.Cancel(ctx, req.ID)
			}
			if (mode == "sync rejected" || mode == "cleanup reply lost") && got.Worker == "local" {
				t.Fatal("sync error was hidden by local fallback")
			}
		})
	}
}

func TestSSHConfigurationNeverTriggersFallback(t *testing.T) {
	c := NewCoordinator(Paths{State: t.TempDir()}, Config{Workers: []WorkerConfig{{Name: "bad", Host: "bad/alias", Socket: "/missing"}}}, testBinary)
	defer c.Close()
	r := request(t, "true")
	r.Spec.Unavailable = "local"
	if _, err := c.Submit(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	rec := wait(t, func() (Record, error) { return c.Store.Get(r.ID) }, "failed")
	if rec.Worker == "local" {
		t.Fatal("SSH configuration failure executed locally")
	}
	if _, err := c.Local.Store.Get(r.ID); !os.IsNotExist(err) {
		t.Fatal("local job was submitted after SSH configuration failure")
	}
}

func TestStateAndLockFaults(t *testing.T) {
	systemd(t)
	ctx := context.Background()
	w := worker(t, 1)
	req := request(t, "true")
	rec, err := w.Prepare(req)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(w.Store.Path(req.ID, "job.lock"), 0700)
	if _, err = w.Prepare(req); err == nil {
		t.Fatal("reservation lock error hidden")
	}
	if _, err = w.Start(ctx, req.ID); err == nil {
		t.Fatal("start lock error hidden")
	}
	if _, err = w.Cancel(ctx, req.ID); err == nil {
		t.Fatal("cancel lock error hidden")
	}
	if err = Execute(ctx, w.Store, req.ID); err == nil {
		t.Fatal("execution lock error hidden")
	}
	os.Remove(w.Store.Path(req.ID, "job.lock"))
	held, err := w.Store.WorkspaceLock(req.Source)
	if err != nil {
		t.Fatal(err)
	}
	conflict := req
	conflict.ID = NewID()
	if _, err = w.Prepare(conflict); err == nil {
		t.Fatal("workspace lock ignored")
	}
	held.Close()
	rec.State = "canceling"
	rec.LeaseUntil = time.Time{}
	w.Store.Put(rec)
	if got, err := w.Status(ctx, req.ID); err != nil || got.State != "canceled" {
		t.Fatalf("%+v %v", got, err)
	}
	// A damaged active record must make health fail rather than release its reservation.
	next := request(t, "true")
	if _, err = w.Prepare(next); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(w.Store.Path(next.ID, "record.json"), []byte("corrupt"), 0600)
	w.mu.Lock()
	_, err = w.recordsLocked()
	w.mu.Unlock()
	if err == nil {
		t.Fatal("damaged active record ignored")
	}
	c := NewCoordinator(Paths{State: t.TempDir()}, Config{Workers: []WorkerConfig{{Name: "worker", Socket: endpoint(t, worker(t, 1).Handler())}}}, testBinary)
	defer c.Close()
	r := Record{Request: request(t, "true"), State: "queued"}
	c.Store.Put(r)
	dir := filepath.Dir(c.Store.Path(r.ID, "record.json"))
	os.Chmod(dir, 0500)
	defer os.Chmod(dir, 0700)
	if err = c.assign(ctx, &r); err == nil {
		t.Fatal("dispatch without durable starting receipt")
	}
	if err = c.run(ctx, r); err == nil {
		t.Fatal("monitor hid an unwritable receipt")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err = c.assign(canceled, &r); err == nil {
		t.Fatal("canceled dispatch")
	}
	r.Spec.Labels = []string{"unmatched"}
	if eligible(WorkerConfig{Name: "worker"}, r.Spec) {
		t.Fatal("missing required label accepted")
	}
}
