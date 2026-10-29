package main

import (
	"fmt"

	cobrahelptree "github.com/alexgorbatchev/cobra-help-tree/v3"
	"github.com/spf13/cobra"
)

const fallbackHelpWidth = 80

// skillSource is where the relay-jobs agent skill is published:
// skills/relay-jobs/SKILL.md on the repository's default branch.
var skillSource = cobrahelptree.SkillSource{Repository: "alexgorbatchev/relay-jobs-cli", Branch: "main"}

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
	err := cobrahelptree.SetupWithOptions(cmd, cobrahelptree.HelpOptions{
		Catalog: techCatalog,
		Skill:   skillSource,
		Tree:    cobrahelptree.TreeOptions{HideGeneratedCommands: true, TerminalWidth: humanHelpWidth()},
	})
	if err != nil {
		// The options are static, so a failure is a programming error.
		panic(fmt.Sprintf("configuring help: %v", err))
	}
}

// humanHelpWidth is the width human help is clipped to. Redirected output has no
// terminal to measure, so it falls back to 80 columns instead of growing unbounded.
func humanHelpWidth() int {
	if width := cobrahelptree.GetTerminalWidth(); width > 0 {
		return width
	}
	return fallbackHelpWidth
}

func commandGroup(use, short string) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error { return c.Help() }}
}
