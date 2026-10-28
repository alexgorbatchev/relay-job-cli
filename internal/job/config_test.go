package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTOMLConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay-jobs.toml")
	text := `[paths]
state = "state"
cache = "~/cache"
[worker]
slots = 3
[retention]
logs = "24h"
workspaces = "0"
interval = "30m"
[[workers]]
name = "linux"
host = "builder"
socket = "/run/user/1000/worker.sock"
labels = ["linux", "amd64"]
`
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Retention.Resolve(DefaultRetention())
	if err != nil || p.Logs != 24*time.Hour || p.Workspaces != 0 || p.Interval != 30*time.Minute || *c.Worker.Slots != 3 || c.Workers[0].Host != "builder" {
		t.Fatal(c, p, err)
	}
	for _, text := range []string{
		"[coordinator]\nworker_timeout = \"0\"", "[coordinator]\nworker_timeout = \"-1s\"", "[coordinator]\nworker_timeout = \"bad\"",
		`{"workers":[]}`, `unknown = true`, `[worker]
slot = 2`, `[worker]
slots = 0`, `[retention]
logs = "7d"`, `[retention]
workspaces = "-1h"`, `[retention]
interval = "0"`, `[retention]
logs = 24`, `[paths]
state = 5`, `[[workers]]
name = "local"
socket = "/tmp/worker.sock"`,
	} {
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadConfig(path); err == nil {
			t.Fatalf("accepted invalid TOML: %s", text)
		}
	}
	if err := os.WriteFile(path, []byte("[worker]\nslots = 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadConfig(path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("coordinator accepted no workers")
	}
}

func TestTOMLProject(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay-jobs.toml")
	text := `[jobs.test]
command = ["sh", "-c", "printf '%s' $VALUE"]
cwd = "src"
exclude = ["node_modules/"]
requires = ["sh"]
workers = ["linux"]
unavailable = "local"
[jobs.test.env]
VALUE = "hello"
`
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	var p Project
	if err := ReadTOML(path, &p); err != nil {
		t.Fatal(err)
	}
	s := p.Jobs["test"]
	if s.Env["VALUE"] != "hello" || s.Cwd != "src" || len(s.Command) != 3 || s.Unavailable != "local" {
		t.Fatal(s)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(text, "command =", "commands =", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ReadTOML(path, &p); err == nil {
		t.Fatal("silently ignored project typo")
	}
}

func TestConfigRelativePaths(t *testing.T) {
	config := filepath.Join(t.TempDir(), "relay-jobs.toml")
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, tc := range []struct{ value, want string }{
		{"state", filepath.Join(filepath.Dir(config), "state")}, {"~/cache", filepath.Join(home, "cache")}, {"~", home}, {"/var/tmp/state", "/var/tmp/state"},
	} {
		got, err := ResolveConfigPath(tc.value, config)
		if err != nil || got != tc.want {
			t.Fatal(got, err, tc.want)
		}
	}
	t.Setenv("HOME", "")
	if _, err := ResolveConfigPath("~/state", config); err == nil {
		t.Fatal("missing home accepted")
	}
}
