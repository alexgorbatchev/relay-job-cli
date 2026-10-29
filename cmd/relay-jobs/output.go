package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/alexgorbatchev/godeps"
	"github.com/alexgorbatchev/relay-jobs-cli/internal/display"
	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
	"github.com/spf13/cobra"
)

type jobReceipt struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	Worker      string `json:"worker"`
	Duration    string `json:"duration"`
	ExitCode    int    `json:"exit_code"`
	Error       string `json:"error,omitempty"`
	LogsExpired bool   `json:"logs_expired,omitempty"`
}

func receipt(r job.Record, now time.Time) jobReceipt {
	end := now
	if r.Terminal() {
		end = r.Updated
	}
	var elapsed time.Duration
	if !r.Created.IsZero() && end.After(r.Created) {
		elapsed = end.Sub(r.Created)
	}
	return jobReceipt{r.ID, r.State, r.Worker, elapsed.Round(time.Millisecond).String(), r.ExitCode, display.Path(r.Error), r.LogsExpired}
}

func humanReceipt(r jobReceipt) string {
	worker := r.Worker
	if worker == "" {
		worker = "unassigned"
	}
	text := fmt.Sprintf("Job: %s\nState: %s\nWorker: %s\nDuration: %s\nExit code: %d\n", r.ID, r.State, worker, r.Duration, r.ExitCode)
	if r.LogsExpired {
		text += "Logs: expired\n"
	}
	if r.Error != "" {
		text += "Error: " + r.Error + "\n"
	}
	return text
}

func printReceipt(c *cobra.Command, r job.Record) error {
	out := receipt(r, time.Now())
	if godeps.IsAgentMode() {
		return json.NewEncoder(c.OutOrStdout()).Encode(out)
	}
	_, err := fmt.Fprint(c.OutOrStdout(), humanReceipt(out))
	return err
}

func (o *options) listCommand() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List durable job receipts, newest first", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		rpc := job.NewRPC(o.paths.Socket)
		defer rpc.Close()
		var records []job.Record
		if err := rpc.Call(c.Context(), "GET", "/jobs", nil, &records); err != nil {
			return err
		}
		out := make([]jobReceipt, 0, len(records))
		now := time.Now()
		for _, r := range records {
			out = append(out, receipt(r, now))
		}
		if godeps.IsAgentMode() {
			return json.NewEncoder(c.OutOrStdout()).Encode(out)
		}
		if len(out) == 0 {
			_, err := fmt.Fprintln(c.OutOrStdout(), "No jobs.")
			return err
		}
		for i, r := range out {
			text := humanReceipt(r)
			if i > 0 {
				text = "\n" + text
			}
			if _, err := fmt.Fprint(c.OutOrStdout(), text); err != nil {
				return err
			}
		}
		return nil
	}}
}

type workerReceipt struct {
	Name   string     `json:"name"`
	Health job.Health `json:"health"`
	Error  string     `json:"error,omitempty"`
}

func printWorkers(c *cobra.Command, workers []workerReceipt) error {
	if workers == nil {
		workers = []workerReceipt{}
	}
	for i := range workers {
		workers[i].Error = display.Path(workers[i].Error)
		for j, missing := range workers[i].Health.Missing {
			workers[i].Health.Missing[j] = display.Path(missing)
		}
	}
	if godeps.IsAgentMode() {
		return json.NewEncoder(c.OutOrStdout()).Encode(workers)
	}
	if len(workers) == 0 {
		_, err := fmt.Fprintln(c.OutOrStdout(), "No workers configured.")
		return err
	}
	for i, w := range workers {
		var text strings.Builder
		if i > 0 {
			text.WriteByte('\n')
		}
		fmt.Fprintf(&text, "Worker: %s\nProtocol: %d\n", w.Name, w.Health.Protocol)
		fmt.Fprintf(&text, "Slots: %d\nBusy: %d\nWarm: %t\n", w.Health.Slots, w.Health.Busy, w.Health.Warm)
		if len(w.Health.Missing) > 0 {
			fmt.Fprintf(&text, "Missing tools: %s\n", strings.Join(w.Health.Missing, ", "))
		}
		if w.Error != "" {
			fmt.Fprintf(&text, "Error: %s\n", w.Error)
		}
		if _, err := fmt.Fprint(c.OutOrStdout(), text.String()); err != nil {
			return err
		}
	}
	return nil
}
