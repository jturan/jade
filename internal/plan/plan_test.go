package plan

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jturan/jade/internal/state"
)

const samplePlan = `
initiative: Agent handoff
summary: Let agents hand work to each other.
plan_note: docs/plans/agent-handoff.md
units:
  - id: 1
    title: Define the handoff record
    body: |
      ## Problem
      Nothing describes a handoff.
    depends_on: []
  - id: 2
    title: Persist handoffs
    body: |
      ## Problem
      Handoffs are not stored.
    depends_on: [1]
    security_review: true
    security_review_reason: touches the datastore and PHI
  - id: 3
    title: Expose the handoff API
    body: |
      ## Problem
      No API surface.
    depends_on: [1, 2]
    autonomy: merge
`

func TestParseValidPlan(t *testing.T) {
	p, err := Parse([]byte(samplePlan))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Initiative != "Agent handoff" {
		t.Errorf("initiative = %q", p.Initiative)
	}
	if len(p.Units) != 3 {
		t.Fatalf("units = %d, want 3", len(p.Units))
	}
	if u, _ := p.Unit(3); u.Autonomy != state.AutonomyMerge {
		t.Errorf("unit 3 autonomy = %q, want merge", u.Autonomy)
	}
}

// Validation reports everything wrong at once. A planning agent that got three
// things wrong should learn all three in one pass, not one per round trip.
func TestValidateReportsAllProblems(t *testing.T) {
	_, err := Parse([]byte(`
initiative: ""
units:
  - id: 1
    title: ""
    body: ""
  - id: 1
    title: Duplicate id
    body: something
    depends_on: [99]
`))
	if err == nil {
		t.Fatal("expected a validation error")
	}

	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("error is not a *ValidationError: %T", err)
	}
	msg := strings.Join(invalid.Problems, "\n")
	for _, want := range []string{
		"initiative is required",
		"title is required",
		"body is required",
		"duplicate unit id 1",
		"depends on unknown unit 99",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error did not mention %q:\n%s", want, msg)
		}
	}
}

// A security-review recommendation with no reason cannot be judged at the
// approval gate, which is the whole point of surfacing it there.
func TestValidateRequiresSecurityReviewReason(t *testing.T) {
	_, err := Parse([]byte(`
initiative: X
units:
  - id: 1
    title: T
    body: B
    security_review: true
`))
	if err == nil || !strings.Contains(err.Error(), "security_review_reason") {
		t.Fatalf("expected a missing-reason error, got %v", err)
	}
}

// A cycle would leave the build loop with nothing ever dispatchable, which is a
// confusing way to fail. Catch it at the gate instead.
func TestValidateDetectsCycle(t *testing.T) {
	_, err := Parse([]byte(`
initiative: X
units:
  - id: 1
    title: A
    body: B
    depends_on: [3]
  - id: 2
    title: B
    body: B
    depends_on: [1]
  - id: 3
    title: C
    body: B
    depends_on: [2]
`))
	if err == nil || !strings.Contains(err.Error(), "dependency cycle") {
		t.Fatalf("expected a cycle error, got %v", err)
	}
}

func TestValidateRejectsSelfDependency(t *testing.T) {
	_, err := Parse([]byte("initiative: X\nunits:\n  - id: 1\n    title: A\n    body: B\n    depends_on: [1]\n"))
	if err == nil || !strings.Contains(err.Error(), "depends on itself") {
		t.Fatalf("expected a self-dependency error, got %v", err)
	}
}

// A single problem reads inline; several are summarized, because error
// renderers collapse newlines and the CLI lays the list out itself.
func TestValidationErrorMessage(t *testing.T) {
	one := &ValidationError{Problems: []string{"initiative is required"}}
	if !strings.Contains(one.Error(), "initiative is required") {
		t.Errorf("a single problem should read inline: %q", one.Error())
	}
	many := &ValidationError{Problems: []string{"a", "b", "c"}}
	if !strings.Contains(many.Error(), "3 problems") {
		t.Errorf("several problems should be summarized: %q", many.Error())
	}
}

func TestOrderIsDependencyFirst(t *testing.T) {
	p, err := Parse([]byte(samplePlan))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	order := p.Order()
	pos := map[int]int{}
	for i, id := range order {
		pos[id] = i
	}
	for _, u := range p.Units {
		for _, dep := range u.DependsOn {
			if pos[dep] > pos[u.ID] {
				t.Errorf("unit %d was ordered before its dependency %d: %v", u.ID, dep, order)
			}
		}
	}
}

// fakeCreator records issue creation and body updates in memory.
type fakeCreator struct {
	next    int
	created []state.NewIssue
	bodies  map[int]string
	labels  bool
}

func newFakeCreator() *fakeCreator {
	return &fakeCreator{next: 100, bodies: map[int]string{}}
}

func (f *fakeCreator) EnsureLabels(context.Context) error { f.labels = true; return nil }

func (f *fakeCreator) CreateIssue(_ context.Context, in state.NewIssue) (state.Issue, error) {
	f.next++
	f.created = append(f.created, in)
	f.bodies[f.next] = in.Body
	return state.Issue{Number: f.next, Title: in.Title, Body: in.Body, State: "OPEN"}, nil
}

func (f *fakeCreator) UpdateBody(_ context.Context, number int, body string) error {
	f.bodies[number] = body
	return nil
}

// Dependencies must be recorded as real issue numbers, which do not exist until
// the issues do. Getting this wrong sequences the whole plan incorrectly and
// would only show up much later, as units running out of order.
func TestApplyTranslatesPlanIDsToIssueNumbers(t *testing.T) {
	p, err := Parse([]byte(samplePlan))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	f := newFakeCreator()

	res, err := Apply(context.Background(), f, p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !f.labels {
		t.Error("apply should bootstrap the label taxonomy")
	}
	if len(res.Issues) != 3 {
		t.Fatalf("created %d issues, want 3", len(res.Issues))
	}

	// Unit 3 depends on plan units 1 and 2; the stored block must name their
	// issue numbers instead.
	n3 := res.Numbers[3]
	unit, err := state.ParseUnit(f.bodies[n3])
	if err != nil {
		t.Fatalf("parsing unit 3 body: %v", err)
	}
	want := []int{res.Numbers[1], res.Numbers[2]}
	if len(unit.DependsOn) != 2 || unit.DependsOn[0] != want[0] || unit.DependsOn[1] != want[1] {
		t.Errorf("depends_on = %v, want %v (issue numbers, not plan ids)", unit.DependsOn, want)
	}
	if unit.DependsOn[0] == 1 || unit.DependsOn[1] == 2 {
		t.Error("depends_on still holds plan-local ids")
	}
}

func TestApplyDefaultsAndLabels(t *testing.T) {
	p, _ := Parse([]byte(samplePlan))
	f := newFakeCreator()

	res, err := Apply(context.Background(), f, p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	unit, err := state.ParseUnit(f.bodies[res.Numbers[1]])
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if unit.Autonomy != state.AutonomyGated {
		t.Errorf("autonomy = %q, want gated by default", unit.Autonomy)
	}
	if unit.RetryLimit != state.DefaultRetryLimit {
		t.Errorf("retry_limit = %d, want %d", unit.RetryLimit, state.DefaultRetryLimit)
	}

	for _, in := range f.created[:len(f.created)-1] {
		if !hasLabel(in.Labels, state.LabelUnitOfWork) || !hasLabel(in.Labels, string(state.StatusReady)) {
			t.Errorf("unit issue %q missing labels: %v", in.Title, in.Labels)
		}
	}
}

func TestApplyCreatesTrackingIssue(t *testing.T) {
	p, _ := Parse([]byte(samplePlan))
	f := newFakeCreator()

	res, err := Apply(context.Background(), f, p)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.Tracking.Number == 0 {
		t.Fatal("no tracking issue was created")
	}

	body := res.Tracking.Body
	for id := 1; id <= 3; id++ {
		if !strings.Contains(body, fmt.Sprintf("#%d", res.Numbers[id])) {
			t.Errorf("tracking body does not reference issue #%d", res.Numbers[id])
		}
	}
	if !strings.Contains(body, "security review: touches the datastore and PHI") {
		t.Error("tracking body should carry the security-review reason")
	}
	if !strings.Contains(body, "autonomy: merge") {
		t.Error("tracking body should flag a non-default autonomy level")
	}
}

// An invalid plan must never reach GitHub — a half-applied plan is worse than
// no plan, since the issues exist but the sequence does not.
func TestApplyRefusesInvalidPlan(t *testing.T) {
	f := newFakeCreator()
	_, err := Apply(context.Background(), f, &Plan{Initiative: "X"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(f.created) != 0 {
		t.Errorf("an invalid plan created %d issues", len(f.created))
	}
}

func hasLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}
