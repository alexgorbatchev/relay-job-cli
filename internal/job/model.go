// Package job implements native remote command execution over private Unix sockets.
package job

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	stateQueued    = "queued"
	stateSyncing   = "syncing"
	statePrepared  = "prepared"
	stateStarting  = "starting"
	stateRunning   = "running"
	stateCanceling = "canceling"
	stateCanceled  = "canceled"
	stateSucceeded = "succeeded"
	stateFailed    = "failed"
	stateLost      = "lost"
)

const Protocol = 1
const PollInterval = 25 * time.Millisecond
const ConnectTimeout = 2 * time.Second
const WorkerTimeout = time.Second
const CancelTimeout = 10 * time.Second

var validID = regexp.MustCompile(`^[a-f0-9]{32}$`)
var validName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type Paths struct{ State, Cache, Socket, Config string }

// Role selects the service a daemon runs. Each role has its own default state
// and cache directories and socket, so a coordinator and a worker on one
// machine never share daemon state.
type Role string

const (
	CoordinatorRole Role = "coordinator"
	WorkerRole      Role = "worker"
)

// Socket is the role's socket path inside a state directory.
func (r Role) Socket(state string) string {
	return filepath.Join(state, string(r)+".sock")
}

// DefaultPaths resolves a role's XDG-based defaults. Unset or empty
// XDG_CONFIG_HOME, XDG_STATE_HOME and XDG_CACHE_HOME fall back to ~/.config,
// ~/.local/state and ~/.cache. Workers use relay-jobs-worker directories.
func DefaultPaths(role Role) Paths {
	home, _ := os.UserHomeDir() // os.UserConfigDir reports a missing home when used by callers.
	dir := func(env, fallback string) string {
		if s := os.Getenv(env); s != "" {
			return s
		}
		return filepath.Join(home, fallback)
	}
	name := "relay-jobs"
	if role == WorkerRole {
		name += "-worker"
	}
	state := filepath.Join(dir("XDG_STATE_HOME", ".local/state"), name)
	return Paths{State: state, Cache: filepath.Join(dir("XDG_CACHE_HOME", ".cache"), name), Socket: role.Socket(state), Config: filepath.Join(dir("XDG_CONFIG_HOME", ".config"), "relay-jobs/relay-jobs.toml")}
}

type WorkerConfig struct {
	Name   string   `json:"name" toml:"name"`
	Host   string   `json:"host,omitempty" toml:"host,omitempty"`
	Socket string   `json:"socket" toml:"socket"`
	Labels []string `json:"labels,omitempty" toml:"labels,omitempty"`
}
type Config struct {
	Coordinator CoordinatorSettings `toml:"coordinator,omitempty"`
	Paths       PathConfig          `toml:"paths,omitempty"`
	Worker      WorkerSettings      `toml:"worker,omitempty"`
	Retention   RetentionSettings   `toml:"retention,omitempty"`
	Workers     []WorkerConfig      `toml:"workers,omitempty"`
}
type Project struct {
	Jobs map[string]Spec `toml:"jobs"`
}
type Spec struct {
	Command     []string          `json:"command" toml:"command"`
	Env         map[string]string `json:"env,omitempty" toml:"env,omitempty"`
	Cwd         string            `json:"cwd,omitempty" toml:"cwd,omitempty"`
	Exclude     []string          `json:"exclude,omitempty" toml:"exclude,omitempty"`
	Requires    []string          `json:"requires,omitempty" toml:"requires,omitempty"`
	Labels      []string          `json:"labels,omitempty" toml:"labels,omitempty"`
	Workers     []string          `json:"workers,omitempty" toml:"workers,omitempty"`
	Unavailable string            `json:"unavailable,omitempty" toml:"unavailable,omitempty"`
}
type Request struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Spec   Spec   `json:"spec"`
}

func sameRequest(a, b Request) bool {
	return a.ID == b.ID && a.Source == b.Source && a.Spec.Cwd == b.Spec.Cwd && a.Spec.Unavailable == b.Spec.Unavailable && maps.Equal(a.Spec.Env, b.Spec.Env) && slices.Equal(a.Spec.Command, b.Spec.Command) && slices.Equal(a.Spec.Exclude, b.Spec.Exclude) && slices.Equal(a.Spec.Requires, b.Spec.Requires) && slices.Equal(a.Spec.Labels, b.Spec.Labels) && slices.Equal(a.Spec.Workers, b.Spec.Workers)
}

type Record struct {
	Request
	State         string    `json:"state"`
	Worker        string    `json:"worker,omitempty"`
	Workspace     string    `json:"workspace,omitempty"`
	Error         string    `json:"error,omitempty"`
	ExitCode      int       `json:"exit_code"`
	Submitted     bool      `json:"submitted,omitempty"`
	LeaseUntil    time.Time `json:"lease_until,omitempty"`
	SyncState     string    `json:"sync_state,omitempty"`
	SyncBinary    string    `json:"sync_binary,omitempty"`
	SyncRsync     string    `json:"sync_rsync,omitempty"`
	Created       time.Time `json:"created"`
	Updated       time.Time `json:"updated"`
	LogsCollected bool      `json:"logs_collected,omitempty"`
	LogsExpired   bool      `json:"logs_expired,omitempty"`
}

func NewID() string { b := make([]byte, 16); rand.Read(b); return hex.EncodeToString(b) }
func WorkspaceKey(source string) string {
	h := sha256.Sum256([]byte(source))
	return hex.EncodeToString(h[:16])
}
func (r Record) Terminal() bool {
	switch r.State {
	case stateSucceeded, stateFailed, stateCanceled, stateLost:
		return true
	}
	return false
}
func (r Request) Validate() error {
	if !validID.MatchString(r.ID) {
		return fmt.Errorf("invalid job ID")
	}
	if !filepath.IsAbs(r.Source) || len(r.Spec.Command) == 0 || r.Spec.Command[0] == "" {
		return fmt.Errorf("absolute source and nonempty command required")
	}
	if r.Spec.Cwd != "" && (filepath.IsAbs(r.Spec.Cwd) || r.Spec.Cwd == ".." || strings.HasPrefix(filepath.Clean(r.Spec.Cwd), ".."+string(os.PathSeparator))) {
		return fmt.Errorf("cwd must stay inside source")
	}
	if r.Spec.Unavailable != "" && r.Spec.Unavailable != "fail" && r.Spec.Unavailable != "local" {
		return fmt.Errorf("unavailable must be fail or local")
	}
	for k, v := range r.Spec.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return fmt.Errorf("invalid environment entry")
		}
	}
	for _, a := range r.Spec.Command {
		if strings.ContainsRune(a, 0) {
			return fmt.Errorf("command contains NUL")
		}
	}
	for _, x := range r.Spec.Exclude {
		if strings.ContainsAny(x, "\n\r\x00") {
			return fmt.Errorf("invalid exclusion")
		}
	}
	return nil
}
func ReadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	if err = oneJSONValue(d); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
func oneJSONValue(d *json.Decoder) error {
	var extra any
	err := d.Decode(&extra)
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return fmt.Errorf("expected one JSON value")
	}
	return err
}
func LoadConfig(path string) (Config, error) {
	c, err := ReadConfig(path)
	if err != nil {
		return c, err
	}
	if len(c.Workers) == 0 {
		return c, fmt.Errorf("configure at least one worker")
	}
	return c, nil
}
