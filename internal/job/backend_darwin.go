package job

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexgorbatchev/relay-jobs-cli/internal/launchd"
)

func Unit(id string) string        { return "com.relay-jobs.task." + id }
func commandUnit(id string) string { return Unit(id) + ".command" }

func startExecution(ctx context.Context, binary string, s Store, id string) ([]byte, error) {
	if _, err := exec.LookPath(binary); err != nil {
		return nil, err
	}
	path := s.Path(id, "execution.plist")
	spec := launchd.Spec{Label: Unit(id), Arguments: []string{binary, "execution", "run", "--state", s.Dir, "--id", id}, Environment: map[string]string{"PATH": os.Getenv("PATH"), "HOME": os.Getenv("HOME")}, Stdout: s.Path(id, "execution.stdout"), Stderr: s.Path(id, "execution.stderr")}
	if err := launchd.Write(ctx, path, spec); err != nil {
		return nil, err
	}
	return launchd.Start(ctx, path)
}

func executionActive(ctx context.Context, _ Store, id string) (bool, error) {
	state, err := launchd.Inspect(ctx, Unit(id))
	if err != nil || state.Active {
		return state.Active, err
	}
	// A killed execution supervisor cannot leave its independently supervised command running.
	_, err = launchd.Stop(ctx, commandUnit(id))
	return false, err
}

func stopExecution(ctx context.Context, s Store, id string) ([]byte, error) {
	runnerOut, runnerErr := launchd.Stop(ctx, Unit(id))
	// Stop the creator first so it cannot submit a command after cancellation's cleanup.
	commandOut, commandErr := launchd.Stop(ctx, commandUnit(id))
	out := append(runnerOut, commandOut...)
	if err := errors.Join(runnerErr, commandErr); err != nil {
		return out, err
	}
	return out, awaitSupervisorExit(ctx, s, id)
}

// Bootout returns while the signaled supervisor may still hold the workspace.
// Its execution lock is released only after the workspace lock and final receipt.
func awaitSupervisorExit(ctx context.Context, s Store, id string) error {
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()
	for {
		lock, err := Lock(s.Path(id, "execution.lock"))
		if err == nil {
			return lock.Close()
		}
		if !lockBusy(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("execution supervisor still exiting: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func finishExecution(ctx context.Context, s Store, id string) error {
	_, err := stopExecution(ctx, s, id)
	return err
}

func runCommand(ctx context.Context, s Store, r Record) (code int, resultErr error) {
	dir, program, err := prepareCommand(r)
	if err != nil {
		return 125, err
	}
	if !filepath.IsAbs(program) {
		program = filepath.Join(dir, program)
	}
	if _, err = exec.LookPath(program); err != nil {
		return 125, err
	}
	env := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	for key, value := range r.Spec.Env {
		env[key] = value
	}
	for _, name := range []string{"stdout", "stderr"} {
		if err := os.WriteFile(s.Path(r.ID, name), nil, 0600); err != nil {
			return 125, err
		}
	}
	label := commandUnit(r.ID)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := launchd.Stop(cleanup, label); err != nil {
			code, resultErr = 125, errors.Join(resultErr, err)
		}
		for _, name := range []string{"stdout", "stderr"} {
			file, err := os.OpenFile(s.Path(r.ID, name), os.O_RDWR, 0)
			if err != nil {
				code, resultErr = 125, errors.Join(resultErr, err)
				continue
			}
			if err = errors.Join(file.Sync(), file.Close()); err != nil {
				code, resultErr = 125, errors.Join(resultErr, err)
			}
		}
	}()
	path := s.Path(r.ID, "command.plist")
	spec := launchd.Spec{Label: label, Arguments: append([]string{program}, r.Spec.Command[1:]...), Directory: dir, Environment: env, Stdout: s.Path(r.ID, "stdout"), Stderr: s.Path(r.ID, "stderr")}
	if err = launchd.Write(ctx, path, spec); err != nil {
		return 125, err
	}
	if _, err = launchd.Start(ctx, path); err != nil {
		return 125, err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := launchd.Inspect(ctx, label)
		if err != nil {
			return 125, err
		}
		if !state.Exists {
			return 125, fmt.Errorf("command job disappeared without an exit status")
		}
		if !state.Active && state.Exited {
			return state.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return 130, ctx.Err()
		case <-ticker.C:
		}
	}
}
