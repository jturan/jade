package build

import (
	"fmt"
	"path"
	"strings"

	"github.com/jturan/jade/internal/state"
)

// DefaultMaxAutoMerges caps how many units one run may merge unattended, so a
// bad plan cannot land nine pull requests while nobody is watching.
const DefaultMaxAutoMerges = 3

// DefaultProtectedPaths force a human gate regardless of autonomy level. These
// are the places where a wrong change is expensive or hard to reverse.
func DefaultProtectedPaths() []string {
	return []string{
		"**/migrations/**",
		"**/migrate/**",
		".github/**",
		"**/auth/**",
		"**/*secret*",
		"**/*credential*",
		".env*",
		"**/Dockerfile*",
		"**/docker-compose*",
		"**/deploy/**",
		"**/k8s/**",
		"**/terraform/**",
	}
}

// AutonomyDecision is whether a finished unit may merge itself, and why.
type AutonomyDecision struct {
	Merge bool
	// Reason always explains the outcome, including when the answer is yes.
	// A silent auto-merge is indistinguishable from a bug.
	Reason string
}

// AutonomyInput is everything the decision depends on. Keeping it a plain
// struct makes the rule set testable without a repo, a plan, or an agent.
type AutonomyInput struct {
	Level state.Autonomy
	// Strict is the profile's review_strictness. Under strict nothing merges
	// itself here, whatever the unit's autonomy says.
	Strict bool
	// SecurityReview is the human's plan-time decision for this unit.
	SecurityReview bool
	// Attempts is how many builder attempts the unit took. More than one
	// means it struggled.
	Attempts int
	// TestsRun records whether the builder actually ran the suite.
	TestsRun bool
	// ReviewsBlocking is how many reviewers objected.
	ReviewsBlocking int
	// ChangedFiles are the paths this unit touched, repo-relative.
	ChangedFiles []string
	// ProtectedPaths are globs that force a gate. Empty means the defaults.
	ProtectedPaths []string
	// AutoMergesSoFar is how many units this run has already merged.
	AutoMergesSoFar int
	MaxAutoMerges   int
}

// DecideAutonomy applies the guardrails. Every rule here exists because the
// alternative is a machine merging something nobody checked.
func DecideAutonomy(in AutonomyInput) AutonomyDecision {
	switch in.Level {
	case state.AutonomyMerge, state.AutonomyFull:
	default:
		return AutonomyDecision{Reason: "autonomy is gated — merging is yours"}
	}

	// Strictness is a property of where the code lives, not of the unit. An
	// employer's review culture outranks a plan-time dial, so this is checked
	// before anything the unit gets a say in.
	if in.Strict {
		return AutonomyDecision{Reason: "review_strictness is strict — merging is yours"}
	}

	// A human decided at the plan gate that this unit warranted a security
	// review. That decision is about risk, not about process, so it also
	// means the merge is worth a human's eyes.
	if in.SecurityReview {
		return AutonomyDecision{Reason: "unit was flagged for security review"}
	}

	if in.ReviewsBlocking > 0 {
		return AutonomyDecision{Reason: fmt.Sprintf("%d reviewer(s) raised blocking findings", in.ReviewsBlocking)}
	}

	if !in.TestsRun {
		return AutonomyDecision{Reason: "the builder did not run the tests"}
	}

	// A unit that needed a retry has earned human eyes, whatever was decided
	// about it before anyone knew it would struggle.
	if in.Attempts > 1 {
		return AutonomyDecision{
			Reason: fmt.Sprintf("took %d attempts — a unit that struggled gets read", in.Attempts),
		}
	}

	protected := in.ProtectedPaths
	if len(protected) == 0 {
		protected = DefaultProtectedPaths()
	}
	if hit, pattern := matchProtected(in.ChangedFiles, protected); hit != "" {
		return AutonomyDecision{Reason: fmt.Sprintf("touches a protected path: %s (%s)", hit, pattern)}
	}

	max := in.MaxAutoMerges
	if max == 0 {
		max = DefaultMaxAutoMerges
	}
	if in.AutoMergesSoFar >= max {
		return AutonomyDecision{
			Reason: fmt.Sprintf("this run has already merged %d units (limit %d)", in.AutoMergesSoFar, max),
		}
	}

	return AutonomyDecision{Merge: true, Reason: fmt.Sprintf("autonomy %s and every guardrail passed", in.Level)}
}

// matchProtected returns the first changed file matching a protected glob, with
// the pattern that caught it.
func matchProtected(files, patterns []string) (string, string) {
	for _, f := range files {
		clean := strings.TrimPrefix(path.Clean(f), "./")
		for _, p := range patterns {
			if globMatch(p, clean) {
				return f, p
			}
		}
	}
	return "", ""
}

// globMatch supports the "**" segment wildcard that path.Match lacks, since
// protected paths are most naturally written as **/migrations/**.
func globMatch(pattern, name string) bool {
	if !strings.Contains(pattern, "**") {
		if ok, _ := path.Match(pattern, name); ok {
			return true
		}
		// A pattern with no wildcard at all should also match a bare
		// filename anywhere, so ".env*" catches "config/.env.local".
		if !strings.ContainsAny(pattern, "*?[") {
			return name == pattern || strings.HasSuffix(name, "/"+pattern)
		}
		ok, _ := path.Match(pattern, path.Base(name))
		return ok
	}

	// Split on "**" and require the literal parts to appear in order.
	parts := strings.Split(pattern, "**")
	rest := name
	for i, part := range parts {
		part = strings.Trim(part, "/")
		if part == "" {
			continue
		}
		idx := indexSegment(rest, part)
		if idx < 0 {
			return false
		}
		if i == 0 && !strings.HasPrefix(pattern, "**") && idx != 0 {
			return false
		}
		rest = rest[idx+len(part):]
	}
	return true
}

// indexSegment finds a pattern part within a path, matching whole segments or a
// glob against one segment.
func indexSegment(name, part string) int {
	if strings.ContainsAny(part, "*?[") {
		offset := 0
		for _, seg := range strings.Split(name, "/") {
			if ok, _ := path.Match(part, seg); ok {
				return offset
			}
			offset += len(seg) + 1
		}
		return -1
	}
	return strings.Index(name, part)
}
