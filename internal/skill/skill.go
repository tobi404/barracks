// Package skill finds skills inside a fetched source tree.
//
// A skill is a directory containing a SKILL.md file. Nothing else qualifies,
// and a skill never contains another skill.
package skill

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Manifest is the file that marks a directory as a skill.
const Manifest = "SKILL.md"

// Skill is one discovered skill directory.
type Skill struct {
	// Name is the directory name, and the name it is spawned under.
	Name string
	// RelPath is the slash-separated path from the source root.
	RelPath string
	// AbsPath is the directory in the store.
	AbsPath string
}

// Discover scans root, or root/subpath when subpath is non-empty, for skills.
//
// Scanning does not descend into a directory that is itself a skill.
func Discover(root, subpath string) ([]Skill, error) {
	scanRoot := root
	if subpath != "" {
		scanRoot = filepath.Join(root, filepath.FromSlash(subpath))
	}
	fi, err := os.Stat(scanRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("subpath %q does not exist in this source", subpath)
		}
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("subpath %q is not a directory", subpath)
	}

	var found []Skill
	add := func(dir string) error {
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			return err
		}
		found = append(found, Skill{
			Name:    filepath.Base(dir),
			RelPath: filepath.ToSlash(rel),
			AbsPath: dir,
		})
		return nil
	}

	// The scan root may itself be a skill, e.g. gh:owner/repo#main:skills/react.
	if isSkillDir(scanRoot) {
		if err := add(scanRoot); err != nil {
			return nil, err
		}
		return found, nil
	}

	err = filepath.WalkDir(scanRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if p == scanRoot {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return fs.SkipDir
		}
		if isSkillDir(p) {
			if err := add(p); err != nil {
				return err
			}
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(found, func(i, j int) bool { return found[i].RelPath < found[j].RelPath })
	return found, nil
}

func isSkillDir(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, Manifest))
	return err == nil && fi.Mode().IsRegular()
}

// Selection is one set of --only/--except glob patterns.
//
// It exists so that a filter can be passed around as one value rather than as
// two slices that could be handed over in the wrong order. `equip` stores its
// selection on the loadout and `spawn` applies one to a single deployment, and
// both mean the same thing by the same string because both end up here.
type Selection struct {
	Only   []string
	Except []string
}

// Narrows reports whether this selection would leave anything out. An empty
// selection is not a filter at all, and callers rely on that being decidable
// without running it: it is the difference between "the whole loadout" and "a
// deliberately chosen part of it".
func (s Selection) Narrows() bool { return len(s.Only) > 0 || len(s.Except) > 0 }

// Apply is Filter with the patterns carried together.
func (s Selection) Apply(skills []Skill) ([]Skill, error) {
	return Filter(skills, s.Only, s.Except)
}

// Filter applies --only and --except glob patterns to a discovered set.
//
// Patterns match either the skill name or its source-relative path, so both
// "react" and "skills/react" select the same skill.
func Filter(skills []Skill, only, except []string) ([]Skill, error) {
	if err := validatePatterns(append(append([]string{}, only...), except...)); err != nil {
		return nil, err
	}
	literal := literalPatterns(skills, only, except)
	out := make([]Skill, 0, len(skills))
	for _, s := range skills {
		if len(only) > 0 && !matchAny(only, s, literal) {
			continue
		}
		if matchAny(except, s, literal) {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// literalPatterns is the patterns that spell one of these skills' name or path
// exactly, and are therefore not globs.
//
// A skill is a directory on somebody else's disk and may legitimately be called
// `report[1]`, which path.Match reads as a character class selecting `report1`
// and not the directory itself. Naming a skill exactly has to mean that skill:
// the roster's picker reports the names it drew, so a name it showed installing
// a *different* skill is the two surfaces meaning different things by one
// choice. This changes nothing for an ordinary pattern - a name with no
// metacharacter matches only itself as a glob too - so the rule bites exactly
// where it has to and nowhere else.
func literalPatterns(skills []Skill, sets ...[]string) map[string]bool {
	spelled := make(map[string]bool, 2*len(skills))
	for _, s := range skills {
		spelled[s.Name] = true
		spelled[s.RelPath] = true
	}
	out := map[string]bool{}
	for _, set := range sets {
		for _, p := range set {
			if spelled[p] {
				out[p] = true
			}
		}
	}
	return out
}

// matchAny reports whether any pattern selects this skill.
func matchAny(patterns []string, s Skill, literal map[string]bool) bool {
	for _, p := range patterns {
		if p == s.Name || p == s.RelPath {
			return true
		}
		if literal[p] {
			continue // it names a skill exactly, so it is not also a glob
		}
		if ok, _ := path.Match(p, s.Name); ok {
			return true
		}
		if ok, _ := path.Match(p, s.RelPath); ok {
			return true
		}
	}
	return false
}

func validatePatterns(patterns []string) error {
	for _, p := range patterns {
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("bad pattern %q: %w", p, err)
		}
	}
	return nil
}

// Names returns just the skill names, in order.
func Names(skills []Skill) []string {
	if len(skills) == 0 {
		return nil
	}
	out := make([]string, len(skills))
	for i, s := range skills {
		out[i] = s.Name
	}
	return out
}
