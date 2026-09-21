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
			if _, shipped, _ := config.ShippedProfile(p.Name); shipped {
				fmt.Fprintf(out, "%s\n", dimStyle.Render(fmt.Sprintf(
					"started from the shipped %s profile — the file records only what differs", p.Name)))
			}
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
		if errors.Is(err, config.ErrNoConfig) {
			return nil, nil
		}
		return nil, fmt.Errorf("%w\n(use --force to start over)", err)
	}
	return cfg, nil
}

// collectProfile starts from the profile this repo ships for the chosen name
// and asks only for what the repo cannot know: paths, and anything the shipped
// profile leaves blank.
func collectProfile(cmd *cobra.Command, name string, existing *config.Config) (*config.Profile, error) {
	name, err := profileName(cmd, name, existing)
	if err != nil {
		return nil, err
	}

	shipped, _, err := config.ShippedProfile(name)
	if err != nil {
		return nil, err
	}

	// Re-running init should not re-ask what this machine already answered.
	local := config.Profile{Name: name}
	if existing != nil {
		for _, e := range existing.Profiles {
			if e.Name == name {
				local = e
			}
		}
	}

	p, _ := config.MergeProfile(shipped, local)
	if p.GitHost == "" {
		p.GitHost = "github"
	}

	// A shipped sink of "vault" means "a notes directory, path supplied
	// locally" — the repo is public and cannot hold anyone's vault path.
	needSink := p.DiscoverySink == "" || p.DiscoverySink == config.SinkVault
	if needSink {
		p.DiscoverySink = defaultSink()
	}
	needVendors := len(p.AllowedVendors) == 0
	if needVendors {
		p.AllowedVendors = installedVendors()
	}

	if !interactive() {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s no terminal — writing defaults; edit the file or re-run interactively\n",
			warnStyle.Render("–"))
		return trim(p, shipped), nil
	}

	var fields []huh.Field
	if needSink {
		fields = append(fields, huh.NewInput().
			Title("Where do discovery notes go?").
			Description(`An absolute path, or "repo" to write into docs/discovery/ of the repo being worked on.`).
			Value(&p.DiscoverySink).
			Validate(validateSink))
	}
	if needVendors {
		fields = append(fields, huh.NewMultiSelect[string]().
			Title("Which agents may run here?").
			Description("Only installed CLIs are listed. A role pinned to an absent vendor fails at load.").
			Options(vendorOptions(p.AllowedVendors)...).
			Value(&p.AllowedVendors).
			Validate(func(v []string) error {
				if len(v) == 0 {
					return errors.New("pick at least one — jade cannot dispatch without an agent")
				}
				return nil
			}))
	}

	if len(fields) > 0 {
		if err := huh.NewForm(huh.NewGroup(fields...)).Run(); err != nil {
			return nil, err
		}
	}
	return trim(p, shipped), nil
}

// profileName asks which machine this is, unless --profile already said.
func profileName(cmd *cobra.Command, name string, existing *config.Config) (string, error) {
	if name != "" {
		return name, nil
	}
	chosen := config.DefaultProfileName
	if existing != nil && existing.ActiveProfile != "" {
		chosen = existing.ActiveProfile
	}
	if !interactive() {
		return chosen, nil
	}

	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("What is this machine?").
			Description("Each name is a profile this repo ships; the local file only records what differs.").
			Options(profileOptions()...).
			Value(&chosen),
	))
	if err := form.Run(); err != nil {
		return "", err
	}
	return chosen, nil
}

// trim reduces a resolved profile to the part that is genuinely local, so the
// config file does not silently fork from the shipped defaults it started with.
func trim(p, shipped config.Profile) *config.Profile {
	out := p.Minus(shipped)
	out.Name = p.Name
	return &out
}

// profileOptions lists the profiles the repo ships.
func profileOptions() []huh.Option[string] {
	names := config.ShippedNames()
	opts := make([]huh.Option[string], 0, len(names))
	for _, n := range names {
		opts = append(opts, huh.NewOption(n, n))
	}
	return opts
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
