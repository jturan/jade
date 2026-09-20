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
