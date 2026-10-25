package job

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSSHWorker(t *testing.T) {
	host := os.Getenv("RELAY_JOB_SSH_TEST_HOST")
	socket := os.Getenv("RELAY_JOB_SSH_TEST_SOCKET")
	if host == "" || socket == "" {
		t.Skip("set RELAY_JOB_SSH_TEST_HOST and RELAY_JOB_SSH_TEST_SOCKET for real SSH integration")
	}
	transport := &Transport{Config: WorkerConfig{Name: "ssh", Host: host, Socket: socket}}
	t.Cleanup(transport.Close)
	ctx := context.Background()
	req := request(t, "printf", "ssh exact")
	os.WriteFile(filepath.Join(req.Source, "dirty"), []byte("dirty input"), 0600)
	h, err := transport.Health(ctx, req)
	if err != nil || h.Protocol != Protocol {
		t.Fatalf("%+v %v", h, err)
	}
	first, err := transport.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	again, err := transport.Connect(ctx)
	if err != nil || again != first {
		t.Fatal("SSH connection not reused")
	}
	var rec Record
	if err = transport.Call(ctx, "POST", "/jobs", req, &rec); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { transport.Call(context.Background(), "POST", "/jobs/"+req.ID+"/cancel", nil, nil) })
	if err = transport.Sync(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err = transport.Call(ctx, "POST", "/jobs/"+req.ID+"/start", nil, &rec); err != nil {
		t.Fatal(err)
	}
	wait(t, func() (Record, error) {
		var r Record
		err := transport.Call(ctx, "GET", "/jobs/"+req.ID, nil, &r)
		return r, err
	}, "succeeded")
	var out bytes.Buffer
	if _, err = first.Log(ctx, req.ID, "stdout", 0, &out); err != nil || out.String() != "ssh exact" {
		t.Fatalf("%q %v", out.String(), err)
	}
	// Kill only the connection owned by this transport and verify a new one is established.
	transport.mu.Lock()
	transport.cmd.Process.Kill()
	done := transport.done
	transport.mu.Unlock()
	<-done
	if _, err = transport.Health(ctx, req); err != nil {
		t.Fatal("SSH reconnection failed:", err)
	}
}

func TestLeaseRenewalWhileSyncing(t *testing.T) {
	for _, mode := range []string{"renew", "unavailable", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			w := worker(t, 1)
			req := request(t, "true")
			rec, err := w.Prepare(req)
			if err != nil {
				t.Fatal(err)
			}
			original := rec.LeaseUntil
			h := w.Handler()
			socket := endpoint(t, http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if mode == "unavailable" {
					http.Error(out, "worker unavailable", 503)
					return
				}
				if mode == "canceled" {
					respond(out, Record{State: "canceled"})
					return
				}
				h.ServeHTTP(out, r)
			}))
			transport := &Transport{Config: WorkerConfig{Socket: socket}}
			defer transport.Close()
			err = syncWorkspace(context.Background(), transport, rec, time.Millisecond)
			if mode != "renew" {
				if err == nil || !strings.Contains(err.Error(), "reservation") {
					t.Fatalf("expected lease failure, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := w.Store.Get(req.ID)
			if err != nil || !got.LeaseUntil.After(original) {
				t.Fatal("reservation was not renewed during sync")
			}
		})
	}
}
