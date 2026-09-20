package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeAgentsPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		override   map[Role]Agent
		role       Role
		wantAgent  Agent
		wantOrigin Origin
	}{
		{
			name:       "no override keeps the default",
			override:   nil,
			role:       RoleBuilder,
			wantAgent:  Agent{Vendor: "claude", Model: "sonnet", Effort: "medium"},
			wantOrigin: Origin{LayerDefault, LayerDefault, LayerDefault},
		},
		{
			name:       "partial override leaves other fields alone",
			override:   map[Role]Agent{RoleBuilder: {Effort: "high"}},
			role:       RoleBuilder,
			wantAgent:  Agent{Vendor: "claude", Model: "sonnet", Effort: "high"},
			wantOrigin: Origin{LayerDefault, LayerDefault, LayerProfile},
		},
		{
			name:       "full override replaces every field",
			override:   map[Role]Agent{RoleCodeReview: {Vendor: "codex", Model: "gpt", Effort: "high"}},
			role:       RoleCodeReview,
			wantAgent:  Agent{Vendor: "codex", Model: "gpt", Effort: "high"},
			wantOrigin: Origin{LayerProfile, LayerProfile, LayerProfile},
		},
		{
			name:       "overriding one role does not disturb another",
			override:   map[Role]Agent{RoleBuilder: {Model: "opus"}},
			role:       RoleSecReview,
			wantAgent:  Agent{Vendor: "claude", Model: "opus", Effort: "high"},
			wantOrigin: Origin{LayerDefault, LayerDefault, LayerDefault},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agents, origins := MergeAgents(DefaultAgents(), tt.override, LayerProfile)
			if got := agents[tt.role]; got != tt.wantAgent {
				t.Errorf("agent = %+v, want %+v", got, tt.wantAgent)
			}
			if got := origins[tt.role]; got != tt.wantOrigin {
				t.Errorf("origin = %+v, want %+v", got, tt.wantOrigin)
			}
		})
	}
}

func TestMergeAgentsCoversEveryRole(t *testing.T) {
	agents, origins := MergeAgents(DefaultAgents(), nil, LayerProfile)
	for _, role := range Roles() {
		if agents[role] == (Agent{}) {
			t.Errorf("role %q resolved to a zero Agent", role)
		}
		if origins[role] == (Origin{}) {
			t.Errorf("role %q resolved to a zero Origin", role)
		}
	}
}

// A unit-of-work override must win over the profile, and must not erase the
// profile's provenance for fields it leaves alone.
func TestApplyUnitOverridesProfile(t *testing.T) {
	agents, origins := MergeAgents(
		DefaultAgents(),
		map[Role]Agent{RoleBuilder: {Model: "opus"}},
		LayerProfile,
	)
	r := &Resolved{Agents: agents, Origins: origins}

	got := r.ApplyUnit(map[Role]Agent{RoleBuilder: {Effort: "high"}})

	want := Agent{Vendor: "claude", Model: "opus", Effort: "high"}
	if got.Agents[RoleBuilder] != want {
		t.Errorf("agent = %+v, want %+v", got.Agents[RoleBuilder], want)
	}
	wantOrigin := Origin{Vendor: LayerDefault, Model: LayerProfile, Effort: LayerUnit}
	if got.Origins[RoleBuilder] != wantOrigin {
		t.Errorf("origin = %+v, want %+v", got.Origins[RoleBuilder], wantOrigin)
	}

	// The original must be unchanged — the build loop resolves many units
	// against one shared config.
	if r.Agents[RoleBuilder].Effort != "medium" {
		t.Error("ApplyUnit mutated the receiver")
	}
}

func TestProfileValidate(t *testing.T) {
	dir := t.TempDir()
	valid := func() Profile {
		return Profile{
			Name:           "personal",
			GitHost:        "github",
			DiscoverySink:  dir,
			AllowedVendors: []string{"claude"},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*Profile)
		wantErr string
	}{
		{"valid profile", func(*Profile) {}, ""},
		{"sink may be the repo sentinel", func(p *Profile) { p.DiscoverySink = SinkRepo }, ""},
		{"missing name", func(p *Profile) { p.Name = "" }, "name is required"},
		{"unsupported git host", func(p *Profile) { p.GitHost = "gitlab" }, "not supported"},
		{"no vendors", func(p *Profile) { p.AllowedVendors = nil }, "at least one vendor"},
		{"missing sink", func(p *Profile) { p.DiscoverySink = "" }, "discovery_sink is required"},
		{"sink does not exist", func(p *Profile) { p.DiscoverySink = filepath.Join(dir, "nope") }, "not readable"},
		{
			"role pinned to a disallowed vendor",
			func(p *Profile) { p.Agents = map[Role]Agent{RoleCodeReview: {Vendor: "codex"}} },
			"not in allowed_vendors",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := valid()
			tt.mutate(&p)
			err := p.Validate()

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
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestActiveProfileOrError(t *testing.T) {
	cfg := &Config{
		ActiveProfile: "consulting",
		Profiles:      []Profile{{Name: "personal"}, {Name: "consulting"}},
	}
	p, err := cfg.ActiveProfileOrError()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "consulting" {
		t.Errorf("selected %q, want consulting", p.Name)
	}

	// An unknown active profile should name what *is* available, since the
	// usual cause is a typo.
	cfg.ActiveProfile = "dayjob"
	_, err = cfg.ActiveProfileOrError()
	if err == nil {
		t.Fatal("expected an error for an unknown profile")
	}
	for _, want := range []string{"dayjob", "consulting", "personal"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	cfg.ActiveProfile = ""
	if _, err := cfg.ActiveProfileOrError(); err == nil {
		t.Error("expected an error when active_profile is unset")
	}
}

func TestResolveSinkUsesProfilePath(t *testing.T) {
	vault := t.TempDir()
	r := &Resolved{Profile: Profile{Name: "personal", DiscoverySink: vault}}

	got, err := r.ResolveSink("/some/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != vault {
		t.Errorf("sink = %q, want %q", got, vault)
	}
}

// A work profile writes into the repo, so nothing from a client or employer
// lands in a personal vault.
func TestResolveSinkRepoWritesIntoRepo(t *testing.T) {
	repo := t.TempDir()
	r := &Resolved{Profile: Profile{Name: "dayjob", DiscoverySink: SinkRepo}}

	got, err := r.ResolveSink(repo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(repo, RepoDiscoveryDir)
	if got != want {
		t.Errorf("sink = %q, want %q", got, want)
	}
	if info, err := os.Stat(got); err != nil || !info.IsDir() {
		t.Errorf("sink directory was not created: %v", err)
	}
}

// Discovery often precedes a repo. A repo-sink profile must say so plainly
// rather than writing notes somewhere surprising.
func TestResolveSinkRepoWithoutRepoFails(t *testing.T) {
	r := &Resolved{Profile: Profile{Name: "dayjob", DiscoverySink: SinkRepo}}

	if _, err := r.ResolveSink(""); err == nil {
		t.Fatal("expected an error when there is no repository")
	}
}

// A note written into someone's vault should follow that vault's rules, not
// invent its own conventions.
func TestSinkConventionsFindsInstructions(t *testing.T) {
	dir := t.TempDir()
	if got := SinkConventions(dir); got != "" {
		t.Errorf("expected no conventions in an empty directory, got %q", got)
	}

	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("Title Case filenames."), 0o644); err != nil {
		t.Fatal(err)
	}
	got := SinkConventions(dir)
	if !strings.Contains(got, "Title Case filenames.") {
		t.Errorf("conventions not picked up: %q", got)
	}
	if !strings.Contains(got, "CLAUDE.md") {
		t.Error("conventions should say where they came from")
	}
}

// Notes usually live in a subdirectory of the vault, so the parent is checked
// too — that is where the instructions actually are.
func TestSinkConventionsChecksParent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("parent rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "Projects")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	if got := SinkConventions(sub); !strings.Contains(got, "parent rules") {
		t.Errorf("parent instructions not found: %q", got)
	}
}
