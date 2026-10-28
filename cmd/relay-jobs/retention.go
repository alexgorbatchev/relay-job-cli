package main

import (
	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
	"github.com/spf13/cobra"
)

func retentionFlags(c *cobra.Command) *job.Retention {
	p := job.DefaultRetention()
	c.Flags().DurationVar(&p.Logs, "log-retention", p.Logs, "Keep completed job logs for this long; 0 disables expiry")
	c.Flags().DurationVar(&p.Workspaces, "workspace-retention", p.Workspaces, "Remove idle worker workspaces after this long; 0 disables expiry")
	c.Flags().DurationVar(&p.Interval, "cleanup-interval", p.Interval, "Time between automatic cleanup passes")
	return &p
}
