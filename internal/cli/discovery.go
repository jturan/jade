package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jturan/jade/internal/config"
	"github.com/jturan/jade/internal/prompts"
	"github.com/jturan/jade/internal/runner"
	"github.com/spf13/cobra"
)

func newDiscoveryCmd() *cobra.Command {
	var (
		resume  string
		tab     string
		timeout time.Duration
		dryRun  bool
	)

	cmd := &cobra.Command{
		Use:   "discovery [idea]",
		Short: "Talk an idea through until it is scoped, or abandoned",
		Long: "Starts a discovery agent in a pane and hands the conversation to you.\n" +
			"It ends in a scoped note — or nowhere, which is a perfectly good\n" +
			"outcome for an idea that did not earn more.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			idea := strings.TrimSpace(strings.Join(args, " "))
			if idea == "" && resume == "" {
				return fmt.Errorf(`say what the idea is, or pass --resume to continue an existing note`)
			}

			resolved, err := config.Load()
			if err != nil {
				return err
			}

			// A repo is only required when the profile writes notes into one.
			// An idea that has no repo yet is the normal case for discovery.
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			sink, err := resolved.ResolveSink(cwd)
			if err != nil {
				return err
			}

			tmpl, err := prompts.Load("discovery")
			if err != nil {
				return err
			}
			prompt, err := prompts.Render(tmpl, map[string]string{
				"Sink":            sink,
				"Profile":         resolved.Profile.Name,
				"SinkConventions": config.SinkConventions(sink),
			})
			if err != nil {
				return err
			}
			prompt += "\n\n" + openingLine(idea, resume, sink)

			out := cmd.OutOrStdout()
			if dryRun {
				fmt.Fprintln(out, prompt)
				return nil
			}

			herdrCtx, err := runner.CallerContext()
			if err != nil {
				return err
			}
			agent := resolved.Agents[config.RoleDiscovery]
			if !vendorAllowed(resolved.Profile.AllowedVendors, agent.Vendor) {
				return fmt.Errorf("discovery wants vendor %q, which is not in allowed_vendors %v",
					agent.Vendor, resolved.Profile.AllowedVendors)
			}

			r := runner.New()
			pane, err := r.AgentPane(cmd.Context(), herdrCtx.WorkspaceID, tab, cwd)
			if err != nil {
				return fmt.Errorf("preparing a pane: %w", err)
			}
			if err := r.StartAgent(cmd.Context(), runner.StartOpts{
				Name: "jade-discovery", Kind: agent.Vendor, PaneID: pane.PaneID,
			}); err != nil {
				return fmt.Errorf("starting discovery: %w", err)
			}

			// Deliberately not waiting: discovery is a conversation the
			// operator has, not a job that finishes on its own.
			if err := r.Prompt(cmd.Context(), "jade-discovery", prompt, false, timeout); err != nil {
				return err
			}
			if err := r.FocusAgent(cmd.Context(), "jade-discovery"); err != nil {
				fmt.Fprintf(out, "%s could not focus the pane: %v\n", warnStyle.Render("–"), err)
			}

			fmt.Fprintf(out, "%s discovery open in %s (%s/%s)\n",
				okStyle.Render("✓"), pane.PaneID, agent.Model, agent.Effort)
			fmt.Fprintf(out, "%s\n", dimStyle.Render("Notes land in "+sink))
			fmt.Fprintf(out, "%s\n", dimStyle.Render("When it is scoped: jade plan <note>"))
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&resume, "resume", "", "continue an existing note by name")
	f.StringVar(&tab, "tab", runner.TabAgents, "herdr tab to run discovery in")
	f.DurationVar(&timeout, "timeout", 2*time.Minute, "how long to wait for the agent to accept the prompt")
	f.BoolVar(&dryRun, "dry-run", false, "print the rendered prompt instead of starting an agent")

	return cmd
}

// openingLine tells the agent what it is being asked about, and where a resumed
// note lives.
func openingLine(idea, resume, sink string) string {
	if resume != "" {
		name := resume
		if !strings.HasSuffix(name, ".md") {
			name += ".md"
		}
		return fmt.Sprintf(
			"Resume the discovery in `%s`. Read it first, summarize where it left off, and continue from there.",
			filepath.Join(sink, name))
	}
	return fmt.Sprintf(
		"The user wants to talk through this idea:\n\n%s\n\nStart by inviting them to brain-dump. Do not ask questions yet.",
		idea)
}
