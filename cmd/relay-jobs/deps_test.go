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

// rsyncTools returns a directory whose rsync prints banner.
func rsyncTools(t *testing.T, banner string) string {
	t.Helper()
	tools := t.TempDir()
	script := "#!/bin/sh\nprintf '" + banner + "'\n"
	if err := os.WriteFile(filepath.Join(tools, "rsync"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return tools
}

func TestServicesRequireUpstreamRsync(t *testing.T) {
	t.Setenv("PATH", rsyncTools(t, `openrsync: protocol version 29\nrsync version 2.6.9 compatible\n`))
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("AGENT", "1")
	dir, err := os.MkdirTemp("", "rj-deps-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	config := filepath.Join(dir, "relay-jobs.toml")
	if err = writeTOML(config, job.Config{Workers: []job.WorkerConfig{{Name: "test", Socket: filepath.Join(dir, "worker.sock")}}}); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "api")
	for _, role := range []string{"worker", "coordinator"} {
		_, stderr, code := execute(context.Background(), role, "serve", "--config", config, "--socket", socket, "--state", filepath.Join(dir, "state"), "--cache", filepath.Join(dir, "cache"))
		if code != 1 || !strings.Contains(stderr, "rsync version 2.6.9 in $PATH is outdated") {
			t.Errorf("%s: %d %q", role, code, stderr)
		}
		if _, err = os.Stat(socket); !os.IsNotExist(err) {
			t.Fatalf("%s served before verifying rsync: %v", role, err)
		}
	}
}

func TestWorkerReservesVerifiedRsync(t *testing.T) {
	tools := rsyncTools(t, `rsync  version 3.5.1  protocol version 33\n`)
	t.Setenv("PATH", tools)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("AGENT", "1")
	dir, err := os.MkdirTemp("", "rj-deps-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "api")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan string, 1)
	go func() {
		_, stderr, _ := execute(ctx, "worker", "serve", "--socket", socket, "--state", filepath.Join(dir, "state"), "--cache", filepath.Join(dir, "cache"))
		done <- stderr
	}()
	defer func() {
		cancel()
		if stderr := <-done; stderr != "" {
			t.Error(stderr)
		}
	}()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if _, err = os.Stat(socket); err == nil {
			break
		}
	}
	rpc := job.NewRPC(socket)
	defer rpc.Close()
	var rec job.Record
	req := job.Request{ID: job.NewID(), Source: t.TempDir(), Spec: job.Spec{Command: []string{"true"}}}
	if err = rpc.Call(ctx, "POST", "/jobs", req, &rec); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(tools, "rsync"); rec.SyncRsync != want {
		t.Fatalf("reservation rsync %q, want %q", rec.SyncRsync, want)
	}
}
