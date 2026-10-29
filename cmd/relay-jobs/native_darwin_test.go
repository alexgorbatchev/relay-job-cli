package main

import (
	"os/exec"
	"testing"

	"github.com/alexgorbatchev/relay-jobs-cli/internal/launchd"
)

func requireNativeManager(t *testing.T) {
	t.Helper()
	if err := exec.Command("/bin/launchctl", "print", launchd.Domain()).Run(); err != nil {
		t.Fatalf("native launchd user domain required: %v", err)
	}
}
