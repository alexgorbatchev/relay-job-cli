package main

import (
	"fmt"

	"github.com/alexgorbatchev/relay-jobs-cli/internal/job"
	"github.com/spf13/cobra"
)

func (o *options) installService(*cobra.Command, string, int, string, string, *job.Retention) error {
	return fmt.Errorf("service install requires Linux; provision macOS workers with the native launchd installation procedure")
}
