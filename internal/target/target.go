// Package target declares where each agent keeps its skills.
//
// This map is the ONLY place in barracks that knows an agent-specific path.
// Command logic resolves a Target and asks it for directories; it never spells
// out ".claude/skills" or anything like it. Supporting a new agent is therefore
// a new entry in Registry, not a code change anywhere else.
package target

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Target describes one agent's skill layout.
type Target struct {
	// ID is the value users pass to --target.
	ID string
	// Display is the agent's human name, used in output.
	Display string
	// RepoDir is the skills directory relative to a repository root.
	RepoDir string
	// GlobalDir is the user-level skills directory. It supports "~" and
	// "${XDG_CONFIG_HOME}" (with a documented fallback) so the map stays data.
	GlobalDir string
	// GlobalFallback is used when GlobalDir references an unset XDG variable.
	GlobalFallback string
	// Unit names what a directory under the skills dir is, for output.
	Unit string
}

// Registry is the declarative target map.
//
// Claude Code is the target barracks is verified against end to end. OpenCode
// is declared alongside it so the abstraction is genuinely exercised rather
// than being a single-entry pretence.
var Registry = []Target{
	{
		ID:             "claude",
		Display:        "Claude Code",
		RepoDir:        filepath.Join(".claude", "skills"),
		GlobalDir:      filepath.Join("~", ".claude", "skills"),
		GlobalFallback: filepath.Join("~", ".claude", "skills"),
		Unit:           "skill",
	},
	{
		ID:             "opencode",
		Display:        "OpenCode",
		RepoDir:        filepath.Join(".opencode", "skill"),
		GlobalDir:      filepath.Join("${XDG_CONFIG_HOME}", "opencode", "skill"),
		GlobalFallback: filepath.Join("~", ".config", "opencode", "skill"),
		Unit:           "skill",
	},
}

// DefaultID is the target used when --target is not given.
const DefaultID = "claude"

// Lookup finds a target by ID. An empty id yields the default target.
func Lookup(id string) (Target, error) {
	if strings.TrimSpace(id) == "" {
		id = DefaultID
	}
	for _, t := range Registry {
		if t.ID == id {
			return t, nil
		}
	}
	return Target{}, fmt.Errorf("unknown target %q (known targets: %s)", id, strings.Join(IDs(), ", "))
}

// Default returns the default target.
func Default() Target {
	t, err := Lookup(DefaultID)
	if err != nil {
		panic("target: default target missing from registry")
	}
	return t
}

// IDs lists every declared target ID, sorted.
func IDs() []string {
	out := make([]string, 0, len(Registry))
	for _, t := range Registry {
		out = append(out, t.ID)
	}
	sort.Strings(out)
	return out
}

// RepoPath is the skills directory inside the repository rooted at root.
func (t Target) RepoPath(root string) string {
	return filepath.Join(root, t.RepoDir)
}

// GlobalPath is the user-level skills directory, with "~" and XDG expanded.
func (t Target) GlobalPath(env func(string) string, home func() (string, error)) (string, error) {
	if env == nil {
		env = os.Getenv
	}
	if home == nil {
		home = os.UserHomeDir
	}
	spec := t.GlobalDir
	if strings.Contains(spec, "${XDG_CONFIG_HOME}") {
		if v := strings.TrimSpace(env("XDG_CONFIG_HOME")); v != "" && filepath.IsAbs(v) {
			spec = strings.ReplaceAll(spec, "${XDG_CONFIG_HOME}", v)
		} else {
			spec = t.GlobalFallback
		}
	}
	if spec == "~" || strings.HasPrefix(spec, "~"+string(filepath.Separator)) {
		hd, err := home()
		if err != nil {
			return "", fmt.Errorf("expand %q: %w", t.GlobalDir, err)
		}
		spec = filepath.Join(hd, strings.TrimPrefix(strings.TrimPrefix(spec, "~"), string(filepath.Separator)))
	}
	if !filepath.IsAbs(spec) {
		return "", fmt.Errorf("target %q has a non-absolute global directory %q", t.ID, spec)
	}
	return filepath.Clean(spec), nil
}
