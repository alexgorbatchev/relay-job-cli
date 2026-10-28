package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alexgorbatchev/relay-jobs-cli/internal/agent"
	"github.com/spf13/cobra"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	cmd := newRootCommand()
	cmd.SetContext(ctx)
	code := run(cmd)
	cancel()
	os.Exit(code)
}

func run(cmd *cobra.Command) int {
	if err := cmd.Execute(); err != nil {
		var exit exitError
		if errors.As(err, &exit) {
			return int(exit)
		}
		if agent.IsAgentMode() {
			fmt.Fprintf(cmd.ErrOrStderr(), "ERR: %s\n", displayPath(err.Error()))
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "[ERROR] %s\n", displayPath(err.Error()))
		}
		return 1
	}
	return 0
}
