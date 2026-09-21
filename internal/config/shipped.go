package config

import (
	"fmt"

	"github.com/jturan/jade/profiles"
	"gopkg.in/yaml.v3"
)

// DefaultProfileName is the profile a machine with no local config resolves to.
// A laptop that is anything else says so once, in `jade init`.
const DefaultProfileName = "personal"

// ShippedProfile returns the profile the repo ships under this name. The second
// return is false for a name nobody ships — a machine is free to invent its
// own profile, it just starts from nothing.
func ShippedProfile(name string) (Profile, bool, error) {
	data, ok := profiles.Read(name)
	if !ok {
		return Profile{}, false, nil
	}
	var p Profile
	if err := yaml.Unmarshal(data, &p); err != nil {
		return Profile{}, true, fmt.Errorf("parsing shipped profile %q: %w", name, err)
	}
	// The filename is the authority, so a mis-set `name:` cannot make the
	// shipped profile disagree with the one that was asked for.
	p.Name = name
	return p, true, nil
}

// ShippedNames lists the profiles the repo ships.
func ShippedNames() []string {
	return profiles.Names()
}
