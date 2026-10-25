package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexgorbatchev/relay-job-cli/internal/job"
	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var integrationBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "rj-cli-")
	if err != nil {
		panic(err)
	}
	integrationBinary = filepath.Join(dir, "relay-job")
	cmd := exec.Command("go", "build", "-o", integrationBinary, ".")
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Stderr.Write(out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
func execute(ctx context.Context, args ...string) (string, string, int) {
	out, stderr := new(bytes.Buffer), new(bytes.Buffer)
	c := newRootCommand()
	c.SetContext(ctx)
	c.SetOut(out)
	c.SetErr(stderr)
	c.SetArgs(args)
	code := run(c)
	return out.String(), stderr.String(), code
}
func executeCommand(args ...string) (string, error) {
	out, stderr, code := execute(context.Background(), args...)
	if code != 0 {
		return out, fmt.Errorf("%s", stderr)
	}
	return out, nil
}
func TestCLIHelpAndSkill(t *testing.T) {
	for _, mode := range []string{"0", "1", "true", "yes"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AGENT", mode)
			for _, args := range [][]string{{"--version"}, {"skill"}, {"--help"}, {"help", "job"}, {"job", "run", "--help"}} {
				out, stderr, code := execute(context.Background(), args...)
				if code != 0 || stderr != "" {
					t.Fatalf("%v: %s %s", args, out, stderr)
				}
				if args[0] == "--version" && out != version+"\n" {
					t.Fatal(out)
				}
				if args[0] == "skill" && out != skillContent {
					t.Fatal(out)
				}
				if (args[0] == "--help" || args[0] == "help") && mode != "0" && !strings.HasPrefix(out, "ALERT:") {
					t.Fatal(out)
				}
			}
			if _, stderr, code := execute(context.Background(), "invalid"); code != 1 || stderr == "" {
				t.Fatal("missing CLI diagnostic")
			}
		})
	}
	t.Chdir(t.TempDir())
	out, err := executeCommand("skill")
	if err != nil || out != skillContent {
		t.Fatal("offline skill contract")
	}
}
func walk(c *cobra.Command, visit func(*cobra.Command)) {
	if c.Hidden {
		return
	}
	visit(c)
	for _, child := range c.Commands() {
		walk(child, visit)
	}
}
func TestSkillCoversInterface(t *testing.T) {
	t.Setenv("AGENT", "1")
	b, err := os.ReadFile("../../skills/relay-job/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	c := newRootCommand()
	c.InitDefaultHelpCmd()
	c.InitDefaultCompletionCmd()
	walk(c, func(c *cobra.Command) {
		c.InitDefaultHelpFlag()
		c.InitDefaultVersionFlag()
		usage := c.Use
		if c.Parent() != nil {
			usage = c.Parent().CommandPath() + " " + usage
		}
		_, section, ok := strings.Cut(text, "## `"+usage+"`\n")
		if !ok {
			t.Errorf("undocumented command %s", usage)
			return
		}
		section, _, _ = strings.Cut(section, "\n## ")
		if c.CommandPath() == "relay-job job run" {
			_, section, _ = strings.Cut(text, "## `relay-job job exec -- <command...>`\n")
			section, _, _ = strings.Cut(section, "\n## ")
		}
		check := func(f *pflag.Flag) {
			if f.Hidden || (f.Name == "help" && c.Parent() != nil) {
				return
			}
			verifyFlagReference(t, section, f)
		}
		c.LocalNonPersistentFlags().VisitAll(check)
		c.PersistentFlags().VisitAll(check)
		args := strings.Fields(c.CommandPath())[1:]
		out, stderr, code := execute(context.Background(), append(args, "--help")...)
		if code != 0 || stderr != "" || !strings.HasPrefix(out, "ALERT: Agents must read") {
			t.Errorf("%s help lost its agent alert", c.CommandPath())
		}
	})
	_, selected, _ := strings.Cut(skillContent, "--skill ")
	name := strings.Fields(selected)[0]
	_, declared, _ := strings.Cut(text, "\nname: ")
	declared, _, _ = strings.Cut(declared, "\n")
	if name != declared || !strings.HasSuffix(skillURL, "/skills/"+name) {
		t.Fatal("printed skill name or URL disagrees with the repository skill")
	}
	if _, _, code := execute(context.Background(), "skill", "extra"); code == 0 {
		t.Fatal("skill accepted positional arguments")
	}
}

func verifyFlagReference(t *testing.T, section string, f *pflag.Flag) {
	t.Helper()
	if f.Name == "no-descriptions" {
		want := fmt.Sprintf("`--%s` (%s, %s)", f.Name, f.Value.Type(), f.DefValue)
		if !strings.Contains(section, want) {
			t.Errorf("missing completion flag metadata %s", want)
		}
		return
	}
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "| `--"+f.Name+"` |") {
			continue
		}
		cells := strings.Split(line, "|")
		if strings.Trim(cells[2], " `") != f.Value.Type() {
			t.Errorf("%s type is undocumented", f.Name)
		}
		switch f.Name {
		case "config", "state", "cache", "socket":
			return
		}
		expected := f.DefValue
		if expected == "" {
			expected = "empty"
		}
		if strings.Trim(cells[3], " `") != expected {
			t.Errorf("%s default is undocumented: %s", f.Name, expected)
		}
		return
	}
	t.Errorf("undocumented flag %s", f.Name)
}
func TestOutputAndPathBoundaries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, tc := range []struct{ in, want string }{{home, "~"}, {home + "/a", "~/a"}, {home + "-other/a", home + "-other/a"}, {"error: '" + home + "/a'", "error: '~/a'"}} {
		if got := displayPath(tc.in); got != tc.want {
			t.Errorf("%q => %q", tc.in, got)
		}
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "closed"))
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	for _, args := range [][]string{{"skill"}, {"--help"}} {
		c := newRootCommand()
		c.SetOut(f)
		c.SetErr(new(bytes.Buffer))
		c.SetArgs(args)
		err = c.Execute()
		if args[0] == "skill" && !errors.Is(err, os.ErrClosed) {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ in, want string }{{"/srv" + home + "/a", "/srv" + home + "/a"}, {home + " and " + home + "/a", "~ and ~/a"}, {"/outside/a", "/outside/a"}} {
		if got := displayPath(tc.in); got != tc.want {
			t.Errorf("%q => %q", tc.in, got)
		}
	}
	t.Setenv("HOME", "")
	if displayPath("/tmp/other-user") != "/tmp/other-user" {
		t.Fatal("missing home changed unrelated path")
	}
}
func socketServer(t *testing.T, h http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rj-cli-sock-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "api")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- job.Serve(ctx, socket, h) }()
	for i := 0; i < 1000; i++ {
		if _, err = os.Stat(socket); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
		os.RemoveAll(dir)
	})
	return socket
}
func TestCLIJobWorkflow(t *testing.T) {
	if err := exec.Command("systemctl", "--user", "show-environment").Run(); err != nil {
		t.Skip("requires user systemd")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &job.Worker{Store: job.Store{Dir: t.TempDir()}, Cache: t.TempDir(), Binary: integrationBinary, Slots: 2}
	workerSocket := socketServer(t, w.Handler())
	paths := job.Paths{State: t.TempDir()}
	config := job.Config{Workers: []job.WorkerConfig{{Name: "test", Socket: workerSocket, Labels: []string{"linux"}}}}
	coordinator := job.NewCoordinator(paths, config, integrationBinary)
	defer coordinator.Close()
	socket := socketServer(t, coordinator.Handler(ctx))
	source := t.TempDir()
	project := job.Project{Jobs: map[string]job.Spec{"example": {Command: []string{"sh", "-c", "printf '%s' \"$VALUE\"; printf err >&2; exit 37"}}}}
	if err := writeTOML(filepath.Join(source, "relay-job.toml"), project); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := execute(ctx, "--socket", socket, "job", "run", "example", "--source", source, "--workers", "test", "--requires", "sh", "--labels", "linux", "--env", "VALUE=byte exact", "--exclude", "cache/", "--unavailable", "fail")
	if code != 37 || out != "byte exact" || !strings.Contains(stderr, "err") {
		t.Fatalf("%d %q %q", code, out, stderr)
	}
	id := strings.Fields(stderr)[1]
	if out, _, code = execute(ctx, "--socket", socket, "job", "status", id); code != 0 || !strings.Contains(out, "failed") {
		t.Fatalf("%d %s", code, out)
	}
	if out, stderr, code = execute(ctx, "--socket", socket, "job", "logs", id); code != 0 || out != "byte exact" || stderr != "err" {
		t.Fatalf("%d %q %q", code, out, stderr)
	}
	if _, _, code = execute(ctx, "--socket", socket, "job", "logs", id, "--follow"); code != 37 {
		t.Fatal(code)
	}
	out, stderr, code = execute(ctx, "--socket", socket, "job", "exec", "--source", source, "--quiet", "--", "sh", "-c", "printf '\\000\\377'; printf '\\357\\273\\277err\\000' >&2; exit 37")
	if code != 37 || !bytes.Equal([]byte(out), []byte{0, 0xff}) || !bytes.Equal([]byte(stderr), []byte{0xef, 0xbb, 0xbf, 'e', 'r', 'r', 0}) {
		t.Fatalf("binary command output: %d %v %v", code, []byte(out), []byte(stderr))
	}
	if out, _, code = execute(ctx, "--socket", socket, "worker", "list"); code != 0 || !strings.Contains(out, "test") {
		t.Fatal(out)
	}
	detached := job.NewID()
	if out, _, code = execute(ctx, "--socket", socket, "job", "exec", "--source", source, "--id", detached, "--detach", "--", "sleep", "5"); code != 0 || !strings.Contains(out, detached) {
		t.Fatalf("%d %s", code, out)
	}
	time.Sleep(100 * time.Millisecond)
	if out, stderr, code = execute(ctx, "--socket", socket, "job", "cancel", detached); code != 0 || !strings.Contains(out, "canceled") {
		t.Fatalf("%d %s %s", code, out, stderr)
	}
	for _, args := range [][]string{
		{"job", "run", "missing", "--source", source}, {"job", "run", "a", "b", "--source", source}, {"job", "exec", "--source", source, "--env", "invalid", "--", "true"},
		{"job", "exec", "--source", source, "--id", "bad", "--", "true"}, {"job", "exec", "--source", "/nonexistent-rj", "--", "true"},
		{"job", "status", job.NewID()}, {"job", "cancel", job.NewID()},
	} {
		if _, _, code = execute(ctx, append([]string{"--socket", socket}, args...)...); code == 0 {
			t.Errorf("unexpected success: %v", args)
		}
	}
}

func writeTOML(path string, v any) error {
	b, err := toml.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0600)
}
