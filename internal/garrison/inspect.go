package garrison

import (
	"fmt"
	"path/filepath"
	"sort"
)

// Problem is one way a checkout can fail to match the lockfile.
type Problem string

const (
	// ProblemMissing means the lockfile records a file that is not there.
	ProblemMissing Problem = "missing"
	// ProblemModified means the file is there with different content.
	ProblemModified Problem = "modified"
	// ProblemUnrecorded means a file sits inside a vendored skill directory that
	// the lockfile does not account for.
	ProblemUnrecorded Problem = "not in the lockfile"
	// ProblemWrongKind means something that is not a regular file is where one
	// should be.
	ProblemWrongKind Problem = "wrong kind of file"
	// ProblemUnreadable means the path could not be inspected.
	ProblemUnreadable Problem = "unreadable"
)

// Finding is one mismatch between the lockfile and the working tree.
type Finding struct {
	// Path is repo-relative and slash-separated, so it reads like a git path.
	Path    string
	Problem Problem
	Detail  string
}

func (f Finding) String() string {
	if f.Detail == "" {
		return fmt.Sprintf("%s: %s", f.Path, f.Problem)
	}
	return fmt.Sprintf("%s: %s (%s)", f.Path, f.Problem, f.Detail)
}

// Check is one garrison verified against the working tree.
type Check struct {
	Garrison *Garrison
	Findings []Finding
	// Notes are things worth saying that are not mismatches - most importantly
	// that the loadout has moved on from what the lockfile pins.
	Notes []string
}

// OK reports whether this garrison's files are exactly what the lockfile says.
func (c *Check) OK() bool { return len(c.Findings) == 0 }

// Inspection is the whole repository verified.
type Inspection struct {
	Root string
	// Lock is the lockfile path, repo-relative.
	Lock   string
	Checks []*Check
	Notes  []string
}

// OK reports whether every garrison matches.
func (i *Inspection) OK() bool {
	for _, c := range i.Checks {
		if !c.OK() {
			return false
		}
	}
	return true
}

// Findings counts the mismatches across every garrison.
func (i *Inspection) Findings() int {
	n := 0
	for _, c := range i.Checks {
		n += len(c.Findings)
	}
	return n
}

// Inspect verifies the working tree against the lockfile.
//
// This is what makes the committed tier honest. Vendored files are ordinary
// files: nothing stops one being edited, half-merged, or dropped in a rebase, and
// without a recorded digest per file there would be no way to tell. A mismatch
// found here is reported, never repaired - repairing is `barracks garrison`, and
// keeping the two apart means a check can be run on a machine, or in CI, that
// must not change anything.
func (e *Engine) Inspect(root string) (*Inspection, error) {
	m, err := Load(root)
	if err != nil {
		return nil, err
	}
	insp := &Inspection{Root: root, Lock: LockName}
	if len(m.Garrisons) == 0 {
		return insp, nil
	}

	for i := range m.Garrisons {
		g := &m.Garrisons[i]
		c := &Check{Garrison: g}

		known := map[string]bool{}
		for _, s := range g.Skills {
			for _, f := range s.Files {
				rel := s.Dir + "/" + f.Path
				known[rel] = true
				state, detail := inspect(filepath.Join(root, filepath.FromSlash(rel)), f)
				switch state {
				case StateMatches:
				case StateMissing:
					c.Findings = append(c.Findings, Finding{Path: rel, Problem: ProblemMissing})
				case StateModified:
					c.Findings = append(c.Findings, Finding{Path: rel, Problem: ProblemModified})
				case StateNotRegular:
					c.Findings = append(c.Findings, Finding{Path: rel, Problem: ProblemWrongKind, Detail: detail})
				default:
					c.Findings = append(c.Findings, Finding{Path: rel, Problem: ProblemUnreadable, Detail: detail})
				}
			}
		}

		for _, dir := range dedupe(skillDirs(g)) {
			found, err := walkFiles(filepath.Join(root, filepath.FromSlash(dir)))
			if err != nil {
				continue // a missing directory is already reported per file
			}
			for _, rel := range found {
				full := dir + "/" + rel
				if !known[full] {
					c.Findings = append(c.Findings, Finding{Path: full, Problem: ProblemUnrecorded})
				}
			}
		}

		sort.Slice(c.Findings, func(a, b int) bool { return c.Findings[a].Path < c.Findings[b].Path })
		c.Notes = append(c.Notes, e.driftNotes(g)...)
		insp.Checks = append(insp.Checks, c)
	}
	return insp, nil
}

// driftNotes says when the loadout definition has moved on from the lockfile.
//
// This is not a mismatch: the lockfile and the files agree, and that is exactly
// what a teammate cloning the repository should get. It is the signal that the
// pins here are behind the loadout, which is what `barracks upgrade` and
// `barracks garrison` exist to reconcile - as a commit, reviewed like any other
// change.
func (e *Engine) driftNotes(g *Garrison) []string {
	if e.Loadouts == nil {
		return nil
	}
	l, err := e.Loadouts.Get(g.Loadout)
	if err != nil {
		return nil // not trained on this machine, which is the normal case
	}
	pinned := map[string]string{}
	for _, s := range g.Sources {
		pinned[s.Ident()] = s.Commit
	}
	var behind []string
	for _, eq := range l.Equipment {
		commit, known := pinned[eq.Ident()]
		switch {
		case !known:
			behind = append(behind, fmt.Sprintf("%s is equipped on the loadout but not in the lockfile", eq.Ident()))
		case commit != eq.Commit:
			behind = append(behind, fmt.Sprintf("%s: lockfile has %s, loadout is pinned at %s", eq.Ident(), shortCommit(commit), shortCommit(eq.Commit)))
		}
	}
	if len(behind) == 0 {
		return nil
	}
	sort.Strings(behind)
	return append(behind, fmt.Sprintf("run `barracks garrison %s` to bring the committed files onto the loadout's pins", g.Loadout))
}

func shortCommit(c string) string {
	if len(c) > 8 {
		return c[:8]
	}
	return c
}
