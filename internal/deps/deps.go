// Package deps declares and verifies the external executables relay-jobs runs.
package deps

import (
	"context"
	"io"
	"os/exec"
	"regexp"

	"github.com/alexgorbatchev/godeps"
)

// AppName selects the managed executable directory prepended to PATH.
const AppName = "relay-jobs"

// openrsync reports its upstream compatibility level on a later line, which
// identifies it as too old instead of leaving its version unparseable.
var rsyncVersion = regexp.MustCompile(`(?m)^rsync\s+version\s+v?(\d+(?:\.\d+)+)`)

func parseRsync(_, output string) string {
	if match := rsyncVersion.FindStringSubmatch(output); match != nil {
		return match[1]
	}
	return ""
}

func manager() *godeps.Manager {
	return godeps.New(godeps.Config{AppName: AppName, Dependencies: []godeps.Dependency{{
		Name: "rsync",
		// Source synchronization requires --protect-args and --delete-delay.
		MinVersion:   "3.0.0",
		InstallURL:   "https://rsync.samba.org",
		ParseVersion: parseRsync,
		Installer:    godeps.SystemPackageManager("rsync"),
	}}})
}

// Ensure verifies services' executables and lets an interactive user install
// missing or outdated ones. Agent mode never prompts.
func Ensure(ctx context.Context, in io.Reader, out io.Writer) error {
	return manager().Ensure(ctx, godeps.EnsureOptions{Input: in, Output: out})
}

// Rsync resolves the rsync that Ensure verified, using the same PATH lookup.
// LookPath rejects relative PATH matches, so a resolved path is absolute.
func Rsync() (string, error) {
	return exec.LookPath("rsync")
}
