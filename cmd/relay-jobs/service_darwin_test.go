package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceInstallRequiresLinux(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	_, stderr, code := execute(context.Background(), "service", "install", "--role", "worker", "--binary", integrationBinary)
	if code != 1 || !strings.Contains(stderr, "service install requires Linux") {
		t.Fatalf("%d %q", code, stderr)
	}
	for _, path := range []string{filepath.Join(dir, "data"), filepath.Join(dir, "config")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unsupported installation wrote %s: %v", path, err)
		}
	}
}
