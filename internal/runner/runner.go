// Package runner drives herdr: the multiplexer jade uses to create panes and
// run agents in them. Agents run in visible panes rather than in-process, which
// is what lets any vendor be dispatched and watched the same way.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// State is a herdr agent lifecycle state.
type State string

const (
	StateIdle    State = "idle"
	StateWorking State = "working"
	StateBlocked State = "blocked"
	StateDone    State = "done"
	StateUnknown State = "unknown"
)

// Settled reports whether a state means herdr has stopped waiting on the agent.
//
// StateUnknown is deliberately not settled: herdr documents it as "an agent is
// present but cannot be classified confidently", which is not proof of
// completion. Treating it as settled would let the build loop advance past an
// agent that is still working.
func (s State) Settled() bool {
	return s == StateIdle || s == StateDone || s == StateBlocked
}

// Herdr error codes jade reacts to specifically.
const (
	CodeAgentBlocked  = "agent_blocked"
	CodeAgentNotReady = "agent_not_ready"
	CodeStalled       = "agent_prompt_stalled"
	CodeAgentNotFound = "agent_not_found"
)

// Error is a structured failure from the herdr CLI. Server errors arrive as
// JSON on stderr with exit status 1; syntax errors exit 2 with plain usage text.
type Error struct {
	Code     string
	Message  string
	ExitCode int
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("herdr: %s", e.Message)
	}
	return fmt.Sprintf("herdr: %s (%s)", e.Message, e.Code)
}

// HasCode reports whether err is a herdr Error carrying the given code.
func HasCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// Context is the herdr location of the process calling jade, injected by herdr
// into every managed pane.
type Context struct {
	WorkspaceID string
	TabID       string
	PaneID      string
}

// Runner issues herdr commands.
type Runner struct {
	bin string
	// run is swappable so tests can exercise argument construction and
	// response parsing without a live herdr server.
	run func(ctx context.Context, args ...string) (stdout []byte, stderr []byte, err error)
}

// New returns a Runner backed by the herdr binary on PATH.
func New() *Runner {
	r := &Runner{bin: "herdr"}
	r.run = r.execute
	return r
}

func (r *Runner) execute(ctx context.Context, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, r.bin, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return []byte(stdout.String()), []byte(stderr.String()), err
}

// CallerContext reads the herdr location of the current process.
//
// jade is meant to be run from a pane inside herdr — the `orchestrator` tab.
// Outside herdr there is no session to attach panes to, so this fails loudly
// rather than silently targeting whatever pane another client happens to have
// focused.
func CallerContext() (Context, error) {
	if os.Getenv("HERDR_ENV") != "1" {
		return Context{}, fmt.Errorf("not running inside herdr — start a herdr session and run jade from a pane in it")
	}
	c := Context{
		WorkspaceID: os.Getenv("HERDR_WORKSPACE_ID"),
		TabID:       os.Getenv("HERDR_TAB_ID"),
		PaneID:      os.Getenv("HERDR_PANE_ID"),
	}
	if c.WorkspaceID == "" || c.TabID == "" || c.PaneID == "" {
		return Context{}, fmt.Errorf("herdr context is incomplete (workspace=%q tab=%q pane=%q)",
			c.WorkspaceID, c.TabID, c.PaneID)
	}
	return c, nil
}

// call runs a herdr command and decodes its JSON response into out.
func (r *Runner) call(ctx context.Context, out any, args ...string) error {
	stdout, stderr, err := r.run(ctx, args...)
	if err != nil {
		return parseError(stdout, stderr, err)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(stdout, out); err != nil {
		return fmt.Errorf("herdr %s: decoding response: %w", strings.Join(args, " "), err)
	}
	return nil
}

// callRaw runs a herdr command whose output is not JSON, returning stdout.
func (r *Runner) callRaw(ctx context.Context, args ...string) (string, error) {
	stdout, stderr, err := r.run(ctx, args...)
	if err != nil {
		return "", parseError(stdout, stderr, err)
	}
	return string(stdout), nil
}

// parseError turns a failed herdr invocation into an *Error. Server errors are
// JSON on stderr; syntax errors are plain usage text, and both have been seen
// on stdout depending on the command, so both streams are searched.
func parseError(stdout, stderr []byte, runErr error) error {
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}

	for _, stream := range [][]byte{stderr, stdout} {
		if len(stream) == 0 {
			continue
		}
		if err := json.Unmarshal(stream, &payload); err == nil && payload.Error.Code != "" {
			return &Error{
				Code:     payload.Error.Code,
				Message:  payload.Error.Message,
				ExitCode: exitCode(runErr),
			}
		}
	}

	msg := strings.TrimSpace(string(stderr))
	if msg == "" {
		msg = strings.TrimSpace(string(stdout))
	}
	if msg == "" {
		msg = runErr.Error()
	}
	return &Error{Message: msg, ExitCode: exitCode(runErr)}
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
