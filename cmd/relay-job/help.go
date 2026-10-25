package main

import (
	"fmt"

	cobrahelptree "github.com/alexgorbatchev/cobra-help-tree/v2"
	"github.com/alexgorbatchev/relay-job-cli/internal/agent"
	"github.com/spf13/cobra"
)

var techCatalog = cobrahelptree.TechCatalog{
	"relay-job": {
		Summary:     "Run commands in persistent remote workspaces",
		Description: "Run commands in persistent remote workspaces - command-line interface.",
	},
	"relay-job job run":    {Args: []cobrahelptree.ArgSpec{{Name: "<name>", Description: "Job in relay-job.toml"}}},
	"relay-job job exec":   {Args: []cobrahelptree.ArgSpec{{Name: "<command...>", Description: "Executable and literal arguments after --"}}},
	"relay-job job status": {Args: []cobrahelptree.ArgSpec{{Name: "<id>", Description: "Durable job ID"}}},
	"relay-job job logs":   {Args: []cobrahelptree.ArgSpec{{Name: "<id>", Description: "Durable job ID"}}},
	"relay-job job cancel": {Args: []cobrahelptree.ArgSpec{{Name: "<id>", Description: "Durable job ID"}}},
}

func setupHelp(cmd *cobra.Command) {
	_ = cobrahelptree.SetupWithOptions(cmd, cobrahelptree.HelpOptions{
		Catalog: techCatalog,
		Tree:    cobrahelptree.TreeOptions{HideGeneratedCommands: true},
	})
	help := cmd.HelpFunc()
	cmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		if agent.IsAgentMode() {
			if _, err := fmt.Fprintln(c.OutOrStdout(), "ALERT: Agents must read `AGENT=1 relay-job skill` before using this tool."); err != nil {
				c.PrintErrln(err)
				return
			}
		}
		help(c, args)
	})
}
