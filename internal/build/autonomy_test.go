package build

import (
	"strings"
	"testing"

	"github.com/jturan/jade/internal/state"
)

// permitted is an input where every guardrail passes, so each test can change
// exactly one thing and assert that alone flips the decision.
func permitted() AutonomyInput {
	return AutonomyInput{
		Level:        state.AutonomyMerge,
		Attempts:     1,
		TestsRun:     true,
		ChangedFiles: []string{"internal/build/loop.go", "README.md"},
	}
}

func TestDecideAutonomyAllowsCleanRun(t *testing.T) {
	d := DecideAutonomy(permitted())
	if !d.Merge {
		t.Fatalf("expected merge, got: %s", d.Reason)
	}
	// A silent auto-merge is indistinguishable from a bug.
	if d.Reason == "" {
		t.Error("an allowed merge must still explain itself")
	}
}

func TestDecideAutonomyGuardrails(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AutonomyInput)
		want   string
	}{
		{
			"gated is the default answer",
			func(in *AutonomyInput) { in.Level = state.AutonomyGated },
			"gated",
		},
		{
			"an unset level is gated, never permissive",
			func(in *AutonomyInput) { in.Level = "" },
			"gated",
		},
		{
			"a security-flagged unit is never auto-merged",
			func(in *AutonomyInput) { in.SecurityReview = true },
			"security review",
		},
		{
			"blocking review findings stop the merge",
			func(in *AutonomyInput) { in.ReviewsBlocking = 1 },
			"blocking findings",
		},
		{
			"no tests means no merge",
			func(in *AutonomyInput) { in.TestsRun = false },
			"did not run the tests",
		},
		{
			"a unit that needed a retry gets read",
			func(in *AutonomyInput) { in.Attempts = 2 },
			"took 2 attempts",
		},
		{
			"migrations are protected",
			func(in *AutonomyInput) { in.ChangedFiles = []string{"db/migrations/001_add.sql"} },
			"protected path",
		},
		{
			"CI config is protected",
			func(in *AutonomyInput) { in.ChangedFiles = []string{".github/workflows/ci.yml"} },
			"protected path",
		},
		{
			"the blast-radius cap halts the run",
			func(in *AutonomyInput) { in.AutoMergesSoFar = 3 },
			"already merged",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := permitted()
			tt.mutate(&in)

			d := DecideAutonomy(in)
			if d.Merge {
				t.Fatalf("expected a gate, but the unit would have merged: %s", d.Reason)
			}
			if !strings.Contains(d.Reason, tt.want) {
				t.Errorf("reason = %q, want it to mention %q", d.Reason, tt.want)
			}
		})
	}
}

// `full` differs from `merge` only in auto-applying the plan; it must not
// weaken any of the merge guardrails.
func TestFullAutonomyStillRespectsGuardrails(t *testing.T) {
	in := permitted()
	in.Level = state.AutonomyFull
	in.SecurityReview = true

	if d := DecideAutonomy(in); d.Merge {
		t.Errorf("full autonomy bypassed the security guardrail: %s", d.Reason)
	}
}

func TestCustomProtectedPathsReplaceDefaults(t *testing.T) {
	in := permitted()
	in.ProtectedPaths = []string{"**/pricing/**"}
	in.ChangedFiles = []string{"db/migrations/001_add.sql"}

	// The defaults no longer apply, so a migration is fine here.
	if d := DecideAutonomy(in); !d.Merge {
		t.Errorf("custom patterns should replace the defaults, not add to them: %s", d.Reason)
	}

	in.ChangedFiles = []string{"app/pricing/rates.go"}
	if d := DecideAutonomy(in); d.Merge {
		t.Error("the custom protected path did not match")
	}
}

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern, name string
		want          bool
	}{
		{"**/migrations/**", "db/migrations/001.sql", true},
		{"**/migrations/**", "migrations/001.sql", true},
		{"**/migrations/**", "db/models/user.go", false},
		{".github/**", ".github/workflows/ci.yml", true},
		{".github/**", "docs/.github/x", false},
		{"**/*secret*", "config/secrets.yml", true},
		{"**/*secret*", "internal/build/loop.go", false},
		{".env*", ".env", true},
		{".env*", "config/.env.local", true},
		{".env*", "internal/env.go", false},
		{"**/terraform/**", "infra/terraform/main.tf", true},
	}

	for _, tt := range tests {
		if got := globMatch(tt.pattern, tt.name); got != tt.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
		}
	}
}
