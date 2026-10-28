package main

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/alexgorbatchev/relay-jobs-cli/internal/agent"
	"github.com/alexgorbatchev/relay-jobs-cli/internal/display"
)

func diagnosticLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if agent.IsAgentMode() && a.Key == slog.TimeKey {
			return slog.Attr{}
		}
		switch a.Value.Kind() {
		case slog.KindString:
			a.Value = slog.StringValue(display.Path(a.Value.String()))
		case slog.KindAny:
			if err, ok := a.Value.Any().(error); ok {
				a.Value = slog.StringValue(display.Path(fmt.Sprint(err)))
			}
		}
		return a
	}}))
}
