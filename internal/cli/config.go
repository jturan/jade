package cli

import (
	"fmt"
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

			fmt.Fprintf(out, "%s  %s\n", dimStyle.Render("config"), resolved.ConfigPath)
			fmt.Fprintf(out, "%s %s\n", dimStyle.Render("profile"), p.Name)
			fmt.Fprintf(out, "%s    %s\n", dimStyle.Render("host"), p.GitHost)
			fmt.Fprintf(out, "%s    %s\n", dimStyle.Render("sink"), p.DiscoverySink)
			fmt.Fprintf(out, "%s %v\n\n", dimStyle.Render("vendors"), p.AllowedVendors)

			// tabwriter measures cell width in bytes and cannot see ANSI
			// escapes, so the header is left unstyled to keep columns aligned.
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
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

// summarizeOrigin collapses a per-field origin to one label when every field
// agrees, which is the common case and keeps the table readable.
func summarizeOrigin(o config.Origin) string {
	if o.Vendor == o.Model && o.Model == o.Effort {
		return o.Vendor
	}
	return fmt.Sprintf("vendor:%s model:%s effort:%s", o.Vendor, o.Model, o.Effort)
}
