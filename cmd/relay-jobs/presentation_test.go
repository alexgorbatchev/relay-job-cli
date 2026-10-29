package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
	"github.com/mattn/go-runewidth"
)

func TestGroupValidationAndScopedFlags(t *testing.T) {
	for _, group := range []string{"job", "worker", "coordinator", "service"} {
		if out, stderr, code := execute(context.Background(), group); code != 0 || out == "" || stderr != "" {
			t.Fatalf("%s: %d %q %q", group, code, out, stderr)
		}
		if _, stderr, code := execute(context.Background(), group, "nonsense"); code != 1 || !strings.Contains(stderr, "unknown command") {
			t.Fatalf("%s accepted unknown action: %d %q", group, code, stderr)
		}
	}
	for _, args := range [][]string{
		{"--cache", "/tmp/cache"}, {"job", "list", "extra"},
		{"job", "status", job.NewID(), "--state", "/tmp/state"},
		{"job", "list", "--worker-timeout", "1s"},
		{"worker", "serve", "--worker-timeout", "1s"},
	} {
		if _, stderr, code := execute(context.Background(), args...); code != 1 || stderr == "" {
			t.Fatalf("accepted %v: %d %q", args, code, stderr)
		}
	}
	// Installation flags are validated before any platform-specific work.
	for want, args := range map[string][]string{
		"--worker-timeout applies only to the coordinator role": {"--role", "worker", "--worker-timeout", "1s"},
		"--slots applies only to the worker role":               {"--role", "coordinator", "--slots", "2"},
		"role must be coordinator or worker":                    {"--role", "invalid"},
		"slots must be positive":                                {"--role", "worker", "--slots", "0"},
	} {
		if _, stderr, code := execute(context.Background(), append([]string{"service", "install"}, args...)...); code != 1 || !strings.Contains(stderr, want) {
			t.Fatalf("service install %v: %d %q, want %q", args, code, stderr, want)
		}
	}
	for _, args := range [][]string{
		{"worker", "serve", "--state", "/tmp/state", "--cache", "/tmp/cache", "--help"},
		{"coordinator", "serve", "--worker-timeout", "2s", "--help"},
		{"worker", "list", "--worker-timeout", "2s", "--help"},
		{"service", "install", "--state", "/tmp/state", "--worker-timeout", "2s", "--help"},
	} {
		if _, stderr, code := execute(context.Background(), args...); code != 0 || stderr != "" {
			t.Fatalf("rejected %v: %d %q", args, code, stderr)
		}
	}
}

func TestHelpWidthAndFailedWrites(t *testing.T) {
	for _, mode := range []string{"0", "1"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AGENT", mode)
			for _, columns := range []string{"", "1", "2", "40", "80", "invalid"} {
				t.Setenv("COLUMNS", columns)
				out, stderr, code := execute(context.Background(), "--help")
				if code != 0 || stderr != "" {
					t.Fatal(code, stderr)
				}
				if mode == "0" {
					width := humanHelpWidth()
					for _, line := range strings.Split(out, "\n") {
						if runewidth.StringWidth(line) > width {
							t.Errorf("width %d exceeded by %q", width, line)
						}
					}
				} else if !strings.Contains(out, "~/.config/relay-jobs/relay-jobs.toml") {
					t.Fatal("agent help was truncated", out)
				}
			}
			f, err := os.Create(filepath.Join(t.TempDir(), "closed"))
			if err != nil {
				t.Fatal(err)
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
			for _, args := range [][]string{{"--help"}, {"help", "job"}, {"completion", "bash", "--help"}, {"--version"}, {"skill"}, {"job"}} {
				c := newRootCommand()
				c.SetOut(f)
				c.SetErr(io.Discard)
				c.SetArgs(args)
				if code := run(c); code != 1 {
					t.Errorf("%v lost write failure: %d", args, code)
				}
			}
		})
	}
}

func TestJobListAndStatusPresentation(t *testing.T) {
	store := job.Store{Dir: t.TempDir()}
	coordinator := job.NewCoordinator(job.Paths{State: store.Dir}, job.Config{}, integrationBinary)
	defer coordinator.Close()
	socket := socketServer(t, coordinator.Handler(context.Background()))
	for _, mode := range []string{"0", "1"} {
		t.Setenv("AGENT", mode)
		out, stderr, code := execute(context.Background(), "job", "list", "--socket", socket)
		want := "No jobs.\n"
		if mode == "1" {
			want = "[]\n"
		}
		if code != 0 || stderr != "" || out != want {
			t.Fatal(mode, out, stderr, code)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().Add(-time.Hour).UTC()
	old := job.Record{Request: job.Request{ID: strings.Repeat("a", 32), Source: t.TempDir(), Spec: job.Spec{Command: []string{"true"}}}, State: "failed", ExitCode: 7, Created: created, Updated: created.Add(1250 * time.Millisecond), Error: "open " + filepath.Join(home, "missing"), LogsExpired: true}
	newer := old
	newer.ID, newer.Created, newer.State, newer.Error, newer.LogsExpired = strings.Repeat("b", 32), created.Add(time.Minute), "running", "", false
	tied := newer
	tied.ID = strings.Repeat("c", 32)
	for _, r := range []job.Record{tied, old, newer} {
		if err := job.WriteJSON(store.Path(r.ID, "record.json"), r); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"0", "1"} {
		t.Setenv("AGENT", mode)
		out, stderr, code := execute(context.Background(), "job", "list", "--socket", socket)
		if code != 0 || stderr != "" {
			t.Fatal(out, stderr, code)
		}
		if strings.Index(out, newer.ID) > strings.Index(out, tied.ID) || strings.Index(out, tied.ID) > strings.Index(out, old.ID) {
			t.Fatal("jobs not newest first", out)
		}
		if strings.Contains(out, home) {
			t.Fatal("home path leaked", out)
		}
		if mode == "1" {
			var rows []jobReceipt
			if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 3 || rows[2].Duration != "1.25s" || rows[2].ExitCode != 7 || !rows[2].LogsExpired {
				t.Fatal(out, err)
			}
		} else if !strings.Contains(out, "Duration: 1.25s") || !strings.Contains(out, "Logs: expired") {
			t.Fatal(out)
		}
		out, stderr, code = execute(context.Background(), "job", "status", old.ID, "--socket", socket)
		if code != 0 || stderr != "" || !strings.Contains(out, "1.25s") || strings.Contains(out, home) {
			t.Fatal(out, stderr, code)
		}
		f, err := os.Create(filepath.Join(t.TempDir(), "closed"))
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		for _, args := range [][]string{{"job", "list"}, {"job", "status", old.ID}} {
			c := newRootCommand()
			c.SetOut(f)
			c.SetErr(io.Discard)
			c.SetArgs(append(args, "--socket", socket))
			if code := run(c); code != 1 {
				t.Fatal("output error lost", args, code)
			}
		}
	}
	if err := os.WriteFile(store.Path(old.ID, "record.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := execute(context.Background(), "job", "list", "--socket", socket); code != 1 || stderr == "" {
		t.Fatal("corrupt receipt ignored", code, stderr)
	}
}

func TestReceiptDuration(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name, state      string
		created, updated time.Time
		want             string
	}{
		{"active", "running", now.Add(-3 * time.Second), now.Add(-time.Second), "3s"},
		{"terminal", "succeeded", now.Add(-3 * time.Second), now.Add(-time.Second), "2s"},
		{"missing time", "running", time.Time{}, now, "0s"},
		{"future time", "running", now.Add(time.Second), now, "0s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := job.Record{State: tc.state, Created: tc.created, Updated: tc.updated}
			if got := receipt(r, now).Duration; got != tc.want {
				t.Fatal(got, tc.want)
			}
		})
	}
}

func TestWorkerAndDiagnosticPathFormatting(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, mode := range []string{"0", "1"} {
		t.Setenv("AGENT", mode)
		c := newRootCommand()
		var out bytes.Buffer
		c.SetOut(&out)
		workers := []workerReceipt{{Name: "build", Health: job.Health{Protocol: 1, Slots: 2, Busy: 1, Warm: true, Missing: []string{"go"}}, Error: "open " + filepath.Join(home, "socket")}}
		if err := printWorkers(c, workers); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), home) || !strings.Contains(out.String(), "~/socket") {
			t.Fatal(out.String())
		}
		if mode == "1" {
			var got []workerReceipt
			if err := json.Unmarshal(out.Bytes(), &got); err != nil || len(got) != 1 || got[0].Health.Busy != 1 {
				t.Fatal(out.String(), err)
			}
		} else if !strings.Contains(out.String(), "MISSING TOOLS") || !strings.Contains(out.String(), "│ build") {
			t.Fatal(out.String())
		}
		out.Reset()
		diagnosticLogger(&out).Error("cleanup", "error", fmt.Errorf("open %s", filepath.Join(home, "record")))
		if strings.Contains(out.String(), home) || !strings.Contains(out.String(), "~/record") {
			t.Fatal(out.String())
		}
	}
}

func TestWorkerTable(t *testing.T) {
	t.Setenv("AGENT", "0")
	c := newRootCommand()
	var out bytes.Buffer
	c.SetOut(&out)
	workers := []workerReceipt{
		{Name: "linux", Health: job.Health{Protocol: 1, Slots: 2, Busy: 1, Warm: true, Missing: []string{"go", "rsync"}}},
		{Name: "macos", Error: "context deadline exceeded"},
	}
	if err := printWorkers(c, workers); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	width := runewidth.StringWidth(lines[0])
	var rows [][]string
	for _, line := range lines {
		if runewidth.StringWidth(line) != width {
			t.Fatalf("misaligned table: %s", out.String())
		}
		if strings.HasPrefix(line, "│ linux ") || strings.HasPrefix(line, "│ macos ") {
			cells := strings.Split(line, "│")[1:7]
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
			}
			rows = append(rows, cells)
		}
	}
	if len(rows) != 2 || strings.Join(rows[0], "|") != "linux|1|1/2|—|go, rsync|unavailable" || strings.Join(rows[1], "|") != "macos|—|—|—|—|unreachable: context deadline exceeded" {
		t.Fatalf("worker fields or order lost: %s", out.String())
	}
	out.Reset()
	workers[1].Error = strings.Repeat("connection ", 12) + "retry later"
	if err := printWorkers(c, workers); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "retry later") || strings.Count(out.String(), "connection") != 12 {
		t.Fatalf("wrapped error lost content: %s", out.String())
	}
	for _, mode := range []string{"0", "1"} {
		t.Setenv("AGENT", mode)
		out.Reset()
		if err := printWorkers(c, nil); err != nil {
			t.Fatal(err)
		}
		want := "No workers configured.\n"
		if mode == "1" {
			want = "[]\n"
		}
		if out.String() != want {
			t.Fatal(out.String())
		}
		f, err := os.CreateTemp(t.TempDir(), "closed")
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		c.SetOut(f)
		if err := printWorkers(c, workers); err == nil {
			t.Fatal("lost output write failure")
		}
		c.SetOut(&out)
	}
}

func TestWorkerStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		health job.Health
		err    string
		want   string
	}{
		{"idle", job.Health{Protocol: job.Protocol, Slots: 2}, "", "ready"},
		{"occupied", job.Health{Protocol: job.Protocol, Slots: 2, Busy: 1}, "", "working"},
		{"full", job.Health{Protocol: job.Protocol, Slots: 2, Busy: 2}, "", "working"},
		{"probe failure", job.Health{}, "context deadline exceeded", "unreachable"},
		{"protocol mismatch", job.Health{Protocol: job.Protocol + 1, Slots: 2}, "worker protocol mismatch", "incompatible"},
		{"no capacity", job.Health{Protocol: job.Protocol}, "", "unavailable"},
		{"missing tool", job.Health{Protocol: job.Protocol, Slots: 2, Missing: []string{"rsync"}}, "", "unavailable"},
		{"no health", job.Health{}, "", "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := workerStatus(workerReceipt{Health: tc.health, Error: tc.err}); got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}
