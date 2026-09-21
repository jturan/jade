package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/jturan/jade/internal/config"
	"github.com/spf13/cobra"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect jade's resolved configuration",
	}
	cmd.AddCommand(newConfigShowCmd())
	return cmd
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the fully resolved config for the active profile",
		Long: "Prints the merged configuration with the layer each value came from,\n" +
			"so a machine behaving unexpectedly can be diagnosed in one command.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			resolved, err := config.Load()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			p := resolved.Profile

			location := resolved.ConfigPath
			if !resolved.HasLocal {
				location += " (absent — running on shipped defaults)"
			}
			fmt.Fprintf(out, "%s  %s\n", dimStyle.Render("config"), location)
			fmt.Fprintf(out, "%s %s\n\n", dimStyle.Render("profile"), p.Name)

			// tabwriter measures cell width in bytes and cannot see ANSI
			// escapes, so the header is left unstyled to keep columns aligned.
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "SETTING\tVALUE\tFROM")
			for _, field := range config.ProfileFields() {
				fmt.Fprintf(w, "%s\t%s\t%s\n",
					field, profileValue(p, field), orLayer(resolved.Sources[field]))
			}
			if err := w.Flush(); err != nil {
				return err
			}
			fmt.Fprintln(out)

			// A second writer, so the two tables size their columns
			// independently rather than padding to each other's widest cell.
			w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ROLE\tVENDOR\tMODEL\tEFFORT\tFROM")
			for _, role := range config.Roles() {
				a, o := resolved.Agents[role], resolved.Origins[role]
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					role, a.Vendor, a.Model, a.Effort, summarizeOrigin(o))
			}
			return w.Flush()
		},
	}
}

// profileValue renders one profile setting for the table, naming the fallback
// where a field is empty rather than printing a blank cell.
func profileValue(p config.Profile, field string) string {
	switch field {
	case config.FieldGitHost:
		return p.GitHost
	case config.FieldDiscoverySink:
		if p.DiscoverySink == config.SinkVault {
			return "vault (no path set — run: jade init)"
		}
		return p.DiscoverySink
	case config.FieldAllowedVendors:
		return strings.Join(p.AllowedVendors, ", ")
	case config.FieldReviewStrictness:
		return p.ReviewStrictness
	case config.FieldProtectedPaths:
		if len(p.ProtectedPaths) == 0 {
			return "built-in defaults"
		}
		return strings.Join(p.ProtectedPaths, ", ")
	case config.FieldAgentArgs:
		return summarizeAgentArgs(p.AgentArgs)
	}
	return ""
}

// summarizeAgentArgs renders the per-role native flags in role order, so two
// machines' output can be diffed.
func summarizeAgentArgs(args map[config.Role][]string) string {
	if len(args) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(args))
	for _, role := range config.Roles() {
		if flags, ok := args[role]; ok {
			parts = append(parts, fmt.Sprintf("%s=%s", role, strings.Join(flags, " ")))
		}
	}
	return strings.Join(parts, ", ")
}

// orLayer names the layer a value came from, defaulting to the built-ins for a
// field no layer supplied.
func orLayer(layer string) string {
	if layer == "" {
		return config.LayerDefault
	}
	return layer
}

// summarizeOrigin collapses a per-field origin to one label when every field
// agrees, which is the common case and keeps the table readable.
func summarizeOrigin(o config.Origin) string {
	if o.Vendor == o.Model && o.Model == o.Effort {
		return o.Vendor
	}
	return fmt.Sprintf("vendor:%s model:%s effort:%s", o.Vendor, o.Model, o.Effort)
}
