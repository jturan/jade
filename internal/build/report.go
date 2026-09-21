// Package build runs the unattended loop between the two human gates: dispatch
// a builder, review its work, retry bounded failures, and open a pull request.
package build

import (
	"context"
	"encoding/json"
	"errors"
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

// Why a report could not be read. AwaitReport needs to tell "not yet" from
// "never": an absent report may still be coming, an unparsable one may be
// half-written, but a complete report jade cannot act on will never improve.
var (
	errNoReport  = errors.New("agent wrote no report")
	errBadReport = errors.New("agent wrote an unreadable report")
)

// ReadReport loads an agent's report.
//
// A missing file is itself a failure: an agent that finished without reporting
// has not demonstrated anything, and treating silence as success is how a loop
// merges work nobody checked.
func ReadReport(path string) (Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Report{}, fmt.Errorf("%w at %s", errNoReport, path)
		}
		return Report{}, err
	}

	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return Report{}, fmt.Errorf("%w at %s: %w", errBadReport, path, err)
	}
	switch r.Verdict {
	case VerdictOK, VerdictBlocked:
	default:
		return Report{}, fmt.Errorf("agent reported an unknown verdict %q in %s", r.Verdict, path)
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

// Cadences for AwaitReport. Variables so tests can shorten them.
var (
	// awaitPoll is how often the report is looked for.
	awaitPoll = time.Second
	// awaitProbe is how often the agent is asked whether it is still there.
	// Slower than the poll because it costs a call to the runner, and an
	// agent that has gone away stays gone.
	awaitProbe = 15 * time.Second
	// awaitSettle is how long a report that exists but does not parse is
	// given to finish being written before it is called corrupt.
	awaitSettle = 5 * time.Second
)

// AwaitReport blocks until the agent's report at path exists and parses, the
// agent is gone, the report proves unusable, the deadline passes, or ctx is
// cancelled. A zero deadline means no deadline.
//
// A runner's own "settled" signal is not proof the work is done: herdr's
// `agent prompt --wait` returns on the first idle state it observes, which can
// come mid-task. The report is the agent's own statement that it finished, so
// that is the only completion signal trusted here.
//
// Waiting out the deadline is the answer only while the agent might still be
// working. gone, when supplied, reports whether the runner is certain the
// agent is no longer there at all; an agent that died is a fast failure rather
// than a role timeout's worth of silence recorded as the run's duration.
func AwaitReport(ctx context.Context, path string, deadline time.Time, gone func(context.Context) bool) error {
	var unparsableSince time.Time
	nextProbe := time.Now().Add(awaitProbe)

	for {
		switch _, err := ReadReport(path); {
		case err == nil:
			return nil
		case errors.Is(err, errNoReport):
			// Nothing written yet, which is what waiting is for.
		case errors.Is(err, errBadReport):
			// A report caught mid-write parses as soon as the agent finishes
			// the file. One still broken after that is corrupt, and holding
			// the loop to the deadline would report the wrong cause.
			if unparsableSince.IsZero() {
				unparsableSince = time.Now()
			}
			if time.Since(unparsableSince) >= awaitSettle {
				return err
			}
		default:
			// A complete report jade cannot act on — an unknown verdict.
			// More time will not change it.
			return err
		}

		if gone != nil && time.Now().After(nextProbe) {
			nextProbe = time.Now().Add(awaitProbe)
			if gone(ctx) {
				// The agent may have reported and exited between the read
				// above and this check, so give the file the last word.
				if _, err := ReadReport(path); err == nil {
					return nil
				}
				return fmt.Errorf("agent stopped without writing a report at %s", path)
			}
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return fmt.Errorf("agent did not write a report at %s before its timeout", path)
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
