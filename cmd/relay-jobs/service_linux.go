package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/alexgorbatchev/relay-jobs-cli/internal/durable"
	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
	"github.com/spf13/cobra"
)

func unitQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(s) + `"`
}
func (o *options) installService(c *cobra.Command, role string, slots int, name, binary string, retention *job.Retention) error {
	if role != "coordinator" && role != "worker" {
		return fmt.Errorf("role must be coordinator or worker")
	}
	if role == "worker" && c.Flags().Changed("worker-timeout") {
		return fmt.Errorf("--worker-timeout applies only to the coordinator role")
	}
	if role == "coordinator" && o.explicit["slots"] {
		return fmt.Errorf("--slots applies only to the worker role")
	}
	if slots < 1 {
		return fmt.Errorf("slots must be positive")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	if name == "" {
		name = "relay-jobs-" + role + ".service"
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*\.service$`).MatchString(name) {
		return fmt.Errorf("invalid unit name")
	}
	if binary == "" {
		binary, err = os.Executable()
		if err != nil {
			return err
		}
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return err
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local/share")
	}
	binDir := filepath.Join(dataHome, "relay-jobs/bin")
	target := filepath.Join(binDir, "relay-jobs")
	if strings.ContainsAny(target, "'\"\\") || strings.IndexFunc(target, func(r rune) bool { return r < ' ' || r == 0x7f }) >= 0 {
		return fmt.Errorf("systemd executable paths cannot contain quotes, backslashes, or control characters; choose another XDG_DATA_HOME")
	}
	contents, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	if err = durable.WriteFile(target, contents, 0755); err != nil {
		return err
	}
	binary = target
	args := []string{binary, role, "serve", "--config", o.paths.Config}
	// Keep TOML settings live on restart; persist only actual CLI overrides.
	// A missing default file is optional, so omit its path unless it exists.
	if _, statErr := os.Stat(o.paths.Config); os.IsNotExist(statErr) && !c.Flags().Changed("config") {
		args = args[:3]
	}
	for _, flag := range []struct{ name, value string }{
		{"state", o.paths.State}, {"cache", o.paths.Cache}, {"socket", o.paths.Socket},
		{"worker-timeout", o.workerTimeout.String()},
		{"log-retention", retention.Logs.String()}, {"workspace-retention", retention.Workspaces.String()}, {"cleanup-interval", retention.Interval.String()},
	} {
		if o.explicit[flag.name] {
			args = append(args, "--"+flag.name, flag.value)
		}
	}
	if role == "worker" && o.explicit["slots"] {
		args = append(args, "--slots", fmt.Sprint(slots))
	}
	for i, a := range args {
		args[i] = unitQuote(a)
	}
	unit := "[Unit]\nDescription=Remote job " + role + "\n\n[Service]\nType=exec\nExecStart=:" + strings.Join(args, " ") + "\nEnvironment=" + unitQuote("PATH="+os.Getenv("PATH")) + "\nRestart=on-failure\nRestartSec=2\nTimeoutStopSec=15\nUMask=0077\n\n[Install]\nWantedBy=default.target\n"
	dir := filepath.Join(configHome, "systemd/user")
	if err = durable.WriteFile(filepath.Join(dir, name), []byte(unit), 0600); err != nil {
		return err
	}
	for _, args := range [][]string{{"--user", "daemon-reload"}, {"--user", "enable", "--now", name}, {"--user", "is-active", "--quiet", name}} {
		out, err := exec.CommandContext(c.Context(), "systemctl", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("install service: %w: %s", err, out)
		}
	}
	_, err = fmt.Fprintln(c.OutOrStdout(), "service: "+name+" active")
	return err
}
