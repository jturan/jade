package build

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jturan/jade/internal/config"
	"github.com/jturan/jade/internal/state"
	"github.com/jturan/jade/internal/telemetry"
)

// record writes a telemetry event if a Recorder is configured.
func (d Deps) record(e telemetry.Event) {
	if d.Recorder != nil {
		d.Recorder.Record(e)
	}
}

// dispatchAndRecord runs an agent, times it, and records the outcome.
func dispatchAndRecord(ctx context.Context, d Deps, disp Dispatch, unit int, attempt int, reportPath string) (Report, error) {
	disp.ReportPath = reportPath
	started := time.Now()
	err := d.Agent.Dispatch(ctx, disp)

	event := telemetry.Event{
		Repo:    d.Repo,
		Unit:    unit,
		Role:    string(disp.Role),
		Vendor:  disp.Vendor,
		Model:   disp.Model,
		Effort:  disp.Effort,
		Attempt: attempt,
		Seconds: time.Since(started).Seconds(),
	}

	if err != nil {
		event.Outcome = telemetry.OutcomeError
		event.Detail = err.Error()
		d.record(event)
		return Report{}, err
	}

	report, readErr := ReadReport(reportPath)
	if readErr != nil {
		// A missing or unreadable report is a harness problem, not a verdict
		// on the model, so it is recorded as an error rather than a failure.
		event.Outcome = telemetry.OutcomeError
		event.Detail = readErr.Error()
		d.record(event)
		return Report{}, readErr
	}

	event.Outcome = telemetry.OutcomeOK
	if report.Verdict == VerdictBlocked {
		event.Outcome = telemetry.OutcomeBlocked
		event.Detail = report.Summary
	}
	d.record(event)
	return report, nil
}

// Agent dispatches one agent and waits for it to finish. The loop does not care
// whether that happens in a herdr pane or anywhere else.
//
// Dispatch must not return while the agent is still working: the loop reads the
// report and records the duration as soon as it does. Implementations whose
// own completion signal can fire early should call AwaitReport before
// returning.
type Agent interface {
	Dispatch(ctx context.Context, d Dispatch) error
	Notify(ctx context.Context, title, body string) error
}

// Dispatch describes one agent run.
type Dispatch struct {
	// Name is the herdr agent name, unique among live agents.
	Name string
	Role config.Role
	// Vendor, Model and Effort come from the resolved config for this unit.
	Vendor string
	Model  string
	Effort string
	Prompt string
	// ReportPath is where the agent will write its report.
	ReportPath string
	Timeout    time.Duration
	// Args are native flags for the agent CLI, from the profile.
	Args []string
}

// Git is the subset of git operations the loop needs.
type Git interface {
	IsClean(ctx context.Context) (bool, error)
	HasChanges(ctx context.Context) (bool, error)
	DefaultBranch(ctx context.Context) (string, error)
	Checkout(ctx context.Context, branch string) error
	CommitAll(ctx context.Context, message string) error
	Push(ctx context.Context, branch string) error
	CreatePR(ctx context.Context, title, body, base string) (string, error)
	ChangedFiles(ctx context.Context, base string) ([]string, error)
	MergePR(ctx context.Context, url string) error
}

// Recorder records what each agent run cost and whether it worked. Telemetry
// failures are ignored by the loop: losing a line is never worth failing a run
// that otherwise succeeded.
type Recorder interface {
	Record(e telemetry.Event)
}

// Store is the subset of the state store the loop needs.
type Store interface {
	SetStatus(ctx context.Context, number int, status state.Status) error
	Comment(ctx context.Context, number int, body string) error
}

// Prompts renders the prompt for a role.
type Prompts interface {
	Build(unit state.Unit, issue state.Issue, reportPath string, previous *Report) (string, error)
	Review(role config.Role, issue state.Issue, reportPath, base string) (string, error)
}

// Deps is everything the loop needs from the outside world. Every dependency is
// an interface so the state machine — the part with the subtle behaviour — can
// be tested without herdr, git, or GitHub.
type Deps struct {
	Agent   Agent
	Git     Git
	Store   Store
	Prompts Prompts
	Config  *config.Resolved
	// Root is the repository working directory.
	Root string
	// BuildTimeout and ReviewTimeout bound a single agent run.
	BuildTimeout  time.Duration
	ReviewTimeout time.Duration
	// AutonomyOverride, when set, replaces the unit's own autonomy level for
	// this run.
	AutonomyOverride state.Autonomy
	// ProtectedPaths force a gate regardless of autonomy. Empty means the
	// built-in defaults.
	ProtectedPaths []string
	// Strict is the profile's review_strictness: when true, nothing merges
	// itself on this machine.
	Strict bool
	// AutoMergesSoFar and MaxAutoMerges cap unattended merges per run.
	AutoMergesSoFar int
	MaxAutoMerges   int
	// Recorder is optional; a nil Recorder disables telemetry.
	Recorder Recorder
	// Repo names the repository in telemetry rows.
	Repo string
	// Log receives human-readable progress.
	Log func(format string, args ...any)
}

// Outcome is how a unit ended.
type Outcome string

const (
	// OutcomePR means a pull request is open and waiting for a human.
	OutcomePR Outcome = "pr"
	// OutcomeMerged means autonomy permitted the unit to merge itself.
	OutcomeMerged Outcome = "merged"
	// OutcomeBlocked means the unit exhausted its retries and needs a human.
	OutcomeBlocked Outcome = "blocked"
)

// Result describes a finished unit.
type Result struct {
	Outcome  Outcome
	PRURL    string
	Branch   string
	Attempts int
	Reason   string
	// Autonomy records why the unit did or did not merge itself. It is always
	// set, because a silent auto-merge is indistinguishable from a bug.
	Autonomy AutonomyDecision
}

// RunUnit takes one unit from ready to either an open pull request or an
// escalation, without human input in between.
func RunUnit(ctx context.Context, d Deps, issue state.Issue, unit state.Unit) (*Result, error) {
	log := d.Log
	if log == nil {
		log = func(string, ...any) {}
	}

	// A dirty tree would be swept into this unit's commit. Refuse rather than
	// silently attribute unrelated work to the unit.
	clean, err := d.Git.IsClean(ctx)
	if err != nil {
		return nil, err
	}
	if !clean {
		return nil, fmt.Errorf("working tree has uncommitted changes — commit or stash them before building")
	}

	started := time.Now()

	base, err := d.Git.DefaultBranch(ctx)
	if err != nil {
		return nil, err
	}
	branch := branchFor(issue)
	if err := d.Git.Checkout(ctx, branch); err != nil {
		return nil, err
	}
	if err := d.Store.SetStatus(ctx, issue.Number, state.StatusInProgress); err != nil {
		return nil, err
	}
	log("unit #%d on %s", issue.Number, branch)

	// From here the issue is marked in-progress. Any failure that is not a
	// unit outcome — a rejected push, a pane that would not open — must still
	// leave the issue in a state someone can act on. Otherwise the unit sits
	// at agent:in-progress with nothing running and no explanation, which is
	// the worst state to come back to.
	res, err := runUnitBody(ctx, d, issue, unit, branch, base, started, log)
	if err != nil {
		strand(ctx, d, issue, branch, err)
		return nil, err
	}
	return res, nil
}

// strand records a harness failure on the issue so an interrupted unit stays
// legible. Errors here are ignored: the caller already has a real failure to
// report, and burying it under a bookkeeping error helps nobody.
func strand(ctx context.Context, d Deps, issue state.Issue, branch string, cause error) {
	body := fmt.Sprintf(
		"**Interrupted.** jade could not finish this unit:\n\n```\n%s\n```\n\nWork in progress is on `%s`.",
		cause, branch)
	_ = d.Store.Comment(ctx, issue.Number, body)
	_ = d.Store.SetStatus(ctx, issue.Number, state.StatusBlocked)
	_ = d.Agent.Notify(ctx, fmt.Sprintf("jade: #%d interrupted", issue.Number), cause.Error())
}

func runUnitBody(
	ctx context.Context,
	d Deps,
	issue state.Issue,
	unit state.Unit,
	branch, base string,
	started time.Time,
	log func(string, ...any),
) (*Result, error) {

	resolved := d.Config.ApplyUnit(unit.Overrides())
	buildReport := ReportPath(d.Root, "builder")

	// attempts is the initial run plus the unit's retry budget.
	attempts := unit.RetryLimit + 1
	var previous *Report

	for attempt := 1; attempt <= attempts; attempt++ {
		if attempt > 1 {
			log("retry %d of %d", attempt-1, unit.RetryLimit)
		}

		report, err := runBuilder(ctx, d, resolved, issue, unit, buildReport, previous, attempt)
		if err != nil {
			return nil, err
		}
		if report.Verdict == VerdictBlocked {
			previous = &report
			continue
		}

		// A builder that claims success without running tests has not
		// demonstrated anything, so treat it as a failed attempt.
		if !report.TestsRun {
			previous = &Report{
				Verdict:  VerdictBlocked,
				Summary:  "You reported success without running the tests. Run them and report again.",
				Findings: []string{"tests_run was false in your report"},
			}
			continue
		}

		changed, err := d.Git.HasChanges(ctx)
		if err != nil {
			return nil, err
		}
		if !changed {
			previous = &Report{
				Verdict: VerdictBlocked,
				Summary: "You reported success but the working tree is unchanged. Make the change, then report.",
			}
			continue
		}

		if err := d.Git.CommitAll(ctx, commitMessage(issue, report)); err != nil {
			return nil, err
		}

		reviews, err := runReviews(ctx, d, resolved, issue, unit, base)
		if err != nil {
			return nil, err
		}
		if blocking := blockingReviews(reviews); len(blocking) > 0 {
			log("review found %d blocking issue(s)", len(blocking))
			previous = mergeReviewFindings(blocking)
			continue
		}

		if err := d.Git.Push(ctx, branch); err != nil {
			return nil, err
		}
		url, err := d.Git.CreatePR(ctx, issue.Title, prBody(issue, report, reviews), base)
		if err != nil {
			return nil, err
		}
		if err := d.Store.SetStatus(ctx, issue.Number, state.StatusReview); err != nil {
			return nil, err
		}
		log("pull request open: %s", url)

		changedFiles, err := d.Git.ChangedFiles(ctx, base)
		if err != nil {
			return nil, err
		}
		level := unit.Autonomy
		if d.AutonomyOverride != "" {
			level = d.AutonomyOverride
		}
		decision := DecideAutonomy(AutonomyInput{
			Level:           level,
			Strict:          d.Strict,
			SecurityReview:  unit.SecurityReview,
			Attempts:        attempt,
			TestsRun:        report.TestsRun,
			ReviewsBlocking: len(blockingReviews(reviews)),
			ChangedFiles:    changedFiles,
			ProtectedPaths:  d.ProtectedPaths,
			AutoMergesSoFar: d.AutoMergesSoFar,
			MaxAutoMerges:   d.MaxAutoMerges,
		})

		result := &Result{PRURL: url, Branch: branch, Attempts: attempt, Autonomy: decision}
		if !decision.Merge {
			log("not merging: %s", decision.Reason)
			result.Outcome = OutcomePR
			d.recordUnit(issue, unit, result, started)
			return result, nil
		}

		if err := d.Git.MergePR(ctx, url); err != nil {
			return nil, fmt.Errorf("auto-merge: %w", err)
		}
		if err := d.Store.SetStatus(ctx, issue.Number, state.StatusDone); err != nil {
			return nil, err
		}
		// Record it on the issue: an unattended merge must leave a trail
		// someone can audit afterwards.
		if err := d.Store.Comment(ctx, issue.Number,
			fmt.Sprintf("Merged automatically — %s.\n\n%s", decision.Reason, url)); err != nil {
			return nil, err
		}
		log("merged automatically: %s", decision.Reason)
		result.Outcome = OutcomeMerged
		d.recordUnit(issue, unit, result, started)
		return result, nil
	}

	res, err := escalate(ctx, d, issue, branch, attempts, previous)
	if err == nil {
		d.recordUnit(issue, unit, res, started)
	}
	return res, err
}

// escalate records a unit that ran out of retries and tells a human.
func escalate(ctx context.Context, d Deps, issue state.Issue, branch string, attempts int, last *Report) (*Result, error) {
	reason := "the builder did not report success"
	if last != nil && last.Summary != "" {
		reason = last.Summary
	}

	var b strings.Builder
	fmt.Fprintf(&b, "**Blocked after %d attempts.**\n\n%s\n", attempts, reason)
	if last != nil && len(last.Findings) > 0 {
		b.WriteString("\nOutstanding:\n\n")
		for _, f := range last.Findings {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	fmt.Fprintf(&b, "\nWork in progress is on `%s`.\n", branch)

	if err := d.Store.Comment(ctx, issue.Number, b.String()); err != nil {
		return nil, err
	}
	if err := d.Store.SetStatus(ctx, issue.Number, state.StatusBlocked); err != nil {
		return nil, err
	}
	// A failed notification must never fail the loop: losing a toast is
	// annoying, losing the run is worse.
	_ = d.Agent.Notify(ctx, fmt.Sprintf("jade: #%d blocked", issue.Number), reason)

	return &Result{
		Outcome:  OutcomeBlocked,
		Branch:   branch,
		Attempts: attempts,
		Reason:   reason,
	}, nil
}

func runBuilder(
	ctx context.Context,
	d Deps,
	resolved *config.Resolved,
	issue state.Issue,
	unit state.Unit,
	reportPath string,
	previous *Report,
	attempt int,
) (Report, error) {
	prompt, err := d.Prompts.Build(unit, issue, reportPath, previous)
	if err != nil {
		return Report{}, err
	}
	// Remove any earlier report so a stale verdict cannot be read as this
	// attempt's result.
	if err := ClearReport(reportPath); err != nil {
		return Report{}, err
	}

	agent := resolved.Agents[config.RoleBuilder]
	return dispatchAndRecord(ctx, d, Dispatch{
		Name:    fmt.Sprintf("jade-builder-%d-%d", issue.Number, attempt),
		Role:    config.RoleBuilder,
		Vendor:  agent.Vendor,
		Model:   agent.Model,
		Effort:  agent.Effort,
		Prompt:  prompt,
		Timeout: d.BuildTimeout,
		Args:    resolved.ArgsFor(config.RoleBuilder),
	}, issue.Number, attempt, reportPath)
}

// reviewResult pairs a role with what its reviewer said.
type reviewResult struct {
	Role   config.Role
	Report Report
}

// runReviews runs code review, and security review when the unit calls for it.
func runReviews(
	ctx context.Context,
	d Deps,
	resolved *config.Resolved,
	issue state.Issue,
	unit state.Unit,
	base string,
) ([]reviewResult, error) {
	roles := []config.Role{config.RoleCodeReview}
	if unit.SecurityReview {
		roles = append(roles, config.RoleSecReview)
	}

	var out []reviewResult
	for _, role := range roles {
		path := ReportPath(d.Root, string(role))
		prompt, err := d.Prompts.Review(role, issue, path, base)
		if err != nil {
			return nil, err
		}
		if err := ClearReport(path); err != nil {
			return nil, err
		}

		agent := resolved.Agents[role]
		report, err := dispatchAndRecord(ctx, d, Dispatch{
			Name:    fmt.Sprintf("jade-%s-%d", shortRole(role), issue.Number),
			Role:    role,
			Vendor:  agent.Vendor,
			Model:   agent.Model,
			Effort:  agent.Effort,
			Prompt:  prompt,
			Timeout: d.ReviewTimeout,
			Args:    resolved.ArgsFor(role),
		}, issue.Number, 1, path)
		if err != nil {
			return nil, err
		}
		out = append(out, reviewResult{Role: role, Report: report})
	}
	return out, nil
}

func blockingReviews(reviews []reviewResult) []reviewResult {
	var out []reviewResult
	for _, r := range reviews {
		if r.Report.Verdict == VerdictBlocked {
			out = append(out, r)
		}
	}
	return out
}

// mergeReviewFindings folds blocking reviews into one report for the builder's
// next attempt, so it sees every objection at once rather than one per cycle.
func mergeReviewFindings(blocking []reviewResult) *Report {
	merged := &Report{Verdict: VerdictBlocked}
	var summaries []string
	for _, r := range blocking {
		if r.Report.Summary != "" {
			summaries = append(summaries, fmt.Sprintf("%s: %s", r.Role, r.Report.Summary))
		}
		for _, f := range r.Report.Findings {
			merged.Findings = append(merged.Findings, fmt.Sprintf("[%s] %s", r.Role, f))
		}
	}
	merged.Summary = strings.Join(summaries, "\n")
	return merged
}

func branchFor(issue state.Issue) string {
	return branchName(issue.Number, issue.Title)
}

func commitMessage(issue state.Issue, report Report) string {
	var b strings.Builder
	b.WriteString(issue.Title)
	if report.Summary != "" {
		b.WriteString("\n\n")
		b.WriteString(report.Summary)
	}
	fmt.Fprintf(&b, "\n\nCloses #%d\n", issue.Number)
	return b.String()
}

func prBody(issue state.Issue, build Report, reviews []reviewResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Implements #%d.\n\n", issue.Number)
	if build.Summary != "" {
		b.WriteString(build.Summary)
		b.WriteString("\n\n")
	}
	if len(reviews) > 0 {
		b.WriteString("## Review\n\n")
		for _, r := range reviews {
			summary := r.Report.Summary
			if summary == "" {
				summary = "no findings"
			}
			fmt.Fprintf(&b, "- **%s**: %s\n", r.Role, summary)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Closes #%d\n", issue.Number)
	return b.String()
}

func shortRole(role config.Role) string {
	switch role {
	case config.RoleSecReview:
		return "sec"
	case config.RoleCodeReview:
		return "review"
	default:
		return string(role)
	}
}

// recordUnit writes the row describing a whole unit's outcome, alongside the
// individual agent runs that produced it.
func (d Deps) recordUnit(issue state.Issue, unit state.Unit, res *Result, started time.Time) {
	d.record(telemetry.Event{
		Repo:     d.Repo,
		Unit:     issue.Number,
		Role:     telemetry.RoleUnit,
		Seconds:  time.Since(started).Seconds(),
		Outcome:  unitOutcome(res.Outcome),
		Detail:   res.Autonomy.Reason,
		Retries:  res.Attempts - 1,
		Autonomy: string(unit.Autonomy),
	})
}

func unitOutcome(o Outcome) telemetry.Outcome {
	if o == OutcomeBlocked {
		return telemetry.OutcomeBlocked
	}
	return telemetry.OutcomeOK
}
