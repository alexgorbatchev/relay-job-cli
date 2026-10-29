package main

import (
	"fmt"

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
		return o.installService(c, role, slots, name, binary, retention)
	}}
	retention = retentionFlags(install)
	o.installPathFlags(install)
	o.timeoutFlag(install)
	install.Flags().StringVar(&role, "role", "", "Service role: coordinator or worker")
	install.Flags().IntVar(&slots, "slots", 1, "Maximum concurrent worker jobs; default from [worker] slots when set")
	install.Flags().StringVar(&name, "name", "", "Unit name, default relay-jobs-<role>.service")
	install.Flags().StringVar(&binary, "binary", "", "Binary to install, default current executable")
	group.AddCommand(install)
	return group
}
