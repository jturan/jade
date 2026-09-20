// Package build runs the unattended loop between the two human gates: dispatch
// a builder, review its work, retry bounded failures, and open a pull request.
package build

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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

// Polling cadence for AwaitReport. Variables so tests can shorten them.
var (
	awaitPoll = time.Second
	// awaitQuiet is how long an agent must stay settled without a report
	// before AwaitReport accepts that none is coming.
	awaitQuiet = 20 * time.Second
)

// AwaitReport blocks until the agent's report at path exists and parses, the
// agent has stayed settled for a while without writing one, or the deadline
// passes. A zero deadline means no deadline.
//
// A runner's own "settled" signal is not proof the work is done: herdr's
// `agent prompt --wait` returns on the first idle state it observes, which can
// come mid-task. The report is the agent's own statement that it finished, so
// that is the signal that counts. settled is still consulted so an agent that
// stopped without reporting does not hold the loop until the deadline; a nil
// settled waits for the report or the deadline alone.
func AwaitReport(ctx context.Context, path string, deadline time.Time, settled func(context.Context) (bool, error)) error {
	var settledSince time.Time
	for {
		if _, err := ReadReport(path); err == nil {
			return nil
		}

		now := time.Now()
		if !deadline.IsZero() && now.After(deadline) {
			return fmt.Errorf("agent did not write a report at %s before its timeout", path)
		}

		if settled != nil {
			done, err := settled(ctx)
			if err != nil {
				return err
			}
			switch {
			case !done:
				settledSince = time.Time{}
			case settledSince.IsZero():
				settledSince = now
			case now.Sub(settledSince) >= awaitQuiet:
				// Let the caller read the (absent or unreadable) report and
				// fail with the usual message.
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(awaitPoll):
		}
	}
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
