package main

import (
	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
	"github.com/spf13/cobra"
)

func (o *options) serviceCommand() *cobra.Command {
	var role string
	var slots int
	var name, binary string
	var retention *job.Retention
	group := commandGroup("service", "Install Linux background services")
	install := &cobra.Command{Use: "install", Short: "Install and start a user service", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if err := retention.Validate(); err != nil {
			return err
		}
		return o.installService(c, role, slots, name, binary, retention)
	}}
	retention = retentionFlags(install)
	o.daemonFlags(install)
	o.timeoutFlag(install)
	install.Flags().StringVar(&role, "role", "", "Service role: coordinator or worker")
	install.Flags().IntVar(&slots, "slots", 1, "Maximum concurrent worker jobs")
	install.Flags().StringVar(&name, "name", "", "Unit name, default relay-jobs-<role>.service")
	install.Flags().StringVar(&binary, "binary", "", "Binary to install, default current executable")
	group.AddCommand(install)
	return group
}
