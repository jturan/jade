package runner

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// Tab is a tab in a herdr workspace.
type Tab struct {
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	AgentStatus State  `json:"agent_status"`
	PaneCount   int    `json:"pane_count"`
	Focused     bool   `json:"focused"`
}

// Pane is a terminal pane.
type Pane struct {
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Cwd         string `json:"cwd"`
	AgentStatus State  `json:"agent_status"`
}

// Agent is a recognized coding agent occupying a pane.
//
// herdr names these fields "agent" (the kind) and "agent_status", not "kind"
// and "status" — guessing otherwise decodes to an empty state that silently
// reads as StateUnknown.
type Agent struct {
	Name             string `json:"name"`
	Kind             string `json:"agent"`
	PaneID           string `json:"pane_id"`
	TabID            string `json:"tab_id"`
	Status           State  `json:"agent_status"`
	InteractiveReady bool   `json:"interactive_ready"`
}

// Semantic tab names. One workspace per active repo, tabs by purpose, so
// related terminals stay together without all of them being on screen.
const (
	TabOrchestrator = "orchestrator"
	TabAgents       = "agents"
	TabDev          = "dev"
	TabChecks       = "checks"
)

// Direction is a pane split direction.
type Direction string

const (
	Right Direction = "right"
	Down  Direction = "down"
)

// ListTabs returns the tabs in a workspace.
func (r *Runner) ListTabs(ctx context.Context, workspaceID string) ([]Tab, error) {
	var resp struct {
		Result struct {
			Tabs []Tab `json:"tabs"`
		} `json:"result"`
	}
	if err := r.call(ctx, &resp, "tab", "list", "--workspace", workspaceID); err != nil {
		return nil, err
	}
	return resp.Result.Tabs, nil
}

// EnsuredTab is a tab that either already existed or was just created. A newly
// created tab carries its root pane, which is a bare shell and therefore
// immediately usable for an agent.
type EnsuredTab struct {
	Tab      Tab
	RootPane Pane
	Created  bool
}

// EnsureTab returns the tab with the given label, creating it if absent.
func (r *Runner) EnsureTab(ctx context.Context, workspaceID, label string) (EnsuredTab, error) {
	tabs, err := r.ListTabs(ctx, workspaceID)
	if err != nil {
		return EnsuredTab{}, err
	}
	for _, t := range tabs {
		if t.Label == label {
			return EnsuredTab{Tab: t}, nil
		}
	}

	var resp struct {
		Result struct {
			Tab      Tab  `json:"tab"`
			RootPane Pane `json:"root_pane"`
		} `json:"result"`
	}
	if err := r.call(ctx, &resp,
		"tab", "create", "--workspace", workspaceID, "--label", label, "--no-focus"); err != nil {
		return EnsuredTab{}, fmt.Errorf("creating tab %q: %w", label, err)
	}
	return EnsuredTab{Tab: resp.Result.Tab, RootPane: resp.Result.RootPane, Created: true}, nil
}

// ListPanes returns the panes in a workspace.
func (r *Runner) ListPanes(ctx context.Context, workspaceID string) ([]Pane, error) {
	var resp struct {
		Result struct {
			Panes []Pane `json:"panes"`
		} `json:"result"`
	}
	if err := r.call(ctx, &resp, "pane", "list", "--workspace", workspaceID); err != nil {
		return nil, err
	}
	return resp.Result.Panes, nil
}

// AgentPane returns a pane in the named tab that is ready to host an agent.
//
// A freshly created tab's root pane is already a bare shell, so it is used as
// is. Otherwise a new pane is split off, since herdr requires an agent to start
// in a pane sitting at an interactive prompt and an existing pane may still be
// occupied.
func (r *Runner) AgentPane(ctx context.Context, workspaceID, tabLabel, cwd string) (Pane, error) {
	ensured, err := r.EnsureTab(ctx, workspaceID, tabLabel)
	if err != nil {
		return Pane{}, err
	}
	if ensured.Created && ensured.RootPane.PaneID != "" {
		return ensured.RootPane, nil
	}

	panes, err := r.ListPanes(ctx, workspaceID)
	if err != nil {
		return Pane{}, err
	}
	var inTab []Pane
	for _, p := range panes {
		if p.TabID == ensured.Tab.TabID {
			inTab = append(inTab, p)
		}
	}
	if len(inTab) == 0 {
		return Pane{}, fmt.Errorf("tab %q has no panes to split from", tabLabel)
	}

	// Alternate direction so repeated splits do not collapse into unusably
	// narrow columns or short rows.
	direction := Right
	if len(inTab)%2 == 0 {
		direction = Down
	}
	return r.Split(ctx, SplitOpts{
		FromPane:  inTab[len(inTab)-1].PaneID,
		Direction: direction,
		Cwd:       cwd,
	})
}

// SplitOpts configures a new pane.
type SplitOpts struct {
	// FromPane is the pane to split. Empty means the calling pane.
	FromPane  string
	Direction Direction
	Cwd       string
}

// Split creates a pane and returns it.
//
// Focus is never taken: agents work in the background and the operator stays in
// the orchestrator pane. herdr's own guidance is to split a wide pane right and
// a tall one down; callers pick, since jade knows which tab it is building.
func (r *Runner) Split(ctx context.Context, opts SplitOpts) (Pane, error) {
	args := []string{"pane", "split"}
	if opts.FromPane == "" {
		args = append(args, "--current")
	} else {
		args = append(args, "--pane", opts.FromPane)
	}
	if opts.Direction == "" {
		opts.Direction = Right
	}
	args = append(args, "--direction", string(opts.Direction))
	if opts.Cwd != "" {
		args = append(args, "--cwd", opts.Cwd)
	}
	args = append(args, "--no-focus")

	var resp struct {
		Result struct {
			Pane Pane `json:"pane"`
		} `json:"result"`
	}
	if err := r.call(ctx, &resp, args...); err != nil {
		return Pane{}, err
	}
	return resp.Result.Pane, nil
}

// ClosePane closes a pane jade created.
func (r *Runner) ClosePane(ctx context.Context, paneID string) error {
	return r.call(ctx, nil, "pane", "close", paneID)
}

// StartOpts configures starting an agent in an existing pane.
type StartOpts struct {
	// Name must match [a-z][a-z0-9_-]{0,31} and be unique among live agents.
	Name string
	// Kind is a herdr agent kind, e.g. "claude" or "codex".
	Kind   string
	PaneID string
	// Timeout bounds waiting for interactive readiness (herdr max: 5m).
	Timeout time.Duration
	// Args are passed to the agent binary itself, after "--".
	Args []string
}

// StartAgent starts an agent in an existing shell pane.
//
// herdr's agent start never creates layout, so the pane must already exist and
// be sitting at an interactive prompt — call Split first.
func (r *Runner) StartAgent(ctx context.Context, opts StartOpts) error {
	args := []string{"agent", "start", opts.Name, "--kind", opts.Kind, "--pane", opts.PaneID}
	if opts.Timeout > 0 {
		args = append(args, "--timeout", millis(opts.Timeout))
	}
	if len(opts.Args) > 0 {
		args = append(args, "--")
		args = append(args, opts.Args...)
	}
	return r.call(ctx, nil, args...)
}

// Prompt submits text to an agent and, when wait is true, blocks until the
// agent reaches a settled state.
//
// A prompt is rejected with CodeAgentBlocked if the agent is already sitting at
// an approval dialog; the caller must inspect it rather than blindly answering.
func (r *Runner) Prompt(ctx context.Context, target, text string, wait bool, timeout time.Duration) error {
	args := []string{"agent", "prompt", target, text}
	if wait {
		args = append(args, "--wait")
	}
	if timeout > 0 {
		args = append(args, "--timeout", millis(timeout))
	}
	return r.call(ctx, nil, args...)
}

// Wait blocks until an agent reaches one of the given states. With no states,
// herdr waits for any settled state.
func (r *Runner) Wait(ctx context.Context, target string, timeout time.Duration, until ...State) (State, error) {
	args := []string{"agent", "wait", target}
	for _, s := range until {
		args = append(args, "--until", string(s))
	}
	if timeout > 0 {
		args = append(args, "--timeout", millis(timeout))
	}
	if err := r.call(ctx, nil, args...); err != nil {
		return StateUnknown, err
	}
	return r.AgentState(ctx, target)
}

// GetAgent returns an agent's current details.
func (r *Runner) GetAgent(ctx context.Context, target string) (Agent, error) {
	var resp struct {
		Result struct {
			Agent Agent `json:"agent"`
		} `json:"result"`
	}
	if err := r.call(ctx, &resp, "agent", "get", target); err != nil {
		return Agent{}, err
	}
	return resp.Result.Agent, nil
}

// AgentState returns an agent's current lifecycle state.
func (r *Runner) AgentState(ctx context.Context, target string) (State, error) {
	agent, err := r.GetAgent(ctx, target)
	if err != nil {
		return StateUnknown, err
	}
	if agent.Status == "" {
		return StateUnknown, fmt.Errorf("herdr reported no status for agent %q", target)
	}
	return agent.Status, nil
}

// FocusAgent brings an agent's pane to the front. Used when the operator is
// meant to take over the conversation, which is the one case where stealing
// focus is the helpful thing to do.
func (r *Runner) FocusAgent(ctx context.Context, target string) error {
	return r.call(ctx, nil, "agent", "focus", target)
}

// ReadSource selects which slice of a pane's output to read.
type ReadSource string

const (
	// SourceRecentUnwrapped joins soft-wrapped lines; preferred for logs and
	// transcripts, which is what jade reads back from agents.
	SourceRecentUnwrapped ReadSource = "recent-unwrapped"
	SourceVisible         ReadSource = "visible"
	SourceRecent          ReadSource = "recent"
	SourceDetection       ReadSource = "detection"
)

// ReadAgent returns recent terminal output from an agent.
func (r *Runner) ReadAgent(ctx context.Context, target string, lines int, source ReadSource) (string, error) {
	if source == "" {
		source = SourceRecentUnwrapped
	}
	args := []string{"agent", "read", target, "--source", string(source)}
	if lines > 0 {
		args = append(args, "--lines", strconv.Itoa(lines))
	}
	// Unlike every other herdr command, read emits raw terminal text rather
	// than JSON — its --format flag chooses text or ansi, never json.
	return r.callRaw(ctx, args...)
}

// Notify shows a desktop notification through herdr.
//
// Routing this through herdr rather than notify-send keeps jade working on
// macOS and Linux alike without a platform switch — herdr is already a hard
// dependency, so this costs nothing.
//
// A failed notification never fails the caller: losing a notification is
// annoying, but failing a build loop because a toast could not be drawn is
// worse.
func (r *Runner) Notify(ctx context.Context, title, body string) error {
	args := []string{"notification", "show", title}
	if body != "" {
		args = append(args, "--body", body)
	}
	args = append(args, "--sound", "request")
	return r.call(ctx, nil, args...)
}

func millis(d time.Duration) string {
	return strconv.FormatInt(d.Milliseconds(), 10)
}
