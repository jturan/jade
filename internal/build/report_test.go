package build

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shortAwait(t *testing.T) {
	t.Helper()
	poll, probe, settle := awaitPoll, awaitProbe, awaitSettle
	awaitPoll, awaitProbe, awaitSettle = 5*time.Millisecond, 10*time.Millisecond, time.Second
	t.Cleanup(func() { awaitPoll, awaitProbe, awaitSettle = poll, probe, settle })
}

// stillThere is the gone check for an agent the runner can still see.
func stillThere(context.Context) bool { return false }

// A half-written report is not a report: AwaitReport keeps waiting until the
// file parses.
func TestAwaitReportWaitsForAParsableReport(t *testing.T) {
	shortAwait(t)
	path := filepath.Join(t.TempDir(), "builder.json")
	if err := os.WriteFile(path, []byte(`{"verdict":"o`), 0o644); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = os.WriteFile(path, []byte(`{"verdict":"ok","tests_run":true}`), 0o644)
	}()

	if err := AwaitReport(context.Background(), path, time.Now().Add(5*time.Second), stillThere); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReport(path); err != nil {
		t.Fatalf("returned before the report was complete: %v", err)
	}
}

// A report that is still unparsable once the agent has had time to finish it
// is corrupt, not partial. Holding the loop to the role timeout would both
// hide the real cause and write the timeout into telemetry as the run's
// duration.
func TestAwaitReportGivesUpOnACorruptReport(t *testing.T) {
	shortAwait(t)
	awaitSettle = 20 * time.Millisecond
	path := filepath.Join(t.TempDir(), "builder.json")
	if err := os.WriteFile(path, []byte("not json at all"), 0o644); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	err := AwaitReport(context.Background(), path, time.Now().Add(2*time.Second), stillThere)
	if !errors.Is(err, errBadReport) {
		t.Fatalf("err = %v, want it to name the unreadable report", err)
	}
	if took := time.Since(started); took > time.Second {
		t.Errorf("waited %s for a corrupt report, near enough the deadline to be a timeout", took)
	}
}

// A complete report carrying a verdict jade cannot act on will not improve
// with time, so it is reported as itself rather than as a timeout.
func TestAwaitReportGivesUpOnAnUnknownVerdict(t *testing.T) {
	shortAwait(t)
	path := filepath.Join(t.TempDir(), "builder.json")
	if err := os.WriteFile(path, []byte(`{"verdict":"finished"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	err := AwaitReport(context.Background(), path, time.Now().Add(2*time.Second), stillThere)
	if err == nil || !strings.Contains(err.Error(), "unknown verdict") {
		t.Fatalf("err = %v, want the unknown verdict", err)
	}
}

// An agent the runner no longer has is not coming back. Waiting out its role
// timeout would record 20 or 45 minutes as the run's duration.
func TestAwaitReportGivesUpOnAnAgentThatIsGone(t *testing.T) {
	shortAwait(t)
	path := filepath.Join(t.TempDir(), "builder.json")

	started := time.Now()
	err := AwaitReport(context.Background(), path, time.Now().Add(2*time.Second),
		func(context.Context) bool { return true })
	if err == nil || !strings.Contains(err.Error(), "stopped without writing") {
		t.Fatalf("err = %v, want the agent to be reported as stopped", err)
	}
	if took := time.Since(started); took > time.Second {
		t.Errorf("waited %s for an agent that was gone, near enough the deadline to be a timeout", took)
	}
}

// An agent that reports and then exits has finished, however the two are
// ordered against the poll.
func TestAwaitReportPrefersTheReportToAGoneAgent(t *testing.T) {
	shortAwait(t)
	path := filepath.Join(t.TempDir(), "builder.json")
	if err := os.WriteFile(path, []byte(`{"verdict":"ok","tests_run":true}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := AwaitReport(context.Background(), path, time.Time{},
		func(context.Context) bool { return true }); err != nil {
		t.Fatalf("err = %v, want the written report to be accepted", err)
	}
}

// An agent that never reports is bounded by its role timeout, not left to
// hold the loop forever.
func TestAwaitReportHonoursTheDeadline(t *testing.T) {
	shortAwait(t)
	path := filepath.Join(t.TempDir(), "builder.json")

	err := AwaitReport(context.Background(), path, time.Now().Add(30*time.Millisecond), stillThere)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("err = %v, want a timeout", err)
	}
}

func TestAwaitReportStopsWithTheContext(t *testing.T) {
	shortAwait(t)
	path := filepath.Join(t.TempDir(), "builder.json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := AwaitReport(ctx, path, time.Time{}, stillThere); err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
