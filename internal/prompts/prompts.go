// Package prompts serves jade's agent prompt templates. Templates are compiled
// into the binary so it stays self-contained, but any of them can be overridden
// by dropping a file of the same name in ~/.config/jade/prompts — prompt
// iteration must never require a rebuild.
package prompts

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

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

// Render fills a prompt template. Templates use text/template, so a missing key
// is an error rather than a silently empty prompt — an agent given a blank
// context path will improvise, which is worse than failing.
func Render(tmpl string, data map[string]string) (string, error) {
	t, err := template.New("prompt").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parsing prompt template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("rendering prompt: %w", err)
	}
	return buf.String(), nil
}
