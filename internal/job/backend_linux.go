package job

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

func Unit(id string) string { return "relay-job-task-" + id + ".service" }

func runCommand(ctx context.Context, s Store, r Record) (int, error) {
	return runDirectCommand(ctx, s, r)
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
