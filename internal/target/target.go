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
//
// Every field is data. Detection, aliases, and the primary source each path was
// taken from all live here, so a new agent never requires new code.
type Target struct {
	// ID is the value users pass to --target.
	ID string
	// Aliases are other names that resolve to this target, for agents that
	// share a directory convention under a different product name.
	Aliases []string
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
	// Markers are repository-relative paths whose presence means this agent is
	// already configured here. They drive detection for a loadout that declares
	// no targets of its own.
	Markers []string
	// Binaries are the program names of this agent's own CLI. `barracks run`
	// matches the command it is about to launch against them, so a run can
	// equip the agent it names. It is optional: an entry declaring none simply
	// never matches a command, which is correct for an agent with no CLI of its
	// own, or one whose CLI name is not recorded in Docs.
	Binaries []string
	// Docs is the primary source these paths were read from. It is printed by
	// `barracks targets` so a stale entry can be checked without guessing.
	Docs string
}

// Registry is the declarative target map.
//
// Every entry consumes the same artifact barracks produces: a directory holding
// a SKILL.md. That is not an assumption - each path below was read from the
// agent's own current documentation, recorded in Docs.
var Registry = []Target{
	{
		ID:             "claude",
		Display:        "Claude Code",
		RepoDir:        filepath.Join(".claude", "skills"),
		GlobalDir:      filepath.Join("~", ".claude", "skills"),
		GlobalFallback: filepath.Join("~", ".claude", "skills"),
		Unit:           "skill",
		Markers:        []string{".claude"},
		Binaries:       []string{"claude"},
		Docs:           "https://code.claude.com/docs/en/skills",
	},
	{
		// The cross-agent convention. Codex reads .agents/skills from the
		// working directory up to the repository root and $HOME/.agents/skills
		// for the user; opencode and Cursor read the same two locations. One
		// spawn here reaches all of them, which is why it carries the codex
		// alias rather than Codex having an entry of its own.
		ID:             "agents",
		Aliases:        []string{"codex"},
		Display:        "AGENTS.md agents (Codex, opencode, Cursor)",
		RepoDir:        filepath.Join(".agents", "skills"),
		GlobalDir:      filepath.Join("~", ".agents", "skills"),
		GlobalFallback: filepath.Join("~", ".agents", "skills"),
		Unit:           "skill",
		Markers:        []string{".agents"},
		Binaries:       []string{"codex"},
		Docs:           "https://learn.chatgpt.com/docs/build-skills",
	},
	{
		ID:             "cursor",
		Display:        "Cursor",
		RepoDir:        filepath.Join(".cursor", "skills"),
		GlobalDir:      filepath.Join("~", ".cursor", "skills"),
		GlobalFallback: filepath.Join("~", ".cursor", "skills"),
		Unit:           "skill",
		Markers:        []string{".cursor"},
		Binaries:       []string{"cursor-agent"},
		Docs:           "https://cursor.com/docs/context/skills",
	},
	{
		ID:             "opencode",
		Display:        "OpenCode",
		RepoDir:        filepath.Join(".opencode", "skills"),
		GlobalDir:      filepath.Join("${XDG_CONFIG_HOME}", "opencode", "skills"),
		GlobalFallback: filepath.Join("~", ".config", "opencode", "skills"),
		Unit:           "skill",
		Markers:        []string{".opencode"},
		Binaries:       []string{"opencode"},
		Docs:           "https://opencode.ai/docs/skills",
	},
	{
		// No Binaries: the doc below documents where Cascade reads skills, not
		// a terminal CLI to match a `barracks run` command against. Filling one
		// in from memory is exactly what the Docs field exists to prevent.
		ID:             "windsurf",
		Display:        "Windsurf",
		RepoDir:        filepath.Join(".windsurf", "skills"),
		GlobalDir:      filepath.Join("~", ".codeium", "windsurf", "skills"),
		GlobalFallback: filepath.Join("~", ".codeium", "windsurf", "skills"),
		Unit:           "skill",
		Markers:        []string{".windsurf"},
		Docs:           "https://docs.devin.ai/desktop/cascade/skills",
	},
}

// DefaultID is the target used when nothing else says otherwise: no --target,
// no loadout declaration, and nothing detected in the repository.
const DefaultID = "claude"

// Origin records why a set of targets was chosen, so a spawn can say so.
type Origin string

const (
	// OriginFlag means the invocation named the targets explicitly.
	OriginFlag Origin = "flag"
	// OriginLoadout means the loadout declared them.
	OriginLoadout Origin = "loadout"
	// OriginDetected means the repository already contains those agents.
	OriginDetected Origin = "detected"
	// OriginLaunched means the command being launched is an agent barracks
	// knows, so that agent joined the selection. It is deliberately distinct
	// from OriginDetected: "because you are starting Claude Code" and "because
	// this repository has a .claude directory" are different answers.
	OriginLaunched Origin = "launched"
	// OriginDefault means nothing said anything and the default was used.
	OriginDefault Origin = "default"
)

// Selection is a resolved set of targets and the reason it was chosen.
type Selection struct {
	Targets []Target
	Origin  Origin
}

// IDs of the selection, in order.
func (s Selection) IDs() []string {
	out := make([]string, 0, len(s.Targets))
	for _, t := range s.Targets {
		out = append(out, t.ID)
	}
	return out
}

// Reason renders the origin as a short phrase for command output.
func (s Selection) Reason() string {
	switch s.Origin {
	case OriginFlag:
		return "given on the command line"
	case OriginLoadout:
		return "declared by the loadout"
	case OriginDetected:
		return "detected in this repository"
	case OriginLaunched:
		return "for the agent this command launches"
	default:
		return "the default target"
	}
}

// Lookup finds a target by ID or alias. An empty id yields the default target.
func Lookup(id string) (Target, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = DefaultID
	}
	for _, t := range Registry {
		if t.ID == id {
			return t, nil
		}
		for _, alias := range t.Aliases {
			if alias == id {
				return t, nil
			}
		}
	}
	return Target{}, fmt.Errorf("unknown target %q (known targets: %s)", id, strings.Join(IDs(), ", "))
}

// LookupAll resolves a list of IDs, keeping the given order and dropping
// repeats. Two spellings of one target - an ID and its alias - collapse to a
// single entry, because spawning the same directory twice would collide.
func LookupAll(ids []string) ([]Target, error) {
	var out []Target
	seen := map[string]bool{}
	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		t, err := Lookup(id)
		if err != nil {
			return nil, err
		}
		if seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		out = append(out, t)
	}
	return out, nil
}

// ForCommand returns the target whose own CLI is the given program, matched on
// the program's base name so "/usr/local/bin/claude" and "./bin/claude" resolve
// the same way "claude" does.
//
// An unrecognised program matches nothing at all. A wrapper script, a shell
// alias, or `sh -c ...` must fall back to the ordinary rules rather than be
// guessed at. Which names count is map data, not code: it is the Binaries field
// of a registry entry, so a new agent's CLI is a new entry like everything else.
func ForCommand(program string) []Target {
	base := filepath.Base(strings.TrimSpace(program))
	if base == "." || base == string(filepath.Separator) {
		return nil
	}
	for _, t := range Registry {
		for _, b := range t.Binaries {
			if b == base {
				return []Target{t}
			}
		}
	}
	return nil
}

// Default returns the default target.
func Default() Target {
	t, err := Lookup(DefaultID)
	if err != nil {
		panic("target: default target missing from registry")
	}
	return t
}

// IDs lists every declared target ID, sorted. Aliases are not listed; they are
// spellings of an ID, not targets of their own.
func IDs() []string {
	out := make([]string, 0, len(Registry))
	for _, t := range Registry {
		out = append(out, t.ID)
	}
	sort.Strings(out)
	return out
}

// Detect returns the targets whose markers are present under root, registry
// order preserved.
//
// This is how a loadout that declares nothing avoids guessing: a repository
// with a .cursor directory is a repository where Cursor is in use.
func Detect(root string) []Target {
	var out []Target
	for _, t := range Registry {
		for _, m := range t.Markers {
			if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(m))); err == nil {
				out = append(out, t)
				break
			}
		}
	}
	return out
}

// DetectGlobal returns the targets whose user-level skills directory already
// has a parent on disk - the agent's own config directory.
//
// It needs no extra map data: the global location is already declared, and its
// parent is the directory the agent creates when it is installed.
func DetectGlobal(env func(string) string, home func() (string, error)) []Target {
	var out []Target
	for _, t := range Registry {
		dir, err := t.GlobalPath(env, home)
		if err != nil {
			continue
		}
		if _, err := os.Lstat(filepath.Dir(dir)); err == nil {
			out = append(out, t)
		}
	}
	return out
}

// Select decides which targets a spawn goes into.
//
// Precedence, highest first: the flags given to this invocation, the loadout's
// own declaration, what `barracks run` is about to launch together with what is
// already present on disk, and finally the default target. An override never
// touches the loadout's declaration - it is an argument to one spawn, not an
// edit.
//
// launched is the agent a `barracks run` command is about to start, empty for
// every other command. It joins the branch that would otherwise only look at
// the repository, and never widens an explicit choice: `run` exists to equip a
// specific agent session, so if the user names the agent and the skills land
// somewhere it does not read, the command has silently done the opposite of
// what was asked. That is knowledge `run` has and `spawn` does not, and using
// it is the point of the command rather than an inconsistency with `spawn`.
func Select(override, declared []string, detected, launched []Target) (Selection, error) {
	if len(override) > 0 {
		ts, err := LookupAll(override)
		if err != nil {
			return Selection{}, err
		}
		if len(ts) > 0 {
			return Selection{Targets: ts, Origin: OriginFlag}, nil
		}
	}
	if len(declared) > 0 {
		ts, err := LookupAll(declared)
		if err != nil {
			return Selection{}, fmt.Errorf("loadout declares a target barracks does not know: %w", err)
		}
		if len(ts) > 0 {
			return Selection{Targets: ts, Origin: OriginLoadout}, nil
		}
	}
	if len(launched) > 0 {
		return Selection{Targets: merge(launched, detected), Origin: OriginLaunched}, nil
	}
	if len(detected) > 0 {
		return Selection{Targets: detected, Origin: OriginDetected}, nil
	}
	return Selection{Targets: []Target{Default()}, Origin: OriginDefault}, nil
}

// merge concatenates target lists, keeping the first occurrence of each target.
func merge(lists ...[]Target) []Target {
	seen := map[string]bool{}
	var out []Target
	for _, list := range lists {
		for _, t := range list {
			if seen[t.ID] {
				continue
			}
			seen[t.ID] = true
			out = append(out, t)
		}
	}
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
