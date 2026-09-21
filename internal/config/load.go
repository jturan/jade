package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
)

// Layer names where a resolved value came from. Reported by `jade config show`
// so a machine behaving unexpectedly can be diagnosed without guesswork.
const (
	// LayerDefault is a value jade compiles in for every machine.
	LayerDefault = "default"
	// LayerShipped is a value from profiles/<name>.yml in this repo.
	LayerShipped = "shipped"
	// LayerProfile is a value from the local ~/.config/jade/config.yml.
	LayerProfile = "profile"
	// LayerUnit is a value from an issue's YAML block.
	LayerUnit = "unit"
)

// Profile field names, as they appear in YAML and in `jade config show`.
const (
	FieldGitHost          = "git_host"
	FieldDiscoverySink    = "discovery_sink"
	FieldAllowedVendors   = "allowed_vendors"
	FieldReviewStrictness = "review_strictness"
	FieldProtectedPaths   = "protected_paths"
	FieldAgentArgs        = "agent_args"
)

// ProfileFields lists the profile settings `jade config show` reports, in the
// order it prints them.
func ProfileFields() []string {
	return []string{
		FieldGitHost, FieldDiscoverySink, FieldAllowedVendors,
		FieldReviewStrictness, FieldProtectedPaths, FieldAgentArgs,
	}
}

// Origin records which layer supplied each field of a resolved Agent.
type Origin struct {
	Vendor string
	Model  string
	Effort string
}

// Resolved is the fully merged configuration for the active profile.
type Resolved struct {
	// ConfigPath is the local config file, whether or not it exists.
	ConfigPath string
	// HasLocal is false on a machine running entirely off shipped profiles.
	HasLocal bool
	Profile  Profile
	Agents   map[Role]Agent
	Origins  map[Role]Origin
	// Sources records which layer supplied each profile field, keyed by its
	// YAML name, so `jade config show` can say where a value came from.
	Sources map[string]string
}

// knownStrictness are the review_strictness values jade understands.
var knownStrictness = []string{StrictnessNormal, StrictnessStrict}

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

// Load resolves the configuration for this machine, in order: built-in role
// defaults, the profile this repo ships, then the local config file. A unit of
// work layers on top of the result via ApplyUnit.
//
// A machine with no local config is not an error. The shipped profiles are the
// point: a fresh laptop gets the same environment from the repo alone, and
// ~/.config/jade/config.yml only has to carry what the repo cannot know.
func Load() (*Resolved, error) {
	path, err := Path()
	if err != nil {
		return nil, err
	}

	var (
		local    Profile
		hasLocal bool
		name     = DefaultProfileName
	)

	cfg, err := ReadFile(path)
	switch {
	case err == nil:
		hasLocal = true
		p, err := cfg.ActiveProfileOrError()
		if err != nil {
			return nil, err
		}
		local, name = *p, p.Name
	case errors.Is(err, ErrNoConfig):
		// First run on this machine: the shipped profiles stand alone.
	default:
		return nil, err
	}

	shipped, _, err := ShippedProfile(name)
	if err != nil {
		return nil, err
	}

	profile, sources := MergeProfile(shipped, local)
	if err := profile.Validate(); err != nil {
		return nil, fmt.Errorf("profile %q: %w", profile.Name, err)
	}

	agents, origins := MergeAgents(DefaultAgents(), shipped.Agents, LayerShipped)
	agents, origins = layerAgents(agents, origins, local.Agents, LayerProfile)

	return &Resolved{
		ConfigPath: path,
		HasLocal:   hasLocal,
		Profile:    profile,
		Agents:     agents,
		Origins:    origins,
		Sources:    sources,
	}, nil
}

// MergeProfile layers a local profile over the shipped one, field by field, and
// records where each surviving value came from. A field the local profile
// leaves empty keeps the shipped value, so the local file stays a short list of
// what is genuinely specific to this machine.
func MergeProfile(shipped, local Profile) (Profile, map[string]string) {
	sources := make(map[string]string, len(ProfileFields()))

	pick := func(field, shippedVal, localVal string) string {
		if localVal != "" {
			sources[field] = LayerProfile
			return localVal
		}
		if shippedVal != "" {
			sources[field] = LayerShipped
			return shippedVal
		}
		sources[field] = LayerDefault
		return ""
	}
	pickList := func(field string, shippedVal, localVal []string) []string {
		if len(localVal) > 0 {
			sources[field] = LayerProfile
			return localVal
		}
		if len(shippedVal) > 0 {
			sources[field] = LayerShipped
			return shippedVal
		}
		sources[field] = LayerDefault
		return nil
	}

	p := Profile{Name: local.Name}
	if p.Name == "" {
		p.Name = shipped.Name
	}
	p.GitHost = pick(FieldGitHost, shipped.GitHost, local.GitHost)
	p.DiscoverySink = pick(FieldDiscoverySink, shipped.DiscoverySink, local.DiscoverySink)
	p.AllowedVendors = pickList(FieldAllowedVendors, shipped.AllowedVendors, local.AllowedVendors)
	p.ProtectedPaths = pickList(FieldProtectedPaths, shipped.ProtectedPaths, local.ProtectedPaths)

	p.ReviewStrictness = pick(FieldReviewStrictness, shipped.ReviewStrictness, local.ReviewStrictness)
	if p.ReviewStrictness == "" {
		// Normal everywhere unless a profile says otherwise.
		p.ReviewStrictness = StrictnessNormal
	}

	sources[FieldAgentArgs] = LayerDefault
	switch {
	case len(local.AgentArgs) > 0:
		p.AgentArgs, sources[FieldAgentArgs] = local.AgentArgs, LayerProfile
	case len(shipped.AgentArgs) > 0:
		p.AgentArgs, sources[FieldAgentArgs] = shipped.AgentArgs, LayerShipped
	}

	// Keep the merged role pins so Validate can check them against the
	// vendors this machine allows. Roles nobody pinned are dropped rather
	// than carried around as empty entries.
	merged, _ := MergeAgents(shipped.Agents, local.Agents, LayerProfile)
	for role, agent := range merged {
		if agent == (Agent{}) {
			continue
		}
		if p.Agents == nil {
			p.Agents = make(map[Role]Agent, len(merged))
		}
		p.Agents[role] = agent
	}

	return p, sources
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
	if !slices.Contains(knownStrictness, p.ReviewStrictness) && p.ReviewStrictness != "" {
		return fmt.Errorf("review_strictness %q is not supported (known: %v)", p.ReviewStrictness, knownStrictness)
	}
	if p.DiscoverySink != SinkRepo && p.DiscoverySink != SinkVault {
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

// Minus returns the part of p that is not already in base — what a local config
// actually has to write down. A local file that restated its shipped profile
// would make `jade config show` report every value as machine-specific, which
// is the one thing that table exists to tell you.
func (p Profile) Minus(base Profile) Profile {
	if p.GitHost == base.GitHost {
		p.GitHost = ""
	}
	if p.DiscoverySink == base.DiscoverySink {
		p.DiscoverySink = ""
	}
	if p.ReviewStrictness == base.ReviewStrictness {
		p.ReviewStrictness = ""
	}
	if slices.Equal(p.AllowedVendors, base.AllowedVendors) {
		p.AllowedVendors = nil
	}
	if slices.Equal(p.ProtectedPaths, base.ProtectedPaths) {
		p.ProtectedPaths = nil
	}
	return p
}

// layerAgents applies one more layer of overrides to an already-layered set,
// preserving the provenance of every field the new layer leaves alone.
func layerAgents(base map[Role]Agent, baseOrigins map[Role]Origin, override map[Role]Agent, layer string) (map[Role]Agent, map[Role]Origin) {
	agents, origins := MergeAgents(base, override, layer)
	for role, prev := range baseOrigins {
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
	return agents, origins
}

// Strict reports whether this machine refuses to merge its own work. It is a
// property of where the code is, not of the unit: an employer's review culture
// outranks a plan-time autonomy dial.
func (r *Resolved) Strict() bool {
	return r.Profile.ReviewStrictness == StrictnessStrict
}

// ArgsFor returns the native agent flags configured for a role on this machine.
func (r *Resolved) ArgsFor(role Role) []string {
	return r.Profile.AgentArgs[role]
}

// ApplyUnit layers a unit-of-work's per-role overrides onto an already-resolved
// config, returning a copy. Used by the build loop when an issue's YAML block
// pins a different model or effort for one unit.
func (r *Resolved) ApplyUnit(override map[Role]Agent) *Resolved {
	agents, origins := layerAgents(r.Agents, r.Origins, override, LayerUnit)

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
	if dir == SinkVault {
		// The shipped profile says notes belong in a notes vault, but the
		// repo is public and cannot hold anyone's vault path.
		return "", fmt.Errorf("profile %q writes discovery notes to a notes vault, but no path is set — run: jade init", r.Profile.Name)
	}
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
