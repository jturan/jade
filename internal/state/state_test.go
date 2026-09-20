package state

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jturan/jade/internal/config"
)

const sampleBody = "## Problem\n\nSomething is broken.\n\n## Plan\n\nFix it.\n\n" +
	"```yaml jade\n" +
	"unit: 3\n" +
	"depends_on: [1, 2]\n" +
	"builder: {vendor: claude, model: sonnet, effort: medium}\n" +
	"code_review: {vendor: claude, model: opus, effort: high}\n" +
	"security_review: true\n" +
	"retry_limit: 2\n" +
	"```\n"

func TestParseUnit(t *testing.T) {
	u, err := ParseUnit(sampleBody)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.Number != 3 {
		t.Errorf("unit = %d, want 3", u.Number)
	}
	if len(u.DependsOn) != 2 || u.DependsOn[0] != 1 || u.DependsOn[1] != 2 {
		t.Errorf("depends_on = %v, want [1 2]", u.DependsOn)
	}
	if u.CodeReview.Model != "opus" || u.CodeReview.Effort != "high" {
		t.Errorf("code_review = %+v, want opus/high", u.CodeReview)
	}
	if !u.SecurityReview {
		t.Error("security_review should be true")
	}
}

// Defaults must be applied on parse, so the build loop never has to decide what
// an absent retry limit or autonomy level means.
func TestParseUnitAppliesDefaults(t *testing.T) {
	u, err := ParseUnit("```yaml jade\nunit: 1\n```")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.RetryLimit != DefaultRetryLimit {
		t.Errorf("retry_limit = %d, want %d", u.RetryLimit, DefaultRetryLimit)
	}
	if u.Autonomy != AutonomyGated {
		t.Errorf("autonomy = %q, want gated — absent must never mean unattended", u.Autonomy)
	}
}

// An issue body that documents the schema contains an example jade block. The
// example must never shadow the unit's real configuration — doing so silently
// gives the unit someone else's settings and, with no depends_on, lets it run
// out of sequence.
func TestParseUnitUsesLastBlockNotFirst(t *testing.T) {
	body := "## Plan\n\nSet it like this:\n\n" +
		"```yaml jade\nunit: 3\nautonomy: merge\n```\n\n" +
		"## Config\n\n" +
		"```yaml jade\nunit: 15\ndepends_on: [7]\nsecurity_review: true\n```\n"

	u, err := ParseUnit(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if u.Number != 15 {
		t.Errorf("unit = %d, want 15 — an example block shadowed the real one", u.Number)
	}
	if len(u.DependsOn) != 1 || u.DependsOn[0] != 7 {
		t.Errorf("depends_on = %v, want [7] — losing this lets the unit run out of sequence", u.DependsOn)
	}
	if u.Autonomy != AutonomyGated {
		t.Errorf("autonomy = %q, want gated — it must not inherit the example's", u.Autonomy)
	}
	if !u.SecurityReview {
		t.Error("security_review was lost")
	}
}

// RenderUnit must rewrite the same block ParseUnit reads, or a write would
// clobber an example and leave the real config untouched.
func TestRenderUnitRewritesTheLastBlock(t *testing.T) {
	body := "```yaml jade\nunit: 3\nautonomy: merge\n```\n\ntext\n\n" +
		"```yaml jade\nunit: 15\ndepends_on: [7]\n```\n"

	u, err := ParseUnit(body)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	u.RetryLimit = 5

	got, err := RenderUnit(body, u)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(got, "unit: 3") || !strings.Contains(got, "autonomy: merge") {
		t.Error("the example block was clobbered")
	}
	round, err := ParseUnit(got)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if round.Number != 15 || round.RetryLimit != 5 {
		t.Errorf("the operative block was not updated: %+v", round)
	}
}

func TestParseUnitNoBlock(t *testing.T) {
	_, err := ParseUnit("Just prose, no configuration.")
	var noBlock ErrNoBlock
	if !errors.As(err, &noBlock) {
		t.Fatalf("error = %v, want ErrNoBlock", err)
	}
}

// Prose must survive a rewrite untouched: the issue body is a human artifact
// that happens to carry machine settings, not the other way round.
func TestRenderUnitPreservesProse(t *testing.T) {
	u, err := ParseUnit(sampleBody)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	u.Autonomy = AutonomyMerge

	got, err := RenderUnit(sampleBody, u)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	for _, prose := range []string{"## Problem", "Something is broken.", "## Plan", "Fix it."} {
		if !strings.Contains(got, prose) {
			t.Errorf("rendered body lost %q", prose)
		}
	}
	if strings.Count(got, "```yaml jade") != 1 {
		t.Errorf("expected exactly one jade block, got:\n%s", got)
	}

	round, err := ParseUnit(got)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if round.Autonomy != AutonomyMerge || round.Number != 3 || !round.SecurityReview {
		t.Errorf("round-tripped unit lost data: %+v", round)
	}
}

// Rendering must be idempotent, or every build-loop write would produce a
// spurious issue edit.
func TestRenderUnitIsIdempotent(t *testing.T) {
	u, _ := ParseUnit(sampleBody)

	once, err := RenderUnit(sampleBody, u)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	reparsed, err := ParseUnit(once)
	if err != nil {
		t.Fatalf("reparse: %v", err)
	}
	twice, err := RenderUnit(once, reparsed)
	if err != nil {
		t.Fatalf("re-render: %v", err)
	}
	if once != twice {
		t.Errorf("rendering twice differed:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

func TestRenderUnitAppendsWhenAbsent(t *testing.T) {
	got, err := RenderUnit("## Problem\n\nNo block here.", Unit{Number: 7})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(got, "## Problem") {
		t.Error("prose was lost")
	}
	u, err := ParseUnit(got)
	if err != nil || u.Number != 7 {
		t.Errorf("appended block did not parse back: %+v %v", u, err)
	}
}

func TestOverridesOnlyIncludesPinnedRoles(t *testing.T) {
	u := Unit{Builder: config.Agent{Model: "opus"}}
	o := u.Overrides()

	if _, ok := o[config.RoleBuilder]; !ok {
		t.Error("builder override missing")
	}
	if _, ok := o[config.RoleCodeReview]; ok {
		t.Error("an unset role must not be overridden, or it would erase the defaults")
	}
}

// stubStore replaces the gh invocation with a canned response.
func stubStore(stdout string) *Store {
	s := &Store{}
	s.run = func(_ context.Context, _ ...string) ([]byte, []byte, error) {
		return []byte(stdout), nil, nil
	}
	return s
}

func TestListReadyRespectsDependencies(t *testing.T) {
	const issues = `[
{"number":1,"title":"config","state":"CLOSED","labels":[{"name":"unit-of-work"},{"name":"agent:done"}],
 "body":"` + "```yaml jade\\nunit: 1\\ndepends_on: []\\n```" + `"},
{"number":3,"title":"state","state":"OPEN","labels":[{"name":"unit-of-work"},{"name":"agent:ready"}],
 "body":"` + "```yaml jade\\nunit: 3\\ndepends_on: [1]\\n```" + `"},
{"number":7,"title":"loop","state":"OPEN","labels":[{"name":"unit-of-work"},{"name":"agent:ready"}],
 "body":"` + "```yaml jade\\nunit: 7\\ndepends_on: [3]\\n```" + `"},
{"number":9,"title":"busy","state":"OPEN","labels":[{"name":"unit-of-work"},{"name":"agent:in-progress"}],
 "body":"` + "```yaml jade\\nunit: 9\\ndepends_on: []\\n```" + `"}
]`

	ready, err := stubStore(issues).ListReady(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("ready = %d units, want 1: %+v", len(ready), ready)
	}
	if ready[0].Issue.Number != 3 {
		t.Errorf("ready issue = #%d, want #3", ready[0].Issue.Number)
	}
}

// A dependency that does not exist must stall the unit. A typo in a plan should
// never be interpreted as "no dependency".
func TestListReadyTreatsUnknownDependencyAsUnmet(t *testing.T) {
	const issues = `[
{"number":5,"state":"OPEN","labels":[{"name":"agent:ready"}],
 "body":"` + "```yaml jade\\nunit: 5\\ndepends_on: [404]\\n```" + `"}
]`

	ready, err := stubStore(issues).ListReady(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ready) != 0 {
		t.Errorf("a unit with an unknown dependency must not be ready: %+v", ready)
	}
}

// A unit-of-work issue with no jade block still runs, on role defaults.
func TestListReadyToleratesMissingBlock(t *testing.T) {
	const issues = `[{"number":2,"state":"OPEN","labels":[{"name":"agent:ready"}],"body":"just prose"}]`

	ready, err := stubStore(issues).ListReady(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ready) != 1 {
		t.Fatalf("want 1 ready unit, got %d", len(ready))
	}
	if ready[0].Unit.RetryLimit != DefaultRetryLimit {
		t.Errorf("retry_limit = %d, want the default", ready[0].Unit.RetryLimit)
	}
}

func TestIssueStatusAndClosed(t *testing.T) {
	i := Issue{State: "CLOSED", Labels: []Label{{Name: "unit-of-work"}, {Name: "agent:done"}}}
	if !i.Closed() {
		t.Error("CLOSED should be recognized regardless of case")
	}
	if i.Status() != StatusDone {
		t.Errorf("status = %q, want agent:done", i.Status())
	}
	if (Issue{Labels: []Label{{Name: "unit-of-work"}}}).Status() != "" {
		t.Error("an issue with no status label should report an empty status")
	}
}

// Every status must have a managed label. A status the build loop applies but
// `labels sync` never creates would fail on a fresh repo — and only at the
// moment the loop tried to use it.
func TestManagedLabelsCoverEveryStatus(t *testing.T) {
	managed := map[string]bool{}
	for _, l := range ManagedLabels() {
		if l.Color == "" || l.Description == "" {
			t.Errorf("label %q is missing a color or description", l.Name)
		}
		managed[l.Name] = true
	}

	for _, s := range Statuses() {
		if !managed[string(s)] {
			t.Errorf("status %q has no managed label", s)
		}
	}
	for _, required := range []string{LabelUnitOfWork, LabelTracking} {
		if !managed[required] {
			t.Errorf("%q is not in the managed taxonomy", required)
		}
	}
}

// recordingStore answers `issue list` with a canned listing and `pr view` with
// the pull request's state from prStates, and records every other gh call, so a
// test can assert which edits were made.
func recordingStore(listing string, prStates map[string]string) (*Store, *[][]string) {
	var calls [][]string
	s := &Store{}
	s.run = func(_ context.Context, args ...string) ([]byte, []byte, error) {
		if len(args) >= 2 && args[0] == "issue" && args[1] == "list" {
			return []byte(listing), nil, nil
		}
		if len(args) >= 3 && args[0] == "pr" && args[1] == "view" {
			return []byte(`{"state":"` + prStates[args[2]] + `"}`), nil, nil
		}
		calls = append(calls, args)
		return nil, nil, nil
	}
	return s, &calls
}

func TestReconcile(t *testing.T) {
	prStates := map[string]string{
		"https://github.com/o/r/pull/1": "MERGED",
		"https://github.com/o/r/pull/2": "CLOSED",
	}
	tests := []struct {
		name      string
		issue     string
		wantEdit  bool
		wantMoved bool
		wantSeen  bool
	}{
		{
			name:      "closed completed in review moves to done",
			issue:     `{"number":27,"state":"CLOSED","stateReason":"COMPLETED","closedByPullRequestsReferences":[{"number":1,"url":"https://github.com/o/r/pull/1"}],"labels":[{"name":"agent:review"}]}`,
			wantEdit:  true,
			wantMoved: true,
			wantSeen:  true,
		},
		{
			name:      "closed completed in progress moves to done",
			issue:     `{"number":28,"state":"CLOSED","stateReason":"COMPLETED","closedByPullRequestsReferences":[{"number":2,"url":"https://github.com/o/r/pull/2"},{"number":1,"url":"https://github.com/o/r/pull/1"}],"labels":[{"name":"agent:in-progress"}]}`,
			wantEdit:  true,
			wantMoved: true,
			wantSeen:  true,
		},
		{
			// GitHub records COMPLETED for a plain Close click as well.
			name:     "closed completed with no linked pr is left alone",
			issue:    `{"number":32,"state":"CLOSED","stateReason":"COMPLETED","closedByPullRequestsReferences":[],"labels":[{"name":"agent:review"}]}`,
			wantSeen: true,
		},
		{
			name:     "closed completed with an unmerged pr is left alone",
			issue:    `{"number":33,"state":"CLOSED","stateReason":"COMPLETED","closedByPullRequestsReferences":[{"number":2,"url":"https://github.com/o/r/pull/2"}],"labels":[{"name":"agent:in-progress"}]}`,
			wantSeen: true,
		},
		{
			name:     "closed not planned is left alone",
			issue:    `{"number":29,"state":"CLOSED","stateReason":"NOT_PLANNED","labels":[{"name":"agent:review"}]}`,
			wantSeen: true,
		},
		{
			name:  "open in review is left alone",
			issue: `{"number":30,"state":"OPEN","stateReason":"","labels":[{"name":"agent:review"}]}`,
		},
		{
			name:  "already done makes no gh call",
			issue: `{"number":31,"state":"CLOSED","stateReason":"COMPLETED","labels":[{"name":"agent:done"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, calls := recordingStore("["+tt.issue+"]", prStates)
			got, err := s.Reconcile(context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantEdit {
				if len(*calls) != 1 {
					t.Fatalf("gh calls = %v, want one label edit", *calls)
				}
				cmd := strings.Join((*calls)[0], " ")
				if !strings.Contains(cmd, "--add-label agent:done") {
					t.Errorf("edit = %q, want it to add agent:done", cmd)
				}
			} else if len(*calls) != 0 {
				t.Errorf("gh calls = %v, want none", *calls)
			}

			if !tt.wantSeen {
				if len(got) != 0 {
					t.Errorf("reconciliations = %+v, want none", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("reconciliations = %+v, want one", got)
			}
			if got[0].Moved != tt.wantMoved {
				t.Errorf("moved = %v, want %v", got[0].Moved, tt.wantMoved)
			}
		})
	}
}

// Reconciling twice must change nothing the second time: once the label reads
// agent:done, the issue is no longer a candidate.
func TestReconcileIsIdempotent(t *testing.T) {
	const after = `[{"number":27,"state":"CLOSED","stateReason":"COMPLETED","labels":[{"name":"agent:done"}]}]`
	s, calls := recordingStore(after, nil)
	got, err := s.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 || len(*calls) != 0 {
		t.Errorf("second pass changed state: reconciliations %+v, calls %v", got, *calls)
	}
}

func TestReconcileReportsEditFailure(t *testing.T) {
	s := &Store{}
	s.run = func(_ context.Context, args ...string) ([]byte, []byte, error) {
		if args[1] == "list" {
			return []byte(`[{"number":27,"state":"CLOSED","stateReason":"COMPLETED","closedByPullRequestsReferences":[{"number":1,"url":"https://github.com/o/r/pull/1"}],"labels":[{"name":"agent:review"}]}]`), nil, nil
		}
		if args[1] == "view" {
			return []byte(`{"state":"MERGED"}`), nil, nil
		}
		return nil, []byte("HTTP 403"), errors.New("exit 1")
	}
	if _, err := s.Reconcile(context.Background()); err == nil || !strings.Contains(err.Error(), "#27") {
		t.Errorf("err = %v, want a failure naming #27", err)
	}
}
