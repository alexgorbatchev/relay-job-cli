package main

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

const skillURL = "https://github.com/alexgorbatchev/relay-job-cli/tree/main/skills/relay-job"
const skillContent = "Install the relay-job skill using the Skills CLI:\n\n  npx skills add alexgorbatchev/relay-job-cli --skill relay-job\n\nSkill:\n\n  " + skillURL + "\n"

func newSkillCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "skill",
		Short: "Print the agent skill installation command and URL",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := io.WriteString(cmd.OutOrStdout(), skillContent); err != nil {
				return fmt.Errorf("printing skill: %w", err)
			}
			return nil
		},
	}
}
