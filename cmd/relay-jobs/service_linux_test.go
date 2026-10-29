package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
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

func TestServiceInstall(t *testing.T) {
	if err := exec.Command("systemctl", "--user", "show-environment").Run(); err != nil {
		t.Skip("requires user systemd")
	}
	dir, err := os.MkdirTemp("", "rj-install-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data $percent% space"))
	tools := filepath.Join(dir, "tools $percent% \"quote\"")
	os.MkdirAll(tools, 0700)
	os.WriteFile(filepath.Join(tools, "relay-jobs-test-tool"), []byte("#!/bin/sh\nexit 0\n"), 0755)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	name := "relay-jobs-test-" + job.NewID() + ".service"
	unitPath := filepath.Join(configHome, "systemd/user", name)
	t.Cleanup(func() {
		exec.Command("systemctl", "--user", "stop", name).Run()
		exec.Command("systemctl", "--user", "disable", name).Run()
		os.Remove(unitPath)
		exec.Command("systemctl", "--user", "daemon-reload").Run()
	})
	socket := filepath.Join(dir, "api")
	state := filepath.Join(dir, "state")
	store := job.Store{Dir: state}
	old := job.Record{Request: job.Request{ID: job.NewID(), Source: t.TempDir(), Spec: job.Spec{Command: []string{"true"}}}, State: "succeeded", LogsCollected: true, Updated: time.Now().Add(-2 * time.Hour)}
	if err := job.WriteJSON(store.Path(old.ID, "record.json"), old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(old.ID, "stdout"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := execute(context.Background(), "--state", state, "--cache", filepath.Join(dir, "cache"), "--socket", socket, "service", "install", "--role", "worker", "--name", name, "--binary", integrationBinary, "--slots", "2", "--log-retention=1h", "--workspace-retention=0", "--cleanup-interval=10ms")
	if code != 0 || !strings.Contains(out, "active") {
		t.Fatalf("%d %s %s", code, out, stderr)
	}
	rpc := job.NewRPC(socket)
	defer rpc.Close()
	var h job.Health
	for i := 0; i < 100; i++ {
		err = rpc.Call(context.Background(), "GET", "/health?requires=relay-jobs-test-tool", nil, &h)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || h.Slots != 2 || len(h.Missing) != 0 {
		t.Fatalf("%+v %v", h, err)
	}
	for i := 0; i < 100; i++ {
		got, err := store.Get(old.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.LogsExpired {
			break
		}
		if i == 99 {
			t.Fatal("installed retention flags did not expire the old log")
		}
		time.Sleep(10 * time.Millisecond)
	}
	info, err := os.Stat(socket)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatal("installed service exposed its socket to other users")
	}
	for _, args := range [][]string{{"--role", "worker", "--name", "../bad"}, {"--role", "worker", "--binary", "/missing/rj-binary"}} {
		if _, _, code = execute(context.Background(), append([]string{"service", "install"}, args...)...); code == 0 {
			t.Fatal(args)
		}
	}
}

func TestInstalledServiceReloadsTOML(t *testing.T) {
	if err := exec.Command("systemctl", "--user", "show-environment").Run(); err != nil {
		t.Skip("requires user systemd")
	}
	path, cfg := configFixture(t)
	if err := writeTOML(path, cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_DATA_HOME", filepath.Join(filepath.Dir(path), "data"))
	name := "relay-jobs-config-" + job.NewID() + ".service"
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		configHome = filepath.Join(home, ".config")
	}
	t.Cleanup(func() {
		exec.Command("systemctl", "--user", "stop", name).Run()
		exec.Command("systemctl", "--user", "disable", name).Run()
		os.Remove(filepath.Join(configHome, "systemd/user", name))
		exec.Command("systemctl", "--user", "daemon-reload").Run()
	})
	if _, stderr, code := execute(context.Background(), "service", "install", "--role", "worker", "--name", name, "--binary", integrationBinary, "--config", path); code != 0 {
		t.Fatal(code, stderr)
	}
	for _, slots := range []int{2, 4} {
		if slots == 4 {
			cfg.Worker.Slots = new(slots)
			if err := writeTOML(path, cfg); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("systemctl", "--user", "restart", name).CombinedOutput(); err != nil {
				t.Fatal(string(out), err)
			}
		}
		rpc := job.NewRPC(cfg.Paths.Socket)
		var h job.Health
		deadline := time.Now().Add(5 * time.Second)
		for {
			if err := rpc.Call(context.Background(), "GET", "/health", nil, &h); err == nil && h.Slots == slots {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("installed service ignored TOML", h)
			}
			time.Sleep(10 * time.Millisecond)
		}
		rpc.Close()
	}
}
