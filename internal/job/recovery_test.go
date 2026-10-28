package job

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLostStartReplyDoesNotReplay(t *testing.T) {
	systemd(t)
	ctx := context.Background()
	w := worker(t, 1)
	fallback := worker(t, 1)
	handler := w.Handler()
	socket := endpoint(t, http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/start") {
			if _, err := w.Start(context.Background(), strings.Split(r.URL.Path, "/")[2]); err != nil {
				t.Error(err)
			}
			conn, _, err := out.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		handler.ServeHTTP(out, r)
	}))
	second := endpoint(t, fallback.Handler())
	c := NewCoordinator(Paths{State: t.TempDir()}, Config{Workers: []WorkerConfig{{Name: "first", Socket: socket}, {Name: "second", Socket: second}}}, testBinary)
	defer c.Close()
	req := request(t, "sh", "-c", "printf execution >> count; sleep .1; printf done")
	req.Spec.Unavailable = "local"
	if _, err := c.Submit(ctx, req); err != nil {
		t.Fatal(err)
	}
	rec := wait(t, func() (Record, error) { return c.Store.Get(req.ID) }, "succeeded")
	b, err := os.ReadFile(filepath.Join(rec.Workspace, "count"))
	if err != nil || string(b) != "execution" || rec.Worker != "first" {
		t.Fatalf("%+v %q %v", rec, b, err)
	}
	if _, err = fallback.Store.Get(req.ID); !os.IsNotExist(err) {
		t.Fatal("job replayed on another worker")
	}
	if _, err = os.Stat(filepath.Join(req.Source, "count")); !os.IsNotExist(err) {
		t.Fatal("job replayed locally")
	}
}

func TestQueueAndCancellationRecovery(t *testing.T) {
	systemd(t)
	ctx := context.Background()
	w := worker(t, 1)
	socket := endpoint(t, w.Handler())
	c := NewCoordinator(Paths{State: t.TempDir()}, Config{Workers: []WorkerConfig{{Name: "worker", Socket: socket}}}, testBinary)
	defer c.Close()
	first := request(t, "sleep", "2")
	if _, err := c.Submit(ctx, first); err != nil {
		t.Fatal(err)
	}
	wait(t, func() (Record, error) { return c.Store.Get(first.ID) }, "running")
	second := first
	second.ID = NewID()
	if _, err := c.Submit(ctx, second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	queued, err := c.Store.Get(second.ID)
	if err != nil || queued.State != "queued" {
		t.Fatalf("%+v %v", queued, err)
	}
	if rec, err := c.Cancel(ctx, second.ID); err != nil || rec.State != "canceled" {
		t.Fatalf("%+v %v", rec, err)
	}
	// Break the connection after execution began. A failed cancellation remains durable.
	if err := os.Rename(socket, socket+".offline"); err != nil {
		t.Fatal(err)
	}
	c.Workers[0].Close()
	if _, err := c.Cancel(ctx, first.ID); err == nil {
		t.Fatal("cancellation should be unconfirmed")
	}
	rec, err := c.Store.Get(first.ID)
	if err != nil || rec.State != "canceling" {
		t.Fatalf("%+v %v", rec, err)
	}
	// A coordinator restart with restored configuration resumes cancellation, never execution.
	c.Close()
	if err := os.Rename(socket+".offline", socket); err != nil {
		t.Fatal(err)
	}
	resumed := NewCoordinator(Paths{State: c.Store.Dir}, Config{Workers: []WorkerConfig{{Name: "worker", Socket: socket}}}, testBinary)
	defer resumed.Close()
	if err = resumed.Start(ctx); err != nil {
		t.Fatal(err)
	}
	wait(t, func() (Record, error) { return resumed.Store.Get(first.ID) }, "canceled")
	if _, err = resumed.Cancel(ctx, NewID()); !os.IsNotExist(err) {
		t.Fatalf("missing cancellation %v", err)
	}
	if _, err = resumed.Cancel(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPreparedRecoveryAndLease(t *testing.T) {
	ctx := context.Background()
	w := worker(t, 1)
	r := request(t, "true")
	rec, err := w.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	original := rec.LeaseUntil
	rec, err = w.Prepare(r)
	if err != nil || !rec.LeaseUntil.After(original) {
		t.Fatal("lease not renewed")
	}
	rec.LeaseUntil = time.Now().Add(-time.Minute)
	w.Store.Put(rec)
	got, err := w.Status(ctx, r.ID)
	if err != nil || got.State != "canceled" {
		t.Fatalf("%+v %v", got, err)
	}
	rec.State = "prepared"
	w.Store.Put(rec)
	if _, err = w.Start(ctx, r.ID); err == nil {
		t.Fatal("expired reservation started")
	}
	rec.State = "prepared"
	w.Store.Put(rec)
	got, err = w.Prepare(r)
	if err != nil || got.State != "canceled" {
		t.Fatal("expired lease renewed")
	}
	socket := endpoint(t, w.Handler())
	paths := Paths{State: t.TempDir()}
	c := NewCoordinator(paths, Config{Workers: []WorkerConfig{{Name: "worker", Socket: socket}}}, testBinary)
	defer c.Close()
	pending := request(t, "true")
	reserved, err := w.Prepare(pending)
	if err != nil {
		t.Fatal(err)
	}
	reserved.State = "syncing"
	reserved.Worker = "worker"
	if err = c.Store.Put(reserved); err != nil {
		t.Fatal(err)
	}
	if err = c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	wait(t, func() (Record, error) { return c.Store.Get(pending.ID) }, "failed")
	if got, err = w.Store.Get(pending.ID); err != nil || got.State != "canceled" {
		t.Fatal("abandoned reservation not released")
	}
	// A reservation that was persisted before a missing start request can resume only on that worker.
	systemd(t)
	adopted := request(t, "printf", "adopted")
	ready, err := w.Prepare(adopted)
	if err != nil {
		t.Fatal(err)
	}
	ready.State = "starting"
	ready.Worker = "worker"
	ready.Submitted = true
	next := NewCoordinator(Paths{State: t.TempDir()}, Config{Workers: []WorkerConfig{{Name: "worker", Socket: socket}}}, testBinary)
	defer next.Close()
	next.Store.Put(ready)
	if err = next.Start(ctx); err != nil {
		t.Fatal(err)
	}
	wait(t, func() (Record, error) { return next.Store.Get(adopted.ID) }, "succeeded")
}

func TestCoordinatorAPI(t *testing.T) {
	ctx := context.Background()
	w := worker(t, 1)
	workerSocket := endpoint(t, w.Handler())
	c := NewCoordinator(Paths{State: t.TempDir()}, Config{Workers: []WorkerConfig{{Name: "worker", Socket: workerSocket}}}, testBinary)
	defer c.Close()
	rpc := NewRPC(endpoint(t, c.Handler(ctx)))
	defer rpc.Close()
	var records []map[string]any
	if err := rpc.Call(ctx, "GET", "/workers", nil, &records); err != nil || len(records) != 1 {
		t.Fatal(err)
	}
	invalid := request(t, "true")
	invalid.ID = "bad"
	if err := rpc.Call(ctx, "POST", "/jobs", invalid, nil); err == nil {
		t.Fatal("invalid submit")
	}
	if err := rpc.Call(ctx, "POST", "/jobs", map[string]any{"unknown": true}, nil); err == nil {
		t.Fatal("unknown property")
	}
	missing := request(t, "true")
	missing.Source = "/nonexistent-rj-source"
	if _, err := c.Submit(ctx, missing); err == nil {
		t.Fatal("missing source")
	}
	unknown := request(t, "true")
	unknown.Spec.Workers = []string{"unknown"}
	var rec Record
	if err := rpc.Call(ctx, "POST", "/jobs", unknown, &rec); err != nil {
		t.Fatal(err)
	}
	wait(t, func() (Record, error) { return c.Store.Get(unknown.ID) }, "failed")
	if err := rpc.Call(ctx, "GET", "/jobs/"+unknown.ID, nil, &rec); err != nil {
		t.Fatal(err)
	}
	if err := rpc.Call(ctx, "POST", "/jobs/"+unknown.ID+"/cancel", nil, &rec); err != nil {
		t.Fatal(err)
	}
	if err := rpc.Call(ctx, "GET", "/jobs/"+NewID(), nil, &rec); err == nil {
		t.Fatal("missing status")
	}
	if err := rpc.Call(ctx, "POST", "/jobs/"+NewID()+"/cancel", nil, nil); err == nil {
		t.Fatal("missing cancel")
	}
	var out bytes.Buffer
	if _, err := rpc.Log(ctx, unknown.ID, "stdout", 0, &out); err != nil || out.Len() != 0 {
		t.Fatal(err)
	}
	conflict := unknown
	conflict.Spec.Command = []string{"false"}
	if _, err := c.Submit(ctx, conflict); err == nil {
		t.Fatal("ID conflict")
	}
	c.Close()
	if _, err := c.Submit(ctx, request(t, "true")); err == nil {
		t.Fatal("submit during shutdown")
	}
}

func TestFilesystemAndProtocolFailures(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	os.WriteFile(file, []byte("not json"), 0600)
	var value Record
	if err := ReadJSON(file, &value); err == nil {
		t.Fatal("malformed record")
	}
	for _, body := range []string{"{} {}", "{} trailing"} {
		os.WriteFile(file, []byte(body), 0600)
		if err := ReadJSON(file, &value); err == nil {
			t.Fatal("trailing JSON input accepted")
		}
	}
	if _, err := LoadConfig(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing config")
	}
	if err := WriteJSON(filepath.Join(file, "child"), value); err == nil {
		t.Fatal("parent is file")
	}
	if err := WriteJSON(filepath.Join(dir, "unsupported"), make(chan int)); err == nil {
		t.Fatal("unsupported JSON")
	}
	if err := WriteJSON(dir, value); err == nil {
		t.Fatal("rename over directory")
	}
	if _, err := Lock(dir); err == nil {
		t.Fatal("lock is directory")
	}
	if _, err := Lock(filepath.Join(file, "child")); err == nil {
		t.Fatal("lock parent is file")
	}
	store := Store{dir}
	if err := store.Put(Record{}); err == nil {
		t.Fatal("invalid persisted record")
	}
	orphan := NewID()
	os.MkdirAll(filepath.Join(dir, "jobs", orphan), 0700)
	if records, err := store.List(); err != nil || len(records) != 0 {
		t.Fatal("uncommitted empty directory prevented recovery")
	}
	os.WriteFile(store.Path(orphan, "started"), nil, 0600)
	if _, err := store.List(); err == nil {
		t.Fatal("execution marker without receipt silently ignored")
	}
	if _, err := (Store{file}).List(); err == nil {
		t.Fatal("invalid store root")
	}
	if err := Execute(context.Background(), Store{dir}, "bad"); err == nil {
		t.Fatal("invalid execution")
	}
	if err := Execute(context.Background(), Store{t.TempDir()}, NewID()); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	socket := endpoint(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("invalid json")) }))
	rpc := NewRPC(socket)
	defer rpc.Close()
	if err := rpc.Call(context.Background(), "GET", "/", nil, &value); err == nil {
		t.Fatal("malformed response")
	}
	if err := rpc.Call(context.Background(), "POST", "/", make(chan int), nil); err == nil {
		t.Fatal("unmarshalable request")
	}
	if err := rpc.Call(context.Background(), "GET", "/\n", nil, nil); err == nil {
		t.Fatal("invalid URL")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Serve(ctx, socket, http.NewServeMux()); err == nil {
		t.Fatal("duplicate service lock")
	}
	if err := Serve(ctx, filepath.Join(file, "socket"), http.NewServeMux()); err == nil {
		t.Fatal("socket parent file")
	}
	bad := &Transport{Config: WorkerConfig{Socket: endpoint(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { respond(w, Health{Protocol: Protocol + 1}) }))}}
	defer bad.Close()
	if _, err := bad.Health(context.Background(), Request{}); err == nil {
		t.Fatal("protocol mismatch")
	}
	// Non-availability failures must not be treated as local fallback opportunities.
	w := worker(t, 1)
	r := request(t, "true")
	rec, err := w.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(rec.Source)
	if err := (&Transport{}).Sync(context.Background(), rec); err == nil {
		t.Fatal("missing source")
	}
	if got := shellQuote("a'b"); got != "'a'\\''b'" {
		t.Fatal(got)
	}
}

func TestExecutionFailureBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *Worker, *Record)
	}{
		{"missing cwd", func(t *testing.T, w *Worker, r *Record) { r.Spec.Cwd = "missing" }},
		{"escaping symlink", func(t *testing.T, w *Worker, r *Record) {
			os.Symlink(t.TempDir(), filepath.Join(r.Workspace, "escape"))
			r.Spec.Cwd = "escape"
		}},
		{"missing program", func(t *testing.T, w *Worker, r *Record) { r.Spec.Command = []string{"relay-jobs-no-such-executable"} }},
		{"stdout directory", func(t *testing.T, w *Worker, r *Record) { os.MkdirAll(w.Store.Path(r.ID, "stdout"), 0700) }},
		{"stderr directory", func(t *testing.T, w *Worker, r *Record) { os.MkdirAll(w.Store.Path(r.ID, "stderr"), 0700) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := worker(t, 1)
			r := request(t, "true")
			rec, err := w.Prepare(r)
			if err != nil {
				t.Fatal(err)
			}
			tc.setup(t, w, &rec)
			rec.State = "starting"
			w.Store.Put(rec)
			if err = Execute(context.Background(), w.Store, rec.ID); err != nil {
				t.Fatal(err)
			}
			got, err := w.Store.Get(rec.ID)
			if err != nil || got.State != "failed" || got.ExitCode != 125 || got.Error == "" {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
	w := worker(t, 1)
	req := request(t, "sleep", "10")
	rec, err := w.Prepare(req)
	if err != nil {
		t.Fatal(err)
	}
	rec.State = "starting"
	w.Store.Put(rec)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err = Execute(ctx, w.Store, rec.ID); err != nil {
		t.Fatal(err)
	}
	got, err := w.Store.Get(rec.ID)
	if err != nil || got.State != "canceled" || got.ExitCode != 130 {
		t.Fatalf("%+v %v", got, err)
	}
	if err = Execute(context.Background(), w.Store, rec.ID); err != nil {
		t.Fatal("terminal execute should be idempotent")
	}
	// A write failure is surfaced through the transport, including partial reads.
	broken := NewRPC(endpoint(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("output")) })))
	defer broken.Close()
	f, err := os.Create(filepath.Join(t.TempDir(), "closed"))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err = broken.Log(context.Background(), NewID(), "stdout", 0, f); !errors.Is(err, os.ErrClosed) {
		t.Fatal(err)
	}
}
