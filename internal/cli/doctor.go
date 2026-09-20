package cli

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var (
	okStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	warnStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	failStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	dimStyle  = lipgloss.NewStyle().Faint(true)
)

// check is a single environment probe run by `jade doctor`.
type check struct {
	name     string
	required bool
	// probe reports a detail string on success, or an error explaining the gap.
	probe func() (string, error)
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that this machine has what jade needs",
		Long: "Probes the tools jade depends on and reports what is missing.\n" +
			"Run this first on any new laptop.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			checks := []check{
				{"git", true, binaryCheck("git", "--version")},
				{"gh", true, ghCheck},
				{"herdr", true, binaryCheck("herdr", "--version")},
				{"claude", true, binaryCheck("claude", "--version")},
				{"codex", false, binaryCheck("codex", "--version")},
			}

			out := cmd.OutOrStdout()
			var missing int

			for _, c := range checks {
				detail, err := c.probe()
				switch {
				case err == nil:
					fmt.Fprintf(out, "%s %-8s %s\n", okStyle.Render("✓"), c.name, dimStyle.Render(detail))
				case c.required:
					missing++
					fmt.Fprintf(out, "%s %-8s %s\n", failStyle.Render("✗"), c.name, err)
				default:
					fmt.Fprintf(out, "%s %-8s %s\n", warnStyle.Render("–"), c.name, dimStyle.Render("optional; "+err.Error()))
				}
			}

			if missing > 0 {
				return fmt.Errorf("%d required dependenc%s missing", missing, plural(missing, "y", "ies"))
			}
			fmt.Fprintln(out, okStyle.Render("\nAll required dependencies present."))
			return nil
		},
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// binaryCheck returns a probe that runs a binary's version flag.
func binaryCheck(bin string, args ...string) func() (string, error) {
	return func() (string, error) {
		if _, err := exec.LookPath(bin); err != nil {
			return "", fmt.Errorf("not found on PATH")
		}
		out, err := exec.Command(bin, args...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("found, but %s %s failed", bin, strings.Join(args, " "))
		}
		return firstLine(out), nil
	}
}

// ghCheck verifies gh is not just installed but authenticated, since an
// unauthenticated gh fails much later and far less legibly.
func ghCheck() (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", fmt.Errorf("not found on PATH")
	}
	if err := exec.Command("gh", "auth", "status").Run(); err != nil {
		return "", fmt.Errorf("installed but not authenticated (run: gh auth login)")
	}
	out, _ := exec.Command("gh", "--version").CombinedOutput()
	return firstLine(out) + "; authenticated", nil
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
