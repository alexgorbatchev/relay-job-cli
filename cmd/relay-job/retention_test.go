package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexgorbatchev/relay-job-cli/internal/job"
)

func TestRetentionCLI(t *testing.T) {
	for _, command := range [][]string{{"worker", "serve"}, {"coordinator", "serve"}, {"service", "install", "--role", "worker"}} {
		for _, flag := range []string{"--log-retention=-1h", "--workspace-retention=-1h", "--cleanup-interval=0"} {
			args := append(append([]string{}, command...), flag)
			if _, stderr, code := execute(context.Background(), args...); code != 1 || stderr == "" {
				t.Fatal(args, code, stderr)
			}
		}
	}
	dir, err := os.MkdirTemp("", "rjgc-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	state, socket := filepath.Join(dir, "state"), filepath.Join(dir, "api")
	r := job.Record{Request: job.Request{ID: job.NewID(), Source: t.TempDir(), Spec: job.Spec{Command: []string{"true"}}}, State: "succeeded", LogsCollected: true, Updated: time.Now().Add(-2 * time.Hour)}
	store := job.Store{Dir: state}
	if err := job.WriteJSON(store.Path(r.ID, "record.json"), r); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(r.ID, "stdout"), []byte("output"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		_, stderr, code := execute(ctx, "worker", "serve", "--state", state, "--cache", filepath.Join(dir, "cache"), "--socket", socket, "--log-retention=1h", "--workspace-retention=0", "--cleanup-interval=10ms")
		if code != 0 {
			t.Error(stderr)
		}
		done <- code
	}()
	t.Cleanup(func() {
		cancel()
		if code := <-done; code != 0 {
			t.Error(code)
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := store.Get(r.ID)
		if err == nil && got.LogsExpired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker flags did not activate cleanup")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The worker's status/log interface has the same receipt contract as the
	// coordinator. Exercise the consumer's CLI against it after expiry.
	out, stderr, code := execute(context.Background(), "job", "status", r.ID, "--socket", socket)
	var receipt struct {
		LogsExpired bool `json:"logs_expired"`
	}
	if err := json.Unmarshal([]byte(out), &receipt); err != nil || !receipt.LogsExpired || code != 0 {
		t.Fatal(out, stderr, code, err)
	}
	if _, stderr, code := execute(context.Background(), "job", "logs", r.ID, "--socket", socket); code != 1 || !strings.Contains(stderr, "expired") {
		t.Fatal(code, stderr)
	}
}
