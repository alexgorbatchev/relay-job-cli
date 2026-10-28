package job

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type PathConfig struct {
	State  string `toml:"state,omitempty"`
	Cache  string `toml:"cache,omitempty"`
	Socket string `toml:"socket,omitempty"`
}

type WorkerSettings struct {
	Slots *int `toml:"slots,omitempty"`
}

type CoordinatorSettings struct {
	WorkerTimeout *string `toml:"worker_timeout,omitempty"`
}

func (s CoordinatorSettings) Timeout() (time.Duration, error) {
	if s.WorkerTimeout == nil {
		return WorkerTimeout, nil
	}
	d, err := time.ParseDuration(*s.WorkerTimeout)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("coordinator.worker_timeout must be a positive duration")
	}
	return d, nil
}

type RetentionSettings struct {
	Logs       *string `toml:"logs,omitempty"`
	Workspaces *string `toml:"workspaces,omitempty"`
	Interval   *string `toml:"interval,omitempty"`
}

func ReadTOML(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = toml.NewDecoder(f).DisallowUnknownFields().Decode(v); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func ReadConfig(path string) (Config, error) {
	var c Config
	if err := ReadTOML(path, &c); err != nil {
		return c, err
	}
	if c.Worker.Slots != nil && *c.Worker.Slots < 1 {
		return c, fmt.Errorf("worker.slots must be positive")
	}
	if _, err := c.Coordinator.Timeout(); err != nil {
		return c, err
	}
	if _, err := c.Retention.Resolve(DefaultRetention()); err != nil {
		return c, err
	}
	names := map[string]bool{}
	for _, w := range c.Workers {
		if w.Name == "local" || !validName.MatchString(w.Name) || names[w.Name] || !filepath.IsAbs(w.Socket) || strings.ContainsAny(w.Host, " \t\n\r:") || strings.HasPrefix(w.Host, "-") {
			return c, fmt.Errorf("invalid or duplicate worker %q", w.Name)
		}
		names[w.Name] = true
	}
	return c, nil
}

func (s RetentionSettings) Resolve(p Retention) (Retention, error) {
	for _, f := range []struct {
		name   string
		value  *string
		target *time.Duration
	}{
		{"logs", s.Logs, &p.Logs}, {"workspaces", s.Workspaces, &p.Workspaces}, {"interval", s.Interval, &p.Interval},
	} {
		if f.value == nil {
			continue
		}
		value, err := time.ParseDuration(*f.value)
		if err != nil {
			return p, fmt.Errorf("retention.%s: %w", f.name, err)
		}
		*f.target = value
	}
	return p, p.Validate()
}

// ResolveConfigPath expands only a leading home marker. Other relative paths
// are anchored to the configuration file, independent of the caller's cwd.
func ResolveConfigPath(value, configFile string) (string, error) {
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(value, "~"), "/")), nil
	}
	if filepath.IsAbs(value) {
		return value, nil
	}
	return filepath.Abs(filepath.Join(filepath.Dir(configFile), value))
}
