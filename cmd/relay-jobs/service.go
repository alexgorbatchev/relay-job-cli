package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
	"github.com/spf13/cobra"
)

func displayPath(s string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return s
	}
	var out strings.Builder
	for offset := 0; ; {
		i := strings.Index(s[offset:], home)
		if i < 0 {
			out.WriteString(s[offset:])
			return out.String()
		}
		i += offset
		end := i + len(home)
		out.WriteString(s[offset:i])
		before := i == 0 || strings.ContainsRune(" \t\r\n\"'=:([", rune(s[i-1]))
		after := end == len(s) || s[end] == '/' || strings.ContainsRune(" \t\r\n\"':)]", rune(s[end]))
		if before && after {
			out.WriteByte('~')
		} else {
			out.WriteString(home)
		}
		offset = end
	}
}
func unitQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(s) + `"`
}
func (o *options) serviceCommand() *cobra.Command {
	var role string
	var slots int
	var name, binary string
	var retention *job.Retention
	group := &cobra.Command{Use: "service", Short: "Install Linux background services", Args: cobra.NoArgs}
	install := &cobra.Command{Use: "install", Short: "Install and start a user service", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if err := retention.Validate(); err != nil {
			return err
		}
		if runtime.GOOS != "linux" {
			return fmt.Errorf("service install requires Linux; provision macOS workers with the native launchd installation procedure")
		}
		if role != "coordinator" && role != "worker" {
			return fmt.Errorf("role must be coordinator or worker")
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
		if err = os.MkdirAll(binDir, 0700); err != nil {
			return err
		}
		contents, err := os.ReadFile(binary)
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(binDir, ".install-")
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		if _, err = f.Write(contents); err != nil {
			f.Close()
			return err
		}
		if err = f.Chmod(0755); err != nil {
			f.Close()
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		if err = os.Rename(f.Name(), target); err != nil {
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
		if err = os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(dir, name), []byte(unit), 0600); err != nil {
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
	}}
	retention = retentionFlags(install)
	install.Flags().StringVar(&role, "role", "", "Service role: coordinator or worker")
	install.Flags().IntVar(&slots, "slots", 1, "Maximum concurrent worker jobs")
	install.Flags().StringVar(&name, "name", "", "Unit name, default relay-jobs-<role>.service")
	install.Flags().StringVar(&binary, "binary", "", "Binary to install, default current executable")
	group.AddCommand(install)
	return group
}
