package job

import (
	"context"
	"io"
	"testing"
	"time"
)

func TestSyncLockSurvivesReservationExpiry(t *testing.T) {
	w := worker(t, 1)
	req := request(t, "true")
	rec, err := w.Prepare(req)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in, writer := io.Pipe()
	defer in.Close()
	defer writer.Close()
	done := make(chan error, 1)
	// A real receiver waits for its peer's protocol input while holding the lock.
	go func() {
		done <- SyncReceive(ctx, w.Store, req.ID, []string{"--server", "-r", ".", rec.Workspace}, in, io.Discard, io.Discard)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		guard, lockErr := w.Store.WorkspaceLock(req.Source)
		if lockErr != nil {
			break
		}
		guard.Close()
		if time.Now().After(deadline) {
			t.Fatal("receiver did not lock workspace")
		}
		time.Sleep(time.Millisecond)
	}
	rec.LeaseUntil = time.Now().Add(-time.Minute)
	if err = w.Store.Put(rec); err != nil {
		t.Fatal(err)
	}
	if got, err := w.Status(context.Background(), req.ID); err != nil || got.State != "canceled" {
		t.Fatalf("%+v %v", got, err)
	}
	next := req
	next.ID = NewID()
	if _, err = w.Prepare(next); err == nil {
		t.Fatal("expired reservation allowed a second writer during active transfer")
	}
	cancel()
	writer.Close()
	if err = <-done; err == nil {
		t.Fatal("interrupted rsync receiver reported success")
	}
	if _, err = w.Prepare(next); err != nil {
		t.Fatal("receiver did not release workspace:", err)
	}
}

func TestSyncReservationValidation(t *testing.T) {
	w := worker(t, 1)
	req := request(t, "true")
	rec, err := w.Prepare(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"--sender", ".", rec.Workspace}, {"--server", "--sender", rec.Workspace}} {
		if err := SyncReceive(context.Background(), w.Store, req.ID, args, nil, io.Discard, io.Discard); err == nil {
			t.Fatal("invalid receiver accepted")
		}
	}
	rec.LeaseUntil = time.Now().Add(-time.Minute)
	w.Store.Put(rec)
	if err = SyncReceive(context.Background(), w.Store, req.ID, []string{"--server", "-r", rec.Workspace}, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("expired receiver accepted")
	}
	if err = SyncReceive(context.Background(), w.Store, NewID(), []string{"--server", "-r", rec.Workspace}, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("unknown reservation accepted")
	}
}
