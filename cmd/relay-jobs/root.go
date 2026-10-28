package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var version = "1.0.1"

type options struct {
	paths         job.Paths
	config        job.Config
	explicit      map[string]bool
	workerTimeout time.Duration
}
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("job exited %d", int(e)) }

func newRootCommand() *cobra.Command {
	o := &options{paths: job.DefaultPaths()}
	root := &cobra.Command{Use: "relay-jobs", Short: "Run commands in persistent remote workspaces", Version: version, SilenceUsage: true, SilenceErrors: true, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return c.Help() }}
	root.SetVersionTemplate("{{.Version}}\n")
	root.PersistentPreRunE = func(c *cobra.Command, _ []string) error {
		if c.Name() == "skill" {
			return nil
		}
		return o.configure(c)
	}
	root.PersistentFlags().StringVar(&o.paths.Config, "config", o.paths.Config, "Worker configuration file")
	root.PersistentFlags().StringVar(&o.paths.State, "state", o.paths.State, "Service state directory")
	root.PersistentFlags().StringVar(&o.paths.Cache, "cache", o.paths.Cache, "Worker workspace directory")
	root.PersistentFlags().StringVar(&o.paths.Socket, "socket", o.paths.Socket, "Coordinator or worker socket")
	root.PersistentFlags().DurationVar(&o.workerTimeout, "worker-timeout", job.WorkerTimeout, "Positive worker availability timeout")
	jobs := &cobra.Command{Use: "job", Short: "Run and inspect jobs", Args: cobra.NoArgs}
	jobs.AddCommand(o.runCommand(false), o.runCommand(true), o.statusCommand(), o.logsCommand(), o.cancelCommand())
	worker := &cobra.Command{Use: "worker", Short: "Manage workers", Args: cobra.NoArgs}
	var slots int
	var workerRetention *job.Retention
	serveWorker := &cobra.Command{Use: "serve", Short: "Run the worker agent", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if slots < 1 {
			return fmt.Errorf("slots must be positive")
		}
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		w := &job.Worker{Store: job.Store{Dir: o.paths.State}, Cache: o.paths.Cache, Binary: binary, Slots: slots, Retention: *workerRetention}
		return job.ServeWorker(c.Context(), o.paths.Socket, w)
	}}
	workerRetention = retentionFlags(serveWorker)
	serveWorker.Flags().IntVar(&slots, "slots", 1, "Maximum concurrent jobs")
	worker.AddCommand(serveWorker, &cobra.Command{Use: "list", Short: "Check configured workers", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		rpc := job.NewRPC(o.paths.Socket)
		defer rpc.Close()
		var out json.RawMessage
		// Leave time for the coordinator to return per-worker timeout errors.
		ctx, cancel := context.WithDeadline(c.Context(), time.Now().Add(o.workerTimeout).Add(job.ConnectTimeout))
		defer cancel()
		if err := rpc.WithTimeout(0).Call(ctx, "GET", "/workers?timeout="+url.QueryEscape(o.workerTimeout.String()), nil, &out); err != nil {
			return err
		}
		_, err := fmt.Fprintln(c.OutOrStdout(), string(out))
		return err
	}})
	coordinator := &cobra.Command{Use: "coordinator", Short: "Manage the local coordinator", Args: cobra.NoArgs}
	var coordinatorRetention *job.Retention
	serveCoordinator := &cobra.Command{Use: "serve", Short: "Run the coordinator", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		if err := coordinatorRetention.Validate(); err != nil {
			return err
		}
		if len(o.config.Workers) == 0 {
			return fmt.Errorf("configure at least one worker in %s", o.paths.Config)
		}
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		s := job.NewCoordinator(o.paths, o.config, binary)
		s.Retention = *coordinatorRetention
		defer s.Close()
		return job.ServeCoordinator(c.Context(), o.paths.Socket, s)
	}}
	coordinatorRetention = retentionFlags(serveCoordinator)
	coordinator.AddCommand(serveCoordinator)
	execution := &cobra.Command{Use: "execution", Hidden: true, Args: cobra.NoArgs}
	var id string
	execute := &cobra.Command{Use: "run", Hidden: true, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		return job.Execute(c.Context(), job.Store{Dir: o.paths.State}, id)
	}}
	execute.Flags().StringVar(&id, "id", "", "Execution ID")
	execution.AddCommand(execute)
	syncGroup := &cobra.Command{Use: "sync", Hidden: true, Args: cobra.NoArgs}
	var syncID string
	receive := &cobra.Command{Use: "serve", Hidden: true, Args: cobra.MinimumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		return job.SyncReceive(c.Context(), job.Store{Dir: o.paths.State}, syncID, args, c.InOrStdin(), c.OutOrStdout(), c.ErrOrStderr())
	}}
	receive.Flags().StringVar(&syncID, "id", "", "Reservation ID")
	syncGroup.AddCommand(receive)
	root.AddCommand(jobs, worker, coordinator, execution, syncGroup, newSkillCommand(), o.serviceCommand())
	root.PersistentFlags().VisitAll(func(f *pflag.Flag) { f.DefValue = displayPath(f.DefValue) })
	setupHelp(root)
	return root
}
func (o *options) absolutePaths() error {
	for _, p := range []*string{&o.paths.Config, &o.paths.State, &o.paths.Cache, &o.paths.Socket} {
		if *p == "" {
			return fmt.Errorf("service paths must be nonempty")
		}
		absolute, err := filepath.Abs(*p)
		if err != nil {
			return err
		}
		*p = absolute
	}
	return nil
}

func (o *options) runCommand(named bool) *cobra.Command {
	use := "exec -- <command...>"
	short := "Run a command remotely"
	if named {
		use = "run <name>"
		short = "Run a configured project job"
	}
	var source, project, id, policy string
	var detached, quiet bool
	var workers, excludes, requires, labels, env []string
	c := &cobra.Command{Use: use, Short: short, Args: cobra.MinimumNArgs(1), RunE: func(c *cobra.Command, args []string) error {
		source, err := filepath.Abs(source)
		if err != nil {
			return err
		}
		source, err = filepath.EvalSymlinks(source)
		if err != nil {
			return err
		}
		spec := job.Spec{Command: args}
		if named {
			if len(args) != 1 {
				return fmt.Errorf("one job name required")
			}
			var p job.Project
			if err = job.ReadTOML(filepath.Join(source, project), &p); err != nil {
				return err
			}
			var ok bool
			spec, ok = p.Jobs[args[0]]
			if !ok {
				return fmt.Errorf("unknown job %q", args[0])
			}
		}
		if c.Flags().Changed("unavailable") {
			spec.Unavailable = policy
		}
		if len(workers) > 0 {
			spec.Workers = workers
		}
		if len(excludes) > 0 {
			spec.Exclude = excludes
		}
		if len(requires) > 0 {
			spec.Requires = requires
		}
		if len(labels) > 0 {
			spec.Labels = labels
		}
		if spec.Env == nil {
			spec.Env = map[string]string{}
		}
		for _, value := range env {
			k, v, ok := strings.Cut(value, "=")
			if !ok || k == "" {
				return fmt.Errorf("env must be KEY=VALUE")
			}
			spec.Env[k] = v
		}
		if id == "" {
			id = job.NewID()
		}
		req := job.Request{ID: id, Source: source, Spec: spec}
		if err = req.Validate(); err != nil {
			return err
		}
		if !quiet {
			if _, err = fmt.Fprintln(c.ErrOrStderr(), "job: "+id); err != nil {
				return err
			}
		}
		rpc := job.NewRPC(o.paths.Socket)
		defer rpc.Close()
		var rec job.Record
		if err = rpc.Call(c.Context(), "POST", "/jobs", req, &rec); err != nil {
			return fmt.Errorf("submission reply unavailable; query job status %s before resubmitting: %w", id, err)
		}
		if detached {
			return printReceipt(c, rec)
		}
		return follow(c, rpc, id, true)
	}}
	c.Flags().StringVar(&source, "source", ".", "Project source directory")
	c.Flags().StringVar(&project, "project", "relay-jobs.toml", "Project job file relative to source")
	c.Flags().StringVar(&id, "id", "", "Use a previously generated job ID")
	c.Flags().StringVar(&policy, "unavailable", "fail", "No reachable worker: fail or local")
	c.Flags().BoolVar(&detached, "detach", false, "Submit without following output")
	c.Flags().BoolVar(&quiet, "quiet", false, "Keep stderr free of the job ID prefix")
	c.Flags().StringSliceVar(&workers, "workers", nil, "Eligible worker names")
	c.Flags().StringSliceVar(&excludes, "exclude", nil, "Source patterns to exclude and preserve remotely")
	c.Flags().StringSliceVar(&requires, "requires", nil, "Required worker executables")
	c.Flags().StringSliceVar(&labels, "labels", nil, "Required worker labels")
	c.Flags().StringArrayVar(&env, "env", nil, "Command environment KEY=VALUE")
	return c
}
func (o *options) statusCommand() *cobra.Command {
	return &cobra.Command{Use: "status <id>", Short: "Read durable job status", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		rpc := job.NewRPC(o.paths.Socket)
		defer rpc.Close()
		var r job.Record
		if err := rpc.Call(c.Context(), "GET", "/jobs/"+args[0], nil, &r); err != nil {
			return err
		}
		return printReceipt(c, r)
	}}
}
func (o *options) cancelCommand() *cobra.Command {
	return &cobra.Command{Use: "cancel <id>", Short: "Cancel a job and its child processes", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		rpc := job.NewRPC(o.paths.Socket)
		rpc.Client.Timeout = job.CancelTimeout
		defer rpc.Close()
		var r job.Record
		if err := rpc.Call(c.Context(), "POST", "/jobs/"+args[0]+"/cancel", nil, &r); err != nil {
			return err
		}
		return printReceipt(c, r)
	}}
}
func (o *options) logsCommand() *cobra.Command {
	var wait bool
	c := &cobra.Command{Use: "logs <id>", Short: "Read a job's original output", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, args []string) error {
		rpc := job.NewRPC(o.paths.Socket)
		defer rpc.Close()
		return follow(c, rpc, args[0], wait)
	}}
	c.Flags().BoolVar(&wait, "follow", false, "Wait for completion and return command exit status")
	return c
}
func printReceipt(c *cobra.Command, r job.Record) error {
	receipt := struct {
		ID          string `json:"id"`
		State       string `json:"state"`
		Worker      string `json:"worker"`
		ExitCode    int    `json:"exit_code"`
		Error       string `json:"error,omitempty"`
		LogsExpired bool   `json:"logs_expired,omitempty"`
	}{r.ID, r.State, r.Worker, r.ExitCode, displayPath(r.Error), r.LogsExpired}
	return json.NewEncoder(c.OutOrStdout()).Encode(receipt)
}
func follow(c *cobra.Command, rpc *job.RPC, id string, wait bool) (result error) {
	defer func() {
		if !wait || c.Context().Err() == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), job.CancelTimeout)
		defer cancel()
		if err := rpc.WithTimeout(job.CancelTimeout).Call(cleanup, "POST", "/jobs/"+id+"/cancel", nil, nil); err != nil {
			result = fmt.Errorf("cancellation unconfirmed; query job status %s: %w", id, err)
		} else {
			result = exitError(130)
		}
	}()
	offsets := map[string]int64{"stdout": 0, "stderr": 0}
	var targets map[string]int64
	for {
		var rec job.Record
		if err := rpc.Call(c.Context(), "GET", "/jobs/"+id, nil, &rec); err != nil {
			return fmt.Errorf("follow interrupted; reconnect with job logs %s --follow: %w", id, err)
		}
		if targets == nil && (!wait || rec.Terminal()) {
			targets = map[string]int64{}
			for _, stream := range []string{"stdout", "stderr"} {
				size, err := rpc.LogSize(c.Context(), id, stream)
				if err != nil {
					return err
				}
				targets[stream] = size
			}
		}
		for _, stream := range []string{"stdout", "stderr"} {
			writer := c.OutOrStdout()
			if stream == "stderr" {
				writer = c.ErrOrStderr()
			}
			for range 8 {
				if targets != nil && offsets[stream] >= targets[stream] {
					break
				}
				n, err := rpc.Log(c.Context(), id, stream, offsets[stream], writer)
				offsets[stream] += n
				if err != nil {
					return err
				}
				if n < 1<<20 {
					break
				}
			}
		}
		if targets != nil && (offsets["stdout"] < targets["stdout"] || offsets["stderr"] < targets["stderr"]) {
			continue
		}
		if !wait {
			return nil
		}
		if rec.Terminal() {
			if rec.Error != "" {
				fmt.Fprintln(c.ErrOrStderr(), displayPath(rec.Error))
			}
			if rec.ExitCode != 0 {
				return exitError(rec.ExitCode)
			}
			return nil
		}
		select {
		case <-c.Context().Done():
			return c.Context().Err()
		case <-time.After(job.PollInterval):
		}
	}
}
