package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func shortAwait(t *testing.T) {
	t.Helper()
	poll, quiet := awaitPoll, awaitQuiet
	awaitPoll, awaitQuiet = 5*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { awaitPoll, awaitQuiet = poll, quiet })
}

func working(context.Context) (bool, error) { return false, nil }
func idle(context.Context) (bool, error)    { return true, nil }

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

	if err := AwaitReport(context.Background(), path, time.Now().Add(5*time.Second), working); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReport(path); err != nil {
		t.Fatalf("returned before the report was complete: %v", err)
	}
}

// An agent that has stopped without reporting must not hold the loop until
// the role timeout.
func TestAwaitReportGivesUpOnASettledAgent(t *testing.T) {
	shortAwait(t)
	path := filepath.Join(t.TempDir(), "builder.json")

	started := time.Now()
	if err := AwaitReport(context.Background(), path, time.Now().Add(5*time.Second), idle); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 2*time.Second {
		t.Errorf("waited %s for an idle agent", time.Since(started))
	}
}

func TestAwaitReportHonoursTheDeadline(t *testing.T) {
	shortAwait(t)
	path := filepath.Join(t.TempDir(), "builder.json")

	err := AwaitReport(context.Background(), path, time.Now().Add(30*time.Millisecond), working)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("err = %v, want a timeout", err)
	}
}
