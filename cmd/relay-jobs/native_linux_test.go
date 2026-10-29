package main

import (
	"os/exec"
	"testing"
)

func requireNativeManager(t *testing.T) {
	t.Helper()
	if err := exec.Command("systemctl", "--user", "show-environment").Run(); err != nil {
		t.Skip("requires user systemd")
	}
}
