package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
)

// Layer names where a resolved value came from. Reported by `jade config show`
// so a machine behaving unexpectedly can be diagnosed without guesswork.
const (
	LayerDefault = "default"
	LayerProfile = "profile"
	LayerUnit    = "unit"
)

// Origin records which layer supplied each field of a resolved Agent.
type Origin struct {
	Vendor string
	Model  string
	Effort string
}

// Resolved is the fully merged configuration for the active profile.
type Resolved struct {
	ConfigPath string
	Profile    Profile
	Agents     map[Role]Agent
	Origins    map[Role]Origin
}

// knownGitHosts are the issue/PR backends jade can talk to. Only GitHub is
// implemented; an unknown host must fail at load rather than deep inside a
// build loop.
var knownGitHosts = []string{"github"}

// Roles returns every role jade dispatches, in pipeline order.
func Roles() []Role {
	return []Role{
		RoleDiscovery, RolePlanning, RoleOrchestrator,
		RoleBuilder, RoleCodeReview, RoleSecReview,
	}
}

// Load reads the local config file, selects the active profile, validates it,
// and merges role defaults with any profile-level overrides.
func Load() (*Resolved, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}

	cfg, err := ReadFile(path)
	if err != nil {
		return nil, err
	}

	profile, err := cfg.ActiveProfileOrError()
	if err != nil {
		return nil, err
	}

	if err := profile.Validate(); err != nil {
		return nil, fmt.Errorf("profile %q: %w", profile.Name, err)
	}

	agents, origins := MergeAgents(DefaultAgents(), profile.Agents, LayerProfile)

	return &Resolved{
		ConfigPath: path,
		Profile:    *profile,
		Agents:     agents,
		Origins:    origins,
	}, nil
}

// ActiveProfileOrError returns the profile named by active_profile.
func (c *Config) ActiveProfileOrError() (*Profile, error) {
	if c.ActiveProfile == "" {
		return nil, fmt.Errorf("no active_profile set")
	}
	for i := range c.Profiles {
		if c.Profiles[i].Name == c.ActiveProfile {
			return &c.Profiles[i], nil
		}
	}
	return nil, fmt.Errorf("no profile named %q — %s",
		c.ActiveProfile, describeProfiles(c.Profiles))
}

func describeProfiles(profiles []Profile) string {
	if len(profiles) == 0 {
		return "the config defines no profiles"
	}
	names := make([]string, 0, len(profiles))
	for _, p := range profiles {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return fmt.Sprintf("the config defines %v", names)
}

// Validate reports the first way this profile is unusable. Config errors should
// surface here, at load, not partway through dispatching agents.
func (p *Profile) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("name is required")
	}
	if !slices.Contains(knownGitHosts, p.GitHost) {
		return fmt.Errorf("git_host %q is not supported (known: %v)", p.GitHost, knownGitHosts)
	}
	if len(p.AllowedVendors) == 0 {
		return fmt.Errorf("allowed_vendors must list at least one vendor")
	}
	if p.DiscoverySink == "" {
		return fmt.Errorf(`discovery_sink is required ("repo", or a path to a notes directory)`)
	}
	if p.DiscoverySink != SinkRepo {
		info, err := os.Stat(p.DiscoverySink)
		if err != nil {
			return fmt.Errorf("discovery_sink %q is not readable: %w", p.DiscoverySink, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("discovery_sink %q is not a directory", p.DiscoverySink)
		}
	}

	// A role may only be pinned to a vendor this machine is allowed to use.
	for role, agent := range p.Agents {
		if agent.Vendor != "" && !slices.Contains(p.AllowedVendors, agent.Vendor) {
			return fmt.Errorf("role %q is pinned to vendor %q, which is not in allowed_vendors %v",
				role, agent.Vendor, p.AllowedVendors)
		}
	}
	return nil
}

// MergeAgents layers overrides onto base, field by field, recording where each
// surviving value came from. An empty override field leaves the base value
// intact — so a profile can pin only the effort of one role without restating
// its vendor and model.
func MergeAgents(base map[Role]Agent, override map[Role]Agent, layer string) (map[Role]Agent, map[Role]Origin) {
	agents := make(map[Role]Agent, len(base))
	origins := make(map[Role]Origin, len(base))

	for _, role := range Roles() {
		agent := base[role]
		origin := Origin{Vendor: LayerDefault, Model: LayerDefault, Effort: LayerDefault}

		if o, ok := override[role]; ok {
			if o.Vendor != "" {
				agent.Vendor, origin.Vendor = o.Vendor, layer
			}
			if o.Model != "" {
				agent.Model, origin.Model = o.Model, layer
			}
			if o.Effort != "" {
				agent.Effort, origin.Effort = o.Effort, layer
			}
		}

		agents[role] = agent
		origins[role] = origin
	}
	return agents, origins
}

// ArgsFor returns the native agent flags configured for a role on this machine.
func (r *Resolved) ArgsFor(role Role) []string {
	return r.Profile.AgentArgs[role]
}

// ApplyUnit layers a unit-of-work's per-role overrides onto an already-resolved
// config, returning a copy. Used by the build loop when an issue's YAML block
// pins a different model or effort for one unit.
func (r *Resolved) ApplyUnit(override map[Role]Agent) *Resolved {
	agents, origins := MergeAgents(r.Agents, override, LayerUnit)

	// Preserve the layer that supplied each value the unit did not override.
	for role, prev := range r.Origins {
		next := origins[role]
		if next.Vendor == LayerDefault {
			next.Vendor = prev.Vendor
		}
		if next.Model == LayerDefault {
			next.Model = prev.Model
		}
		if next.Effort == LayerDefault {
			next.Effort = prev.Effort
		}
		origins[role] = next
	}

	clone := *r
	clone.Agents = agents
	clone.Origins = origins
	return &clone
}

// RepoDiscoveryDir is where discovery notes go when a profile's sink is the
// repo rather than an external notes directory.
const RepoDiscoveryDir = "docs/discovery"

// ResolveSink returns the directory discovery notes should be written to, and
// creates it if needed.
//
// A work profile writes into the repo so nothing lands in a personal vault; a
// personal profile writes to the vault so homeless ideas still have a home. The
// note itself is identical either way.
func (r *Resolved) ResolveSink(repoRoot string) (string, error) {
	dir := r.Profile.DiscoverySink
	if dir == SinkRepo {
		if repoRoot == "" {
			return "", fmt.Errorf("profile %q writes discovery notes into the repo, but this is not a repository", r.Profile.Name)
		}
		dir = filepath.Join(repoRoot, RepoDiscoveryDir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating discovery sink %s: %w", dir, err)
	}
	return dir, nil
}

// SinkConventions returns any agent instructions governing the sink, so a
// discovery note written into someone's vault follows that vault's rules for
// frontmatter, naming, and linking rather than inventing its own.
func SinkConventions(dir string) string {
	for _, candidate := range []string{
		filepath.Join(dir, "CLAUDE.md"),
		filepath.Join(dir, "AGENTS.md"),
		filepath.Join(filepath.Dir(dir), "CLAUDE.md"),
		filepath.Join(filepath.Dir(dir), "AGENTS.md"),
	} {
		if data, err := os.ReadFile(candidate); err == nil {
			return fmt.Sprintf("From %s:\n\n%s", candidate, string(data))
		}
	}
	return ""
}
