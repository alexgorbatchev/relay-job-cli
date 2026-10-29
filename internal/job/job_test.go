package job

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

var testBinary, testRsync string

func TestMain(m *testing.M) {
	var err error
	if testRsync, err = exec.LookPath("rsync"); err != nil {
		panic(err)
	}
	if binary := os.Getenv("RELAY_JOBS_TEST_BINARY"); binary != "" {
		testBinary, err = filepath.Abs(binary)
		if err != nil {
			panic(err)
		}
		os.Exit(m.Run())
	}
	dir, err := os.MkdirTemp("", "relay-jobs-test-")
	if err != nil {
		panic(err)
	}
	testBinary = filepath.Join(dir, "relay-jobs")
	build := exec.Command("go", "build", "-o", testBinary, "../../cmd/relay-jobs")
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		os.Stderr.Write(out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
func request(t *testing.T, command ...string) Request {
	t.Helper()
	return Request{ID: NewID(), Source: t.TempDir(), Spec: Spec{Command: command}}
}
func worker(t *testing.T, slots int) *Worker {
	t.Helper()
	return &Worker{Store: Store{t.TempDir()}, Cache: t.TempDir(), Binary: testBinary, Rsync: testRsync, Slots: slots}
}
func endpoint(t *testing.T, handler http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rjt-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "api")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, socket, handler) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket did not start")
		}
		time.Sleep(time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		os.RemoveAll(dir)
	})
	return socket
}
func wait(t *testing.T, get func() (Record, error), states ...string) Record {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, err := get()
		if err != nil {
			t.Fatal(err)
		}
		for _, state := range states {
			if r.State == state {
				return r
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %+v", r)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func start(t *testing.T, w *Worker, r Request) Record {
	t.Helper()
	rec, err := w.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Start(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		w.Cancel(ctx, r.ID)
	})
	return rec
}

func TestValidationAndStore(t *testing.T) {
	base := request(t, "true")
	for _, tc := range []struct {
		name   string
		change func(*Request)
	}{
		{"id", func(r *Request) { r.ID = "../bad" }}, {"source", func(r *Request) { r.Source = "relative" }}, {"command", func(r *Request) { r.Spec.Command = nil }},
		{"absolute cwd", func(r *Request) { r.Spec.Cwd = "/tmp" }}, {"parent cwd", func(r *Request) { r.Spec.Cwd = "a/../../b" }},
		{"policy", func(r *Request) { r.Spec.Unavailable = "always" }}, {"env", func(r *Request) { r.Spec.Env = map[string]string{"A=B": "x"} }},
		{"nul", func(r *Request) { r.Spec.Command = []string{"a\x00"} }}, {"exclude", func(r *Request) { r.Spec.Exclude = []string{"x\ny"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			r.Spec = Spec{Command: []string{"true"}}
			tc.change(&r)
			if r.Validate() == nil {
				t.Fatal("accepted invalid request")
			}
		})
	}
	s := Store{t.TempDir()}
	r := Record{Request: base, State: "queued"}
	if err := s.Put(r); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(r.ID)
	if err != nil || got.Source != base.Source {
		t.Fatalf("%+v %v", got, err)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("%+v %v", list, err)
	}
	if _, err = s.Get(".."); err == nil {
		t.Fatal("invalid store ID")
	}
	lock, err := s.WorkspaceLock(base.Source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.WorkspaceLock(base.Source); err == nil {
		t.Fatal("double lock")
	}
	lock.Close()
	if _, err = WorkspaceConfig(t, Config{}); err == nil {
		t.Fatal("empty worker config")
	}
	for _, w := range []WorkerConfig{{Name: "local", Socket: "/a"}, {Name: "x", Host: "-bad", Socket: "/a"}, {Name: "x", Socket: "relative"}} {
		if _, err = WorkspaceConfig(t, Config{Workers: []WorkerConfig{w}}); err == nil {
			t.Fatal("invalid config")
		}
	}
	c := Config{Workers: []WorkerConfig{{Name: "test", Socket: "/tmp/api"}}}
	if _, err = WorkspaceConfig(t, c); err != nil {
		t.Fatal(err)
	}
	c.Workers = append(c.Workers, c.Workers[0])
	if _, err = WorkspaceConfig(t, c); err == nil {
		t.Fatal("duplicate worker")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p := DefaultPaths()
	if !strings.HasPrefix(p.State, os.Getenv("XDG_STATE_HOME")) {
		t.Fatal(p)
	}
}
func WorkspaceConfig(t *testing.T, c Config) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relay-jobs.toml")
	if err := writeTOML(path, c); err != nil {
		t.Fatal(err)
	}
	return LoadConfig(path)
}

func writeTOML(path string, v any) error {
	b, err := toml.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}

func TestWorkerReservation(t *testing.T) {
	w := worker(t, 2)
	r := request(t, "true")
	rec, err := w.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	again, err := w.Prepare(r)
	if err != nil || again.ID != rec.ID {
		t.Fatal(err)
	}
	changed := r
	changed.Spec.Command = []string{"false"}
	if _, err = w.Prepare(changed); err == nil {
		t.Fatal("same ID different command")
	}
	same := r
	same.ID = NewID()
	if _, err = w.Prepare(same); err == nil {
		t.Fatal("concurrent workspace")
	}
	other := request(t, "true")
	if _, err = w.Prepare(other); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Prepare(request(t, "true")); err == nil {
		t.Fatal("capacity")
	}
	if rec, err = w.Cancel(context.Background(), r.ID); err != nil || rec.State != "canceled" {
		t.Fatalf("%+v %v", rec, err)
	}
	if _, err = w.Prepare(same); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Start(context.Background(), "bad"); err == nil {
		t.Fatal("bad start")
	}
	if _, err = w.Cancel(context.Background(), "bad"); err == nil {
		t.Fatal("bad cancel")
	}
}

func TestExecutionBytesAndOnce(t *testing.T) {
	// Shells differ in the native status they assign a failed exec. Preserve the host's result.
	err := exec.Command("sh", "-c", "exec /does/not/exist").Run()
	var missingExit *exec.ExitError
	if !errors.As(err, &missingExit) {
		t.Fatalf("missing executable did not fail normally: %v", err)
	}
	for _, tc := range []struct {
		name, script string
		code         int
	}{
		{"binary", "printf '\357\273\277 \\000\\377\\n'; printf '\\000err \\r\\n' >&2; exit 37", 37},
		{"success", "printf '%s' \"$VALUE\"; pwd", 0}, {"missing", "exec /does/not/exist", missingExit.ExitCode()},
		{"sysexits status", "exit 78", 78},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := worker(t, 1)
			r := request(t, "sh", "-c", tc.script)
			r.Spec.Env = map[string]string{"VALUE": "env"}
			rec, err := w.Prepare(r)
			if err != nil {
				t.Fatal(err)
			}
			rec.State = "starting"
			if err = w.Store.Put(rec); err != nil {
				t.Fatal(err)
			}
			if err = Execute(context.Background(), w.Store, r.ID); err != nil {
				t.Fatal(err)
			}
			got, err := w.Store.Get(r.ID)
			if err != nil || got.ExitCode != tc.code {
				t.Fatalf("%+v %v", got, err)
			}
			b, err := os.ReadFile(w.Store.Path(r.ID, "stdout"))
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "binary" && !bytes.Equal(b, []byte{0xef, 0xbb, 0xbf, ' ', 0, 0xff, '\n'}) {
				t.Fatalf("bytes %v", b)
			}
			if tc.name == "success" && !bytes.HasPrefix(b, []byte("env")) {
				t.Fatalf("env %q", b)
			}
			// Even restoring an old starting record cannot replay the exclusive marker.
			if err = w.Store.Put(rec); err != nil {
				t.Fatal(err)
			}
			if err = Execute(context.Background(), w.Store, r.ID); !os.IsExist(err) {
				t.Fatalf("replay: %v", err)
			}
		})
	}
}

func TestRsyncCorrectness(t *testing.T) {
	w := worker(t, 1)
	r := request(t, "true")
	r.Spec.Exclude = []string{"cache/"}
	src := filepath.Join(r.Source, "input")
	os.WriteFile(src, []byte("AAAA"), 0755)
	os.WriteFile(filepath.Join(r.Source, "deleted"), []byte("delete"), 0600)
	rec, err := w.Prepare(r)
	if err != nil {
		t.Fatal(err)
	}
	transport := &Transport{}
	if err = transport.Sync(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(rec.Workspace, "input")
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(rec.Workspace, "cache"), 0700)
	cache := filepath.Join(rec.Workspace, "cache", "dep")
	os.WriteFile(cache, []byte("preserve"), 0600)
	// Reproduce D4: same size and restored mtime must still transfer the change.
	os.WriteFile(src, []byte("BBBB"), 0755)
	os.Chtimes(src, info.ModTime(), info.ModTime())
	os.Remove(filepath.Join(r.Source, "deleted"))
	os.WriteFile(filepath.Join(r.Source, "added"), []byte("new"), 0600)
	os.Chmod(src, 0644)
	if err = transport.Sync(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(dst)
	if err != nil || string(b) != "BBBB" {
		t.Fatalf("%q %v", b, err)
	}
	if _, err = os.Stat(filepath.Join(rec.Workspace, "deleted")); !os.IsNotExist(err) {
		t.Fatal("deletion missing")
	}
	if _, err = os.Stat(cache); err != nil {
		t.Fatal("cache deleted")
	}
	info, err = os.Stat(dst)
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatal("mode not synchronized")
	}
}

func TestWorkerRPC(t *testing.T) {
	w := worker(t, 1)
	socket := endpoint(t, w.Handler())
	rpc := NewRPC(socket)
	defer rpc.Close()
	ctx := context.Background()
	r := request(t, "true")
	var rec Record
	if err := rpc.Call(ctx, "POST", "/jobs", r, &rec); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte{0, 0xff, '\n'}, 400000)
	if err := os.WriteFile(w.Store.Path(r.ID, "stdout"), data, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	offset := int64(0)
	for {
		n, err := rpc.Log(ctx, r.ID, "stdout", offset, &out)
		if err != nil {
			t.Fatal(err)
		}
		offset += n
		if n == 0 {
			break
		}
	}
	if !bytes.Equal(data, out.Bytes()) {
		t.Fatal("logs changed")
	}
	if _, err := rpc.Log(ctx, r.ID, "invalid", 0, &out); err == nil {
		t.Fatal("invalid stream")
	}
	if _, err := rpc.Log(ctx, r.ID, "stdout", -1, &out); err == nil {
		t.Fatal("invalid offset")
	}
	var h Health
	if err := rpc.Call(ctx, "GET", "/health?source="+url.QueryEscape(r.Source)+"&requires=missing-tool-relay-jobs", nil, &h); err != nil || len(h.Missing) != 1 || !h.Warm || h.Busy != 1 {
		t.Fatalf("%+v %v", h, err)
	}
	if err := rpc.Call(ctx, "GET", "/jobs/"+r.ID, nil, &rec); err != nil {
		t.Fatal(err)
	}
	if err := rpc.Call(ctx, "POST", "/jobs/"+r.ID+"/cancel", nil, &rec); err != nil {
		t.Fatal(err)
	}
	if err := rpc.Call(ctx, "GET", "/jobs/"+NewID(), nil, &rec); err == nil {
		t.Fatal("missing job")
	}
}

func TestCoordinatorSchedulingAndRecovery(t *testing.T) {
	nativeManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := worker(t, 1), worker(t, 1)
	as, bs := endpoint(t, a.Handler()), endpoint(t, b.Handler())
	paths := Paths{State: t.TempDir(), Cache: t.TempDir()}
	config := Config{Workers: []WorkerConfig{{Name: "a", Socket: as, Labels: []string{"linux"}}, {Name: "b", Socket: bs, Labels: []string{"linux"}}}}
	c := NewCoordinator(paths, config, testBinary)
	defer c.Close()
	r := request(t, "sh", "-c", "printf exact; sleep .2")
	r.Spec.Labels = []string{"linux"}
	os.MkdirAll(filepath.Join(b.Cache, "workspaces", WorkspaceKey(r.Source)), 0700)
	rec, err := c.Submit(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Submit(ctx, r); err != nil {
		t.Fatal(err)
	}
	rec = wait(t, func() (Record, error) { return c.Store.Get(r.ID) }, "succeeded")
	if rec.Worker != "b" {
		t.Fatalf("warm worker not preferred: %+v", rec)
	}
	output, _ := os.ReadFile(c.Store.Path(r.ID, "stdout"))
	if string(output) != "exact" {
		t.Fatalf("%q", output)
	}
	// Recovery adopts the same accepted worker ID instead of launching a replacement job.
	second := r
	second.ID = NewID()
	second.Spec.Command = []string{"sh", "-c", "printf once; sleep .5"}
	if _, err = c.Submit(ctx, second); err != nil {
		t.Fatal(err)
	}
	wait(t, func() (Record, error) { return c.Store.Get(second.ID) }, "running")
	c.Close()
	resumed := NewCoordinator(paths, config, testBinary)
	defer resumed.Close()
	if err = resumed.Start(ctx); err != nil {
		t.Fatal(err)
	}
	rec = wait(t, func() (Record, error) { return resumed.Store.Get(second.ID) }, "succeeded")
	if rec.Worker != "b" {
		t.Fatal(rec)
	}
	// Offline strict jobs fail, and incompatible toolchains never trigger local fallback.
	offline := NewCoordinator(Paths{State: t.TempDir()}, Config{Workers: []WorkerConfig{{Name: "down", Socket: "/missing/relay-jobs.sock"}}}, testBinary)
	defer offline.Close()
	for _, policy := range []string{"fail", "local"} {
		req := request(t, "printf", "fallback")
		req.Spec.Unavailable = policy
		if _, err = offline.Submit(ctx, req); err != nil {
			t.Fatal(err)
		}
		want := "failed"
		if policy == "local" {
			want = "succeeded"
		}
		got := wait(t, func() (Record, error) { return offline.Store.Get(req.ID) }, want)
		if policy == "local" && got.Worker != "local" {
			t.Fatal(got)
		}
	}
	bad := request(t, "true")
	bad.Spec.Requires = []string{"nonexistent-relay-jobs-tool"}
	bad.Spec.Unavailable = "local"
	if _, err = resumed.Submit(ctx, bad); err != nil {
		t.Fatal(err)
	}
	rec = wait(t, func() (Record, error) { return resumed.Store.Get(bad.ID) }, "failed")
	if !strings.Contains(rec.Error, "missing tools") {
		t.Fatal(rec)
	}
}

func TestConcurrentReservation(t *testing.T) {
	w := worker(t, 8)
	r := request(t, "true")
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := r
			req.ID = NewID()
			_, err := w.Prepare(req)
			if err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if success != 1 {
		t.Fatalf("reservations %d", success)
	}
}

func TestCommandPathUsesJobEnvironment(t *testing.T) {
	w := worker(t, 1)
	req := request(t, "custom-tool")
	req.Spec.Cwd = "sub"
	req.Spec.Env = map[string]string{"PATH": "tools"}
	rec, err := w.Prepare(req)
	if err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(rec.Workspace, "sub/tools")
	os.MkdirAll(tools, 0700)
	os.WriteFile(filepath.Join(tools, "custom-tool"), []byte("#!/bin/sh\nprintf custom-path"), 0755)
	rec.State = "starting"
	if err = w.Store.Put(rec); err != nil {
		t.Fatal(err)
	}
	if err = Execute(context.Background(), w.Store, req.ID); err != nil {
		t.Fatal(err)
	}
	result, err := w.Store.Get(req.ID)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("%+v %v", result, err)
	}
	data, err := os.ReadFile(w.Store.Path(req.ID, "stdout"))
	if err != nil || string(data) != "custom-path" {
		t.Fatalf("%q %v", data, err)
	}
	if got, err := resolveExecutable("./tool", rec.Workspace, ""); err != nil || got != "./tool" {
		t.Fatal(got, err)
	}
	os.Chmod(filepath.Join(tools, "custom-tool"), 0600)
	if _, err = resolveExecutable("custom-tool", rec.Workspace, tools); err == nil {
		t.Fatal("non-executable command accepted")
	}
}

func TestTransportUnavailable(t *testing.T) {
	transport := &Transport{Config: WorkerConfig{Name: "down", Host: "127.0.0.1", Socket: "/missing"}}
	defer transport.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := transport.Health(ctx, Request{}); err == nil {
		t.Fatal("unavailable worker accepted")
	}
	// JSON rejects unsupported fields instead of silently accepting misspelled options.
	w := worker(t, 1)
	socket := endpoint(t, w.Handler())
	rpc := NewRPC(socket)
	defer rpc.Close()
	err := rpc.Call(ctx, "POST", "/jobs", json.RawMessage(`{"unknown":true}`), nil)
	var api *APIError
	if !errors.As(err, &api) || api.Status != 400 {
		t.Fatalf("%v", err)
	}
}

func TestMissingSSHIsReportedAndLogSizeErrors(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	transport := &Transport{Config: WorkerConfig{Host: "worker", Socket: "/missing"}}
	defer transport.Close()
	var api *APIError
	if _, err := transport.Connect(context.Background()); !errors.As(err, &api) || api.Status != 422 {
		t.Fatalf("missing SSH: %v", err)
	}
	w := worker(t, 1)
	rpc := NewRPC(endpoint(t, w.Handler()))
	defer rpc.Close()
	if _, err := rpc.LogSize(context.Background(), NewID(), "stdout"); err == nil {
		t.Fatal("missing log size appeared valid")
	}
	if _, err := rpc.LogSize(context.Background(), "bad\nID", "stdout"); err == nil {
		t.Fatal("invalid size request accepted")
	}
}
