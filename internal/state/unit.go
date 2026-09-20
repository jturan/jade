// Package state reads and writes jade's orchestration state, which lives in
// GitHub rather than in an agent's context. That is what keeps the orchestrator
// thin: it can be restarted, compacted, or moved to another machine without
// losing its place.
package state

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/jturan/jade/internal/config"
	"gopkg.in/yaml.v3"
)

// Status is a unit's position in the build loop, carried as a GitHub label.
type Status string

const (
	StatusReady      Status = "agent:ready"
	StatusInProgress Status = "agent:in-progress"
	StatusReview     Status = "agent:review"
	StatusBlocked    Status = "agent:blocked"
	StatusDone       Status = "agent:done"
)

// Statuses returns every status label, in pipeline order.
func Statuses() []Status {
	return []Status{StatusReady, StatusInProgress, StatusReview, StatusBlocked, StatusDone}
}

// Autonomy is how far a unit may proceed without a human.
type Autonomy string

const (
	// AutonomyGated is the default: a human approves the plan and the merge.
	AutonomyGated Autonomy = "gated"
	// AutonomyMerge auto-merges on green review; the plan gate still applies.
	AutonomyMerge Autonomy = "merge"
	// AutonomyFull also auto-applies the plan.
	AutonomyFull Autonomy = "full"
)

// DefaultRetryLimit bounds how many times a builder is re-prompted with its own
// failure before the unit escalates to a human.
const DefaultRetryLimit = 2

// Unit is the machine-readable half of a unit-of-work issue: the settings the
// build loop needs, carried in a fenced YAML block inside the issue body so
// that the prose and the configuration stay in one place.
type Unit struct {
	// Number is the unit's ordinal within its initiative, for humans.
	Number int `yaml:"unit"`
	// DependsOn lists GitHub issue numbers that must be closed first.
	//
	// These are issue numbers, not unit ordinals: ordinals are only unique
	// within one plan, and the build loop sequences across a whole repo.
	DependsOn []int `yaml:"depends_on"`
	// Autonomy is how far this unit may proceed unattended. Empty means the
	// default, which is gated.
	Autonomy Autonomy `yaml:"autonomy,omitempty"`

	Builder    config.Agent `yaml:"builder"`
	CodeReview config.Agent `yaml:"code_review"`
	// SecurityReview is decided by a human at plan time and recorded here, so
	// it is never re-argued when the PR is up.
	SecurityReview bool `yaml:"security_review"`
	RetryLimit     int  `yaml:"retry_limit"`
}

// blockRE matches a fenced ```yaml jade block.
//
// An issue body may contain more than one: prose that documents the schema is
// itself written in a jade block, and an example must never be mistaken for the
// unit's real configuration. The **last** block wins, matching where RenderUnit
// writes, so the operative block is always the one at the end.
var blockRE = regexp.MustCompile("(?ms)^```yaml jade[ \t]*\r?\n(.*?)^```[ \t]*$")

// lastBlock returns the final jade block in a body: its submatch and its bounds.
func lastBlock(body string) (content string, loc []int) {
	matches := blockRE.FindAllStringSubmatchIndex(body, -1)
	if len(matches) == 0 {
		return "", nil
	}
	m := matches[len(matches)-1]
	return body[m[2]:m[3]], []int{m[0], m[1]}
}

// ErrNoBlock reports an issue body with no jade configuration block. Callers
// fall back to role defaults rather than treating this as a failure.
type ErrNoBlock struct{}

func (ErrNoBlock) Error() string { return "issue body has no ```yaml jade block" }

// ParseUnit extracts a unit's settings from an issue body.
func ParseUnit(body string) (Unit, error) {
	content, loc := lastBlock(body)
	if loc == nil {
		return Unit{}, ErrNoBlock{}
	}

	var u Unit
	if err := yaml.Unmarshal([]byte(content), &u); err != nil {
		return Unit{}, fmt.Errorf("parsing jade block: %w", err)
	}
	if u.RetryLimit == 0 {
		u.RetryLimit = DefaultRetryLimit
	}
	if u.Autonomy == "" {
		u.Autonomy = AutonomyGated
	}
	return u, nil
}

// RenderUnit writes a unit's settings back into an issue body, replacing an
// existing block or appending one. Prose outside the block is preserved
// byte for byte.
func RenderUnit(body string, u Unit) (string, error) {
	out, err := yaml.Marshal(u)
	if err != nil {
		return "", err
	}
	block := "```yaml jade\n" + string(out) + "```"

	if _, loc := lastBlock(body); loc != nil {
		return body[:loc[0]] + block + body[loc[1]:], nil
	}
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + "\n" + block + "\n", nil
}

// Overrides returns the unit's per-role agent settings, for layering onto a
// resolved config. Only roles the unit actually pins are included.
func (u Unit) Overrides() map[config.Role]config.Agent {
	out := map[config.Role]config.Agent{}
	if u.Builder != (config.Agent{}) {
		out[config.RoleBuilder] = u.Builder
	}
	if u.CodeReview != (config.Agent{}) {
		out[config.RoleCodeReview] = u.CodeReview
	}
	return out
}
