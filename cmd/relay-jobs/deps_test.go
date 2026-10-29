package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
)

func TestServicesRequireUpstreamRsync(t *testing.T) {
	tools := t.TempDir()
	script := "#!/bin/sh\nprintf 'openrsync: protocol version 29\\nrsync version 2.6.9 compatible\\n'\n"
	if err := os.WriteFile(filepath.Join(tools, "rsync"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools)
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
	daemon := []string{"--config", config, "--socket", socket, "--state", filepath.Join(dir, "state"), "--cache", filepath.Join(dir, "cache")}
	for _, args := range [][]string{
		append([]string{"worker", "serve"}, daemon...),
		append([]string{"coordinator", "serve"}, daemon...),
		{"sync", "serve", "--state", filepath.Join(dir, "state"), "--id", job.NewID(), "--", "--server", "."},
	} {
		_, stderr, code := execute(context.Background(), args...)
		if code != 1 || !strings.Contains(stderr, "rsync version 2.6.9 in $PATH is outdated") {
			t.Errorf("%v: %d %q", args, code, stderr)
		}
		if _, err = os.Stat(socket); !os.IsNotExist(err) {
			t.Fatalf("%v served before verifying rsync: %v", args, err)
		}
	}
}
