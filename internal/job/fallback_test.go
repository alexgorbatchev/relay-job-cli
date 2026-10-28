package job

import (
	"context"
	"testing"
	"time"
)

func TestLocalFallbackQueuesAndCancels(t *testing.T) {
	systemd(t)
	ctx := context.Background()
	c := NewCoordinator(Paths{State: t.TempDir()}, Config{Workers: []WorkerConfig{{Name: "offline", Socket: "/missing/relay-jobs.sock"}}}, testBinary)
	defer c.Close()
	first := request(t, "sleep", "3")
	first.Spec.Unavailable = "local"
	if _, err := c.Submit(ctx, first); err != nil {
		t.Fatal(err)
	}
	wait(t, func() (Record, error) { return c.Store.Get(first.ID) }, "running")
	second := request(t, "printf", "queued fallback")
	second.Spec.Unavailable = "local"
	if _, err := c.Submit(ctx, second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	queued, err := c.Store.Get(second.ID)
	if err != nil || queued.State != "queued" {
		t.Fatalf("%+v %v", queued, err)
	}
	if _, err = c.Cancel(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	got := wait(t, func() (Record, error) { return c.Store.Get(second.ID) }, "succeeded")
	if got.Worker != "local" {
		t.Fatal(got)
	}
}
