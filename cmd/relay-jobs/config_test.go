package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
)

func configFixture(t *testing.T) (string, job.Config) {
	t.Helper()
	dir, err := os.MkdirTemp("", "rjcfg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "relay-jobs.toml"), job.Config{Paths: job.PathConfig{State: filepath.Join(dir, "state"), Cache: filepath.Join(dir, "cache"), Socket: filepath.Join(dir, "api")}, Worker: job.WorkerSettings{Slots: new(2)}, Retention: job.RetentionSettings{Logs: new("1h"), Workspaces: new("0"), Interval: new("10ms")}}
}

func TestWorkerDiscoversDefaultConfiguration(t *testing.T) {
	for _, xdg := range []bool{false, true} {
		t.Run(map[bool]string{false: "home", true: "xdg"}[xdg], func(t *testing.T) {
			// Keep the default socket below Darwin's sockaddr_un path limit.
			dir, err := os.MkdirTemp("/tmp", "rj-default-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(dir) })
			t.Setenv("HOME", dir)
			configBase, stateBase := filepath.Join(dir, ".config"), filepath.Join(dir, ".local/state")
			for _, name := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
				t.Setenv(name, "")
			}
			if xdg {
				configBase, stateBase = filepath.Join(dir, "config"), filepath.Join(dir, "state")
				t.Setenv("XDG_CONFIG_HOME", configBase)
				t.Setenv("XDG_STATE_HOME", stateBase)
				t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
			}
			configDir := filepath.Join(configBase, "relay-jobs")
			if err := os.MkdirAll(configDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := writeTOML(filepath.Join(configDir, "relay-jobs.toml"), job.Config{Worker: job.WorkerSettings{Slots: new(3)}}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, stderr, code := execute(ctx, "worker", "serve")
				if code != 0 {
					t.Errorf("worker failed: %d %s", code, stderr)
				}
			}()
			t.Cleanup(func() { cancel(); <-done })
			rpc := job.NewRPC(filepath.Join(stateBase, "relay-jobs-worker", "worker.sock"))
			defer rpc.Close()
			deadline := time.Now().Add(5 * time.Second)
			for {
				var health job.Health
				if err := rpc.Call(ctx, "GET", "/health", nil, &health); err == nil {
					if health.Slots != 3 {
						t.Fatalf("default configuration was not loaded: %+v", health)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("worker did not start on its default socket")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestTOMLWorkerAndFlagPrecedence(t *testing.T) {
	for _, override := range []bool{false, true} {
		t.Run(map[bool]string{false: "toml", true: "flags"}[override], func(t *testing.T) {
			path, cfg := configFixture(t)
			if err := writeTOML(path, cfg); err != nil {
				t.Fatal(err)
			}
			args := []string{"worker", "serve", "--config", path}
			wantSlots := 2
			if override {
				args = append(args, "--slots", "3", "--log-retention=0", "--socket", filepath.Join(filepath.Dir(path), "override.sock"))
				cfg.Paths.Socket = filepath.Join(filepath.Dir(path), "override.sock")
				wantSlots = 3
			}
			store := job.Store{Dir: cfg.Paths.State}
			r := job.Record{Request: job.Request{ID: job.NewID(), Source: t.TempDir(), Spec: job.Spec{Command: []string{"true"}}}, State: "succeeded", LogsCollected: true, Updated: time.Now().Add(-2 * time.Hour)}
			if err := job.WriteJSON(store.Path(r.ID, "record.json"), r); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.Path(r.ID, "stdout"), []byte("keep or expire"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan int, 1)
			go func() {
				_, stderr, code := execute(ctx, args...)
				if code != 0 {
					t.Error(stderr)
				}
				done <- code
			}()
			t.Cleanup(func() { cancel(); <-done })
			rpc := job.NewRPC(cfg.Paths.Socket)
			defer rpc.Close()
			var h job.Health
			deadline := time.Now().Add(5 * time.Second)
			for {
				if err := rpc.Call(ctx, "GET", "/health", nil, &h); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("TOML worker did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if h.Slots != wantSlots {
				t.Fatal(h)
			}
			for {
				got, err := store.Get(r.ID)
				if err != nil {
					t.Fatal(err)
				}
				if override {
					if got.LogsExpired {
						t.Fatal("TOML beat --log-retention=0")
					}
					break
				}
				if got.LogsExpired {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("TOML retention did not run")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestTOMLErrorsAndRoleDefaults(t *testing.T) {
	path, cfg := configFixture(t)
	if err := os.WriteFile(path, []byte("[worker]\nslot=2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := execute(context.Background(), "--config", path, "worker", "serve"); code != 1 || !strings.Contains(stderr, "decode") {
		t.Fatal(code, stderr)
	}
	if _, _, code := execute(context.Background(), "--config", path+".missing", "job", "status", job.NewID()); code != 1 {
		t.Fatal(code)
	}
	if err := writeTOML(path, cfg); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := execute(context.Background(), "--config", path, "coordinator", "serve"); code != 1 || !strings.Contains(stderr, "at least one worker") {
		t.Fatal(code, stderr)
	}
	for _, role := range []string{"worker", "coordinator"} {
		root := newRootCommand()
		cmd, _, err := root.Find([]string{role, "serve"})
		if err != nil {
			t.Fatal(err)
		}
		cmd.Flags().AddFlagSet(root.PersistentFlags())
		if err := cmd.Flags().Set("config", path+".missing"); err != nil {
			t.Fatal(err)
		}
		cmd.Flags().Lookup("config").Changed = false
		o := &options{}
		o.paths.Config = path + ".missing"
		if err := o.configure(cmd); err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(o.paths.State, role+".sock")
		if o.paths.Socket != want {
			t.Fatal(o.paths.Socket, want)
		}
		if role == "worker" && !strings.HasSuffix(o.paths.State, "relay-jobs-worker") {
			t.Fatal(o.paths.State)
		}
	}
}
