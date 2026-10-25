package job

import (
	"context"
	"testing"
)

func TestEmptyContainersRemainIdempotentAfterPersistence(t *testing.T) {
	req := request(t, "true")
	req.Spec.Env = map[string]string{}
	req.Spec.Exclude = []string{}
	req.Spec.Requires = []string{}
	req.Spec.Labels = []string{}
	req.Spec.Workers = []string{}
	w := worker(t, 1)
	if _, err := w.Prepare(req); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Prepare(req); err != nil {
		t.Fatal("identical empty containers rejected after JSON round trip:", err)
	}
	c := NewCoordinator(Paths{State: t.TempDir()}, Config{}, testBinary)
	defer c.Close()
	if _, err := c.Submit(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Submit(context.Background(), req); err != nil {
		t.Fatal("duplicate coordinator submission rejected:", err)
	}
	changed := req
	changed.Spec.Env = map[string]string{"CHANGED": "value"}
	if _, err := c.Submit(context.Background(), changed); err == nil {
		t.Fatal("changed environment reused an existing execution ID")
	}
}
