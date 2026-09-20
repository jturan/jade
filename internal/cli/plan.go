package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jturan/jade/internal/config"
	"github.com/jturan/jade/internal/plan"
	"github.com/jturan/jade/internal/prompts"
	"github.com/jturan/jade/internal/runner"
	"github.com/jturan/jade/internal/state"
	"github.com/spf13/cobra"
)

// defaultPlanYAML is where the planning agent writes its breakdown and where
// `plan show` and `plan apply` look for it.
const defaultPlanYAML = "plan.yml"

func newPlanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan [discovery-note]",
		Short: "Turn a discovery note into a plan and a unit breakdown",
		Long: "Starts a planning agent that writes a sectioned plan and a breakdown\n" +
			"into units of work. Review the breakdown with `jade plan show`, then\n" +
			"create the issues with `jade plan apply`.",
		Args: cobra.ExactArgs(1),
		RunE: runPlan,
	}

	f := cmd.Flags()
	f.String("out", defaultPlanYAML, "where the agent should write the breakdown")
	f.String("plan-note", "docs/plans/plan.md", "where the agent should write the plan document")
	f.Duration("timeout", 30*time.Minute, "how long to wait for the planning agent")
	f.String("tab", runner.TabAgents, "herdr tab to run the planner in")

	cmd.AddCommand(newPlanShowCmd(), newPlanApplyCmd())
	return cmd
}

func runPlan(cmd *cobra.Command, args []string) error {
	resolved, err := config.Load()
	if err != nil {
		return err
	}
	herdrCtx, err := runner.CallerContext()
	if err != nil {
		return err
	}

	note, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	if _, err := os.Stat(note); err != nil {
		return fmt.Errorf("discovery note %s: %w", args[0], err)
	}

	outPath, _ := cmd.Flags().GetString("out")
	planNote, _ := cmd.Flags().GetString("plan-note")
	timeout, _ := cmd.Flags().GetDuration("timeout")
	tab, _ := cmd.Flags().GetString("tab")

	tmpl, err := prompts.Load("plan")
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	prompt, err := prompts.Render(tmpl, map[string]string{
		"DiscoveryNote": note,
		"PlanPath":      planNote,
		"PlanYAMLPath":  outPath,
		"Repo":          cwd,
		"Profile":       resolved.Profile.Name,
	})
	if err != nil {
		return err
	}

	agent := resolved.Agents[config.RolePlanning]
	ctx := cmd.Context()
	r := runner.New()
	out := cmd.OutOrStdout()

	pane, err := r.AgentPane(ctx, herdrCtx.WorkspaceID, tab, cwd)
	if err != nil {
		return fmt.Errorf("preparing a pane: %w", err)
	}
	if err := r.StartAgent(ctx, runner.StartOpts{
		Name: "jade-planner", Kind: agent.Vendor, PaneID: pane.PaneID,
		Args: resolved.ArgsFor(config.RolePlanning),
	}); err != nil {
		return fmt.Errorf("starting planner: %w", err)
	}
	fmt.Fprintf(out, "%s planning in %s (%s/%s)\n",
		okStyle.Render("✓"), pane.PaneID, agent.Model, agent.Effort)

	if err := r.Prompt(ctx, "jade-planner", prompt, true, timeout); err != nil {
		if runner.HasCode(err, runner.CodeAgentBlocked) {
			fmt.Fprintf(out, "%s planner is blocked and needs you in %s\n", warnStyle.Render("–"), pane.PaneID)
			return nil
		}
		return err
	}

	if _, err := os.Stat(outPath); err != nil {
		return fmt.Errorf("planner finished but wrote no %s — check the pane %s", outPath, pane.PaneID)
	}
	fmt.Fprintf(out, "%s wrote %s\n\n", okStyle.Render("✓"), outPath)
	fmt.Fprintln(out, dimStyle.Render("Review it:  jade plan show"))
	fmt.Fprintln(out, dimStyle.Render("Then apply: jade plan apply"))
	return nil
}

func newPlanShowCmd() *cobra.Command {
	var path string

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the proposed breakdown for approval",
		Long: "This is the plan gate. Everything the build loop will do without asking\n" +
			"again is decided here: sequencing, models, effort, security reviews, and\n" +
			"how far each unit may proceed unattended.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := loadPlan(cmd, path)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s\n", p.Initiative)
			if p.Summary != "" {
				fmt.Fprintf(out, "%s\n", dimStyle.Render(p.Summary))
			}
			fmt.Fprintln(out)

			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tAFTER\tBUILDER\tREVIEW\tSEC\tAUTONOMY\tTITLE")
			for _, id := range p.Order() {
				u, ok := p.Unit(id)
				if !ok {
					continue
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
					u.ID,
					joinIDs(u.DependsOn),
					describeAgent(u.Builder),
					describeAgent(u.CodeReview),
					yesNo(u.SecurityReview),
					autonomyLabel(u.Autonomy),
					truncate(u.Title, 46),
				)
			}
			if err := w.Flush(); err != nil {
				return err
			}

			var flagged []plan.Unit
			for _, id := range p.Order() {
				if u, ok := p.Unit(id); ok && u.SecurityReview {
					flagged = append(flagged, u)
				}
			}
			if len(flagged) > 0 {
				fmt.Fprintf(out, "\n%s\n", warnStyle.Render("Security review recommended:"))
				for _, u := range flagged {
					fmt.Fprintf(out, "  unit %d — %s\n", u.ID, u.SecurityReviewReason)
				}
				fmt.Fprintf(out, "%s\n", dimStyle.Render("  These are recommendations. Edit the plan to overrule them."))
			}

			fmt.Fprintf(out, "\n%s\n", dimStyle.Render(
				fmt.Sprintf("%d units. Apply with: jade plan apply", len(p.Units))))
			return nil
		},
	}

	cmd.Flags().StringVar(&path, "file", defaultPlanYAML, "plan file to read")
	return cmd
}

func newPlanApplyCmd() *cobra.Command {
	var (
		path   string
		repo   string
		dryRun bool
	)

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Create the issues for an approved plan",
		Long: "Creates one issue per unit plus a tracking issue, and bootstraps the\n" +
			"label taxonomy. After this, GitHub holds the state.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := loadPlan(cmd, path)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()

			if dryRun {
				fmt.Fprintf(out, "%s would create %d issues plus a tracking issue:\n\n",
					dimStyle.Render("dry run:"), len(p.Units))
				for _, id := range p.Order() {
					if u, ok := p.Unit(id); ok {
						fmt.Fprintf(out, "  unit %d  %s\n", u.ID, u.Title)
					}
				}
				fmt.Fprintf(out, "\n%s\n", dimStyle.Render(
					"Dependencies are written in a second pass, once issue numbers exist."))
				return nil
			}

			res, err := plan.Apply(cmd.Context(), state.NewStore(repo), p)
			if err != nil {
				return err
			}
			for _, id := range p.Order() {
				if u, ok := p.Unit(id); ok {
					fmt.Fprintf(out, "%s #%-4d %s\n", okStyle.Render("✓"), res.Numbers[id], u.Title)
				}
			}
			fmt.Fprintf(out, "%s #%-4d %s\n", okStyle.Render("✓"), res.Tracking.Number, "(tracking)")
			fmt.Fprintf(out, "\n%s\n", dimStyle.Render("Next: jade build"))
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&path, "file", defaultPlanYAML, "plan file to apply")
	f.StringVar(&repo, "repo", "", "owner/name (default: inferred from the working directory)")
	f.BoolVar(&dryRun, "dry-run", false, "print what would be created without creating it")
	return cmd
}

func loadPlan(cmd *cobra.Command, path string) (*plan.Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no plan at %s (run: jade plan <discovery-note>)", path)
		}
		return nil, err
	}

	p, err := plan.Parse(data)
	if err != nil {
		// Lay the problems out here: a flattened list of six is unreadable,
		// and this is the moment the plan gets fixed.
		var invalid *plan.ValidationError
		if errors.As(err, &invalid) && len(invalid.Problems) > 1 {
			w := cmd.ErrOrStderr()
			fmt.Fprintf(w, "%s %s has %d problems:\n", failStyle.Render("✗"), path, len(invalid.Problems))
			for _, p := range invalid.Problems {
				fmt.Fprintf(w, "  - %s\n", p)
			}
		}
		return nil, err
	}
	return p, nil
}

func joinIDs(ids []int) string {
	if len(ids) == 0 {
		return "-"
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%d", id)
	}
	return strings.Join(parts, ",")
}

func describeAgent(a config.Agent) string {
	if a == (config.Agent{}) {
		return dimStyle.Render("default")
	}
	return fmt.Sprintf("%s/%s", a.Model, a.Effort)
}

func yesNo(b bool) string {
	if b {
		return warnStyle.Render("yes")
	}
	return "-"
}

func autonomyLabel(a state.Autonomy) string {
	if a == "" {
		return string(state.AutonomyGated)
	}
	if a != state.AutonomyGated {
		return warnStyle.Render(string(a))
	}
	return string(a)
}
