package job

import (
	"context"
	"net/http/httptest"
	"os"
	"testing"
)

func TestPreparedReceiptCannotBecomeUndurableStart(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission faults require an unprivileged user")
	}
	w := worker(t, 1)
	req := request(t, "true")
	rec, err := w.Prepare(req)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := recordLock(w.Store.Path(req.ID, "job.lock"))
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
	os.Chmod(w.Store.Path(req.ID, ""), 0500)
	defer os.Chmod(w.Store.Path(req.ID, ""), 0700)
	if _, err = w.Start(context.Background(), req.ID); err == nil {
		t.Fatal("start proceeded without durable receipt")
	}
	if _, err = w.Prepare(req); err == nil {
		t.Fatal("renewal ignored persistence failure")
	}
	os.Chmod(w.Store.Path(req.ID, ""), 0700)
	rec.State = "starting"
	if err = w.Store.Put(rec); err != nil {
		t.Fatal(err)
	}
	guard, err := w.Store.WorkspaceLock(req.Source)
	if err != nil {
		t.Fatal(err)
	}
	if err = Execute(context.Background(), w.Store, req.ID); err == nil {
		t.Fatal("execution ignored active sync lock")
	}
	guard.Close()
	rec.State = "prepared"
	w.Store.Put(rec)
	lock, err = w.Store.WorkspaceLock(req.Source)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = syncGuard(w.Store, req.ID); err == nil {
		t.Fatal("receiver ignored sync lock")
	}
	lock.Close()
}

func TestUnreadableAndTruncatedLogs(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission faults require an unprivileged user")
	}
	w := worker(t, 1)
	req := request(t, "true")
	if _, err := w.Prepare(req); err != nil {
		t.Fatal(err)
	}
	path := w.Store.Path(req.ID, "stdout")
	os.WriteFile(path, []byte("bytes"), 0000)
	defer os.Chmod(path, 0600)
	response := httptest.NewRecorder()
	w.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/jobs/"+req.ID+"/log?stream=stdout&offset=0", nil))
	if response.Code != 500 {
		t.Fatal("unreadable log appeared successful")
	}
	os.Chmod(path, 0600)
	response = httptest.NewRecorder()
	w.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/jobs/"+req.ID+"/log?stream=stdout&offset=100", nil))
	if response.Code != 409 {
		t.Fatal("truncated log appeared successful")
	}
	c := NewCoordinator(Paths{State: t.TempDir()}, Config{Workers: []WorkerConfig{{Name: "worker", Socket: endpoint(t, w.Handler())}}}, testBinary)
	defer c.Close()
	absent := Record{Request: request(t, "true"), State: "running", Worker: "worker"}
	if err := c.Store.Put(absent); err != nil {
		t.Fatal(err)
	}
	if err := c.copyLogs(context.Background(), absent); err == nil {
		t.Fatal("missing worker log record ignored")
	}
	local := NewCoordinator(Paths{State: t.TempDir()}, Config{}, testBinary)
	defer local.Close()
	absent.Worker = "local"
	local.Store.Put(absent)
	local.Local.Store.Put(absent)
	localPath := local.Local.Store.Path(absent.ID, "stdout")
	os.WriteFile(localPath, []byte("bytes"), 0000)
	defer os.Chmod(localPath, 0600)
	if err := local.copyLogs(context.Background(), absent); err == nil {
		t.Fatal("unreadable local log ignored")
	}
}
