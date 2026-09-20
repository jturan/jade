// Package cli wires up the jade command tree.
package cli

import (
	"context"

	"github.com/charmbracelet/fang"
	"github.com/spf13/cobra"
)

// version is overridden at build time via -ldflags.
var version = "dev"

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "jade",
		Short: "Jason's Agentic Development Environment",
		Long: "jade orchestrates AI coding agents across the discovery → plan → build\n" +
			"pipeline. GitHub holds the state, herdr runs the agents, and you keep the\n" +
			"decisions that matter: approving the plan and approving the merge.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.AddCommand(
		newBuildCmd(),
		newConfigCmd(),
		newDoctorCmd(),
		newInitCmd(),
		newLabelsCmd(),
		newPlanCmd(),
		newRunCmd(),
		newStatusCmd(),
	)

	return root
}

// Run executes the jade command tree with the given arguments.
func Run(ctx context.Context, args []string) error {
	root := newRootCmd()
	root.SetArgs(args)
	return fang.Execute(ctx, root, fang.WithVersion(version))
}
