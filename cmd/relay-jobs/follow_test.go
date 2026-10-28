package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
)

func TestFollowCancellationAndLargeOutput(t *testing.T) {
	ctx := context.Background()
	w := &job.Worker{Store: job.Store{Dir: t.TempDir()}, Cache: t.TempDir(), Slots: 1, Binary: integrationBinary}
	req := job.Request{ID: job.NewID(), Source: t.TempDir(), Spec: job.Spec{Command: []string{"true"}}}
	rec, err := w.Prepare(req)
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte{0, 0xff, '\n'}, 3_000_000)
	os.WriteFile(w.Store.Path(req.ID, "stdout"), data, 0600)
	os.WriteFile(w.Store.Path(req.ID, "stderr"), []byte("exact err"), 0600)
	rec.State = "succeeded"
	rec.ExitCode = 0
	w.Store.Put(rec)
	socket := socketServer(t, w.Handler())
	for _, wait := range []bool{false, true} {
		args := []string{"--socket", socket, "job", "logs", req.ID}
		if wait {
			args = append(args, "--follow")
		}
		out, stderr, code := execute(ctx, args...)
		if code != 0 || !bytes.Equal([]byte(out), data) || stderr != "exact err" {
			t.Fatalf("large follow: %d bytes=%d stderr=%q", code, len(out), stderr)
		}
	}
	// Cancel while an RPC is in flight, as well as while waiting between polls.
	for _, delay := range []time.Duration{0, 20 * time.Millisecond} {
		rec.State = "prepared"
		w.Store.Put(rec)
		followCtx, cancel := context.WithCancel(ctx)
		go func() { time.Sleep(delay); cancel() }()
		rpc := job.NewRPC(socket)
		cmd := newRootCommand()
		cmd.SetContext(followCtx)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err = follow(cmd, rpc, req.ID, true)
		rpc.Close()
		if err != exitError(130) {
			t.Fatalf("signal cancellation: %v", err)
		}
	}
	rpc := job.NewRPC("/missing/relay-jobs.sock")
	defer rpc.Close()
	c := newRootCommand()
	c.SetContext(ctx)
	if err = follow(c, rpc, req.ID, true); err == nil || !strings.Contains(err.Error(), "reconnect") {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	c.SetContext(canceled)
	if err = follow(c, rpc, req.ID, true); err == nil || !strings.Contains(err.Error(), "cancellation unconfirmed") {
		t.Fatal(err)
	}
	if exitError(37).Error() != "job exited 37" {
		t.Fatal("exit diagnostic lost command status")
	}
}
