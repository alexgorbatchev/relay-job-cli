package job

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Transport struct {
	Config      WorkerConfig
	Timeout     time.Duration
	once        sync.Once
	gate        chan struct{}
	rpc         *RPC
	cmd         *exec.Cmd
	done        chan struct{}
	output      diagnosticBuffer
	dir, master string
}

func (t *Transport) lock(ctx context.Context) error {
	t.once.Do(func() { t.gate = make(chan struct{}, 1) })
	select {
	case t.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (t *Transport) unlock() { <-t.gate }

func (t *Transport) Connect(ctx context.Context) (*RPC, error) {
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.healthTimeout())
		defer cancel()
	}
	if err := t.lock(ctx); err != nil {
		return nil, err
	}
	defer t.unlock()
	if t.Config.Host == "" {
		if t.rpc == nil {
			t.rpc = NewRPC(t.Config.Socket)
		}
		return t.rpc, nil
	}
	if t.cmd != nil {
		select {
		case <-t.done:
			t.cleanup()
		default:
			return t.rpc, nil
		}
	}
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	dir, err := os.MkdirTemp(base, "rj-")
	if err != nil {
		return nil, err
	}
	t.dir = dir
	t.master = filepath.Join(dir, "ssh")
	socket := filepath.Join(dir, "api")
	t.output.Reset()
	deadline, _ := ctx.Deadline()
	seconds := max(1, int64(time.Until(deadline)/time.Second)+1)
	args := []string{"-M", "-S", t.master, "-N", "-o", "BatchMode=yes", "-o", "ConnectTimeout=" + strconv.FormatInt(seconds, 10), "-o", "ConnectionAttempts=1", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=1", "-o", "ExitOnForwardFailure=yes", "-L", socket + ":" + t.Config.Socket, t.Config.Host}
	t.cmd = exec.Command("ssh", args...)
	t.cmd.Env = append(os.Environ(), "LC_ALL=C")
	t.cmd.Stderr = &t.output
	if err = t.cmd.Start(); err != nil {
		t.cleanup()
		return nil, &APIError{422, "SSH configuration: " + err.Error()}
	}
	t.done = make(chan struct{})
	cmd := t.cmd
	done := t.done
	go func() { cmd.Wait(); close(done) }()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-t.done:
			message := strings.TrimSpace(t.output.String())
			t.cleanup()
			return nil, sshFailure(message)
		case <-ctx.Done():
			t.stop()
			return nil, ctx.Err()
		case <-tick.C:
			if _, err = os.Stat(socket); err == nil {
				t.rpc = NewRPC(socket)
				return t.rpc, nil
			}
		}
	}
}
func sshFailure(message string) error {
	if strings.HasPrefix(message, diagnosticOmitted) {
		return &APIError{422, "SSH startup diagnostics were truncated: " + message}
	}
	for _, cause := range []string{"Permission denied", "Host key verification", "HOST IDENTIFICATION HAS CHANGED", "Bad configuration", "Could not resolve hostname", "Too many authentication failures"} {
		if strings.Contains(message, cause) {
			return &APIError{422, "SSH configuration: " + message}
		}
	}
	for _, cause := range []string{"Connection refused", "Connection timed out", "No route to host", "Network is unreachable", "Connection reset", "Connection closed", "Broken pipe"} {
		if strings.Contains(message, cause) {
			return fmt.Errorf("worker unavailable: %s", message)
		}
	}
	return &APIError{422, "SSH configuration: " + message}
}
func (t *Transport) cleanup() {
	if t.rpc != nil {
		t.rpc.Close()
	}
	if t.dir != "" {
		os.RemoveAll(t.dir)
	}
	t.rpc = nil
	t.cmd = nil
	t.dir = ""
}
func (t *Transport) stop() {
	if t.cmd != nil {
		t.cmd.Process.Kill()
		<-t.done
	}
	t.cleanup()
}
func (t *Transport) Close() {
	_ = t.lock(context.Background())
	defer t.unlock()
	t.stop()
}
func (t *Transport) healthTimeout() time.Duration {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return WorkerTimeout
}
func (t *Transport) Health(ctx context.Context, r Request) (Health, error) {
	return t.health(ctx, r, t.healthTimeout())
}
func (t *Transport) health(ctx context.Context, r Request, timeout time.Duration) (Health, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var h Health
	rpc, err := t.Connect(ctx)
	if err != nil {
		return h, err
	}
	q := url.Values{"source": {r.Source}, "requires": r.Spec.Requires}
	err = rpc.WithTimeout(0).Call(ctx, "GET", "/health?"+q.Encode(), nil, &h)
	if err == nil && h.Protocol != Protocol {
		return h, &APIError{422, "worker protocol mismatch"}
	}
	return h, err
}
func (t *Transport) Call(ctx context.Context, method, path string, input, output any) error {
	rpc, err := t.Connect(ctx)
	if err != nil {
		return err
	}
	return rpc.Call(ctx, method, path, input, output)
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func (t *Transport) Sync(ctx context.Context, r Record) error {
	args := []string{"-a", "--checksum", "--delete-delay", "--protect-args", "--exclude=.git/"}
	for _, x := range r.Spec.Exclude {
		args = append(args, "--exclude="+x)
	}
	dest := r.Workspace + "/"
	if t.Config.Host != "" {
		remote := []string{r.SyncBinary, "sync", "serve", "--state", r.SyncState, "--id", r.ID, "--"}
		for i, arg := range remote {
			remote[i] = shellQuote(arg)
		}
		args = append(args, "--rsync-path="+strings.Join(remote, " "))
		if err := t.lock(ctx); err != nil {
			return err
		}
		master := t.master
		t.unlock()
		args = append(args, "-e", "ssh -o BatchMode=yes -o ConnectTimeout=2 -S "+shellQuote(master))
		dest = t.Config.Host + ":" + dest
	}
	var guard *os.File
	if t.Config.Host == "" {
		var err error
		guard, _, err = syncGuard(Store{r.SyncState}, r.ID)
		if err != nil {
			return err
		}
		defer guard.Close()
	}
	args = append(args, "--", r.Source+"/", dest)
	cmd := exec.CommandContext(ctx, "rsync", args...)
	if guard != nil {
		cmd.ExtraFiles = []*os.File{guard}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	return runSyncCommand(cmd)
}

func runSyncCommand(cmd *exec.Cmd) error {
	cmd.Stdout = io.Discard
	var diagnostic diagnosticBuffer
	cmd.Stderr = &diagnostic
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sync failed: %w: %s", err, strings.TrimSpace(diagnostic.String()))
	}
	return nil
}
