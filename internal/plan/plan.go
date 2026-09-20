// Package plan turns an approved breakdown into GitHub issues.
//
// The planning agent's output contract is a plan.yml file rather than prose, so
// everything downstream of the approval gate is deterministic and testable. An
// agent that writes free text would have to be re-parsed on every run; an agent
// that writes a schema can be validated once and then trusted.
package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jturan/jade/internal/config"
	"github.com/jturan/jade/internal/state"
	"gopkg.in/yaml.v3"
)

// Unit is one unit of work in a proposed plan, before it becomes an issue.
type Unit struct {
	// ID is plan-local and used only to express dependencies before the
	// issues exist and real numbers are known.
	ID    int    `yaml:"id"`
	Title string `yaml:"title"`
	// Body is the issue body: problem, plan, acceptance criteria.
	Body string `yaml:"body"`
	// DependsOn holds plan-local IDs, translated to issue numbers on apply.
	DependsOn []int `yaml:"depends_on"`

	Autonomy   state.Autonomy `yaml:"autonomy,omitempty"`
	Builder    config.Agent   `yaml:"builder"`
	CodeReview config.Agent   `yaml:"code_review"`

	SecurityReview bool `yaml:"security_review"`
	// SecurityReviewReason is the planner's justification, shown at the
	// approval gate. A recommendation without a reason cannot be judged.
	SecurityReviewReason string `yaml:"security_review_reason,omitempty"`

	RetryLimit int `yaml:"retry_limit,omitempty"`
	// Milestone is optional and applied to every unit that names it.
	Milestone string `yaml:"milestone,omitempty"`
}

// Plan is a full proposed breakdown, the artifact a human approves.
type Plan struct {
	Initiative string `yaml:"initiative"`
	Summary    string `yaml:"summary,omitempty"`
	// PlanNote points at the sectioned plan document this came from.
	PlanNote string `yaml:"plan_note,omitempty"`
	Units    []Unit `yaml:"units"`
}

// Parse reads a plan from YAML and validates it.
func Parse(data []byte) (*Plan, error) {
	var p Plan
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parsing plan: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate reports every way the plan is unusable, rather than only the first.
// A planning agent that got several things wrong should learn all of them in
// one pass instead of one per round trip.
func (p *Plan) Validate() error {
	var problems []string

	if strings.TrimSpace(p.Initiative) == "" {
		problems = append(problems, "initiative is required")
	}
	if len(p.Units) == 0 {
		problems = append(problems, "plan contains no units")
	}

	seen := map[int]bool{}
	for i, u := range p.Units {
		where := fmt.Sprintf("unit %d", u.ID)
		if u.ID == 0 {
			where = fmt.Sprintf("units[%d]", i)
			problems = append(problems, where+": id is required and must be non-zero")
		}
		if seen[u.ID] && u.ID != 0 {
			problems = append(problems, fmt.Sprintf("duplicate unit id %d", u.ID))
		}
		seen[u.ID] = true

		if strings.TrimSpace(u.Title) == "" {
			problems = append(problems, where+": title is required")
		}
		if strings.TrimSpace(u.Body) == "" {
			problems = append(problems, where+": body is required")
		}
		if u.SecurityReview && strings.TrimSpace(u.SecurityReviewReason) == "" {
			problems = append(problems, where+
				": security_review is set but security_review_reason is empty — a recommendation without a reason cannot be judged")
		}
		switch u.Autonomy {
		case "", state.AutonomyGated, state.AutonomyMerge, state.AutonomyFull:
		default:
			problems = append(problems, fmt.Sprintf("%s: unknown autonomy %q", where, u.Autonomy))
		}
	}

	for _, u := range p.Units {
		for _, dep := range u.DependsOn {
			if !seen[dep] {
				problems = append(problems, fmt.Sprintf("unit %d depends on unknown unit %d", u.ID, dep))
			}
			if dep == u.ID {
				problems = append(problems, fmt.Sprintf("unit %d depends on itself", u.ID))
			}
		}
	}

	if cycle := p.findCycle(); len(cycle) > 0 {
		problems = append(problems, "dependency cycle: "+formatCycle(cycle))
	}

	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

// ValidationError carries every problem found in a plan. The problems are kept
// as a list rather than a single joined string so a caller can lay them out —
// error renderers collapse newlines, and a run-on sentence of six problems is
// not something anyone will read.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	if len(e.Problems) == 1 {
		return "plan is invalid: " + e.Problems[0]
	}
	return fmt.Sprintf("plan is invalid (%d problems)", len(e.Problems))
}

// findCycle returns a dependency cycle if one exists. A cycle would leave the
// build loop with nothing ever dispatchable, which is a confusing way to fail.
func (p *Plan) findCycle() []int {
	deps := make(map[int][]int, len(p.Units))
	for _, u := range p.Units {
		deps[u.ID] = u.DependsOn
	}

	const (
		unvisited = 0
		active    = 1
		done      = 2
	)
	mark := make(map[int]int, len(deps))
	var stack, cycle []int

	var visit func(int) bool
	visit = func(id int) bool {
		switch mark[id] {
		case active:
			// Trim the stack to the repeated node so the reported cycle is
			// the loop itself, not the path that led into it.
			for i, n := range stack {
				if n == id {
					cycle = append(append([]int{}, stack[i:]...), id)
					break
				}
			}
			return true
		case done:
			return false
		}
		mark[id] = active
		stack = append(stack, id)
		for _, dep := range deps[id] {
			// Self-edges are reported explicitly as "depends on itself";
			// reporting them again as a cycle is noise at the gate.
			if dep == id {
				continue
			}
			if visit(dep) {
				return true
			}
		}
		stack = stack[:len(stack)-1]
		mark[id] = done
		return false
	}

	ids := make([]int, 0, len(deps))
	for id := range deps {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		if visit(id) {
			return cycle
		}
	}
	return nil
}

func formatCycle(cycle []int) string {
	parts := make([]string, len(cycle))
	for i, id := range cycle {
		parts[i] = fmt.Sprintf("%d", id)
	}
	return strings.Join(parts, " → ")
}

// Order returns unit IDs in dependency order, so issues are created after the
// units they depend on and the plan reads top to bottom.
func (p *Plan) Order() []int {
	deps := make(map[int][]int, len(p.Units))
	ids := make([]int, 0, len(p.Units))
	for _, u := range p.Units {
		deps[u.ID] = u.DependsOn
		ids = append(ids, u.ID)
	}
	sort.Ints(ids)

	placed := map[int]bool{}
	var out []int

	var place func(int)
	place = func(id int) {
		if placed[id] {
			return
		}
		placed[id] = true // set first: a cycle must not recurse forever
		for _, dep := range deps[id] {
			place(dep)
		}
		out = append(out, id)
	}
	for _, id := range ids {
		place(id)
	}
	return out
}

// Unit returns the unit with the given plan-local ID.
func (p *Plan) Unit(id int) (Unit, bool) {
	for _, u := range p.Units {
		if u.ID == id {
			return u, true
		}
	}
	return Unit{}, false
}
