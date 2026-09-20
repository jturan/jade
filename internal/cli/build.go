package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jturan/jade/internal/build"
	"github.com/jturan/jade/internal/config"
	"github.com/jturan/jade/internal/gitx"
	"github.com/jturan/jade/internal/prompts"
	"github.com/jturan/jade/internal/runner"
	"github.com/jturan/jade/internal/state"
	"github.com/spf13/cobra"
)

func newBuildCmd() *cobra.Command {
	var (
		repo          string
		unitNumber    int
		tab           string
		buildTimeout  time.Duration
		reviewTimeout time.Duration
		explain       bool
	)

	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build the next ready unit and open a pull request",
		Long: "Runs one unit end to end without stopping for you: dispatch a builder,\n" +
			"review its work, retry bounded failures, and open a pull request.\n" +
			"jade never merges — that is your gate.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			store := state.NewStore(repo)

			ready, err := store.ListReady(ctx)
			if err != nil {
				return err
			}
			if len(ready) == 0 {
				fmt.Fprintln(out, dimStyle.Render("nothing is ready — every open unit is waiting on a dependency"))
				return nil
			}

			target := ready[0]
			if unitNumber > 0 {
				found := false
				for _, r := range ready {
					if r.Issue.Number == unitNumber {
						target, found = r, true
						break
					}
				}
				if !found {
					return fmt.Errorf("issue #%d is not ready (check `jade status`)", unitNumber)
				}
			}

			if explain {
				return explainReady(out, ready, target)
			}

			resolved, err := config.Load()
			if err != nil {
				return err
			}
			herdrCtx, err := runner.CallerContext()
			if err != nil {
				return err
			}
			root, err := os.Getwd()
			if err != nil {
				return err
			}

			deps := build.Deps{
				Agent: &herdrAgent{
					r:           runner.New(),
					workspaceID: herdrCtx.WorkspaceID,
					tab:         tab,
					cwd:         root,
					allowed:     resolved.Profile.AllowedVendors,
				},
				Git:           gitx.New(root),
				Store:         store,
				Prompts:       promptSet{},
				Config:        resolved,
				Root:          root,
				BuildTimeout:  buildTimeout,
				ReviewTimeout: reviewTimeout,
				Log: func(format string, args ...any) {
					fmt.Fprintf(out, "%s %s\n", okStyle.Render("·"), fmt.Sprintf(format, args...))
				},
			}

			res, err := build.RunUnit(ctx, deps, target.Issue, target.Unit)
			if err != nil {
				return err
			}

			switch res.Outcome {
			case build.OutcomePR:
				fmt.Fprintf(out, "\n%s #%d ready for you: %s\n", okStyle.Render("✓"), target.Issue.Number, res.PRURL)
				fmt.Fprintln(out, dimStyle.Render("Merge it, then run jade build again for the next unit."))
			case build.OutcomeBlocked:
				fmt.Fprintf(out, "\n%s #%d blocked after %d attempts: %s\n",
					failStyle.Render("✗"), target.Issue.Number, res.Attempts, res.Reason)
				fmt.Fprintf(out, "%s\n", dimStyle.Render("Work in progress is on "+res.Branch+"."))
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&repo, "repo", "", "owner/name (default: inferred from the working directory)")
	f.IntVar(&unitNumber, "unit", 0, "build a specific ready issue instead of the next one")
	f.StringVar(&tab, "tab", runner.TabAgents, "herdr tab to run agents in")
	f.DurationVar(&buildTimeout, "build-timeout", 45*time.Minute, "how long a builder may run")
	f.DurationVar(&reviewTimeout, "review-timeout", 20*time.Minute, "how long a reviewer may run")
	f.BoolVar(&explain, "explain", false, "show what would run, without dispatching anything")

	return cmd
}

func explainReady(out interface{ Write([]byte) (int, error) }, ready []state.Ready, target state.Ready) error {
	fmt.Fprintf(out, "%d unit(s) ready. Next: #%d %s\n\n",
		len(ready), target.Issue.Number, target.Issue.Title)

	fmt.Fprintf(out, "  builder        %s/%s\n", orDefault(target.Unit.Builder.Model), orDefault(target.Unit.Builder.Effort))
	fmt.Fprintf(out, "  code review    %s/%s\n", orDefault(target.Unit.CodeReview.Model), orDefault(target.Unit.CodeReview.Effort))
	fmt.Fprintf(out, "  security       %v\n", target.Unit.SecurityReview)
	fmt.Fprintf(out, "  retries        %d\n", target.Unit.RetryLimit)
	fmt.Fprintf(out, "  autonomy       %s\n", target.Unit.Autonomy)
	fmt.Fprintf(out, "  branch         %s\n", gitx.BranchName(target.Issue.Number, target.Issue.Title))

	if len(ready) > 1 {
		others := make([]string, 0, len(ready)-1)
		for _, r := range ready {
			if r.Issue.Number != target.Issue.Number {
				others = append(others, fmt.Sprintf("#%d", r.Issue.Number))
			}
		}
		fmt.Fprintf(out, "\nAlso ready: %s\n", strings.Join(others, " "))
	}
	return nil
}

func orDefault(s string) string {
	if s == "" {
		return "default"
	}
	return s
}

// herdrAgent dispatches build.Dispatch requests into herdr panes.
type herdrAgent struct {
	r           *runner.Runner
	workspaceID string
	tab         string
	cwd         string
	allowed     []string
}

func (h *herdrAgent) Dispatch(ctx context.Context, d build.Dispatch) error {
	if !vendorAllowed(h.allowed, d.Vendor) {
		return fmt.Errorf("role %s wants vendor %q, which is not in this profile's allowed_vendors %v",
			d.Role, d.Vendor, h.allowed)
	}

	pane, err := h.r.AgentPane(ctx, h.workspaceID, h.tab, h.cwd)
	if err != nil {
		return fmt.Errorf("preparing a pane for %s: %w", d.Role, err)
	}
	// The pane is torn down whatever happens, so a long run does not leave
	// the agents tab full of dead panes.
	defer func() { _ = h.r.ClosePane(context.WithoutCancel(ctx), pane.PaneID) }()

	if err := h.r.StartAgent(ctx, runner.StartOpts{
		Name: d.Name, Kind: d.Vendor, PaneID: pane.PaneID,
	}); err != nil {
		return fmt.Errorf("starting %s: %w", d.Role, err)
	}

	if err := h.r.Prompt(ctx, d.Name, d.Prompt, true, d.Timeout); err != nil {
		// A blocked agent is a real outcome, not a crash. Let the loop read
		// the (absent or blocked) report and count it as a failed attempt.
		if runner.HasCode(err, runner.CodeAgentBlocked) {
			return nil
		}
		return err
	}
	return nil
}

func (h *herdrAgent) Notify(ctx context.Context, title, body string) error {
	return h.r.Notify(ctx, title, body)
}

// promptSet renders the build and review prompts.
type promptSet struct{}

func (promptSet) Build(unit state.Unit, issue state.Issue, reportPath string, previous *build.Report) (string, error) {
	tmpl, err := prompts.Load("build")
	if err != nil {
		return "", err
	}
	prev := ""
	if previous != nil {
		prev = previous.Blocking()
	}
	return prompts.Render(tmpl, map[string]string{
		"IssueNumber": fmt.Sprintf("%d", issue.Number),
		"IssueTitle":  issue.Title,
		"IssueBody":   issue.Body,
		"ReportPath":  reportPath,
		"Previous":    prev,
	})
}

func (promptSet) Review(role config.Role, issue state.Issue, reportPath, base string) (string, error) {
	tmpl, err := prompts.Load(string(role))
	if err != nil {
		return "", err
	}
	return prompts.Render(tmpl, map[string]string{
		"IssueNumber": fmt.Sprintf("%d", issue.Number),
		"IssueTitle":  issue.Title,
		"IssueBody":   issue.Body,
		"ReportPath":  reportPath,
		"Base":        base,
	})
}
