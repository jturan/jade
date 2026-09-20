package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeBin is a name no real installation will have, so the fallback branch is
// actually exercised rather than short-circuited by a tool that happens to be
// on a developer's PATH.
const fakeBin = "jade-test-nonexistent-tool"

// A tool that is installed but unreachable must say so. Reporting "not found"
// to someone who installed it sends them to reinstall it rather than to fix
// their PATH — which is the actual problem, and common on macOS where
// Homebrew's prefix is often missing from a non-login shell.
func TestLookPathReportsInstalledButUnreachable(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, fakeBin)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := lookPathIn(fakeBin+"-absent", []string{dir})
	if err == nil || !strings.Contains(err.Error(), "not found on PATH") {
		t.Errorf("a genuinely absent binary should report not found: %v", err)
	}

	_, err = lookPathIn(fakeBin, []string{dir})
	if err == nil {
		t.Fatal("expected an error for a binary outside PATH")
	}
	if !strings.Contains(err.Error(), "not on PATH") || !strings.Contains(err.Error(), dir) {
		t.Errorf("error should name the install location and the fix: %v", err)
	}
}

// A non-executable file of the right name is not an install.
func TestLookPathIgnoresNonExecutable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, fakeBin), []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := lookPathIn(fakeBin, []string{dir})
	if err == nil || !strings.Contains(err.Error(), "not found on PATH") {
		t.Errorf("a non-executable file should not count as installed: %v", err)
	}
}

func TestExtraPrefixesCoverBothMacArchitectures(t *testing.T) {
	if runtime.GOOS != "darwin" {
		// The list is chosen by GOOS, so on other platforms just assert the
		// function is total rather than skipping the check entirely.
		if extraPrefixes() == nil && runtime.GOOS == "linux" {
			t.Error("linux should have fallback prefixes")
		}
		return
	}
	got := strings.Join(extraPrefixes(), " ")
	for _, want := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
		if !strings.Contains(got, want) {
			t.Errorf("macOS prefixes missing %q — Apple Silicon and Intel differ", want)
		}
	}
}
