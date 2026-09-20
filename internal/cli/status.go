package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/jturan/jade/internal/state"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	var repo string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the unit-of-work table for a repo",
		Long: "Reads orchestration state from GitHub and prints what is ready, in\n" +
			"flight, blocked, or done. This is the whole reason state lives in\n" +
			"GitHub: it is the same answer on any machine.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			store := state.NewStore(repo)

			issues, err := store.ListUnits(ctx)
			if err != nil {
				return err
			}
			if len(issues) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), dimStyle.Render("no unit-of-work issues found"))
				return nil
			}

			ready, err := store.ListReady(ctx)
			if err != nil {
				return err
			}
			readyNow := make(map[int]bool, len(ready))
			for _, r := range ready {
				readyNow[r.Issue.Number] = true
			}

			sort.Slice(issues, func(a, b int) bool { return issues[a].Number < issues[b].Number })

			out := cmd.OutOrStdout()
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ISSUE\tSTATUS\tDEPENDS ON\tTITLE")

			for _, issue := range issues {
				status := string(issue.Status())
				if status == "" {
					status = "-"
				}
				if issue.Closed() {
					status = "closed"
				} else if readyNow[issue.Number] {
					status += " *"
				}

				deps := "-"
				if u, err := state.ParseUnit(issue.Body); err == nil && len(u.DependsOn) > 0 {
					parts := make([]string, len(u.DependsOn))
					for i, d := range u.DependsOn {
						parts[i] = "#" + strconv.Itoa(d)
					}
					deps = strings.Join(parts, " ")
				}

				fmt.Fprintf(w, "#%d\t%s\t%s\t%s\n", issue.Number, status, deps, truncate(issue.Title, 52))
			}
			if err := w.Flush(); err != nil {
				return err
			}

			fmt.Fprintf(out, "\n%s\n", dimStyle.Render("* dispatchable now — dependencies satisfied"))
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "owner/name (default: inferred from the working directory)")
	return cmd
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
