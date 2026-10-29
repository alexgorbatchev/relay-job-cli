package job

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func Unit(id string) string { return "relay-jobs-task-" + id + ".service" }

func runCommand(ctx context.Context, s Store, r Record) (int, error) {
	out, err := os.OpenFile(s.Path(r.ID, "stdout"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return 125, err
	}
	defer out.Close()
	stderr, err := os.OpenFile(s.Path(r.ID, "stderr"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return 125, err
	}
	defer stderr.Close()
	dir, program, err := prepareCommand(r)
	if err != nil {
		return 125, err
	}
	cmd := exec.CommandContext(ctx, program, r.Spec.Command[1:]...)
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.Stderr = stderr
	cmd.Env = os.Environ()
	for k, v := range r.Spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 5 * time.Second
	err = cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	} // terminate remaining descendants before releasing the workspace.
	if syncErr := out.Sync(); syncErr != nil {
		return 125, syncErr
	}
	if syncErr := stderr.Sync(); syncErr != nil {
		return 125, syncErr
	}
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code := exit.ExitCode()
		if code < 0 {
			code = 128 + int(exit.Sys().(syscall.WaitStatus).Signal())
		}
		return code, nil
	}
	return 125, err
}

func startExecution(ctx context.Context, binary string, s Store, id string) ([]byte, error) {
	return exec.CommandContext(ctx, "systemd-run", "--user", "--quiet", "--collect", "--expand-environment=no", "--service-type=exec", "--unit="+Unit(id), "--setenv=PATH="+os.Getenv("PATH"), "--property=TimeoutStopSec=5s", "--property=KillMode=control-group", binary, "execution", "run", "--state", s.Dir, "--id", id).CombinedOutput()
}

func executionActive(ctx context.Context, _ Store, id string) (bool, error) {
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "show", Unit(id), "--property=ActiveState", "--property=LoadState").Output()
	active := string(out)
	return strings.Contains(active, "ActiveState=active\n") || strings.Contains(active, "ActiveState=activating\n") || strings.Contains(active, "ActiveState=deactivating\n"), err
}

func stopExecution(ctx context.Context, _ Store, id string) ([]byte, error) {
	return exec.CommandContext(ctx, "systemctl", "--user", "stop", Unit(id)).CombinedOutput()
}

func finishExecution(context.Context, Store, string) error { return nil }
