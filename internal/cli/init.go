package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/jturan/jade/internal/config"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// knownVendors are the agent CLIs jade offers, in preference order. herdr
// supports many more; these are the ones jade's prompts are written for.
var knownVendors = []string{"claude", "codex", "gemini", "opencode", "copilot"}

func newInitCmd() *cobra.Command {
	var (
		force   bool
		profile string
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate local config for this machine",
		Long: "Asks what this machine is and writes ~/.config/jade/config.yml.\n" +
			"That file is machine-specific and is never committed.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.Path()
			if err != nil {
				return err
			}

			existing, err := loadExisting(path)
			if err != nil {
				return err
			}
			if existing != nil && force {
				existing = nil // --force starts over
			}

			p, err := collectProfile(cmd, profile, existing)
			if err != nil {
				return err
			}

			cfg := existing
			if cfg == nil {
				cfg = &config.Config{}
			}
			if cfg.RepoRoot == "" {
				cfg.RepoRoot = defaultRepoRoot()
			}
			upsertProfile(cfg, *p)
			cfg.ActiveProfile = p.Name

			if err := writeConfig(path, cfg); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s wrote %s\n", okStyle.Render("✓"), path)
			fmt.Fprintf(out, "%s\n", dimStyle.Render(fmt.Sprintf(
				"active profile: %s · %d profile(s) configured", cfg.ActiveProfile, len(cfg.Profiles))))
			fmt.Fprintf(out, "%s\n", dimStyle.Render("Check it with: jade config show"))
			return nil
		},
	}

	f := cmd.Flags()
	f.BoolVar(&force, "force", false, "discard the existing config and start over")
	f.StringVar(&profile, "profile", "", "name of the profile to add or replace")
	return cmd
}

// loadExisting reads the current config, if any. A parse failure is surfaced
// rather than silently overwritten — clobbering a file someone hand-edited is
// worse than making them fix it.
func loadExisting(path string) (*config.Config, error) {
	cfg, err := config.ReadFile(path)
	if err != nil {
		if strings.Contains(err.Error(), "no config at") {
			return nil, nil
		}
		return nil, fmt.Errorf("%w\n(use --force to start over)", err)
	}
	return cfg, nil
}

// collectProfile asks for the settings describing this machine.
func collectProfile(cmd *cobra.Command, name string, existing *config.Config) (*config.Profile, error) {
	p := config.Profile{
		Name:    name,
		GitHost: "github",
	}
	if existing != nil && name != "" {
		// Editing a known profile starts from its current values.
		for _, e := range existing.Profiles {
			if e.Name == name {
				p = e
			}
		}
	}
	if p.Name == "" {
		p.Name = "personal"
	}
	if p.DiscoverySink == "" {
		p.DiscoverySink = defaultSink()
	}
	if len(p.AllowedVendors) == 0 {
		p.AllowedVendors = installedVendors()
	}

	if !interactive() {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s no terminal — writing defaults; edit the file or re-run interactively\n",
			warnStyle.Render("–"))
		return &p, nil
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("What is this machine?").
				Description("Profiles let one config behave differently per laptop.").
				Options(
					huh.NewOption("personal", "personal"),
					huh.NewOption("consulting", "consulting"),
					huh.NewOption("dayjob", "dayjob"),
				).
				Value(&p.Name),

			huh.NewInput().
				Title("Where do discovery notes go?").
				Description(`An absolute path, or "repo" to write into docs/discovery/ of the repo being worked on.`).
				Value(&p.DiscoverySink).
				Validate(validateSink),

			huh.NewMultiSelect[string]().
				Title("Which agents may run here?").
				Description("Only installed CLIs are listed. A role pinned to an absent vendor fails at load.").
				Options(vendorOptions(p.AllowedVendors)...).
				Value(&p.AllowedVendors).
				Validate(func(v []string) error {
					if len(v) == 0 {
						return errors.New("pick at least one — jade cannot dispatch without an agent")
					}
					return nil
				}),
		),
	)

	if err := form.Run(); err != nil {
		return nil, err
	}
	return &p, nil
}

// validateSink checks a sink before it is written, so a typo is caught here
// rather than at the start of a discovery session.
func validateSink(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("required")
	}
	if s == config.SinkRepo {
		return nil
	}
	if !filepath.IsAbs(s) {
		return errors.New("use an absolute path, or the word \"repo\"")
	}
	info, err := os.Stat(s)
	if err != nil {
		return fmt.Errorf("%s does not exist", s)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", s)
	}
	return nil
}

func vendorOptions(selected []string) []huh.Option[string] {
	var opts []huh.Option[string]
	for _, v := range installedVendors() {
		opts = append(opts, huh.NewOption(v, v).Selected(slices.Contains(selected, v)))
	}
	if len(opts) == 0 {
		// Offer the full list rather than an empty form; doctor is the place
		// that reports a machine with no agent installed.
		for _, v := range knownVendors {
			opts = append(opts, huh.NewOption(v+" (not installed)", v))
		}
	}
	return opts
}

// installedVendors returns the agent CLIs actually present on this machine.
func installedVendors() []string {
	var found []string
	for _, v := range knownVendors {
		if _, err := lookPath(v); err == nil {
			found = append(found, v)
		}
	}
	return found
}

func defaultRepoRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Projects")
}

// defaultSink prefers an Obsidian vault if one is obvious, since that is where
// notes belong on a personal machine.
func defaultSink() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return config.SinkRepo
	}
	for _, candidate := range []string{
		filepath.Join(home, "Documents", "jmt-obsidian"),
		filepath.Join(home, "Documents", "Obsidian"),
		filepath.Join(home, "Notes"),
	} {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return config.SinkRepo
}

// upsertProfile replaces a profile of the same name, or appends it. Running
// init twice must add a second profile rather than destroying the first.
func upsertProfile(cfg *config.Config, p config.Profile) {
	for i := range cfg.Profiles {
		if cfg.Profiles[i].Name == p.Name {
			cfg.Profiles[i] = p
			return
		}
	}
	cfg.Profiles = append(cfg.Profiles, p)
}

func writeConfig(path string, cfg *config.Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}
