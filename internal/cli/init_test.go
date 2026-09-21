package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jturan/jade/internal/config"
	"github.com/spf13/cobra"
)

// init starts from the profile the repo ships and writes down only what the
// repo cannot know. A local file that restated the shipped values would fork
// from them silently the next time one of them changed.
func TestCollectProfileRecordsOnlyWhatIsLocal(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetErr(io.Discard)

	p, err := collectProfile(cmd, "dayjob", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "dayjob" {
		t.Fatalf("name = %q", p.Name)
	}
	if p.GitHost != "" || len(p.AllowedVendors) != 0 || p.ReviewStrictness != "" {
		t.Errorf("shipped values were copied into the local profile: %+v", p)
	}
	// dayjob writes notes into the repo, so there is no path to ask for.
	if p.DiscoverySink != "" {
		t.Errorf("discovery_sink = %q, want it left to the shipped profile", p.DiscoverySink)
	}

	shipped, _, err := config.ShippedProfile("dayjob")
	if err != nil {
		t.Fatal(err)
	}
	merged, _ := config.MergeProfile(shipped, *p)
	if merged.ReviewStrictness != config.StrictnessStrict {
		t.Errorf("resolved strictness = %q, want the shipped %q", merged.ReviewStrictness, config.StrictnessStrict)
	}
}

// A personal machine keeps its notes in a vault the repo cannot name, so that
// is the one thing init has to settle.
func TestCollectProfileSettlesTheVaultPath(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetErr(io.Discard)

	p, err := collectProfile(cmd, "personal", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.DiscoverySink == "" || p.DiscoverySink == config.SinkVault {
		t.Errorf("discovery_sink = %q — init must resolve the placeholder", p.DiscoverySink)
	}
}

// Running init twice must add a second profile, not destroy the first. The
// three-laptop setup depends on one config holding several profiles.
func TestUpsertProfileAddsThenReplaces(t *testing.T) {
	cfg := &config.Config{}

	upsertProfile(cfg, config.Profile{Name: "personal", DiscoverySink: "/vault"})
	upsertProfile(cfg, config.Profile{Name: "dayjob", DiscoverySink: config.SinkRepo})
	if len(cfg.Profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(cfg.Profiles))
	}

	upsertProfile(cfg, config.Profile{Name: "personal", DiscoverySink: "/other"})
	if len(cfg.Profiles) != 2 {
		t.Fatalf("re-adding a profile changed the count: %d", len(cfg.Profiles))
	}
	for _, p := range cfg.Profiles {
		if p.Name == "personal" && p.DiscoverySink != "/other" {
			t.Errorf("personal sink = %q, want the updated value", p.DiscoverySink)
		}
		if p.Name == "dayjob" && p.DiscoverySink != config.SinkRepo {
			t.Error("updating one profile disturbed another")
		}
	}
}

// A typo in the sink should be caught here, not at the start of a discovery
// session when the user is ready to think.
func TestValidateSink(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, in, wantErr string
	}{
		{"an existing directory", dir, ""},
		{"the repo sentinel", config.SinkRepo, ""},
		{"empty", "", "required"},
		{"relative path", "notes/", "absolute"},
		{"missing directory", filepath.Join(dir, "nope"), "does not exist"},
		{"a file, not a directory", file, "not a directory"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSink(tt.in)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

// An unparseable config must not be silently overwritten. Clobbering a file
// someone hand-edited is worse than making them fix it.
func TestLoadExistingRefusesToClobberBrokenConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	if err := os.WriteFile(path, []byte("active_profile: [broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := loadExisting(path)
	if err == nil {
		t.Fatal("expected an error for an unparseable config")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("the error should say how to proceed: %v", err)
	}
}

func TestLoadExistingMissingFileIsNotAnError(t *testing.T) {
	cfg, err := loadExisting(filepath.Join(t.TempDir(), "config.yml"))
	if err != nil {
		t.Fatalf("a missing config is the normal first-run case: %v", err)
	}
	if cfg != nil {
		t.Error("expected no config")
	}
}

// The vendor list must only offer agents that are actually installed, since a
// role pinned to an absent vendor fails at config load.
func TestInstalledVendorsAreReal(t *testing.T) {
	for _, v := range installedVendors() {
		if _, err := lookPath(v); err != nil {
			t.Errorf("%q was offered but is not installed: %v", v, err)
		}
	}
}
