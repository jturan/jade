package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jturan/jade/internal/build"
	"github.com/jturan/jade/internal/config"
	"github.com/jturan/jade/internal/gitx"
	"github.com/jturan/jade/internal/prompts"
	"github.com/jturan/jade/internal/runner"
	"github.com/jturan/jade/internal/state"
	"github.com/jturan/jade/internal/telemetry"
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
		autonomy      string
		yolo          bool
		all           bool
		maxAutoMerges int
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

			if err := reconcile(ctx, store, out); err != nil {
				return err
			}
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

			if yolo {
				autonomy = string(state.AutonomyFull)
			}
			if explain {
				return explainReady(out, ready, target, state.Autonomy(autonomy), protectedPaths())
			}

			if yolo {
				autonomy = string(state.AutonomyFull)
			}
			switch state.Autonomy(autonomy) {
			case "", state.AutonomyGated, state.AutonomyMerge, state.AutonomyFull:
			default:
				return fmt.Errorf("unknown autonomy %q (gated, merge, or full)", autonomy)
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

			// A telemetry failure must never stop a build, so an unopenable
			// log degrades to no recording rather than an error.
			var recorder build.Recorder
			if tlog, err := telemetry.Open(""); err == nil {
				recorder = telemetryRecorder{log: tlog}
			}

			deps := build.Deps{
				Agent: &herdrAgent{
					r:           runner.New(),
					workspaceID: herdrCtx.WorkspaceID,
					tab:         tab,
					cwd:         root,
					allowed:     resolved.Profile.AllowedVendors,
				},
				Git:              gitx.New(root),
				Store:            store,
				Prompts:          promptSet{},
				Config:           resolved,
				Root:             root,
				BuildTimeout:     buildTimeout,
				ReviewTimeout:    reviewTimeout,
				AutonomyOverride: state.Autonomy(autonomy),
				ProtectedPaths:   resolved.Profile.ProtectedPaths,
				MaxAutoMerges:    maxAutoMerges,
				Recorder:         recorder,
				Repo:             repo,
				Log: func(format string, args ...any) {
					fmt.Fprintf(out, "%s %s\n", okStyle.Render("·"), fmt.Sprintf(format, args...))
				},
			}

			for {
				res, err := build.RunUnit(ctx, deps, target.Issue, target.Unit)
				if err != nil {
					return err
				}

				switch res.Outcome {
				case build.OutcomeMerged:
					deps.AutoMergesSoFar++
					fmt.Fprintf(out, "\n%s #%d merged — %s\n",
						okStyle.Render("✓"), target.Issue.Number, res.Autonomy.Reason)
				case build.OutcomePR:
					fmt.Fprintf(out, "\n%s #%d ready for you: %s\n",
						okStyle.Render("✓"), target.Issue.Number, res.PRURL)
					fmt.Fprintf(out, "%s\n", dimStyle.Render("Not merged: "+res.Autonomy.Reason))
				case build.OutcomeBlocked:
					fmt.Fprintf(out, "\n%s #%d blocked after %d attempts: %s\n",
						failStyle.Render("✗"), target.Issue.Number, res.Attempts, res.Reason)
					fmt.Fprintf(out, "%s\n", dimStyle.Render("Work in progress is on "+res.Branch+"."))
				}

				// Continue only when the last unit merged itself. A unit left
				// waiting for a human is the point at which to stop: the next
				// unit may well depend on it.
				if !all || res.Outcome != build.OutcomeMerged {
					return nil
				}

				if err := reconcile(ctx, store, out); err != nil {
					return err
				}
				ready, err = store.ListReady(ctx)
				if err != nil {
					return err
				}
				if len(ready) == 0 {
					fmt.Fprintf(out, "\n%s\n", dimStyle.Render("nothing left that is ready"))
					return nil
				}
				target = ready[0]
			}
		},
	}

	f := cmd.Flags()
	f.StringVar(&repo, "repo", "", "owner/name (default: inferred from the working directory)")
	f.IntVar(&unitNumber, "unit", 0, "build a specific ready issue instead of the next one")
	f.StringVar(&tab, "tab", runner.TabAgents, "herdr tab to run agents in")
	f.DurationVar(&buildTimeout, "build-timeout", 45*time.Minute, "how long a builder may run")
	f.DurationVar(&reviewTimeout, "review-timeout", 20*time.Minute, "how long a reviewer may run")
	f.BoolVar(&explain, "explain", false, "show what would run, without dispatching anything")
	f.StringVar(&autonomy, "autonomy", "", "override each unit's level: gated, merge, or full")
	f.BoolVar(&yolo, "yolo", false, "alias for --autonomy full")
	f.BoolVar(&all, "all", false, "keep building ready units until none are left")
	f.IntVar(&maxAutoMerges, "max-auto-merges", build.DefaultMaxAutoMerges,
		"stop after this many unattended merges in one run")

	return cmd
}

func explainReady(out interface{ Write([]byte) (int, error) }, ready []state.Ready, target state.Ready, override state.Autonomy, protected []string) error {
	fmt.Fprintf(out, "%d unit(s) ready. Next: #%d %s\n\n",
		len(ready), target.Issue.Number, target.Issue.Title)

	fmt.Fprintf(out, "  builder        %s/%s\n", orDefault(target.Unit.Builder.Model), orDefault(target.Unit.Builder.Effort))
	fmt.Fprintf(out, "  code review    %s/%s\n", orDefault(target.Unit.CodeReview.Model), orDefault(target.Unit.CodeReview.Effort))
	fmt.Fprintf(out, "  security       %v\n", target.Unit.SecurityReview)
	fmt.Fprintf(out, "  retries        %d\n", target.Unit.RetryLimit)
	level := target.Unit.Autonomy
	if override != "" {
		level = override
	}
	fmt.Fprintf(out, "  autonomy       %s\n", level)

	// Show the guardrails that can already be evaluated. Attempts and test
	// results are unknowable until the builder has run, so assume the best
	// case: this is the ceiling, not a promise.
	decision := build.DecideAutonomy(build.AutonomyInput{
		Level:          level,
		SecurityReview: target.Unit.SecurityReview,
		Attempts:       1,
		TestsRun:       true,
		ProtectedPaths: protected,
	})
	verdict := "would stop at the PR"
	if decision.Merge {
		verdict = "could merge itself"
	}
	fmt.Fprintf(out, "  on success     %s — %s\n", verdict, decision.Reason)
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

// protectedPaths reads the active profile's protected globs, falling back to
// the built-in defaults. Explain must not fail just because config is missing.
func protectedPaths() []string {
	resolved, err := config.Load()
	if err != nil {
		return nil
	}
	return resolved.Profile.ProtectedPaths
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
		Name: d.Name, Kind: d.Vendor, PaneID: pane.PaneID, Args: d.Args,
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

// reconcile brings closed units' labels up to date before a pass reads state,
// so a PR a human merged since the last pass counts as done.
func reconcile(ctx context.Context, store *state.Store, out io.Writer) error {
	changes, err := store.Reconcile(ctx)
	for _, c := range changes {
		if c.Moved {
			fmt.Fprintf(out, "%s #%d closed as completed: %s → %s\n",
				okStyle.Render("·"), c.Issue.Number, c.From, state.StatusDone)
			continue
		}
		reason := strings.ToLower(strings.ReplaceAll(c.Issue.StateReason, "_", " "))
		if reason == "" {
			reason = "unknown reason"
		}
		fmt.Fprintf(out, "%s #%d closed (%s), not completed; left at %s\n",
			okStyle.Render("·"), c.Issue.Number, reason, c.From)
	}
	return err
}
