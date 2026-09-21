// Package profiles serves jade's shipped profile defaults. The YAML lives
// beside this file so a second or third laptop inherits the same environment
// from the repo rather than from someone's memory of the init form.
//
// This package deliberately knows nothing about the config types: it hands back
// bytes, and internal/config parses them. That keeps the dependency pointing
// one way.
package profiles

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed *.yml
var shipped embed.FS

// Read returns the shipped YAML for a profile name, or false if the repo ships
// no profile by that name. A machine is free to invent its own.
func Read(name string) ([]byte, bool) {
	data, err := shipped.ReadFile(name + ".yml")
	if err != nil {
		return nil, false
	}
	return data, true
}

// Names lists the profiles the repo ships, sorted.
func Names() []string {
	entries, err := fs.ReadDir(shipped, ".")
	if err != nil {
		// The FS is compiled in; an error here means the build is broken.
		panic(fmt.Sprintf("reading embedded profiles: %v", err))
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, strings.TrimSuffix(e.Name(), ".yml"))
	}
	sort.Strings(names)
	return names
}
