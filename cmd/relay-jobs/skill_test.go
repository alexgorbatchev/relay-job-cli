package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cobrahelptree "github.com/alexgorbatchev/cobra-help-tree/v3"
	"github.com/spf13/cobra"
)

const skillAlert = "ALERT: Agents must read `AGENT=1 relay-jobs skill` before using this tool.\n"

const skillInstructions = `Install the relay-jobs skill using the Skills CLI:

  npx skills add alexgorbatchev/relay-jobs-cli --skill relay-jobs

Skill:

  https://github.com/alexgorbatchev/relay-jobs-cli/tree/main/skills/relay-jobs
`

var skillPath = filepath.Join("..", "..", "skills", "relay-jobs", "SKILL.md")

func readSkill(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("stdout write failed") }

func TestSkillCommandOutput(t *testing.T) {
	// The command must not depend on repository files or configuration.
	t.Chdir(t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "missing"))
	for _, mode := range []string{"0", "1", "true", "yes"} {
		t.Setenv("AGENT", mode)
		if out, stderr, code := execute(context.Background(), "skill"); code != 0 || out != skillInstructions || stderr != "" {
			t.Fatalf("AGENT=%s: exit %d stdout %q stderr %q", mode, code, out, stderr)
		}
		out, stderr, code := execute(context.Background(), "skill", "extra")
		if code != 1 || out != "" || !strings.Contains(stderr, `unknown command "extra" for "relay-jobs skill"`) {
			t.Fatalf("AGENT=%s: extra argument accepted: exit %d stdout %q stderr %q", mode, code, out, stderr)
		}
	}
}

func TestSkillCommandReportsWriteFailure(t *testing.T) {
	for _, mode := range []string{"0", "1"} {
		t.Setenv("AGENT", mode)
		stderr := new(bytes.Buffer)
		c := newRootCommand()
		c.SetOut(failingWriter{})
		c.SetErr(stderr)
		c.SetArgs([]string{"skill"})
		if code := run(c); code != 1 || !strings.Contains(stderr.String(), "writing the skill: stdout write failed") {
			t.Fatalf("AGENT=%s: exit %d stderr %q", mode, code, stderr.String())
		}
	}
}

func TestSkillPointerMatchesRepositorySkill(t *testing.T) {
	const prefix = "https://github.com/alexgorbatchev/relay-jobs-cli/tree/main/"
	i := strings.Index(skillInstructions, prefix)
	if i < 0 {
		t.Fatalf("skill URL missing from %q", skillInstructions)
	}
	dir := strings.TrimSpace(skillInstructions[i+len(prefix):])
	if filepath.Join("..", "..", dir, "SKILL.md") != skillPath {
		t.Fatalf("printed skill path %q does not point at %s", dir, skillPath)
	}
	if !strings.Contains(readSkill(t), "\nname: relay-jobs\n") || !strings.Contains(skillInstructions, "--skill relay-jobs\n") {
		t.Fatal("SKILL.md frontmatter name does not match the printed --skill value")
	}
}

func TestSkillDocumentsEveryCommandAndFlag(t *testing.T) {
	// Pin the environment-dependent path defaults to the ones SKILL.md documents.
	t.Setenv("HOME", t.TempDir())
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		t.Setenv(name, "")
	}
	if missing := cobrahelptree.SkillOmissions(newRootCommand(), techCatalog, readSkill(t)); len(missing) > 0 {
		t.Fatalf("SKILL.md omits:\n%s", strings.Join(missing, "\n"))
	}
}

func TestAgentHelpOpensWithSkillAlert(t *testing.T) {
	var paths [][]string
	var collect func(c *cobra.Command, path []string)
	collect = func(c *cobra.Command, path []string) {
		paths = append(paths, path)
		for _, sub := range c.Commands() {
			if sub.IsAvailableCommand() || sub.Name() == "help" {
				collect(sub, append(append([]string{}, path...), sub.Name()))
			}
		}
	}
	root := newRootCommand()
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	collect(root, nil)
	if len(paths) < 20 {
		t.Fatalf("walked only %d commands", len(paths))
	}
	var screens [][]string
	for _, path := range paths {
		screens = append(screens, append(append([]string{}, path...), "--help"), append([]string{"help"}, path...))
	}
	// Groups and the bare root answer with help when invoked without an action.
	screens = append(screens, nil, []string{"job"}, []string{"worker"}, []string{"coordinator"}, []string{"service"})
	for _, args := range screens {
		for _, mode := range []string{"1", "true", "yes", "0"} {
			t.Setenv("AGENT", mode)
			out, stderr, code := execute(context.Background(), args...)
			if code != 0 || stderr != "" {
				t.Fatalf("AGENT=%s %v: exit %d stderr %q", mode, args, code, stderr)
			}
			if hasAlert := strings.HasPrefix(out, skillAlert); hasAlert != (mode != "0") {
				t.Fatalf("AGENT=%s %v alert=%v:\n%.200s", mode, args, hasAlert, out)
			}
		}
	}
}
