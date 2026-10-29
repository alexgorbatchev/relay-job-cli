package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSubmissionDiagnosticsCannotHideFailure(t *testing.T) {
	source := t.TempDir()
	f, err := os.Create(filepath.Join(t.TempDir(), "closed"))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	c := newRootCommand()
	c.SetErr(f)
	c.SetArgs([]string{"job", "exec", "--source", source, "--", "true"})
	if code := run(c); code != 1 {
		t.Fatal(code)
	}
	for _, args := range [][]string{{"--state", "", "job", "exec", "--", "true"}, {"job", "run", "example", "--source", source, "--project", "missing.json"}} {
		if _, _, code := execute(context.Background(), args...); code == 0 {
			t.Fatal(args)
		}
	}
}
