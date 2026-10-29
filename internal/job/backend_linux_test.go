package job

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func nativeManager(t *testing.T) {
	t.Helper()
	if err := exec.Command("systemctl", "--user", "show-environment").Run(); err != nil {
		t.Skip("integration requires the Linux user systemd manager")
	}
}

func TestSystemdLifecycle(t *testing.T) {
	nativeManager(t)
	ctx := context.Background()
	w := worker(t, 2)
	r := request(t, "sh", "-c", "printf started; sleep .2; printf done; exit 37")
	start(t, w, r)
	if _, err := w.Start(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	// A fresh agent object reads the running unit and final receipt from durable state.
	replacement := &Worker{Store: w.Store, Cache: w.Cache, Binary: w.Binary, Slots: 2}
	rec := wait(t, func() (Record, error) { return replacement.Status(ctx, r.ID) }, "failed")
	if rec.ExitCode != 37 {
		t.Fatal(rec)
	}
	b, _ := os.ReadFile(w.Store.Path(r.ID, "stdout"))
	if string(b) != "starteddone" {
		t.Fatalf("%q", b)
	}
	slow := request(t, "sh", "-c", "sleep 30 & wait")
	start(t, w, slow)
	wait(t, func() (Record, error) { return w.Status(ctx, slow.ID) }, "running")
	rec, err := w.Cancel(ctx, slow.ID)
	if err != nil || rec.State != "canceled" {
		t.Fatalf("%+v %v", rec, err)
	}
	out, err := exec.Command("systemctl", "--user", "show", Unit(slow.ID), "--property=ActiveState", "--value").Output()
	if err == nil && strings.TrimSpace(string(out)) == "active" {
		t.Fatal("canceled unit is still active")
	}
	// Unknown execution after a disappeared transient unit is terminal and never replayed.
	lost := request(t, "true")
	receipt, err := w.Prepare(lost)
	if err != nil {
		t.Fatal(err)
	}
	receipt.State = "running"
	w.Store.Put(receipt)
	rec, err = w.Status(ctx, lost.ID)
	if err != nil || rec.State != "lost" {
		t.Fatalf("%+v %v", rec, err)
	}
}
