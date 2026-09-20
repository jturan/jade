// Package build runs the unattended loop between the two human gates: dispatch
// a builder, review its work, retry bounded failures, and open a pull request.
package build

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Verdict is how an agent says it went.
type Verdict string

const (
	// VerdictOK means the work is done and, for a reviewer, that nothing
	// blocking was found.
	VerdictOK Verdict = "ok"
	// VerdictBlocked means the agent could not finish, or the reviewer found
	// something that must be fixed before merge.
	VerdictBlocked Verdict = "blocked"
)

// Report is the contract between jade and any agent it dispatches.
//
// Agents report by writing a JSON file, not by printing to the terminal.
// Terminal output is unreliable to parse — an agent running on the alternate
// screen loses scrollback entirely — and a file works identically for every
// vendor.
type Report struct {
	Verdict Verdict `json:"verdict"`
	// Summary is one or two sentences, used in issue comments and PR bodies.
	Summary string `json:"summary"`
	// Findings are the specific things a reviewer wants changed.
	Findings []string `json:"findings,omitempty"`
	// TestsRun records whether the agent actually ran the test suite, so a
	// builder cannot claim success without having checked.
	TestsRun bool `json:"tests_run,omitempty"`
}

// Dir is where agents write their reports, relative to the repo root.
const Dir = ".jade"

// ReportPath returns the path an agent should write its report to.
func ReportPath(root, role string) string {
	return filepath.Join(root, Dir, role+".json")
}

// ReadReport loads an agent's report.
//
// A missing file is itself a failure: an agent that finished without reporting
// has not demonstrated anything, and treating silence as success is how a loop
// merges work nobody checked.
func ReadReport(path string) (Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Report{}, fmt.Errorf("agent wrote no report at %s", path)
		}
		return Report{}, err
	}

	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return Report{}, fmt.Errorf("agent wrote an unreadable report at %s: %w", path, err)
	}
	switch r.Verdict {
	case VerdictOK, VerdictBlocked:
	default:
		return Report{}, fmt.Errorf("agent reported an unknown verdict %q", r.Verdict)
	}
	return r, nil
}

// ClearReport removes a stale report before dispatching an agent, so a previous
// attempt's verdict can never be mistaken for this one's.
func ClearReport(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Blocking returns the findings that must be addressed, formatted for a prompt.
func (r Report) Blocking() string {
	var b strings.Builder
	if r.Summary != "" {
		b.WriteString(r.Summary)
		b.WriteString("\n")
	}
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "- %s\n", f)
	}
	return b.String()
}
