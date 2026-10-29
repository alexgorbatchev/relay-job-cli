package main

import (
	"fmt"
	"strings"

	cobrahelptree "github.com/alexgorbatchev/cobra-help-tree/v2"
	"github.com/alexgorbatchev/godeps"
	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"
)

var techCatalog = cobrahelptree.TechCatalog{
	"relay-jobs": {
		Summary:     "Run commands in persistent remote workspaces",
		Description: "Run commands in persistent remote workspaces - command-line interface.",
	},
	"relay-jobs job run":    {Args: []cobrahelptree.ArgSpec{{Name: "<name>", Description: "Job in relay-jobs.toml"}}},
	"relay-jobs job exec":   {Args: []cobrahelptree.ArgSpec{{Name: "<command...>", Description: "Executable and literal arguments after --"}}},
	"relay-jobs job status": {Args: []cobrahelptree.ArgSpec{{Name: "<id>", Description: "Durable job ID"}}},
	"relay-jobs job logs":   {Args: []cobrahelptree.ArgSpec{{Name: "<id>", Description: "Durable job ID"}}},
	"relay-jobs job cancel": {Args: []cobrahelptree.ArgSpec{{Name: "<id>", Description: "Durable job ID"}}},
}

func setupHelp(cmd *cobra.Command) {
	_ = cobrahelptree.SetupWithOptions(cmd, cobrahelptree.HelpOptions{
		Catalog: techCatalog,
		Tree:    cobrahelptree.TreeOptions{HideGeneratedCommands: true},
	})
	cmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		if godeps.IsAgentMode() {
			if _, err := fmt.Fprintln(c.OutOrStdout(), "ALERT: Agents must read `AGENT=1 relay-jobs skill` before using this tool."); err != nil {
				c.PrintErrln(err)
				return
			}
		}
		var screen string
		if godeps.IsAgentMode() {
			screen = cobrahelptree.RenderAgentHelp(c, techCatalog, cobrahelptree.AgentOptions{})
		} else {
			options := humanHelpOptions()
			screen = boundedHelp(cobrahelptree.RenderTreeHelp(c, techCatalog, options), options.TerminalWidth)
		}
		if _, err := fmt.Fprint(c.OutOrStdout(), screen); err != nil {
			c.PrintErrln(err)
		}
	})
	cmd.SetUsageFunc(func(c *cobra.Command) error {
		options := humanHelpOptions()
		screen := boundedHelp(cobrahelptree.RenderTreeUsage(c, techCatalog, options), options.TerminalWidth)
		if godeps.IsAgentMode() {
			screen = "ALERT: Agents must read `AGENT=1 relay-jobs skill` before using this tool.\n" + cobrahelptree.RenderAgentHelp(c, techCatalog, cobrahelptree.AgentOptions{})
		}
		_, err := fmt.Fprint(c.ErrOrStderr(), screen)
		return err
	})
}

func boundedHelp(screen string, width int) string {
	lines := strings.Split(screen, "\n")
	for i, line := range lines {
		lines[i] = runewidth.Truncate(line, width, strings.Repeat(".", min(width, 3)))
	}
	return strings.Join(lines, "\n")
}

func humanHelpOptions() cobrahelptree.TreeOptions {
	width := cobrahelptree.GetTerminalWidth()
	if width == 0 {
		width = 80
	}
	return cobrahelptree.TreeOptions{HideGeneratedCommands: true, TerminalWidth: width}
}

func commandGroup(use, short string) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return c.Help() }}
}
