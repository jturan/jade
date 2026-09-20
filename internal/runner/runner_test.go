package runner

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// stub replaces the herdr invocation, recording the arguments it was given and
// returning a canned response.
func stub(t *testing.T, stdout string, stderr string, err error) (*Runner, *[][]string) {
	t.Helper()
	var calls [][]string
	r := &Runner{bin: "herdr"}
	r.run = func(_ context.Context, args ...string) ([]byte, []byte, error) {
		calls = append(calls, args)
		return []byte(stdout), []byte(stderr), err
	}
	return r, &calls
}

func TestStateSettled(t *testing.T) {
	settled := []State{StateIdle, StateDone, StateBlocked}
	for _, s := range settled {
		if !s.Settled() {
			t.Errorf("%q should be settled", s)
		}
	}
	// herdr documents "unknown" as an agent it cannot classify confidently,
	// which is not proof of completion. If this ever flips to settled, the
	// build loop will advance past agents that are still working.
	for _, s := range []State{StateWorking, StateUnknown} {
		if s.Settled() {
			t.Errorf("%q should not be settled", s)
		}
	}
}

func TestSplitParsesPaneID(t *testing.T) {
	r, calls := stub(t, `{"id":"cli:pane:split","result":{"pane":{"pane_id":"w4:p9","tab_id":"w4:t2","workspace_id":"w4","cwd":"/repo","agent_status":"unknown"},"type":"pane_info"}}`, "", nil)

	pane, err := r.Split(context.Background(), SplitOpts{Direction: Down, Cwd: "/repo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pane.PaneID != "w4:p9" {
		t.Errorf("pane_id = %q, want w4:p9", pane.PaneID)
	}

	args := strings.Join((*calls)[0], " ")
	for _, want := range []string{"pane split", "--current", "--direction down", "--cwd /repo", "--no-focus"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q missing %q", args, want)
		}
	}
}

// Background work must never steal the operator's focus.
func TestSplitAlwaysPassesNoFocus(t *testing.T) {
	for _, opts := range []SplitOpts{{}, {FromPane: "w4:p1"}, {Direction: Right, Cwd: "/x"}} {
		r, calls := stub(t, `{"result":{"pane":{"pane_id":"w1:p1"}}}`, "", nil)
		if _, err := r.Split(context.Background(), opts); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !contains((*calls)[0], "--no-focus") {
			t.Errorf("opts %+v produced args without --no-focus: %v", opts, (*calls)[0])
		}
	}
}

func TestEnsureTabReusesExistingTab(t *testing.T) {
	r, calls := stub(t, `{"result":{"tabs":[{"tab_id":"w4:t1","label":"dev"},{"tab_id":"w4:t2","label":"agents"}]}}`, "", nil)

	ensured, err := r.EnsureTab(context.Background(), "w4", TabAgents)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ensured.Tab.TabID != "w4:t2" {
		t.Errorf("tab_id = %q, want w4:t2", ensured.Tab.TabID)
	}
	if ensured.Created {
		t.Error("an existing tab must not be reported as created")
	}
	if len(*calls) != 1 {
		t.Errorf("expected only a list call, got %d calls: %v", len(*calls), *calls)
	}
}

func TestStartAgentArgs(t *testing.T) {
	r, calls := stub(t, `{"result":{"type":"ok"}}`, "", nil)

	err := r.StartAgent(context.Background(), StartOpts{
		Name:    "builder",
		Kind:    "claude",
		PaneID:  "w4:p9",
		Timeout: 45 * time.Second,
		Args:    []string{"--model", "sonnet"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	args := (*calls)[0]
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "agent start builder --kind claude --pane w4:p9") {
		t.Errorf("unexpected args: %q", joined)
	}
	// Timeouts are milliseconds in herdr's CLI.
	if !strings.Contains(joined, "--timeout 45000") {
		t.Errorf("args %q should carry a millisecond timeout", joined)
	}
	// Native agent arguments must come after a bare "--".
	sep := indexOf(args, "--")
	if sep < 0 || indexOf(args, "--model") != sep+1 {
		t.Errorf("agent args must follow a bare separator: %v", args)
	}
}

// herdr's agent JSON uses "agent" for the kind and "agent_status" for the
// lifecycle state. Decoding into differently-named fields yields an empty
// status that would read as "unknown" and stall the build loop.
func TestGetAgentDecodesHerdrFieldNames(t *testing.T) {
	r, _ := stub(t, `{"id":"cli:agent:get","result":{"agent":{"agent":"claude","agent_status":"done","name":"jade-smoke","pane_id":"w4:p5","tab_id":"w4:t3","interactive_ready":true},"type":"agent_info"}}`, "", nil)

	agent, err := r.GetAgent(context.Background(), "jade-smoke")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if agent.Status != StateDone {
		t.Errorf("status = %q, want done", agent.Status)
	}
	if agent.Kind != "claude" {
		t.Errorf("kind = %q, want claude", agent.Kind)
	}
	if !agent.InteractiveReady {
		t.Error("interactive_ready should have decoded as true")
	}
}

// An absent status must be an error rather than a silent "unknown", which the
// build loop would misread as an agent it cannot classify.
func TestAgentStateRejectsEmptyStatus(t *testing.T) {
	r, _ := stub(t, `{"result":{"agent":{"name":"x"}}}`, "", nil)

	if _, err := r.AgentState(context.Background(), "x"); err == nil {
		t.Fatal("expected an error when herdr reports no status")
	}
}

func TestParseServerError(t *testing.T) {
	r, _ := stub(t, `{"error":{"code":"agent_not_found","message":"agent target nope not found"},"id":"cli:agent:get"}`,
		"", &exec.ExitError{})

	_, err := r.AgentState(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !HasCode(err, CodeAgentNotFound) {
		t.Errorf("HasCode(%v, %q) = false", err, CodeAgentNotFound)
	}

	var herr *Error
	if !errors.As(err, &herr) {
		t.Fatalf("error is not a *runner.Error: %T", err)
	}
	if !strings.Contains(herr.Message, "not found") {
		t.Errorf("message = %q, want it to mention the failure", herr.Message)
	}
}

// A blocked agent must be distinguishable, since the build loop escalates to a
// human rather than guessing an answer to an approval dialog.
func TestBlockedAgentIsIdentifiable(t *testing.T) {
	r, _ := stub(t, "", `{"error":{"code":"agent_blocked","message":"agent is waiting at a prompt"}}`, &exec.ExitError{})

	err := r.Prompt(context.Background(), "builder", "go", true, time.Minute)
	if !HasCode(err, CodeAgentBlocked) {
		t.Errorf("expected agent_blocked, got %v", err)
	}
}

// Syntax errors exit 2 with plain usage text rather than JSON, and must still
// produce a legible error instead of a decode failure.
func TestParseNonJSONError(t *testing.T) {
	r, _ := stub(t, "", "usage: herdr agent start <name> --kind KIND --pane ID", &exec.ExitError{})

	err := r.StartAgent(context.Background(), StartOpts{Name: "x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "usage: herdr agent start") {
		t.Errorf("error = %q, want it to surface the usage text", err)
	}
	if HasCode(err, CodeAgentBlocked) {
		t.Error("a usage error should not be reported as a herdr error code")
	}
}

// agent read is the one command that emits raw terminal text instead of JSON.
func TestReadAgentReturnsRawText(t *testing.T) {
	r, calls := stub(t, "line one\nline two\n", "", nil)

	got, err := r.ReadAgent(context.Background(), "builder", 120, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "line one\nline two\n" {
		t.Errorf("content = %q, want the raw terminal text", got)
	}
	if !contains((*calls)[0], string(SourceRecentUnwrapped)) {
		t.Errorf("args %v should default to %s", (*calls)[0], SourceRecentUnwrapped)
	}
}

func contains(haystack []string, needle string) bool {
	return indexOf(haystack, needle) >= 0
}

func indexOf(haystack []string, needle string) int {
	for i, s := range haystack {
		if s == needle {
			return i
		}
	}
	return -1
}
