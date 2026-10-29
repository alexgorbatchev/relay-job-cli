package deps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	upstreamBanner  = "rsync  version 3.5.1  protocol version 33\nCopyright (C) 1996-2026 by Andrew Tridgell, Wayne Davison, and others.\n"
	openrsyncBanner = "openrsync: protocol version 29\nrsync version 2.6.9 compatible\n"
)

// fakeRsync puts an executable rsync printing banner first on an isolated PATH.
func fakeRsync(t *testing.T, banner string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("AGENT", "")
	t.Setenv("PATH", dir)
	if banner == "" {
		return
	}
	script := "#!/bin/sh\nprintf '%s' '" + banner + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "rsync"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestParseRsync(t *testing.T) {
	for output, want := range map[string]string{
		upstreamBanner:             "3.5.1",
		openrsyncBanner:            "2.6.9",
		"rsync version v3.2.7\n":   "3.2.7",
		"openrsync: protocol 29\n": "",
		"":                         "",
	} {
		if got := parseRsync("rsync", output); got != want {
			t.Errorf("parseRsync(%q) = %q, want %q", output, got, want)
		}
	}
}

func TestVerify(t *testing.T) {
	fakeRsync(t, upstreamBanner)
	if err := Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(os.Getenv("XDG_DATA_HOME"), AppName, "bin")
	if !strings.HasPrefix(os.Getenv("PATH"), managed+string(os.PathListSeparator)) {
		t.Fatalf("managed directory not prepended: %s", os.Getenv("PATH"))
	}
	for banner, want := range map[string]string{
		openrsyncBanner: "version 2.6.9) is outdated, must be version 3.0.0 or newer",
		"":              "rsync is missing in $PATH",
	} {
		fakeRsync(t, banner)
		if err := Verify(context.Background()); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("banner %q: %v, want %q", banner, err, want)
		}
	}
}

func TestEnsure(t *testing.T) {
	fakeRsync(t, upstreamBanner)
	var out strings.Builder
	if err := Ensure(context.Background(), strings.NewReader(""), &out); err != nil || out.Len() != 0 {
		t.Fatalf("satisfied dependency: %v %q", err, out.String())
	}
	fakeRsync(t, openrsyncBanner)
	if err := Ensure(context.Background(), strings.NewReader("n\n"), &out); err == nil || !strings.Contains(out.String(), "[y/N]") {
		t.Fatalf("declined installation: %v %q", err, out.String())
	}
	// No package manager is reachable on the isolated PATH.
	out.Reset()
	if err := Ensure(context.Background(), strings.NewReader("y\n"), &out); err == nil || !strings.Contains(err.Error(), "no supported package manager") {
		t.Fatalf("accepted installation: %v %q", err, out.String())
	}
	out.Reset()
	t.Setenv("AGENT", "yes")
	if err := Ensure(context.Background(), strings.NewReader("y\n"), &out); err == nil || out.Len() != 0 {
		t.Fatalf("agent mode prompted: %v %q", err, out.String())
	}
}
