package main

import (
	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
	"github.com/spf13/cobra"
)

func retentionFlags(c *cobra.Command) *job.Retention {
	p := job.DefaultRetention()
	c.Flags().DurationVar(&p.Logs, "log-retention", p.Logs, "Keep completed job logs for this long; 0 disables expiry; default from [retention] logs when set")
	c.Flags().DurationVar(&p.Workspaces, "workspace-retention", p.Workspaces, "Remove idle worker workspaces after this long; 0 disables expiry; default from [retention] workspaces when set")
	c.Flags().DurationVar(&p.Interval, "cleanup-interval", p.Interval, "Time between automatic cleanup passes; default from [retention] interval when set")
	return &p
}
