package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
	"github.com/spf13/cobra"
)

func (o *options) configure(c *cobra.Command) error {
	o.explicit = map[string]bool{}
	for _, name := range []string{"state", "cache", "socket", "slots", "log-retention", "workspace-retention", "cleanup-interval"} {
		o.explicit[name] = c.Flags().Changed(name)
	}
	if err := o.absolutePaths(); err != nil {
		return err
	}
	config, err := job.ReadConfig(o.paths.Config)
	if err != nil && !(os.IsNotExist(err) && !c.Flags().Changed("config")) {
		return err
	}
	o.config = config
	workerRole := c.CommandPath() == "relay-jobs worker serve"
	if c.CommandPath() == "relay-jobs service install" {
		role, err := c.Flags().GetString("role")
		if err != nil {
			return err
		}
		workerRole = role == "worker"
	}
	if workerRole {
		if !c.Flags().Changed("state") {
			o.paths.State += "-worker"
		}
		if !c.Flags().Changed("cache") {
			o.paths.Cache += "-worker"
		}
	}
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
		name := "coordinator.sock"
		if workerRole {
			name = "worker.sock"
		}
		o.paths.Socket = filepath.Join(o.paths.State, name)
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
