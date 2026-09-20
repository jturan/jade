package plan

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jturan/jade/internal/state"
)

// Creator is the subset of the state store apply needs. Narrow so the two-pass
// write can be tested without touching GitHub.
type Creator interface {
	CreateIssue(ctx context.Context, in state.NewIssue) (state.Issue, error)
	UpdateBody(ctx context.Context, number int, body string) error
	EnsureLabels(ctx context.Context) error
}

// Result records what an apply created.
type Result struct {
	// Numbers maps plan-local unit IDs to real issue numbers.
	Numbers  map[int]int
	Issues   []state.Issue
	Tracking state.Issue
}

// Apply creates an issue per unit and a tracking issue for the sequence.
//
// It writes in two passes. Dependencies are recorded as issue numbers, which do
// not exist until the issues do, so the first pass creates every issue with an
// empty depends_on and the second fills them in. A single pass is impossible
// without guessing issue numbers, and guessing them is how a plan silently
// sequences itself wrong.
func Apply(ctx context.Context, c Creator, p *Plan) (*Result, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if err := c.EnsureLabels(ctx); err != nil {
		return nil, fmt.Errorf("ensuring labels: %w", err)
	}

	res := &Result{Numbers: make(map[int]int, len(p.Units))}

	// Pass 1: create issues in dependency order.
	for _, id := range p.Order() {
		u, ok := p.Unit(id)
		if !ok {
			continue
		}

		body, err := state.RenderUnit(u.Body, state.Unit{
			Number:         u.ID,
			DependsOn:      nil, // filled in pass 2
			Autonomy:       autonomyOrDefault(u.Autonomy),
			Builder:        u.Builder,
			CodeReview:     u.CodeReview,
			SecurityReview: u.SecurityReview,
			RetryLimit:     retryOrDefault(u.RetryLimit),
		})
		if err != nil {
			return nil, fmt.Errorf("unit %d: %w", u.ID, err)
		}

		issue, err := c.CreateIssue(ctx, state.NewIssue{
			Title:     u.Title,
			Body:      body,
			Labels:    []string{state.LabelUnitOfWork, string(state.StatusReady)},
			Milestone: u.Milestone,
		})
		if err != nil {
			return nil, fmt.Errorf("creating issue for unit %d (%q): %w", u.ID, u.Title, err)
		}

		res.Numbers[u.ID] = issue.Number
		res.Issues = append(res.Issues, issue)
	}

	// Pass 2: rewrite depends_on now that every issue number is known.
	for _, issue := range res.Issues {
		unit, err := state.ParseUnit(issue.Body)
		if err != nil {
			return nil, fmt.Errorf("issue #%d: %w", issue.Number, err)
		}
		planUnit, ok := p.Unit(unit.Number)
		if !ok || len(planUnit.DependsOn) == 0 {
			continue
		}

		deps := make([]int, 0, len(planUnit.DependsOn))
		for _, dep := range planUnit.DependsOn {
			number, ok := res.Numbers[dep]
			if !ok {
				return nil, fmt.Errorf("unit %d depends on unit %d, which was not created", unit.Number, dep)
			}
			deps = append(deps, number)
		}
		sort.Ints(deps)
		unit.DependsOn = deps

		body, err := state.RenderUnit(issue.Body, unit)
		if err != nil {
			return nil, err
		}
		if err := c.UpdateBody(ctx, issue.Number, body); err != nil {
			return nil, fmt.Errorf("issue #%d: recording dependencies: %w", issue.Number, err)
		}
	}

	tracking, err := c.CreateIssue(ctx, state.NewIssue{
		Title:  "Tracking: " + p.Initiative,
		Body:   trackingBody(p, res),
		Labels: []string{"tracking"},
	})
	if err != nil {
		return nil, fmt.Errorf("creating tracking issue: %w", err)
	}
	res.Tracking = tracking

	return res, nil
}

func autonomyOrDefault(a state.Autonomy) state.Autonomy {
	if a == "" {
		return state.AutonomyGated
	}
	return a
}

func retryOrDefault(n int) int {
	if n == 0 {
		return state.DefaultRetryLimit
	}
	return n
}

func trackingBody(p *Plan, res *Result) string {
	var b strings.Builder

	if p.Summary != "" {
		b.WriteString(p.Summary)
		b.WriteString("\n\n")
	}
	if p.PlanNote != "" {
		fmt.Fprintf(&b, "Plan: %s\n\n", p.PlanNote)
	}
	b.WriteString("## Units\n\n")

	for _, id := range p.Order() {
		u, ok := p.Unit(id)
		if !ok {
			continue
		}
		number := res.Numbers[id]
		fmt.Fprintf(&b, "- [ ] #%d — %s", number, u.Title)

		var notes []string
		if len(u.DependsOn) > 0 {
			deps := make([]string, 0, len(u.DependsOn))
			for _, d := range u.DependsOn {
				deps = append(deps, fmt.Sprintf("#%d", res.Numbers[d]))
			}
			notes = append(notes, "after "+strings.Join(deps, ", "))
		}
		if u.SecurityReview {
			notes = append(notes, "security review: "+u.SecurityReviewReason)
		}
		if a := autonomyOrDefault(u.Autonomy); a != state.AutonomyGated {
			notes = append(notes, "autonomy: "+string(a))
		}
		if len(notes) > 0 {
			fmt.Fprintf(&b, " _(%s)_", strings.Join(notes, "; "))
		}
		b.WriteString("\n")
	}

	b.WriteString("\nSequencing comes from each issue's `depends_on`, not from this list.\n")
	return b.String()
}
