package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/alexgorbatchev/relay-jobs-cli/internal/agent"
	"github.com/alexgorbatchev/relay-jobs-cli/internal/display"
	"github.com/spf13/cobra"
)

func main() {
	slog.SetDefault(diagnosticLogger(os.Stderr))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	cmd := newRootCommand()
	cmd.SetContext(ctx)
	code := run(cmd)
	cancel()
	os.Exit(code)
}

func run(cmd *cobra.Command) int {
	out, stderr := &checkedWriter{Writer: cmd.OutOrStdout()}, &checkedWriter{Writer: cmd.ErrOrStderr()}
	cmd.SetOut(out)
	cmd.SetErr(stderr)
	defer func() { cmd.SetOut(out.Writer); cmd.SetErr(stderr.Writer) }()
	err := cmd.Execute()
	if writeErr := errors.Join(out.err, stderr.err); writeErr != nil {
		err = errors.Join(err, writeErr)
	}
	if err != nil {
		var exit exitError
		if out.err == nil && stderr.err == nil && errors.As(err, &exit) {
			return int(exit)
		}
		if agent.IsAgentMode() {
			fmt.Fprintf(cmd.ErrOrStderr(), "ERR: %s\n", display.Path(err.Error()))
		} else {
			fmt.Fprintf(cmd.ErrOrStderr(), "[ERROR] %s\n", display.Path(err.Error()))
		}
		return 1
	}
	return 0
}

// Cobra's help callbacks cannot return errors. Track writes at the execution
// boundary so help, version, and generated commands still fail on broken output.
type checkedWriter struct {
	io.Writer
	err error
}

func (w *checkedWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if w.err == nil {
		w.err = err
	}
	return n, err
}
