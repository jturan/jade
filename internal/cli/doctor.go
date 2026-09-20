package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
			fmt.Fprintf(out, "%s %s/%s\n\n",
				dimStyle.Render("platform"), runtime.GOOS, runtime.GOARCH)

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
		path, err := lookPath(bin)
		if err != nil {
			return "", err
		}
		out, err := exec.Command(path, args...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("found, but %s %s failed", bin, strings.Join(args, " "))
		}
		return firstLine(out), nil
	}
}

// extraPrefixes are install locations worth checking when a binary is not on
// PATH. On macOS, Homebrew's prefix differs between Apple Silicon and Intel and
// is frequently missing from a non-login shell's PATH, so "not found" is a
// misleading thing to tell someone who has the tool installed.
func extraPrefixes() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/opt/homebrew/bin", "/usr/local/bin"}
	case "linux":
		return []string{"/home/linuxbrew/.linuxbrew/bin", "/usr/local/bin"}
	default:
		return nil
	}
}

// lookPath finds a binary on PATH, falling back to known install prefixes so a
// tool that is installed but unreachable reports the actual problem.
func lookPath(bin string) (string, error) {
	return lookPathIn(bin, extraPrefixes())
}

// lookPathIn is lookPath with the fallback prefixes supplied, so the
// not-on-PATH branch can be tested without a Mac.
func lookPathIn(bin string, prefixes []string) (string, error) {
	if path, err := exec.LookPath(bin); err == nil {
		return path, nil
	}
	for _, dir := range prefixes {
		candidate := filepath.Join(dir, bin)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return "", fmt.Errorf("installed at %s but not on PATH — add %s to PATH", candidate, dir)
		}
	}
	return "", fmt.Errorf("not found on PATH")
}

// ghCheck verifies gh is not just installed but authenticated, since an
// unauthenticated gh fails much later and far less legibly.
func ghCheck() (string, error) {
	path, err := lookPath("gh")
	if err != nil {
		return "", err
	}
	if err := exec.Command(path, "auth", "status").Run(); err != nil {
		return "", fmt.Errorf("installed but not authenticated (run: gh auth login)")
	}
	out, _ := exec.Command(path, "--version").CombinedOutput()
	return firstLine(out) + "; authenticated", nil
}

func firstLine(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
