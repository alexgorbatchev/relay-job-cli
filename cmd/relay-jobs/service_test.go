package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
)

func TestServeCommands(t *testing.T) {
	requireNativeManager(t)
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
