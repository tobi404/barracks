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

func revokeLink(link Link, guard StoreGuard) (removed bool, kept *Kept) {
	fi, err := os.Lstat(link.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil // already gone; nothing to do and nothing to report
		}
		return false, &Kept{Path: link.Path, Reason: "cannot inspect: " + err.Error()}
	}

	if fi.Mode()&os.ModeSymlink == 0 {
		what := "a regular file"
		if fi.IsDir() {
			what = "a real directory"
		}
		return false, &Kept{Path: link.Path, Reason: "not a barracks symlink any more - it is " + what}
	}

	dest, err := os.Readlink(link.Path)
	if err != nil {
		return false, &Kept{Path: link.Path, Reason: "cannot read symlink: " + err.Error()}
	}
	if !filepath.IsAbs(dest) {
		dest = filepath.Join(filepath.Dir(link.Path), dest)
	}
	if filepath.Clean(dest) != filepath.Clean(link.Target) {
		return false, &Kept{Path: link.Path, Reason: "symlink now points at " + dest}
	}
	if guard != nil && !guard.Contains(dest) {
		return false, &Kept{Path: link.Path, Reason: "symlink resolves outside the barracks store"}
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
