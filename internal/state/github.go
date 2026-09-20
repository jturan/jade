package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Label marking an issue as a unit of work the build loop may pick up.
const LabelUnitOfWork = "unit-of-work"

// Issue is a GitHub issue carrying a unit of work.
type Issue struct {
	Number int     `json:"number"`
	Title  string  `json:"title"`
	Body   string  `json:"body"`
	State  string  `json:"state"`
	URL    string  `json:"url"`
	Labels []Label `json:"labels"`
}

// Label is a GitHub label.
type Label struct {
	Name string `json:"name"`
}

// Status returns the issue's agent status label, or empty if it has none.
func (i Issue) Status() Status {
	for _, l := range i.Labels {
		for _, s := range Statuses() {
			if l.Name == string(s) {
				return s
			}
		}
	}
	return ""
}

// Closed reports whether the issue is closed. GitHub reports state in varying
// case depending on the API surface, so compare case-insensitively.
func (i Issue) Closed() bool {
	return strings.EqualFold(i.State, "closed")
}

// Store reads and writes orchestration state through the gh CLI.
type Store struct {
	// Repo is "owner/name". Empty means gh infers it from the working directory.
	Repo string
	run  func(ctx context.Context, args ...string) ([]byte, []byte, error)
}

// NewStore returns a Store backed by the gh CLI.
func NewStore(repo string) *Store {
	s := &Store{Repo: repo}
	s.run = s.execute
	return s
}

func (s *Store) execute(ctx context.Context, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return []byte(stdout.String()), []byte(stderr.String()), err
}

func (s *Store) gh(ctx context.Context, out any, args ...string) error {
	if s.Repo != "" {
		args = append(args, "--repo", s.Repo)
	}
	stdout, stderr, err := s.run(ctx, args...)
	if err != nil {
		msg := strings.TrimSpace(string(stderr))
		if msg == "" {
			msg = strings.TrimSpace(string(stdout))
		}
		return fmt.Errorf("gh %s: %s", strings.Join(args, " "), msg)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(stdout, out)
}

// ListUnits returns every unit-of-work issue, open and closed. Closed ones are
// needed to evaluate dependencies.
func (s *Store) ListUnits(ctx context.Context) ([]Issue, error) {
	var issues []Issue
	err := s.gh(ctx, &issues,
		"issue", "list",
		"--label", LabelUnitOfWork,
		"--state", "all",
		"--limit", "200",
		"--json", "number,title,body,state,url,labels",
	)
	if err != nil {
		return nil, err
	}
	return issues, nil
}

// Ready is a unit whose dependencies are satisfied and which is waiting to run.
type Ready struct {
	Issue Issue
	Unit  Unit
}

// ListReady returns the units that may be dispatched now: labeled agent:ready,
// still open, and with every dependency closed.
//
// Dependency order is what makes the loop sequential without a scheduler — the
// planner encodes the sequence, and the loop simply refuses to run ahead of it.
func (s *Store) ListReady(ctx context.Context) ([]Ready, error) {
	issues, err := s.ListUnits(ctx)
	if err != nil {
		return nil, err
	}

	closed := make(map[int]bool, len(issues))
	for _, i := range issues {
		closed[i.Number] = i.Closed()
	}

	var ready []Ready
	for _, issue := range issues {
		if issue.Closed() || issue.Status() != StatusReady {
			continue
		}
		unit, err := ParseUnit(issue.Body)
		if err != nil {
			// An issue without a jade block is still a unit; it simply runs
			// on role defaults rather than being skipped silently.
			var noBlock ErrNoBlock
			if !errors.As(err, &noBlock) {
				return nil, fmt.Errorf("issue #%d: %w", issue.Number, err)
			}
			unit = Unit{RetryLimit: DefaultRetryLimit, Autonomy: AutonomyGated}
		}

		if dependenciesMet(unit, closed) {
			ready = append(ready, Ready{Issue: issue, Unit: unit})
		}
	}
	return ready, nil
}

// dependenciesMet reports whether every issue this unit depends on is closed.
// An unknown dependency counts as unmet: a typo in a plan must stall the loop
// rather than quietly let a unit run out of order.
func dependenciesMet(u Unit, closed map[int]bool) bool {
	for _, dep := range u.DependsOn {
		done, known := closed[dep]
		if !known || !done {
			return false
		}
	}
	return true
}

// SetStatus moves an issue to a status, removing whichever status it had.
func (s *Store) SetStatus(ctx context.Context, number int, status Status) error {
	args := []string{"issue", "edit", strconv.Itoa(number), "--add-label", string(status)}
	for _, other := range Statuses() {
		if other != status {
			args = append(args, "--remove-label", string(other))
		}
	}
	return s.gh(ctx, nil, args...)
}

// Comment posts a comment on an issue. The build loop uses this to leave a
// failure summary legible from the issue alone.
func (s *Store) Comment(ctx context.Context, number int, body string) error {
	return s.gh(ctx, nil, "issue", "comment", strconv.Itoa(number), "--body", body)
}

// UpdateUnit rewrites an issue's jade block, leaving its prose untouched.
func (s *Store) UpdateUnit(ctx context.Context, issue Issue, u Unit) error {
	body, err := RenderUnit(issue.Body, u)
	if err != nil {
		return err
	}
	if body == issue.Body {
		return nil
	}
	return s.gh(ctx, nil, "issue", "edit", strconv.Itoa(issue.Number), "--body", body)
}
