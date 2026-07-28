package lease

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tobi404/barracks/internal/gitexclude"
)

// StoreGuard decides whether a path belongs to the barracks store. Revocation
// touches nothing that fails this check.
type StoreGuard interface {
	Contains(path string) bool
}

// Kept is a path revocation refused to remove, and why.
//
// These are surfaced to the user on every recall and reap. A path barracks did
// not create is never a silent no-op.
type Kept struct {
	Path   string
	Reason string
}

// Report is the outcome of revoking one lease.
type Report struct {
	Lease   *Lease
	Reason  string
	Removed []string
	Kept    []Kept
	Errors  []error
}

// Foreign reports whether anything was left in place.
func (r *Report) Foreign() bool { return len(r.Kept) > 0 }

// Revoke removes exactly what the lease recorded, and nothing else.
//
// The rule barracks must never break: a recorded path is removed only if it is
// still a symlink AND still points at the exact store directory the lease says
// it does. A real file, a directory, or a symlink aimed somewhere else is left
// untouched and reported. That is what makes a user's own
// .claude/skills/my-skill impossible to destroy.
func Revoke(l *Lease, guard StoreGuard, store *Store, reason string) *Report {
	rep := &Report{Lease: l, Reason: reason}

	for _, link := range l.Links {
		removed, kept := revokeLink(link, guard)
		if kept != nil {
			rep.Kept = append(rep.Kept, *kept)
			continue
		}
		if removed {
			rep.Removed = append(rep.Removed, link.Path)
		}
	}

	if err := gitexclude.Remove(l.Exclude, l.ID); err != nil {
		rep.Errors = append(rep.Errors, fmt.Errorf("clean git exclude: %w", err))
	}

	// Only prune directories barracks created, and only while they are empty.
	// A non-empty directory means the user (or another lease) put something
	// there, and it stays.
	for _, dir := range deepestFirst(l.CreatedDirs) {
		if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
			continue // non-empty or not ours to prune; that is a normal outcome
		}
	}

	if store != nil {
		if err := store.Delete(l.ID); err != nil {
			rep.Errors = append(rep.Errors, fmt.Errorf("delete lease record: %w", err))
		}
	}
	return rep
}

// LinkState is what a recorded link looks like on disk right now.
type LinkState int

const (
	// LinkOurs is still exactly the symlink barracks created: a symlink,
	// pointing at the recorded store directory, and that directory is inside
	// the store. This is the only state barracks may act on.
	LinkOurs LinkState = iota
	// LinkGone means nothing is there any more.
	LinkGone
	// LinkForeign means something else is there. It is never touched.
	LinkForeign
)

// InspectLink classifies a recorded link without changing anything.
//
// Every operation that removes or replaces a spawned path goes through here
// first, so the rule barracks must never break lives in exactly one function:
// a recorded path is acted on only if it is still a symlink AND still points at
// the exact store directory the lease says it does. A real file, a directory,
// or a symlink aimed somewhere else is left untouched and reported. That is
// what makes a user's own .claude/skills/my-skill impossible to destroy.
func InspectLink(link Link, guard StoreGuard) (LinkState, *Kept) {
	fi, err := os.Lstat(link.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return LinkGone, nil // nothing to do and nothing to report
		}
		return LinkForeign, &Kept{Path: link.Path, Reason: "cannot inspect: " + err.Error()}
	}

	if fi.Mode()&os.ModeSymlink == 0 {
		what := "a regular file"
		if fi.IsDir() {
			what = "a real directory"
		}
		return LinkForeign, &Kept{Path: link.Path, Reason: "not a barracks symlink any more - it is " + what}
	}

	dest, err := os.Readlink(link.Path)
	if err != nil {
		return LinkForeign, &Kept{Path: link.Path, Reason: "cannot read symlink: " + err.Error()}
	}
	if !filepath.IsAbs(dest) {
		dest = filepath.Join(filepath.Dir(link.Path), dest)
	}
	if filepath.Clean(dest) != filepath.Clean(link.Target) {
		return LinkForeign, &Kept{Path: link.Path, Reason: "symlink now points at " + dest}
	}
	if guard != nil && !guard.Contains(dest) {
		return LinkForeign, &Kept{Path: link.Path, Reason: "symlink resolves outside the barracks store"}
	}
	return LinkOurs, nil
}

// SymlinkPointsAt reports whether path is right now a symlink pointing at
// exactly target.
//
// It answers identity and nothing else, deliberately without a StoreGuard, and
// exists for callers deciding what to RECORD - reconciling a lease against the
// paths an upgrade actually produced, say. Anything deciding what to remove or
// replace must call InspectLink with the store guard instead: that extra check
// is what stops barracks acting on a symlink resolving outside its own store,
// and no mutation path may reach for the cheaper answer here.
func SymlinkPointsAt(path, target string) bool {
	state, _ := InspectLink(Link{Path: path, Target: target}, nil)
	return state == LinkOurs
}

func revokeLink(link Link, guard StoreGuard) (removed bool, kept *Kept) {
	state, k := InspectLink(link, guard)
	if state != LinkOurs {
		return false, k
	}
	if err := os.Remove(link.Path); err != nil {
		return false, &Kept{Path: link.Path, Reason: "remove failed: " + err.Error()}
	}
	return true, nil
}

// deepestFirst orders directories so children are pruned before parents.
func deepestFirst(dirs []string) []string {
	out := append([]string(nil), dirs...)
	sort.Slice(out, func(i, j int) bool {
		di := strings.Count(filepath.Clean(out[i]), string(filepath.Separator))
		dj := strings.Count(filepath.Clean(out[j]), string(filepath.Separator))
		if di == dj {
			return out[i] > out[j]
		}
		return di > dj
	})
	return out
}
