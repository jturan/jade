// Package prompts serves jade's agent prompt templates. Templates are compiled
// into the binary so it stays self-contained, but any of them can be overridden
// by dropping a file of the same name in ~/.config/jade/prompts — prompt
// iteration must never require a rebuild.
package prompts

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jturan/jade/internal/config"
)

//go:embed all:templates
var builtin embed.FS

// Load returns the prompt template with the given name (e.g. "discovery"),
// preferring a user override on disk over the embedded copy.
func Load(name string) (string, error) {
	dir, err := config.Dir()
	if err == nil {
		override := filepath.Join(dir, "prompts", name+".md")
		if data, err := os.ReadFile(override); err == nil {
			return string(data), nil
		}
	}

	data, err := builtin.ReadFile(filepath.Join("templates", name+".md"))
	if err != nil {
		return "", fmt.Errorf("no prompt template named %q", name)
	}
	return string(data), nil
}
