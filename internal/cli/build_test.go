package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jturan/jade/internal/build"
	"github.com/jturan/jade/internal/config"
	"github.com/jturan/jade/internal/runner"
)

// promptRunner stands in for herdr with the behaviour this fix exists for:
// `agent prompt --wait` returns while the agent is still working. The agent's
// report lands afterDelay later, if at all.
type promptRunner struct {
	reportPath string
	afterDelay time.Duration
	// missing makes herdr answer that it has no such agent, as it does once
	// the agent has exited or its pane has died. getErr is any other failure
	// to ask — a herdr that is briefly unreachable, say.
	missing bool
	getErr  error

	mu sync.Mutex
	// reportOnClose records whether the report was on disk when jade closed
	// the pane — closing it earlier kills a working agent.
	reportOnClose bool
	closed        bool
}

func (p *promptRunner) AgentPane(context.Context, string, string, string) (runner.Pane, error) {
	return runner.Pane{PaneID: "w1:p1", TabID: "w1:t1"}, nil
}

func (p *promptRunner) StartAgent(context.Context, runner.StartOpts) error { return nil }

func (p *promptRunner) GetAgent(_ context.Context, target string) (runner.Agent, error) {
	if p.getErr != nil {
		return runner.Agent{}, p.getErr
	}
	if p.missing {
		return runner.Agent{}, &runner.Error{
			Code:    runner.CodeAgentNotFound,
			Message: "agent target " + target + " not found",
		}
	}
	return runner.Agent{Name: target, Status: runner.StateWorking}, nil
}

func (p *promptRunner) Prompt(_ context.Context, _, _ string, _ bool, _ time.Duration) error {
	if p.afterDelay > 0 {
		go func() {
			time.Sleep(p.afterDelay)
			_ = os.WriteFile(p.reportPath, []byte(`{"verdict":"ok","tests_run":true}`), 0o644)
		}()
	}
	return nil // settles at once, mid-task
}

func (p *promptRunner) ClosePane(context.Context, string) error {
	_, err := os.Stat(p.reportPath)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reportOnClose, p.closed = err == nil, true
	return nil
}

func (p *promptRunner) Notify(context.Context, string, string) error { return nil }

func dispatch(path string, timeout time.Duration) build.Dispatch {
	return build.Dispatch{
		Name: "jade-builder", Role: config.RoleBuilder, Vendor: "claude",
		Prompt: "do the unit", ReportPath: path, Timeout: timeout,
	}
}

// Dispatch must not return — and must not close the pane — until the agent
// has written its report, however early the prompt settles. Otherwise the
// recorded duration stops mid-task and the working agent is killed.
func TestDispatchWaitsForTheReportAfterThePromptReturns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "builder.json")
	delay := 250 * time.Millisecond
	r := &promptRunner{reportPath: path, afterDelay: delay}
	h := &herdrAgent{r: r, allowed: []string{"claude"}}

	started := time.Now()
	if err := h.Dispatch(context.Background(), dispatch(path, time.Minute)); err != nil {
		t.Fatal(err)
	}
	took := time.Since(started)

	if took < delay {
		t.Errorf("Dispatch returned after %s, before the report at %s", took, delay)
	}
	if _, err := build.ReadReport(path); err != nil {
		t.Fatalf("Dispatch returned without a readable report: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		t.Error("the pane was left open")
	}
	if !r.reportOnClose {
		t.Error("the pane was closed before the agent had reported, which kills it")
	}
}

// An agent that stops without reporting is bounded by the role timeout.
func TestDispatchGivesUpAtTheRoleTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "builder.json")
	r := &promptRunner{reportPath: path}
	h := &herdrAgent{r: r, allowed: []string{"claude"}}

	err := h.Dispatch(context.Background(), dispatch(path, 50*time.Millisecond))
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("err = %v, want a timeout", err)
	}
}

// The gone check must answer "for good" only when herdr has no such agent.
// A settled state is not that answer: prompt --wait already returns on
// settled states that turn out to be pauses mid-task, so counting one would
// kill a working agent — and the fast failure it buys is what keeps a dead
// agent's role timeout out of the recorded duration.
func TestGoneOnlyCountsAnAgentHerdrNoLongerHas(t *testing.T) {
	r := &promptRunner{}
	h := &herdrAgent{r: r}

	if h.gone("jade-builder")(context.Background()) {
		t.Error("a working agent was reported as gone")
	}
	r.missing = true
	if !h.gone("jade-builder")(context.Background()) {
		t.Error("an agent herdr no longer has was not reported as gone")
	}

	// Failing to ask is not an answer. Counting it would close the pane on an
	// agent that is working perfectly well, over a moment's unreachability.
	r.missing, r.getErr = false, errors.New("connection refused")
	if h.gone("jade-builder")(context.Background()) {
		t.Error("a failed lookup was read as the agent being gone")
	}
}

// A role with no timeout lets an agent work as long as it likes. It does not
// let a pane nobody is watching hold the loop until the process is killed.
func TestAwaitDeadlineIsBoundedWithoutARoleTimeout(t *testing.T) {
	started := time.Now()

	if got := awaitDeadline(started, time.Hour); !got.Equal(started.Add(time.Hour)) {
		t.Errorf("deadline = %s, want the role timeout measured from the start of the run", got)
	}

	got := awaitDeadline(started, 0)
	if got.IsZero() {
		t.Fatal("a role with no timeout got no deadline, so AwaitReport would wait for ever")
	}
	if got.After(time.Now().Add(untimedAwaitCap + time.Minute)) {
		t.Errorf("deadline = %s, want it within %s", got, untimedAwaitCap)
	}
}
