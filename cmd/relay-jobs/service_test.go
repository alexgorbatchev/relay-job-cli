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

func TestServeCommands(t *testing.T) {
	if err := exec.Command("systemctl", "--user", "show-environment").Run(); err != nil {
		t.Skip("requires user systemd")
	}
	w := &job.Worker{Store: job.Store{Dir: t.TempDir()}, Cache: t.TempDir(), Binary: integrationBinary, Slots: 1}
	workerSocket := socketServer(t, w.Handler())
	config := filepath.Join(t.TempDir(), "relay-jobs.toml")
	if err := writeTOML(config, job.Config{Workers: []job.WorkerConfig{{Name: "test", Socket: workerSocket}}}); err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"worker", "coordinator"} {
		t.Run(role, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "rj-serve-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "api")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan int, 1)
			go func() {
				_, stderr, code := execute(ctx, "--socket", socket, "--state", filepath.Join(dir, "state"), "--cache", filepath.Join(dir, "cache"), "--config", config, role, "serve")
				if code != 0 {
					t.Error(stderr)
				}
				done <- code
			}()
			for i := 0; i < 1000; i++ {
				if _, err = os.Stat(socket); err == nil {
					break
				}
				time.Sleep(time.Millisecond)
			}
			rpc := job.NewRPC(socket)
			defer rpc.Close()
			path := "/health"
			if role == "coordinator" {
				path = "/workers"
			}
			var reply any
			if err = rpc.Call(context.Background(), "GET", path, nil, &reply); err != nil {
				t.Fatal(err)
			}
			cancel()
			if code := <-done; code != 0 {
				t.Fatal(code)
			}
		})
	}
	for _, args := range [][]string{{"worker", "serve", "--slots", "0"}, {"coordinator", "serve", "--config", "/missing/rj-config"}, {"execution", "run", "--id", "bad"}} {
		if _, _, code := execute(context.Background(), args...); code == 0 {
			t.Fatal(args)
		}
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
	for _, args := range [][]string{{"--role", "invalid"}, {"--role", "worker", "--slots", "0"}, {"--role", "worker", "--name", "../bad"}, {"--role", "worker", "--binary", "/missing/rj-binary"}} {
		if _, _, code = execute(context.Background(), append([]string{"service", "install"}, args...)...); code == 0 {
			t.Fatal(args)
		}
	}
}
