package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/tobi404/barracks/internal/gitexclude"
	"github.com/tobi404/barracks/internal/lease"
)

// Apply performs exactly what Plan described, recording what happened back into
// the plan so the caller renders one report whether or not it ran for real.
//
// Every filesystem decision Plan made is re-checked here against the live path
// before anything is removed or replaced. Plan and Apply are separated by a
// window in which the user may have changed something, and a stale plan must
// never become a licence to delete.
func (e *Engine) Apply(plans []*LoadoutPlan) {
	for _, p := range plans {
		if p.definitionChanged {
			if err := e.Loadouts.Save(p.Next); err != nil {
				// Relinking spawns to commits the definition does not record
				// would leave the two out of step, so stop at the loadout.
				p.Errs = append(p.Errs, fmt.Errorf("save loadout %s: %w", p.Name, err))
				continue
			}
		}
		for i := range p.Spawns {
			e.applySpawn(&p.Spawns[i])
		}
	}
}

func (e *Engine) applySpawn(sp *SpawnPlan) {
	if sp.Skip != "" || !sp.Changed() {
		return
	}
	l := sp.Lease

	// Register the new paths before creating them. An exclude pattern for a
	// path that does not exist yet is harmless; a created link with no pattern
	// would show up in `git status`, which is the one thing spawning promises
	// never to do.
	if !sp.Recall && sp.gitDir != "" && !sameStrings(sp.patterns, excludePatterns(l)) {
		if err := gitexclude.Remove(l.Exclude, l.ID); err != nil {
			sp.Errs = append(sp.Errs, fmt.Errorf("clean git exclude: %w", err))
		}
		rec, err := gitexclude.Add(sp.gitDir, l.ID, sp.patterns)
		if err != nil {
			sp.Errs = append(sp.Errs, fmt.Errorf("update git exclude: %w", err))
		} else {
			l.Exclude = rec
		}
	}

	for i := range sp.Ops {
		kept, err := applyOp(&sp.Ops[i], e.Store)
		if kept != nil {
			sp.Kept = append(sp.Kept, *kept)
		}
		if err != nil {
			sp.Ops[i].Err = err
			sp.Errs = append(sp.Errs, err)
		}
	}

	if sp.Recall {
		// Every skill this spawn carried is gone upstream. Revoke rather than
		// leave an empty lease and an empty directory behind; it removes the
		// exclude block and prunes only directories the lease recorded creating.
		rep := lease.Revoke(l, e.Store, e.Leases, "no skills left after upgrade")
		sp.Kept = append(sp.Kept, rep.Kept...)
		sp.Errs = append(sp.Errs, rep.Errors...)
		return
	}

	l.Links = reconcileLinks(sp.links, sp.Lease.Links)
	if err := e.Leases.Save(l); err != nil {
		sp.Errs = append(sp.Errs, fmt.Errorf("write lease %s: %w", l.ID, err))
	}
}

// applyOp performs one link change, re-checking the path first.
func applyOp(op *Op, guard lease.StoreGuard) (*lease.Kept, error) {
	switch op.Kind {
	case OpRemove, OpRelink:
		state, kept := lease.InspectLink(lease.Link{Path: op.Path, Target: op.From, Skill: op.Skill}, guard)
		if state == lease.LinkForeign {
			return kept, nil // not ours; reported, never touched
		}
		if state == lease.LinkOurs {
			if err := os.Remove(op.Path); err != nil {
				return nil, fmt.Errorf("unlink %s: %w", op.Path, err)
			}
		}
		if op.Kind == OpRemove {
			return nil, nil
		}
		if err := os.Symlink(op.To, op.Path); err != nil {
			return nil, fmt.Errorf("link %s: %w", op.Path, err)
		}
		return nil, nil

	case OpAdd:
		if occupied, reason := pathOccupied(op.Path); occupied {
			return &lease.Kept{Path: op.Path, Reason: reason}, nil
		}
		if err := os.Symlink(op.To, op.Path); err != nil {
			return nil, fmt.Errorf("link %s: %w", op.Path, err)
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unknown operation %q", op.Kind)
}

// reconcileLinks records what the spawn actually looks like now, not what the
// plan hoped for. A link that failed to be created is not recorded, and one
// that failed to be removed stays recorded so a later recall still knows about
// it.
func reconcileLinks(planned, original []lease.Link) []lease.Link {
	byPath := map[string]lease.Link{}
	for _, l := range original {
		byPath[filepath.Clean(l.Path)] = l
	}

	var out []lease.Link
	claimed := map[string]bool{}
	for _, want := range planned {
		key := filepath.Clean(want.Path)
		switch {
		case pointsAt(want.Path, want.Target):
			out = append(out, want)
		case byPath[key].Path != "":
			// The change did not land; the previous record is still the truth,
			// whether the path is our old link or something foreign.
			out = append(out, byPath[key])
		default:
			continue // a link that was never created records nothing
		}
		claimed[key] = true
	}
	for _, prev := range original {
		key := filepath.Clean(prev.Path)
		if claimed[key] {
			continue
		}
		if _, err := os.Lstat(prev.Path); err == nil {
			out = append(out, prev) // removal did not happen; keep the record
			claimed[key] = true
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Skill < out[j].Skill })
	return out
}

func pointsAt(path, target string) bool {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		return false
	}
	dest, err := os.Readlink(path)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(dest) {
		dest = filepath.Join(filepath.Dir(path), dest)
	}
	return filepath.Clean(dest) == filepath.Clean(target)
}

// pathOccupied reports whether something already sits where barracks would
// create a link. barracks never overwrites.
func pathOccupied(path string) (bool, string) {
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, ""
		}
		return true, "cannot inspect: " + err.Error()
	}
	what := "a regular file"
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		what = "a symlink barracks did not record"
	case fi.IsDir():
		what = "a real directory"
	}
	return true, "already exists and was not created by barracks - it is " + what
}

func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
