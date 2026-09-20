package config

import (
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
