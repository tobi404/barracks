package garrison

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tobi404/barracks/internal/lease"
)

// Report is the outcome of removing one garrison.
//
// Kept is lease.Kept because the rule is the same one revocation obeys, and the
// CLI prints both through one function: a path barracks did not write, or no
// longer recognises as what it wrote, is left alone and always reported.
type Report struct {
	Loadout string
	Removed []string
	Kept    []lease.Kept
	Errors  []error
	// Lock reports whether the lockfile entry was dropped.
	Lock bool
}

// Foreign reports whether anything was left in place.
func (r *Report) Foreign() bool { return len(r.Kept) > 0 }

// Remove takes a garrison out of the repository.
//
// It removes exactly the files the lockfile records, and only those whose
// content is still the digest it recorded. A file a teammate has edited since it
// was committed is kept and reported - never deleted, and never quietly. That is
// the same rule internal/lease follows for a symlink someone re-pointed, and it
// is why the digests are in the lockfile at all.
//
// Unlike an update, removal does not offer to override this. There is nothing to
// keep coherent afterwards: an edited file that stays behind is simply a file the
// repository still has, and the lockfile no longer claims it.
func (e *Engine) Remove(root string, ref Ref) (*Report, error) {
	m, err := Load(root)
	if err != nil {
		return nil, err
	}
	g := m.FindFor(ref.ID, ref.Loadout)
	if g == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotGarrisoned, ref.Loadout)
	}
	rep := &Report{Loadout: g.Loadout}

	for _, s := range g.Skills {
		for _, f := range s.Files {
			rel := s.Dir + "/" + f.Path
			abs := filepath.Join(root, filepath.FromSlash(rel))
			state, detail := inspect(abs, f)
			switch state {
			case StateMissing:
				// Already gone: nothing to remove and nothing to report.
			case StateMatches:
				if err := os.Remove(abs); err != nil {
					rep.Kept = append(rep.Kept, lease.Kept{Path: rel, Reason: "remove failed: " + err.Error()})
					continue
				}
				rep.Removed = append(rep.Removed, rel)
			case StateModified:
				rep.Kept = append(rep.Kept, lease.Kept{Path: rel, Reason: "edited since it was committed - your change is kept"})
			default:
				rep.Kept = append(rep.Kept, lease.Kept{Path: rel, Reason: stateReason(state, detail)})
			}
		}
	}

	// The skill directories and the directories barracks had to create to reach
	// them, deepest first so children go before parents. Each is only ever
	// removed while empty, so a kept file - or anything the user put there -
	// holds its directory open.
	known := knownPaths(g)
	for _, rel := range deepestFirst(dedupe(append(skillDirs(g), g.Dirs...))) {
		e.pruneDir(root, rel, known, rep)
	}

	m.Drop(g.ID, g.Loadout)
	if err := m.Save(root); err != nil {
		rep.Errors = append(rep.Errors, fmt.Errorf("update %s: %w", LockName, err))
		return rep, nil
	}
	rep.Lock = true
	return rep, nil
}

// pruneDir removes a directory the garrison recorded, but only while empty.
//
// A directory left standing is not itself worth reporting - the kept file that
// holds it open already was. What is worth reporting is anything inside it that
// the lockfile has no record of at all: barracks is leaving somebody's file
// behind, and that must never be something the user has to discover for
// themselves.
func (e *Engine) pruneDir(root, rel string, known map[string]bool, rep *Report) {
	abs := filepath.Join(root, filepath.FromSlash(rel))
	entries, err := os.ReadDir(abs)
	if err != nil {
		return // absent, or not a directory any more; neither is ours to fix
	}
	if len(entries) == 0 {
		_ = os.Remove(abs)
		return
	}
	for _, ent := range entries {
		child := rel + "/" + ent.Name()
		if known[child] {
			continue
		}
		rep.Kept = append(rep.Kept, lease.Kept{Path: child, Reason: "barracks has no record of putting it there"})
	}
}

// knownPaths is every path this garrison recorded: its files, its skill
// directories, and the directories it had to create.
func knownPaths(g *Garrison) map[string]bool {
	out := map[string]bool{}
	for _, d := range g.Dirs {
		out[d] = true
	}
	for _, s := range g.Skills {
		out[s.Dir] = true
		for _, f := range s.Files {
			out[s.Dir+"/"+f.Path] = true
		}
	}
	return out
}

func skillDirs(g *Garrison) []string {
	out := make([]string, 0, len(g.Skills))
	for _, s := range g.Skills {
		out = append(out, s.Dir)
	}
	return out
}
