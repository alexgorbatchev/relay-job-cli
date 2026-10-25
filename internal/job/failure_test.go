package job

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorageFailuresRemainVisible(t *testing.T) {
	ctx := context.Background()
	w := worker(t, 1)
	req := request(t, "true")
	if _, err := w.Prepare(Request{}); err == nil {
		t.Fatal("invalid reservation")
	}
	// A full or unwritable state location must fail, never masquerade as a busy worker.
	blocked := filepath.Join(t.TempDir(), "blocked")
	os.WriteFile(blocked, nil, 0600)
	broken := &Worker{Store: Store{blocked}, Cache: t.TempDir(), Slots: 1}
	if _, err := broken.Prepare(req); err == nil {
		t.Fatal("unwritable state")
	}
	w.Cache = blocked
	if _, err := w.Prepare(req); err == nil {
		t.Fatal("unwritable workspace")
	}
	c := NewCoordinator(Paths{State: blocked}, Config{}, testBinary)
	defer c.Close()
	if _, err := c.Submit(ctx, req); err == nil {
		t.Fatal("submit without durable receipt")
	}
	if err := c.Start(ctx); err == nil {
		t.Fatal("unreadable recovery store")
	}
	brokenStore := Store{t.TempDir()}
	os.MkdirAll(brokenStore.Path(req.ID, ""), 0700)
	os.WriteFile(brokenStore.Path(req.ID, "record.json"), []byte("corrupt"), 0600)
	broken = &Worker{Store: brokenStore, Cache: t.TempDir(), Slots: 1}
	if _, err := broken.Prepare(req); err == nil {
		t.Fatal("corrupt existing receipt")
	}
	if _, err := broken.Prepare(request(t, "true")); err == nil {
		t.Fatal("corrupt active index")
	}
	corruptCoordinator := NewCoordinator(Paths{State: brokenStore.Dir}, Config{}, testBinary)
	defer corruptCoordinator.Close()
	if _, err := corruptCoordinator.Submit(ctx, req); err == nil {
		t.Fatal("corrupt submission receipt")
	}
	dir := t.TempDir()
	os.Chmod(dir, 0500)
	defer os.Chmod(dir, 0700)
	if err := WriteJSON(filepath.Join(dir, "receipt"), req); err == nil && os.Geteuid() != 0 {
		t.Fatal("read-only receipt directory")
	}
	// Non-job files are harmless; an empty store is a valid initial state.
	clean := Store{t.TempDir()}
	os.MkdirAll(filepath.Join(clean.Dir, "jobs"), 0700)
	os.WriteFile(filepath.Join(clean.Dir, "jobs", "note"), nil, 0600)
	if records, err := clean.List(); err != nil || len(records) != 0 {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	if !strings.Contains(DefaultPaths().State, ".local/state/relay-job") {
		t.Fatal("default path resolution")
	}
}

func TestWorkerFailureRoutes(t *testing.T) {
	badBody := httptest.NewRecorder()
	worker(t, 1).Handler().ServeHTTP(badBody, httptest.NewRequest("POST", "/jobs", strings.NewReader("{} {}")))
	if badBody.Code != 400 {
		t.Fatal("API accepted multiple JSON values")
	}
	ctx := context.Background()
	w := worker(t, 1)
	req := request(t, "true")
	rec, err := w.Prepare(req)
	if err != nil {
		t.Fatal(err)
	}
	w.Binary = "/does/not/exist"
	if _, err = w.Start(ctx, req.ID); err == nil {
		t.Fatal("invalid execution binary")
	}
	if _, err = w.Cancel(ctx, NewID()); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// Every malformed log request must fail without opening paths outside the job store.
	for _, query := range []string{"/jobs/bad/log?stream=stdout&offset=0", "/jobs/" + NewID() + "/log?stream=stdout&offset=0", "/jobs/" + req.ID + "/log?stream=stdout&offset=bad"} {
		response := httptest.NewRecorder()
		w.Handler().ServeHTTP(response, httptest.NewRequest("GET", query, nil))
		if response.Code == 200 {
			t.Fatal(query)
		}
	}
	os.MkdirAll(w.Store.Path(req.ID, "stdout"), 0700)
	response := httptest.NewRecorder()
	w.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/jobs/"+req.ID+"/log?stream=stdout&offset=0", nil))
	if response.Code == 200 {
		t.Fatal("directory read reported success")
	}
	bad := &Worker{Store: Store{filepath.Join(t.TempDir(), "state")}, Slots: 1}
	os.WriteFile(bad.Store.Dir, nil, 0600)
	response = httptest.NewRecorder()
	bad.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/health", nil))
	if response.Code == 200 {
		t.Fatal("health ignored unreadable state")
	}
	for _, route := range []string{"/jobs/bad/start", "/jobs/bad/cancel"} {
		response = httptest.NewRecorder()
		w.Handler().ServeHTTP(response, httptest.NewRequest("POST", route, nil))
		if response.Code != 400 {
			t.Fatal(response.Code)
		}
	}
	rec.State = "running"
	rec.ID = NewID()
	if err = w.Store.Put(rec); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = w.Status(canceled, rec.ID); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestLogCollectionFairness(t *testing.T) {
	ctx := context.Background()
	w := worker(t, 1)
	req := request(t, "true")
	rec, err := w.Prepare(req)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte{0, 0xff, 0xef, 0xbb, 0xbf, '\n'}, int(maxLogBatch/6)+100)
	os.WriteFile(w.Store.Path(req.ID, "stdout"), data, 0600)
	os.WriteFile(w.Store.Path(req.ID, "stderr"), []byte("stderr gets a turn"), 0600)
	socket := endpoint(t, w.Handler())
	c := NewCoordinator(Paths{State: t.TempDir()}, Config{Workers: []WorkerConfig{{Name: "worker", Socket: socket}}}, testBinary)
	defer c.Close()
	rec.Worker = "worker"
	rec.State = "running"
	if err = c.Store.Put(rec); err != nil {
		t.Fatal(err)
	}
	if err = c.copyLogs(ctx, rec); !errors.Is(err, errLogsPending) {
		t.Fatalf("expected bounded batch, got %v", err)
	}
	b, err := os.ReadFile(c.Store.Path(rec.ID, "stderr"))
	if err != nil || string(b) != "stderr gets a turn" {
		t.Fatal("stdout starved stderr")
	}
	if err = c.copyLogs(ctx, rec); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(c.Store.Path(rec.ID, "stdout"))
	if err != nil || !bytes.Equal(b, data) {
		t.Fatal("chunked collection corrupted binary data")
	}
	// The local fallback collector has the same bounded behavior.
	local := NewCoordinator(Paths{State: t.TempDir()}, Config{}, testBinary)
	defer local.Close()
	rec.Worker = "local"
	local.Local.Store.Put(rec)
	local.Store.Put(rec)
	os.WriteFile(local.Local.Store.Path(rec.ID, "stdout"), data, 0600)
	os.WriteFile(local.Local.Store.Path(rec.ID, "stderr"), []byte("local err"), 0600)
	if err = local.copyLogs(ctx, rec); !errors.Is(err, errLogsPending) {
		t.Fatal(err)
	}
	if err = local.copyLogs(ctx, rec); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(c.Store.Path(NewID(), "stdout"), 0700)
	absent := rec
	absent.ID = NewID()
	absent.Worker = "worker"
	if err = c.copyLogs(ctx, absent); err == nil {
		t.Fatal("log collection without record")
	}
	if _, err = c.status(ctx, Record{Worker: "removed"}); err == nil {
		t.Fatal("unconfigured assigned worker")
	}
	if err = c.cancelWorker(ctx, Record{Worker: "removed"}); err == nil {
		t.Fatal("unconfigured cancellation")
	}
}

func TestSocketStartupFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	socket := filepath.Join(dir, "nonempty")
	os.MkdirAll(filepath.Join(socket, "child"), 0700)
	if err := Serve(ctx, socket, http.NewServeMux()); err == nil {
		t.Fatal("nonempty directory socket")
	}
	tooLong := filepath.Join(dir, strings.Repeat("x", 150))
	if err := Serve(ctx, tooLong, http.NewServeMux()); err == nil {
		t.Fatal("oversized socket path")
	}
	c := NewCoordinator(Paths{State: filepath.Join(dir, "state")}, Config{}, testBinary)
	defer c.Close()
	runDir, err := os.MkdirTemp("", "rj-serve-coord-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(runDir)
	done := make(chan error, 1)
	go func() { done <- ServeCoordinator(ctx, filepath.Join(runDir, "api"), c) }()
	for i := 0; i < 1000; i++ {
		if _, err = os.Stat(filepath.Join(runDir, "api")); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}
