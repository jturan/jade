package build

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jturan/jade/internal/config"
	"github.com/jturan/jade/internal/state"
)

// fakeAgent writes a scripted report each time it is dispatched, standing in
// for a real agent in a pane.
type fakeAgent struct {
	root string
	// scripts maps a role to the sequence of reports it will produce.
	scripts  map[config.Role][]Report
	calls    []Dispatch
	notified []string
}

func (f *fakeAgent) Dispatch(_ context.Context, d Dispatch) error {
	f.calls = append(f.calls, d)

	queue := f.scripts[d.Role]
	if len(queue) == 0 {
		return nil // writes nothing: simulates an agent that never reported
	}
	report := queue[0]
	f.scripts[d.Role] = queue[1:]

	role := string(d.Role)
	if d.Role == config.RoleBuilder {
		role = "builder"
	}
	path := ReportPath(f.root, role)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (f *fakeAgent) Notify(_ context.Context, title, _ string) error {
	f.notified = append(f.notified, title)
	return nil
}

func (f *fakeAgent) dispatchedRoles() []config.Role {
	var out []config.Role
	for _, c := range f.calls {
		out = append(out, c.Role)
	}
	return out
}

type fakeGit struct {
	clean        bool
	changes      bool
	changedFiles []string
	merged       []string
	committed    []string
	pushed       []string
	prCreated    bool
	prTitle      string
	prBody       string
	checkedOut   string
}

func (g *fakeGit) IsClean(context.Context) (bool, error)         { return g.clean, nil }
func (g *fakeGit) HasChanges(context.Context) (bool, error)      { return g.changes, nil }
func (g *fakeGit) DefaultBranch(context.Context) (string, error) { return "main", nil }
func (g *fakeGit) Checkout(_ context.Context, b string) error    { g.checkedOut = b; return nil }
func (g *fakeGit) CommitAll(_ context.Context, m string) error {
	g.committed = append(g.committed, m)
	return nil
}
func (g *fakeGit) Push(_ context.Context, b string) error { g.pushed = append(g.pushed, b); return nil }
func (g *fakeGit) ChangedFiles(context.Context, string) ([]string, error) {
	if g.changedFiles == nil {
		return []string{"internal/thing.go"}, nil
	}
	return g.changedFiles, nil
}
func (g *fakeGit) MergePR(_ context.Context, url string) error {
	g.merged = append(g.merged, url)
	return nil
}
func (g *fakeGit) CreatePR(_ context.Context, t, b, _ string) (string, error) {
	g.prCreated, g.prTitle, g.prBody = true, t, b
	return "https://github.com/o/r/pull/42", nil
}

type fakeStore struct {
	statuses []state.Status
	comments []string
}

func (s *fakeStore) SetStatus(_ context.Context, _ int, st state.Status) error {
	s.statuses = append(s.statuses, st)
	return nil
}

func (s *fakeStore) Comment(_ context.Context, _ int, body string) error {
	s.comments = append(s.comments, body)
	return nil
}

type fakePrompts struct{}

func (fakePrompts) Build(_ state.Unit, _ state.Issue, path string, prev *Report) (string, error) {
	if prev != nil {
		return "retry, previous failure: " + prev.Summary + " report to " + path, nil
	}
	return "build it, report to " + path, nil
}

func (fakePrompts) Review(role config.Role, _ state.Issue, path, _ string) (string, error) {
	return string(role) + " the diff, report to " + path, nil
}

func newDeps(t *testing.T, agent *fakeAgent, git *fakeGit, store *fakeStore) Deps {
	t.Helper()
	agents, origins := config.MergeAgents(config.DefaultAgents(), nil, config.LayerProfile)
	return Deps{
		Agent:   agent,
		Git:     git,
		Store:   store,
		Prompts: fakePrompts{},
		Config:  &config.Resolved{Agents: agents, Origins: origins},
		Root:    agent.root,
	}
}

func testIssue() state.Issue {
	return state.Issue{Number: 42, Title: "Add the thing", State: "OPEN"}
}

func testUnit() state.Unit {
	return state.Unit{Number: 1, RetryLimit: 2, Autonomy: state.AutonomyGated}
}

func ok(summary string) Report {
	return Report{Verdict: VerdictOK, Summary: summary, TestsRun: true}
}

func TestRunUnitHappyPath(t *testing.T) {
	root := t.TempDir()
	agent := &fakeAgent{root: root, scripts: map[config.Role][]Report{
		config.RoleBuilder:    {ok("built it")},
		config.RoleCodeReview: {ok("looks fine")},
	}}
	git := &fakeGit{clean: true, changes: true}
	store := &fakeStore{}

	res, err := RunUnit(context.Background(), newDeps(t, agent, git, store), testIssue(), testUnit())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomePR {
		t.Fatalf("outcome = %q, want pr", res.Outcome)
	}
	if res.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", res.Attempts)
	}
	if !git.prCreated {
		t.Error("no pull request was created")
	}
	if git.checkedOut != "unit/42-add-the-thing" {
		t.Errorf("branch = %q", git.checkedOut)
	}
	// jade must never merge: that is the human's gate.
	if last := store.statuses[len(store.statuses)-1]; last != state.StatusReview {
		t.Errorf("final status = %q, want agent:review", last)
	}
	if !strings.Contains(git.prBody, "Closes #42") {
		t.Error("PR body should close the issue")
	}
}

// Exactly retry_limit retries, then escalate — not one more, not one fewer.
func TestRunUnitEscalatesAfterExactRetryLimit(t *testing.T) {
	root := t.TempDir()
	fail := Report{Verdict: VerdictBlocked, Summary: "tests fail"}
	agent := &fakeAgent{root: root, scripts: map[config.Role][]Report{
		config.RoleBuilder: {fail, fail, fail, ok("too late")},
	}}
	git := &fakeGit{clean: true, changes: true}
	store := &fakeStore{}

	res, err := RunUnit(context.Background(), newDeps(t, agent, git, store), testIssue(), testUnit())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeBlocked {
		t.Fatalf("outcome = %q, want blocked", res.Outcome)
	}
	if res.Attempts != 3 {
		t.Errorf("attempts = %d, want 3 (initial + 2 retries)", res.Attempts)
	}
	if got := len(agent.calls); got != 3 {
		t.Errorf("dispatched %d times, want 3", got)
	}
	if last := store.statuses[len(store.statuses)-1]; last != state.StatusBlocked {
		t.Errorf("final status = %q, want agent:blocked", last)
	}
	if len(store.comments) != 1 || !strings.Contains(store.comments[0], "tests fail") {
		t.Errorf("the failure must be legible from the issue alone: %v", store.comments)
	}
	if len(agent.notified) != 1 {
		t.Errorf("expected exactly one notification, got %v", agent.notified)
	}
	if git.prCreated {
		t.Error("a blocked unit must not open a pull request")
	}
}

// A builder claiming success without running tests has demonstrated nothing.
func TestRunUnitRejectsSuccessWithoutTests(t *testing.T) {
	root := t.TempDir()
	noTests := Report{Verdict: VerdictOK, Summary: "done", TestsRun: false}
	agent := &fakeAgent{root: root, scripts: map[config.Role][]Report{
		config.RoleBuilder:    {noTests, ok("ran them this time")},
		config.RoleCodeReview: {ok("fine")},
	}}
	git := &fakeGit{clean: true, changes: true}

	res, err := RunUnit(context.Background(), newDeps(t, agent, git, &fakeStore{}), testIssue(), testUnit())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Attempts != 2 {
		t.Errorf("attempts = %d — an untested success should cost an attempt", res.Attempts)
	}
	if !strings.Contains(agent.calls[1].Prompt, "without running the tests") {
		t.Errorf("the retry prompt should say why: %q", agent.calls[1].Prompt)
	}
}

// A builder that reports success while changing nothing has not done the work.
func TestRunUnitRejectsEmptyDiff(t *testing.T) {
	root := t.TempDir()
	agent := &fakeAgent{root: root, scripts: map[config.Role][]Report{
		config.RoleBuilder: {ok("done"), ok("done"), ok("done")},
	}}
	git := &fakeGit{clean: true, changes: false}
	store := &fakeStore{}

	res, err := RunUnit(context.Background(), newDeps(t, agent, git, store), testIssue(), testUnit())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeBlocked {
		t.Errorf("outcome = %q, want blocked", res.Outcome)
	}
	if len(git.committed) != 0 {
		t.Error("nothing should be committed when the tree is unchanged")
	}
}

// Blocking review findings go back to the builder, and cost a retry.
func TestRunUnitFeedsReviewFindingsBack(t *testing.T) {
	root := t.TempDir()
	agent := &fakeAgent{root: root, scripts: map[config.Role][]Report{
		config.RoleBuilder: {ok("first pass"), ok("fixed it")},
		config.RoleCodeReview: {
			{Verdict: VerdictBlocked, Summary: "unchecked error", Findings: []string{"err is ignored in loop.go:42"}},
			ok("good now"),
		},
	}}
	git := &fakeGit{clean: true, changes: true}

	res, err := RunUnit(context.Background(), newDeps(t, agent, git, &fakeStore{}), testIssue(), testUnit())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomePR {
		t.Fatalf("outcome = %q, want pr", res.Outcome)
	}
	if res.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", res.Attempts)
	}
	retry := agent.calls[2].Prompt // build, review, build
	if !strings.Contains(retry, "unchecked error") {
		t.Errorf("the builder should see the reviewer's objection: %q", retry)
	}
}

// Security review runs only when the unit calls for it — that decision was made
// by a human at the plan gate and must be honoured exactly.
func TestSecurityReviewRunsOnlyWhenFlagged(t *testing.T) {
	for _, flagged := range []bool{false, true} {
		root := t.TempDir()
		agent := &fakeAgent{root: root, scripts: map[config.Role][]Report{
			config.RoleBuilder:    {ok("built")},
			config.RoleCodeReview: {ok("fine")},
			config.RoleSecReview:  {ok("no issues")},
		}}
		unit := testUnit()
		unit.SecurityReview = flagged

		if _, err := RunUnit(context.Background(), newDeps(t, agent, &fakeGit{clean: true, changes: true}, &fakeStore{}), testIssue(), unit); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		ran := false
		for _, role := range agent.dispatchedRoles() {
			if role == config.RoleSecReview {
				ran = true
			}
		}
		if ran != flagged {
			t.Errorf("security_review=%v but security reviewer ran=%v", flagged, ran)
		}
	}
}

// A gated unit must never merge itself, which is the default for every unit.
func TestGatedUnitStopsAtThePR(t *testing.T) {
	root := t.TempDir()
	agent := &fakeAgent{root: root, scripts: map[config.Role][]Report{
		config.RoleBuilder:    {ok("built")},
		config.RoleCodeReview: {ok("fine")},
	}}
	git := &fakeGit{clean: true, changes: true}

	res, err := RunUnit(context.Background(), newDeps(t, agent, git, &fakeStore{}), testIssue(), testUnit())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomePR {
		t.Errorf("outcome = %q, want pr", res.Outcome)
	}
	if len(git.merged) != 0 {
		t.Error("a gated unit merged itself")
	}
}

// With autonomy and a clean run, the unit merges and leaves an audit trail.
func TestAutonomousUnitMergesAndRecordsWhy(t *testing.T) {
	root := t.TempDir()
	agent := &fakeAgent{root: root, scripts: map[config.Role][]Report{
		config.RoleBuilder:    {ok("built")},
		config.RoleCodeReview: {ok("fine")},
	}}
	git := &fakeGit{clean: true, changes: true}
	store := &fakeStore{}

	unit := testUnit()
	unit.Autonomy = state.AutonomyMerge

	res, err := RunUnit(context.Background(), newDeps(t, agent, git, store), testIssue(), unit)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomeMerged {
		t.Fatalf("outcome = %q, want merged: %s", res.Outcome, res.Autonomy.Reason)
	}
	if len(git.merged) != 1 {
		t.Fatalf("merged %d times, want 1", len(git.merged))
	}
	if last := store.statuses[len(store.statuses)-1]; last != state.StatusDone {
		t.Errorf("final status = %q, want agent:done", last)
	}
	if len(store.comments) != 1 || !strings.Contains(store.comments[0], "Merged automatically") {
		t.Errorf("an unattended merge must leave an audit trail: %v", store.comments)
	}
}

// A protected path gates the merge even when autonomy would allow it.
func TestAutonomousUnitStopsOnProtectedPath(t *testing.T) {
	root := t.TempDir()
	agent := &fakeAgent{root: root, scripts: map[config.Role][]Report{
		config.RoleBuilder:    {ok("built")},
		config.RoleCodeReview: {ok("fine")},
	}}
	git := &fakeGit{clean: true, changes: true, changedFiles: []string{"db/migrations/003_add.sql"}}

	unit := testUnit()
	unit.Autonomy = state.AutonomyMerge

	res, err := RunUnit(context.Background(), newDeps(t, agent, git, &fakeStore{}), testIssue(), unit)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Outcome != OutcomePR {
		t.Errorf("outcome = %q, want pr", res.Outcome)
	}
	if len(git.merged) != 0 {
		t.Error("a migration was merged unattended")
	}
	if !strings.Contains(res.Autonomy.Reason, "protected path") {
		t.Errorf("reason = %q", res.Autonomy.Reason)
	}
}

// A unit that needed a retry must not merge itself, even at full autonomy.
func TestRetriedUnitIsNotAutoMerged(t *testing.T) {
	root := t.TempDir()
	agent := &fakeAgent{root: root, scripts: map[config.Role][]Report{
		config.RoleBuilder:    {{Verdict: VerdictBlocked, Summary: "oops"}, ok("fixed")},
		config.RoleCodeReview: {ok("fine")},
	}}
	git := &fakeGit{clean: true, changes: true}

	unit := testUnit()
	unit.Autonomy = state.AutonomyFull

	res, err := RunUnit(context.Background(), newDeps(t, agent, git, &fakeStore{}), testIssue(), unit)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(git.merged) != 0 {
		t.Errorf("a unit that took %d attempts merged itself", res.Attempts)
	}
	if !strings.Contains(res.Autonomy.Reason, "attempts") {
		t.Errorf("reason = %q", res.Autonomy.Reason)
	}
}

// Unrelated work in progress must not be swept into a unit's commit.
func TestRunUnitRefusesDirtyTree(t *testing.T) {
	root := t.TempDir()
	agent := &fakeAgent{root: root, scripts: map[config.Role][]Report{}}
	git := &fakeGit{clean: false}

	_, err := RunUnit(context.Background(), newDeps(t, agent, git, &fakeStore{}), testIssue(), testUnit())
	if err == nil {
		t.Fatal("expected a refusal on a dirty working tree")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Errorf("error should explain itself: %v", err)
	}
	if len(agent.calls) != 0 {
		t.Error("nothing should be dispatched when the tree is dirty")
	}
}

// An agent that finishes without writing a report has demonstrated nothing;
// silence must not read as success.
func TestMissingReportIsAnError(t *testing.T) {
	root := t.TempDir()
	agent := &fakeAgent{root: root, scripts: map[config.Role][]Report{}} // writes nothing
	_, err := RunUnit(context.Background(), newDeps(t, agent, &fakeGit{clean: true, changes: true}, &fakeStore{}), testIssue(), testUnit())
	if err == nil {
		t.Fatal("expected an error when the agent wrote no report")
	}
	if !strings.Contains(err.Error(), "no report") {
		t.Errorf("error = %v", err)
	}
}

// A stale report from a previous attempt must never be read as the new one's.
func TestStaleReportIsCleared(t *testing.T) {
	root := t.TempDir()
	path := ReportPath(root, "builder")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"verdict":"ok","tests_run":true,"summary":"stale"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	agent := &fakeAgent{root: root, scripts: map[config.Role][]Report{}} // writes nothing
	_, err := RunUnit(context.Background(), newDeps(t, agent, &fakeGit{clean: true, changes: true}, &fakeStore{}), testIssue(), testUnit())
	if err == nil || !strings.Contains(err.Error(), "no report") {
		t.Fatalf("a stale report was read as this attempt's result: %v", err)
	}
}
