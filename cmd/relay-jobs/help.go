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

// Environment variables the CLI reads. Every command reads the root's; serve and
// install commands also list the variables their own defaults and work depend on.
var (
	envStateHome = cobrahelptree.EnvSpec{Name: "XDG_STATE_HOME", Description: "State and socket base; default ~/.local/state"}
	envCacheHome = cobrahelptree.EnvSpec{Name: "XDG_CACHE_HOME", Description: "--cache base; default ~/.cache"}
	envDataHome  = cobrahelptree.EnvSpec{Name: "XDG_DATA_HOME", Description: "Managed rsync base; default ~/.local/share"}
)

var techCatalog = cobrahelptree.TechCatalog{
	"relay-jobs": {
		Summary:     "Run commands in persistent remote workspaces",
		Description: "Run commands in persistent remote workspaces - command-line interface.",
		Env: []cobrahelptree.EnvSpec{
			{Name: "AGENT", Description: "1, true, or yes selects agent output"},
			{Name: "COLUMNS", Description: "Human help width; default terminal width or 80"},
			{Name: "HOME", Description: "XDG fallback base; ~ in TOML paths and output"},
			{Name: "XDG_CONFIG_HOME", Description: "--config base; default ~/.config"},
			envStateHome,
		},
	},
	"relay-jobs coordinator serve": {Env: []cobrahelptree.EnvSpec{
		envStateHome, envCacheHome, envDataHome,
		{Name: "PATH", Description: "Finds ssh, rsync, native tools; fallback gets it"},
		{Name: "XDG_RUNTIME_DIR", Description: "SSH socket directory; default TMPDIR"},
		{Name: "TMPDIR", Description: "SSH socket fallback directory; default /tmp"},
	}},
	"relay-jobs worker serve": {Env: []cobrahelptree.EnvSpec{
		envStateHome, envCacheHome, envDataHome,
		{Name: "PATH", Description: "Finds rsync, native and required tools; jobs get it"},
	}},
	"relay-jobs service install": {Env: []cobrahelptree.EnvSpec{
		envStateHome, envCacheHome,
		{Name: "XDG_CONFIG_HOME", Description: "Unit directory base; default ~/.config"},
		{Name: "XDG_DATA_HOME", Description: "Installed binary base; default ~/.local/share"},
		{Name: "PATH", Description: "Finds systemctl; captured into the unit"},
	}},
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
