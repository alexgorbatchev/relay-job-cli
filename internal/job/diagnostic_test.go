package job

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSSHFailureClassification(t *testing.T) {
	var truncated *APIError
	if err := sshFailure(diagnosticOmitted + "Connection closed by peer"); !errors.As(err, &truncated) {
		t.Fatal("truncated diagnostics allowed fallback")
	}
	for _, tc := range []struct {
		message       string
		configuration bool
	}{
		{"Connection refused", false}, {"Connection timed out", false}, {"No route to host", false}, {"Network is unreachable", false}, {"Connection reset by peer", false}, {"Connection closed by peer", false}, {"Broken pipe", false},
		{"Permission denied (publickey).\nConnection closed by UNKNOWN port 65535", true}, {"Host key verification failed", true}, {"Could not resolve hostname", true}, {"unrecognized SSH failure", true},
	} {
		err := sshFailure(tc.message)
		var api *APIError
		if errors.As(err, &api) != tc.configuration {
			t.Fatalf("%q classified as %v", tc.message, err)
		}
	}
}

func TestSyncDiagnosticsAreBoundedAndDrained(t *testing.T) {
	cmd := exec.Command("sh", "-c", "printf '%s' \"$DIAGNOSTIC\" >&2; exit 23")
	cmd.Env = append(os.Environ(), "DIAGNOSTIC="+strings.Repeat("diagnostic ", 4000))
	err := runSyncCommand(cmd)
	if err == nil || !strings.Contains(err.Error(), "exit status 23") || !strings.Contains(err.Error(), "omitted") || len(err.Error()) > maxDiagnosticBytes+200 {
		t.Fatalf("unbounded or missing sync diagnostic: %v", err)
	}
	if err = runSyncCommand(exec.Command("true")); err != nil {
		t.Fatal(err)
	}
	if err = runSyncCommand(exec.Command("/missing/relay-job-rsync")); err == nil {
		t.Fatal("missing sync binary appeared successful")
	}
}

func TestDiagnosticStreamKeepsBoundedTail(t *testing.T) {
	var b diagnosticBuffer
	want := ""
	for range 100 {
		chunk := strings.Repeat("streamed diagnostic", 7)
		want += chunk
		if n, err := b.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatal(n, err)
		}
	}
	if len(b.data) != maxDiagnosticBytes || !strings.HasSuffix(b.String(), want[len(want)-maxDiagnosticBytes:]) {
		t.Fatal("diagnostic stream grew without bound or lost its tail")
	}
	b.Reset()
	if b.String() != "" {
		t.Fatal("reset retained stale diagnostics")
	}
}
