package main

import (
	"fmt"
	"os"

	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
	"github.com/spf13/cobra"
)

// role is the service whose defaults a command resolves: worker serve and
// service install --role worker serve a worker; every other command, clients
// included, uses the coordinator's.
func role(c *cobra.Command) (job.Role, error) {
	switch c.CommandPath() {
	case "relay-jobs worker serve":
		return job.WorkerRole, nil
	case "relay-jobs service install":
		role, err := c.Flags().GetString("role")
		if err != nil || role != string(job.WorkerRole) {
			return job.CoordinatorRole, err
		}
		return job.WorkerRole, nil
	}
	return job.CoordinatorRole, nil
}

// configure resolves the command's settings: explicit flags first, then TOML,
// then the role's defaults. Help states the same order.
func (o *options) configure(c *cobra.Command) error {
	o.explicit = map[string]bool{}
	for _, name := range []string{"state", "cache", "socket", "slots", "worker-timeout", "log-retention", "workspace-retention", "cleanup-interval"} {
		o.explicit[name] = c.Flags().Changed(name)
	}
	role, err := role(c)
	if err != nil {
		return err
	}
	defaults := job.DefaultPaths(role)
	// Every command binds the same options, so a bound default can belong to
	// another command; unset paths are always resolved here.
	for _, p := range []struct {
		name, value string
		target      *string
	}{
		{"state", defaults.State, &o.paths.State}, {"cache", defaults.Cache, &o.paths.Cache},
	} {
		if !c.Flags().Changed(p.name) {
			*p.target = p.value
		}
	}
	paths := []*string{&o.paths.Config, &o.paths.State, &o.paths.Cache}
	if c.Flags().Changed("socket") {
		paths = append(paths, &o.paths.Socket)
	}
	if err := absolutePaths(paths...); err != nil {
		return err
	}
	config, err := job.ReadConfig(o.paths.Config)
	if err != nil && !(os.IsNotExist(err) && !c.Flags().Changed("config")) {
		return err
	}
	o.config = config
	if !c.Flags().Changed("worker-timeout") {
		o.workerTimeout, err = config.Coordinator.Timeout()
		if err != nil {
			return err
		}
	}
	if o.workerTimeout <= 0 {
		return fmt.Errorf("worker-timeout must be positive")
	}
	o.config.Coordinator.WorkerTimeout = new(o.workerTimeout.String())
	for _, p := range []struct {
		name, value string
		target      *string
	}{
		{"state", config.Paths.State, &o.paths.State}, {"cache", config.Paths.Cache, &o.paths.Cache},
	} {
		if p.value != "" && !c.Flags().Changed(p.name) {
			*p.target, err = job.ResolveConfigPath(p.value, o.paths.Config)
			if err != nil {
				return err
			}
		}
	}
	if !c.Flags().Changed("socket") {
		o.paths.Socket = role.Socket(o.paths.State)
		if config.Paths.Socket != "" {
			o.paths.Socket, err = job.ResolveConfigPath(config.Paths.Socket, o.paths.Config)
			if err != nil {
				return err
			}
		}
	}
	if config.Worker.Slots != nil && c.Flags().Lookup("slots") != nil && !c.Flags().Changed("slots") {
		if err := c.Flags().Set("slots", fmt.Sprint(*config.Worker.Slots)); err != nil {
			return err
		}
	}
	for _, f := range []struct {
		name  string
		value *string
	}{
		{"log-retention", config.Retention.Logs}, {"workspace-retention", config.Retention.Workspaces}, {"cleanup-interval", config.Retention.Interval},
	} {
		if f.value != nil && c.Flags().Lookup(f.name) != nil && !c.Flags().Changed(f.name) {
			if err := c.Flags().Set(f.name, *f.value); err != nil {
				return err
			}
		}
	}
	return nil
}
