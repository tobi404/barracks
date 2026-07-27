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

// Filter applies --only and --except glob patterns to a discovered set.
//
// Patterns match either the skill name or its source-relative path, so both
// "react" and "skills/react" select the same skill.
func Filter(skills []Skill, only, except []string) ([]Skill, error) {
	if err := validatePatterns(append(append([]string{}, only...), except...)); err != nil {
		return nil, err
	}
	out := make([]Skill, 0, len(skills))
	for _, s := range skills {
		if len(only) > 0 && !matchAny(only, s) {
			continue
		}
		if matchAny(except, s) {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func matchAny(patterns []string, s Skill) bool {
	for _, p := range patterns {
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
