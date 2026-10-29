package main

import (
	"github.com/alexgorbatchev/relay-jobs-cli/internal/display"
	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
	"github.com/spf13/cobra"
)

// socketFlag registers --socket. The socket lives in the effective state
// directory of the command's role, which depends on --state, TOML and, for
// service install, --role, so no single default exists to register.
func (o *options) socketFlag(c *cobra.Command) {
	coordinator, worker := job.DefaultPaths(job.CoordinatorRole), job.DefaultPaths(job.WorkerRole)
	c.PersistentFlags().StringVar(&o.paths.Socket, "socket", "", "Coordinator or worker socket; default from [paths] socket when set, else "+
		"coordinator.sock, or worker.sock for the worker role, in the effective state directory; by default "+
		display.Path(coordinator.Socket)+" or "+display.Path(worker.Socket))
}

// daemonFlags registers --state and --cache for a command that serves one role,
// with that role's directories as defaults. Options are shared by every command,
// so configure resolves unset path flags itself, from the same DefaultPaths; the
// registered defaults are what help shows for that resolution.
func (o *options) daemonFlags(c *cobra.Command, role job.Role) {
	d := job.DefaultPaths(role)
	c.Flags().StringVar(&o.paths.State, "state", d.State, "Service state directory; default from [paths] state when set")
	c.Flags().StringVar(&o.paths.Cache, "cache", d.Cache, "Worker workspace directory; default from [paths] cache when set")
}

// installPathFlags registers --state and --cache for service install, whose
// role, and therefore whose default directories, are known only once --role
// is parsed.
func (o *options) installPathFlags(c *cobra.Command) {
	coordinator, worker := job.DefaultPaths(job.CoordinatorRole), job.DefaultPaths(job.WorkerRole)
	c.Flags().StringVar(&o.paths.State, "state", "", "Service state directory; default from [paths] state when set, else "+
		display.Path(coordinator.State)+", or "+display.Path(worker.State)+" for --role worker")
	c.Flags().StringVar(&o.paths.Cache, "cache", "", "Worker workspace directory; default from [paths] cache when set, else "+
		display.Path(coordinator.Cache)+", or "+display.Path(worker.Cache)+" for --role worker")
}

func (o *options) timeoutFlag(c *cobra.Command) {
	c.Flags().DurationVar(&o.workerTimeout, "worker-timeout", job.WorkerTimeout, "Positive worker availability timeout; default from [coordinator] worker_timeout when set")
}
