// Package telemetry records what each agent run cost and whether it worked.
//
// The role defaults — Sonnet builders, Sonnet reviews — are educated guesses.
// On a budget-constrained subscription, guessing is expensive: a cheap builder
// that triggers two retries costs more than one expensive builder that gets it
// right. This is the data that settles that question instead of re-arguing it.
package telemetry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Outcome is how a single agent run ended.
type Outcome string

const (
	OutcomeOK      Outcome = "ok"
	OutcomeBlocked Outcome = "blocked"
	// OutcomeError is a failure of jade or the agent harness, not of the
	// work — a pane that would not start, a missing report. Kept distinct so
	// a flaky environment is not read as a model being bad at its job.
	OutcomeError Outcome = "error"
)

// Event is one agent run.
type Event struct {
	At       time.Time `json:"at"`
	Repo     string    `json:"repo,omitempty"`
	Unit     int       `json:"unit,omitempty"`
	Role     string    `json:"role"`
	Vendor   string    `json:"vendor,omitempty"`
	Model    string    `json:"model,omitempty"`
	Effort   string    `json:"effort,omitempty"`
	Attempt  int       `json:"attempt,omitempty"`
	Seconds  float64   `json:"seconds"`
	Outcome  Outcome   `json:"outcome"`
	Detail   string    `json:"detail,omitempty"`
	Retries  int       `json:"retries,omitempty"`
	Autonomy string    `json:"autonomy,omitempty"`
}

// RoleUnit is the pseudo-role recording a whole unit's outcome, alongside the
// individual agent runs that made it up.
const RoleUnit = "unit"

// Path returns the telemetry log location, honoring XDG_DATA_HOME.
func Path() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "jade", "telemetry.jsonl"), nil
}

// Log appends events to a JSONL file.
type Log struct{ path string }

// Open returns a Log writing to path. An empty path uses the default location.
func Open(path string) (*Log, error) {
	if path == "" {
		p, err := Path()
		if err != nil {
			return nil, err
		}
		path = p
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return &Log{path: path}, nil
}

// Append records one event.
//
// Callers are expected to ignore the error: losing a telemetry line is a worse
// outcome than losing a build, but only barely, and never worth failing a run
// that otherwise succeeded.
func (l *Log) Append(e Event) error {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write(append(data, '\n'))
	return err
}

// Read loads every event from a log.
//
// A malformed line is skipped rather than failing the read: a partially written
// line from an interrupted run should not make the whole history unreadable.
func Read(path string) ([]Event, error) {
	if path == "" {
		p, err := Path()
		if err != nil {
			return nil, err
		}
		path = p
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var events []Event
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		events = append(events, e)
	}
	return events, nil
}

// Stat summarizes the runs sharing one role, model and effort.
type Stat struct {
	Role, Model, Effort string
	Runs                int
	OK                  int
	Blocked             int
	Errors              int
	// JudgedSeconds is the time spent on runs the model is answerable for.
	// Harness errors are left out, so it is not the wall time of the group.
	JudgedSeconds float64
}

// SuccessRate is the share of runs that reached a good outcome, ignoring
// harness errors — a pane that failed to start says nothing about the model.
func (s Stat) SuccessRate() float64 {
	judged := s.OK + s.Blocked
	if judged == 0 {
		return 0
	}
	return float64(s.OK) / float64(judged)
}

// MeanSeconds is the average duration of a judged run, ignoring harness
// errors for the same reason SuccessRate does. An agent killed before it could
// report, or one held to its role timeout after it died, measures the
// environment rather than how long the model takes — and being a timeout, it
// is far enough from a real run to move the mean on its own.
func (s Stat) MeanSeconds() float64 {
	judged := s.OK + s.Blocked
	if judged == 0 {
		return 0
	}
	return s.JudgedSeconds / float64(judged)
}

// Key identifies a Stat group.
func (s Stat) Key() string {
	return fmt.Sprintf("%s/%s/%s", s.Role, s.Model, s.Effort)
}

// Summarize groups events by role, model and effort.
func Summarize(events []Event) []Stat {
	groups := map[string]*Stat{}

	for _, e := range events {
		if e.Role == RoleUnit {
			continue // unit rows are outcomes, not agent runs
		}
		key := fmt.Sprintf("%s\x00%s\x00%s", e.Role, e.Model, e.Effort)
		s, ok := groups[key]
		if !ok {
			s = &Stat{Role: e.Role, Model: e.Model, Effort: e.Effort}
			groups[key] = s
		}
		s.Runs++
		switch e.Outcome {
		case OutcomeOK:
			s.OK++
			s.JudgedSeconds += e.Seconds
		case OutcomeBlocked:
			s.Blocked++
			s.JudgedSeconds += e.Seconds
		case OutcomeError:
			s.Errors++
		}
	}

	out := make([]Stat, 0, len(groups))
	for _, s := range groups {
		out = append(out, *s)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Key() < out[b].Key() })
	return out
}

// ForUnit returns the events for one unit, oldest first.
func ForUnit(events []Event, unit int) []Event {
	var out []Event
	for _, e := range events {
		if e.Unit == unit {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].At.Before(out[b].At) })
	return out
}

// openAppend exposes the log's append handle for tests that need to write a
// deliberately malformed line.
func openAppend(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
}
