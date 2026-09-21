package cli

import (
	"context"
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
