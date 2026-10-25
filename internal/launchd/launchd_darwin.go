// Package launchd manages independent native macOS execution jobs.
package launchd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type Spec struct {
	Label, Directory, Stdout, Stderr string
	Arguments                        []string
	Environment                      map[string]string
	KeepAlive                        bool
}

type State struct {
	Exists, Active, Exited bool
	ExitCode               int
}

func Domain() string { return "user/" + strconv.Itoa(os.Getuid()) }

func invoke(ctx context.Context, command string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", command, err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// Write delegates property-list encoding and validation to Apple's native plutil.
func Write(ctx context.Context, path string, spec Spec) error {
	if _, err := invoke(ctx, "/usr/bin/plutil", "-create", "xml1", path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0600); err != nil {
		return err
	}
	fields := map[string]any{
		"Label": spec.Label, "ProgramArguments": spec.Arguments, "RunAtLoad": true,
		"KeepAlive": spec.KeepAlive, "AbandonProcessGroup": false, "ExitTimeOut": 10,
		"Umask": 63, "ProcessType": "Background", "EnvironmentVariables": spec.Environment,
		"LimitLoadToSessionType": "Background",
	}
	for key, value := range map[string]string{"WorkingDirectory": spec.Directory, "StandardOutPath": spec.Stdout, "StandardErrorPath": spec.Stderr} {
		if value != "" {
			fields[key] = value
		}
	}
	for key, value := range fields {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if _, err = invoke(ctx, "/usr/bin/plutil", "-insert", key, "-json", string(encoded), path); err != nil {
			return err
		}
	}
	_, err := invoke(ctx, "/usr/bin/plutil", "-lint", path)
	return err
}

func Start(ctx context.Context, path string) ([]byte, error) {
	return invoke(ctx, "/bin/launchctl", "bootstrap", Domain(), path)
}

func Inspect(ctx context.Context, label string) (State, error) {
	out, err := exec.CommandContext(ctx, "/bin/launchctl", "print", Domain()+"/"+label).CombinedOutput()
	if ctx.Err() != nil {
		return State{}, ctx.Err()
	}
	if err != nil {
		if strings.Contains(string(out), "Could not find service") {
			return State{}, nil
		}
		return State{}, fmt.Errorf("inspect launchd job: %w: %s", err, out)
	}
	state := State{Exists: true, Active: true}
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), " = ")
		if !ok {
			continue
		}
		switch key {
		case "state":
			state.Active = value != "not running" && value != "exited"
		case "last exit code":
			if value == "(never exited)" {
				continue
			}
			code, parseErr := strconv.Atoi(value)
			if parseErr != nil {
				return State{}, fmt.Errorf("invalid launchd exit status %q: %w", value, parseErr)
			}
			state.Exited, state.ExitCode = true, code
		case "last terminating signal":
			_, number, found := strings.Cut(value, ": ")
			if !found {
				return State{}, fmt.Errorf("invalid launchd terminating signal %q", value)
			}
			code, parseErr := strconv.Atoi(number)
			if parseErr != nil {
				return State{}, fmt.Errorf("invalid launchd terminating signal %q: %w", value, parseErr)
			}
			state.Exited, state.ExitCode = true, 128+code
		}
	}
	return state, nil
}

func Stop(ctx context.Context, label string) ([]byte, error) {
	state, err := Inspect(ctx, label)
	if err != nil || !state.Exists {
		return nil, err
	}
	out, err := invoke(ctx, "/bin/launchctl", "bootout", Domain()+"/"+label)
	if err != nil {
		current, inspectErr := Inspect(ctx, label)
		if inspectErr == nil && !current.Exists {
			return out, nil
		}
		return out, errors.Join(err, inspectErr)
	}
	return out, nil
}
