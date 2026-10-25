package job

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/alexgorbatchev/relay-job-cli/internal/launchd"
)

func darwinWorker(t *testing.T) *Worker {
	t.Helper()
	if err := exec.Command("/bin/launchctl", "print", launchd.Domain()).Run(); err != nil {
		t.Fatalf("native launchd user domain required: %v", err)
	}
	w := &Worker{Store: Store{Dir: t.TempDir()}, Cache: t.TempDir(), Binary: testBinary, Slots: 2}
	return w
}

func darwinPrepare(t *testing.T, w *Worker, script string) Record {
	t.Helper()
	r, err := w.Prepare(Request{ID: NewID(), Source: t.TempDir(), Spec: Spec{Command: []string{"/bin/sh", "-c", script}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := stopExecution(ctx, w.Store, r.ID); err != nil {
			t.Error(err)
		}
	})
	if _, err := w.Start(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	return r
}

func darwinTerminal(t *testing.T, w *Worker, id string) Record {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		r, err := w.Status(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Terminal() {
			return r
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("native task did not reach a terminal state")
	return Record{}
}

func TestDarwinExecutionAndRecovery(t *testing.T) {
	w := darwinWorker(t)
	r := darwinPrepare(t, w, "printf start; sleep 2; printf end; printf error >&2; printf x >> marker; exit 7")
	// Replace the API agent while the independent execution and command jobs run.
	replacement := &Worker{Store: w.Store, Cache: w.Cache, Binary: w.Binary, Slots: w.Slots}
	if _, err := replacement.Start(context.Background(), r.ID); err != nil {
		t.Fatal(err)
	}
	final := darwinTerminal(t, replacement, r.ID)
	if final.State != stateFailed || final.ExitCode != 7 {
		t.Fatalf("unexpected completion: %+v", final)
	}
	for path, want := range map[string]string{w.Store.Path(r.ID, "stdout"): "startend", w.Store.Path(r.ID, "stderr"): "error", filepath.Join(r.Workspace, "marker"): "x"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, error %v; want %q", path, got, err, want)
		}
	}
}

func darwinChild(t *testing.T, r Record) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join(r.Workspace, "child.pid"))
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			return pid
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("command child never started")
	return 0
}

func darwinChildGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("command descendant %d survived native supervision cleanup", pid)
}

func TestDarwinCancellationAndSupervisorLoss(t *testing.T) {
	for _, loss := range []bool{false, true} {
		t.Run(strconv.FormatBool(loss), func(t *testing.T) {
			w := darwinWorker(t)
			r := darwinPrepare(t, w, "sleep 120 & echo $! > child.pid; wait")
			pid := darwinChild(t, r)
			want := stateCanceled
			if loss {
				if out, err := exec.Command("/bin/launchctl", "kill", "SIGKILL", launchd.Domain()+"/"+Unit(r.ID)).CombinedOutput(); err != nil {
					t.Fatalf("kill execution supervisor: %v: %s", err, out)
				}
				want = stateLost
			} else if _, err := w.Cancel(context.Background(), r.ID); err != nil {
				t.Fatal(err)
			}
			final := darwinTerminal(t, w, r.ID)
			if final.State != want {
				t.Fatalf("state %s, want %s: %+v", final.State, want, final)
			}
			darwinChildGone(t, pid)
		})
	}
}
