package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/jturan/jade/internal/config"
	"github.com/jturan/jade/internal/runner"
	"github.com/spf13/cobra"
)

func newRunCmd() *cobra.Command {
	var (
		kind    string
		role    string
		name    string
		promptF string
		tab     string
		timeout time.Duration
		keep    bool
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Start an agent in a herdr pane and wait for it to settle",
		Long: "Dispatches a single agent the way the build loop will: create a pane in\n" +
			"the agents tab, start the agent there, send it a prompt, and wait for a\n" +
			"settled state. Useful on its own for checking that a machine can actually\n" +
			"drive herdr.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := config.Load()
			if err != nil {
				return err
			}

			herdrCtx, err := runner.CallerContext()
			if err != nil {
				return err
			}

			agent := resolved.Agents[config.Role(role)]
			if agent == (config.Agent{}) {
				return fmt.Errorf("unknown role %q (known: %v)", role, config.Roles())
			}
			if kind == "" {
				kind = agent.Vendor
			}
			if !vendorAllowed(resolved.Profile.AllowedVendors, kind) {
				return fmt.Errorf("vendor %q is not in this profile's allowed_vendors %v",
					kind, resolved.Profile.AllowedVendors)
			}

			prompt, err := os.ReadFile(promptF)
			if err != nil {
				return fmt.Errorf("reading prompt: %w", err)
			}

			cwd, err := os.Getwd()
			if err != nil {
				return err
			}

			ctx := cmd.Context()
			r := runner.New()
			out := cmd.OutOrStdout()

			pane, err := r.AgentPane(ctx, herdrCtx.WorkspaceID, tab, cwd)
			if err != nil {
				return fmt.Errorf("preparing a pane: %w", err)
			}
			fmt.Fprintf(out, "%s pane %s in %s\n", okStyle.Render("✓"), pane.PaneID, tab)

			if err := r.StartAgent(ctx, runner.StartOpts{
				Name:   name,
				Kind:   kind,
				PaneID: pane.PaneID,
				Args:   resolved.ArgsFor(config.Role(role)),
			}); err != nil {
				return fmt.Errorf("starting %s: %w", kind, err)
			}
			fmt.Fprintf(out, "%s started %s as %q\n", okStyle.Render("✓"), kind, name)

			if err := r.Prompt(ctx, name, string(prompt), true, timeout); err != nil {
				// A blocked agent is a legitimate outcome, not a crash: the
				// build loop escalates rather than answering an approval
				// dialog on the operator's behalf.
				if runner.HasCode(err, runner.CodeAgentBlocked) {
					fmt.Fprintf(out, "%s agent is blocked and needs a human\n", warnStyle.Render("–"))
					return nil
				}
				return err
			}

			state, err := r.AgentState(ctx, name)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "%s settled: %s\n", okStyle.Render("✓"), state)

			if !keep {
				if err := r.ClosePane(ctx, pane.PaneID); err != nil {
					fmt.Fprintf(out, "%s could not close pane %s: %v\n", warnStyle.Render("–"), pane.PaneID, err)
				}
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&kind, "kind", "", "agent kind (default: the role's configured vendor)")
	f.StringVar(&role, "role", string(config.RoleBuilder), "role whose model defaults to use")
	f.StringVar(&name, "name", "jade-run", "unique agent name")
	f.StringVar(&promptF, "prompt", "", "file containing the prompt to send")
	f.StringVar(&tab, "tab", runner.TabAgents, "herdr tab to run in")
	f.DurationVar(&timeout, "timeout", 5*time.Minute, "how long to wait for the agent to settle")
	f.BoolVar(&keep, "keep", false, "leave the pane open after the agent settles")
	_ = cmd.MarkFlagRequired("prompt")

	return cmd
}

func vendorAllowed(allowed []string, vendor string) bool {
	for _, v := range allowed {
		if v == vendor {
			return true
		}
	}
	return false
}
