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
	poll := awaitPoll
	awaitPoll = 5 * time.Millisecond
	t.Cleanup(func() { awaitPoll = poll })
}

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

	if err := AwaitReport(context.Background(), path, time.Now().Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadReport(path); err != nil {
		t.Fatalf("returned before the report was complete: %v", err)
	}
}

// An agent that never reports is bounded by its role timeout, not left to
// hold the loop forever.
func TestAwaitReportHonoursTheDeadline(t *testing.T) {
	shortAwait(t)
	path := filepath.Join(t.TempDir(), "builder.json")

	err := AwaitReport(context.Background(), path, time.Now().Add(30*time.Millisecond))
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("err = %v, want a timeout", err)
	}
}

func TestAwaitReportStopsWithTheContext(t *testing.T) {
	shortAwait(t)
	path := filepath.Join(t.TempDir(), "builder.json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := AwaitReport(ctx, path, time.Time{}); err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
