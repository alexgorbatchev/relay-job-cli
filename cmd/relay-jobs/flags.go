package main

import (
	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
	"github.com/spf13/cobra"
)

func (o *options) daemonFlags(c *cobra.Command) {
	c.Flags().StringVar(&o.paths.State, "state", o.paths.State, "Service state directory")
	c.Flags().StringVar(&o.paths.Cache, "cache", o.paths.Cache, "Worker workspace directory")
}

func (o *options) timeoutFlag(c *cobra.Command) {
	c.Flags().DurationVar(&o.workerTimeout, "worker-timeout", job.WorkerTimeout, "Positive worker availability timeout")
}
