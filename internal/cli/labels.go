package cli

import (
	"fmt"

	"github.com/jturan/jade/internal/state"
	"github.com/spf13/cobra"
)

func newLabelsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "labels",
		Short: "Manage jade's label taxonomy",
	}
	cmd.AddCommand(newLabelsSyncCmd())
	return cmd
}

func newLabelsSyncCmd() *cobra.Command {
	var repo string

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Create or update jade's labels in a repo",
		Long: "Idempotent, so it is safe to re-run. `jade plan apply` calls this\n" +
			"automatically; use it directly to prepare a repo before planning, or\n" +
			"to repair labels someone edited by hand.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			store := state.NewStore(repo)
			if err := store.EnsureLabels(cmd.Context()); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			for _, l := range state.ManagedLabels() {
				fmt.Fprintf(out, "%s %-20s %s\n",
					okStyle.Render("✓"), l.Name, dimStyle.Render(l.Description))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "owner/name (default: inferred from the working directory)")
	return cmd
}
