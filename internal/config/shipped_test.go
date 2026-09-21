package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The repo is public. A shipped profile that named a path, a client, or a
// machine would leak it to everyone who clones jade.
func TestShippedProfilesHoldNothingPersonal(t *testing.T) {
	for _, name := range ShippedNames() {
		t.Run(name, func(t *testing.T) {
			p, ok, err := ShippedProfile(name)
			if err != nil || !ok {
				t.Fatalf("shipped profile %q: ok=%v err=%v", name, ok, err)
			}
			if p.DiscoverySink != SinkRepo && p.DiscoverySink != SinkVault {
				t.Errorf("discovery_sink = %q — a shipped profile may only describe the shape of a sink", p.DiscoverySink)
			}
			for role, args := range p.AgentArgs {
				for _, a := range args {
					if strings.Contains(a, "/") {
						t.Errorf("role %q ships an agent arg that looks like a path: %q", role, a)
					}
				}
			}
			for _, glob := range p.ProtectedPaths {
				if strings.HasPrefix(glob, "/") || strings.Contains(glob, "~") {
					t.Errorf("protected path %q is absolute — protected paths are repo-relative", glob)
				}
			}
		})
	}
}

// Every shipped profile must stand on its own: a fresh machine has nothing else
// to fall back on.
func TestShippedProfilesValidate(t *testing.T) {
	for _, name := range ShippedNames() {
		t.Run(name, func(t *testing.T) {
			p, _, err := ShippedProfile(name)
			if err != nil {
				t.Fatal(err)
			}
			merged, _ := MergeProfile(p, Profile{})
			if err := merged.Validate(); err != nil {
				t.Fatalf("shipped profile does not validate: %v", err)
			}
			if merged.ReviewStrictness == "" {
				t.Error("review_strictness resolved to empty")
			}
		})
	}
}

// The three profiles the plan names must exist, or setting up the second laptop
// is back to answering the form from memory.
func TestShippedNamesCoverEveryMachine(t *testing.T) {
	names := ShippedNames()
	for _, want := range []string{"personal", "consulting", "dayjob"} {
		if !slices.Contains(names, want) {
			t.Errorf("no shipped profile named %q (have %v)", want, names)
		}
	}
	if _, ok, _ := ShippedProfile("nobody-ships-this"); ok {
		t.Error("an unknown name should not resolve to a shipped profile")
	}
}

// Somewhere with code review as a norm, a machine merging its own work is the
// wrong default however well the guardrails hold.
func TestDayjobShipsStrict(t *testing.T) {
	p, _, err := ShippedProfile("dayjob")
	if err != nil {
		t.Fatal(err)
	}
	if p.ReviewStrictness != StrictnessStrict {
		t.Errorf("dayjob review_strictness = %q, want %q", p.ReviewStrictness, StrictnessStrict)
	}
	for _, other := range []string{"personal", "consulting"} {
		o, _, err := ShippedProfile(other)
		if err != nil {
			t.Fatal(err)
		}
		if o.ReviewStrictness == StrictnessStrict {
			t.Errorf("%s ships strict — normal is the default everywhere else", other)
		}
	}
}

// The whole point of shipping profiles: a laptop with no local config gets the
// same environment from the repo alone.
func TestLoadWithoutLocalConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	r, err := Load()
	if err != nil {
		t.Fatalf("a machine with no local config must still resolve: %v", err)
	}
	if r.HasLocal {
		t.Error("HasLocal should be false with no config file")
	}
	if r.Profile.Name != DefaultProfileName {
		t.Errorf("profile = %q, want %q", r.Profile.Name, DefaultProfileName)
	}
	if r.Profile.GitHost == "" || len(r.Profile.AllowedVendors) == 0 || r.Profile.DiscoverySink == "" {
		t.Errorf("profile is incomplete: %+v", r.Profile)
	}
	if r.Profile.ReviewStrictness != StrictnessNormal {
		t.Errorf("review_strictness = %q, want %q", r.Profile.ReviewStrictness, StrictnessNormal)
	}
	for _, role := range Roles() {
		if r.Agents[role] == (Agent{}) {
			t.Errorf("role %q resolved to a zero Agent", role)
		}
	}
	if r.Sources[FieldGitHost] != LayerShipped {
		t.Errorf("git_host source = %q, want %q", r.Sources[FieldGitHost], LayerShipped)
	}
}

// The local file holds the machine's own paths; everything else keeps coming
// from the repo, so a value that moves can be told apart from one that did not.
func TestLoadLayersLocalOverShipped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)

	vault := t.TempDir()
	dir := filepath.Join(home, "jade")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	local := "active_profile: dayjob\nprofiles:\n  - name: dayjob\n    discovery_sink: " + vault + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !r.HasLocal {
		t.Error("HasLocal should be true when a config file exists")
	}
	if r.Profile.DiscoverySink != vault {
		t.Errorf("sink = %q, want the local path %q", r.Profile.DiscoverySink, vault)
	}
	if r.Sources[FieldDiscoverySink] != LayerProfile {
		t.Errorf("sink source = %q, want %q", r.Sources[FieldDiscoverySink], LayerProfile)
	}
	// Untouched by the local file, so still the employer's posture.
	if !r.Strict() {
		t.Error("dayjob resolved to non-strict")
	}
	if r.Sources[FieldReviewStrictness] != LayerShipped {
		t.Errorf("review_strictness source = %q, want %q", r.Sources[FieldReviewStrictness], LayerShipped)
	}
	if got := r.Profile.AllowedVendors; len(got) != 1 || got[0] != "claude" {
		t.Errorf("allowed_vendors = %v, want the shipped list", got)
	}
}
