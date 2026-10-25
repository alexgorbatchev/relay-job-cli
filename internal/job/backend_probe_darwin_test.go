package job

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexgorbatchev/relay-job-cli/internal/launchd"
)

func TestDarwinCompletedNativeJobState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	label := "com.relay-job.probe." + NewID()
	dir := t.TempDir()
	marker := filepath.Join(dir, "done")
	plist := filepath.Join(dir, "job.plist")
	if err := launchd.Write(ctx, plist, launchd.Spec{Label: label, Arguments: []string{"/usr/bin/touch", marker}}); err != nil {
		t.Fatal(err)
	}
	if _, err := launchd.Start(ctx, plist); err != nil {
		t.Fatal(err)
	}
	defer launchd.Stop(context.Background(), label)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state, err := launchd.Inspect(ctx, label)
		if err != nil {
			t.Fatal(err)
		}
		if !state.Active && state.Exited {
			if _, err := os.Stat(marker); err != nil || state.ExitCode != 0 {
				t.Fatal(state, err)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	out, err := exec.CommandContext(ctx, "/bin/launchctl", "print", launchd.Domain()+"/"+label).CombinedOutput()
	state, inspectErr := launchd.Inspect(ctx, label)
	_, markerErr := os.Stat(marker)
	t.Fatalf("completed native job stuck: parsed=%+v inspect=%v marker=%v print=%v\n%s", state, inspectErr, markerErr, err, out)
}
