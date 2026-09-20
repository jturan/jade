package telemetry

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAppendAndRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	log, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	for _, e := range []Event{
		{Role: "builder", Model: "sonnet", Outcome: OutcomeOK, Seconds: 30},
		{Role: "code_review", Model: "sonnet", Outcome: OutcomeBlocked, Seconds: 10},
	} {
		if err := log.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	events, err := Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("read %d events, want 2", len(events))
	}
	// A caller that omits the timestamp should still get one.
	if events[0].At.IsZero() {
		t.Error("Append should stamp the time")
	}
}

func TestReadMissingFileIsNotAnError(t *testing.T) {
	events, err := Read(filepath.Join(t.TempDir(), "nope.jsonl"))
	if err != nil {
		t.Fatalf("a missing log should read as empty, not fail: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events", len(events))
	}
}

// A partially written line from an interrupted run must not make the whole
// history unreadable.
func TestReadSkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "telemetry.jsonl")
	log, _ := Open(path)
	if err := log.Append(Event{Role: "builder", Outcome: OutcomeOK}); err != nil {
		t.Fatal(err)
	}

	f, err := openAppend(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"role":"builder","outcome":` + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if err := log.Append(Event{Role: "code_review", Outcome: OutcomeOK}); err != nil {
		t.Fatal(err)
	}

	events, err := Read(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("read %d events, want the 2 well-formed ones", len(events))
	}
}

// Harness errors must not count against a model. A pane that failed to start
// says nothing about whether Sonnet is good enough to be the builder default,
// and letting it drag the rate down would make the data useless for the one
// decision it exists to inform.
func TestSuccessRateIgnoresHarnessErrors(t *testing.T) {
	stats := Summarize([]Event{
		{Role: "builder", Model: "sonnet", Outcome: OutcomeOK},
		{Role: "builder", Model: "sonnet", Outcome: OutcomeOK},
		{Role: "builder", Model: "sonnet", Outcome: OutcomeBlocked},
		{Role: "builder", Model: "sonnet", Outcome: OutcomeError},
	})
	if len(stats) != 1 {
		t.Fatalf("got %d groups, want 1", len(stats))
	}

	s := stats[0]
	if s.Runs != 4 || s.Errors != 1 {
		t.Errorf("stat = %+v", s)
	}
	if got := s.SuccessRate(); got < 0.66 || got > 0.67 {
		t.Errorf("success rate = %.3f, want 2/3 — the harness error must not count", got)
	}
}

func TestSummarizeGroupsByRoleModelEffort(t *testing.T) {
	stats := Summarize([]Event{
		{Role: "builder", Model: "sonnet", Effort: "medium", Outcome: OutcomeOK, Seconds: 10},
		{Role: "builder", Model: "sonnet", Effort: "medium", Outcome: OutcomeOK, Seconds: 20},
		{Role: "builder", Model: "opus", Effort: "high", Outcome: OutcomeOK, Seconds: 60},
		// Unit rows are outcomes, not agent runs, and must not pollute
		// per-model statistics.
		{Role: RoleUnit, Outcome: OutcomeOK, Seconds: 300},
	})
	if len(stats) != 2 {
		t.Fatalf("got %d groups, want 2: %+v", len(stats), stats)
	}

	for _, s := range stats {
		if s.Role == RoleUnit {
			t.Error("unit rows must be excluded from agent statistics")
		}
		if s.Model == "sonnet" && s.MeanSeconds() != 15 {
			t.Errorf("sonnet mean = %.1f, want 15", s.MeanSeconds())
		}
	}
}

func TestForUnitIsChronological(t *testing.T) {
	base := time.Now()
	events := []Event{
		{Unit: 7, Role: "code_review", At: base.Add(2 * time.Minute)},
		{Unit: 9, Role: "builder", At: base},
		{Unit: 7, Role: "builder", At: base},
	}

	got := ForUnit(events, 7)
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
	if got[0].Role != "builder" {
		t.Errorf("first event = %q, want the earlier builder run", got[0].Role)
	}
}
