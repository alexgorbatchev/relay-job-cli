package launchd

import (
	"fmt"
	"strconv"
	"strings"
)

type State struct {
	Exists, Active, Exited bool
	ExitCode               int
}

// A print dump also contains coalition and environment dictionaries with their
// own state keys. Only the job's direct properties describe command execution.
func parseState(output string) (State, error) {
	state := State{Exists: true, Active: true}
	indent := ""
	found := false
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		prefix := line[:len(line)-len(trimmed)]
		if prefix == "" || strings.TrimSpace(trimmed) == "" {
			continue
		}
		if indent == "" {
			indent = prefix
		}
		if prefix != indent {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimSpace(line), " = ")
		if !ok {
			continue
		}
		switch key {
		case "state":
			found = true
			state.Active = value != "not running" && value != "exited"
		case "last exit code":
			if value == "(never exited)" {
				continue
			}
			code, err := exitNumber(value)
			if err != nil {
				return State{}, err
			}
			state.Exited, state.ExitCode = true, code
		case "last terminating signal":
			_, number, ok := strings.Cut(value, ": ")
			if !ok {
				return State{}, fmt.Errorf("invalid launchd terminating signal %q", value)
			}
			code, err := exitNumber(number)
			if err != nil {
				return State{}, err
			}
			state.Exited, state.ExitCode = true, 128+code
		}
	}
	if !found {
		return State{}, fmt.Errorf("launchctl did not report the job's state")
	}
	return state, nil
}

func exitNumber(value string) (int, error) {
	// launchctl annotates sysexits values, for example "78: EX_CONFIG".
	number, _, _ := strings.Cut(value, ":")
	code, err := strconv.Atoi(strings.TrimSpace(number))
	if err != nil {
		return 0, fmt.Errorf("invalid launchd exit status %q: %w", value, err)
	}
	return code, nil
}
