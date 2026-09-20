package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jturan/jade/internal/config"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func newInitCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate local config for this machine",
		Long: "Writes ~/.config/jade/config.yml with a starter profile.\n" +
			"This file is machine-specific and is never committed.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := config.Path()
			if err != nil {
				return err
			}
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", path)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}

			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}

			// TODO(#2): replace this static scaffold with a Huh form that
			// prompts for profile, repo root, and discovery sink.
			cfg := config.Config{
				ActiveProfile: "personal",
				RepoRoot:      filepath.Join(home, "Projects"),
				Profiles: []config.Profile{{
					Name:           "personal",
					GitHost:        "github",
					DiscoverySink:  filepath.Join(home, "Documents", "jmt-obsidian"),
					AllowedVendors: []string{"claude"},
				}},
			}

			data, err := yaml.Marshal(cfg)
			if err != nil {
				return err
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "%s wrote %s\n", okStyle.Render("✓"), path)
			fmt.Fprintln(cmd.OutOrStdout(), dimStyle.Render("Edit it to set your active profile and discovery sink."))
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config file")
	return cmd
}
