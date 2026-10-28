package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallerFilesystemFailures(t *testing.T) {
	for _, mode := range []string{"data parent file", "data read only", "target directory", "config parent file", "unit directory", "missing systemctl", "unsupported executable path"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "data read only" && os.Geteuid() == 0 {
				t.Skip("permission faults require an unprivileged user")
			}
			dir := t.TempDir()
			data, config := filepath.Join(dir, "data"), filepath.Join(dir, "config")
			t.Setenv("XDG_DATA_HOME", data)
			t.Setenv("XDG_CONFIG_HOME", config)
			name := "relay-jobs-failure-test.service"
			switch mode {
			case "unsupported executable path":
				t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "quotes\""))
			case "data parent file":
				os.WriteFile(data, nil, 0600)
			case "data read only":
				bin := filepath.Join(data, "relay-jobs/bin")
				os.MkdirAll(bin, 0700)
				os.Chmod(bin, 0500)
				defer os.Chmod(bin, 0700)
			case "target directory":
				os.MkdirAll(filepath.Join(data, "relay-jobs/bin/relay-jobs"), 0700)
			case "config parent file":
				os.WriteFile(config, nil, 0600)
			case "unit directory":
				os.MkdirAll(filepath.Join(config, "systemd/user", name), 0700)
			case "missing systemctl":
				t.Setenv("PATH", dir)
			}
			_, _, code := execute(context.Background(), "service", "install", "--role", "worker", "--name", name, "--binary", integrationBinary)
			if code == 0 {
				t.Fatalf("installer ignored %s", mode)
			}
		})
	}
}

func TestSubmissionDiagnosticsCannotHideFailure(t *testing.T) {
	source := t.TempDir()
	f, err := os.Create(filepath.Join(t.TempDir(), "closed"))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	c := newRootCommand()
	c.SetErr(f)
	c.SetArgs([]string{"job", "exec", "--source", source, "--", "true"})
	if code := run(c); code != 1 {
		t.Fatal(code)
	}
	for _, args := range [][]string{{"--state", "", "job", "exec", "--", "true"}, {"job", "run", "example", "--source", source, "--project", "missing.json"}} {
		if _, _, code := execute(context.Background(), args...); code == 0 {
			t.Fatal(args)
		}
	}
}
