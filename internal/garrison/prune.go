package garrison

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// pruner takes back the directories a garrison's record accounts for, and
// gathers whatever it finds holding one open that no record does.
//
// Both removal paths use it - a whole garrison going, and one skill dropped out
// of an update - because the rule is the same in either case and getting it
// wrong the same way twice is exactly what a shared owner prevents.
type pruner struct {
	root  string
	known map[string]bool
	// elsewhere is what some *other* barracks record in this repository accounts
	// for. A removal walks directories it shares with them - .claude/skills most
	// of all - and "barracks has no record of putting it there" has to be true
	// when it is said: another garrison's committed skill is not the user's work,
	// and neither is a personal spawn's symlink. Both are left standing in
	// silence, and pruning stops at them rather than descending into somebody
	// else's record to report strays this operation was never about.
	elsewhere map[string]bool
	seen      map[string]bool
	found     []string
}

func newPruner(root string, known, elsewhere map[string]bool) *pruner {
	return &pruner{root: root, known: known, elsewhere: elsewhere, seen: map[string]bool{}}
}

// prune removes rel while it is empty, descending first into the directories
// inside it that some record accounts for.
//
// A record names files, so the directories barracks itself made *inside* a skill
// are only ever implied by them - a skill whose SKILL.md sits beside a ref/
// directory records .claude/skills/css/ref/notes.md and never .claude/skills/css/ref.
// Judging a child by an exact match would therefore call barracks' own directory
// somebody else's work, report it as such, and leave it standing; a child is
// known when any recorded path sits under it. Directories are pruned bottom-up
// so an empty chain goes entirely, and each is visited once so an ancestor
// pruned afterwards cannot report the same file a second time.
func (p *pruner) prune(rel string) {
	if p.seen[rel] {
		return
	}
	p.seen[rel] = true

	abs := filepath.Join(p.root, filepath.FromSlash(rel))
	entries, err := os.ReadDir(abs)
	if err != nil {
		return // absent, or not a directory any more; neither is ours to fix
	}
	for _, ent := range entries {
		child := rel + "/" + ent.Name()
		switch {
		case covers(p.known, child):
			if ent.IsDir() {
				p.prune(child)
			}
		case covers(p.elsewhere, child):
			// Another barracks record's: not ours to take, and not the user's to
			// be told about.
		default:
			p.found = append(p.found, child)
		}
	}
	if rest, err := os.ReadDir(abs); err == nil && len(rest) == 0 {
		_ = os.Remove(abs)
	}
}

// covers reports whether a set of recorded paths accounts for rel, either by
// naming it or by naming something inside it.
func covers(known map[string]bool, rel string) bool {
	if known[rel] {
		return true
	}
	prefix := rel + "/"
	for k := range known {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

// foreign is everything left behind that no record accounts for, in a stable
// order so one run reports it the way the next one does.
func (p *pruner) foreign() []string {
	out := append([]string(nil), p.found...)
	sort.Strings(out)
	return out
}
