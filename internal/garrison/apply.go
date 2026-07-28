package garrison

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// writeOp is one file the install will create, overwrite, or remove.
type writeOp struct {
	rel    string // repo-relative, slash-separated
	abs    string
	src    string // store path, empty for a removal
	mode   os.FileMode
	remove bool
}

// writePlan is every change an install will make to the working tree, decided
// before a single byte is written.
//
// Splitting the decision from the write is what lets the whole thing be refused
// on a locally modified file without having half-updated the tree first, and
// what lets a mid-write failure be undone exactly.
type writePlan struct {
	ops  []writeOp
	dirs []string // repo-relative directories that must be created
	// prune are directories of skills this install drops, removed once their
	// files are gone and only while they are empty.
	prune     []string
	wrote     []string
	deleted   []string
	unchanged []string
	notices   []string

	root    string
	journal *journal
	made    []string // absolute directories actually created, shallowest first
}

// planWrite decides the file-level changes, and refuses the ones that would
// destroy somebody's work.
//
// Two refusals, on purpose different from each other:
//
//   - A vendored file that has been edited since it was committed. The lockfile
//     digest proves barracks wrote it and that it has changed. Overwriting it
//     silently would discard a teammate's edit; skipping it silently would leave
//     the lockfile claiming content the file does not have. So the whole install
//     is refused, and --force is the user saying to take the new content anyway.
//   - A file barracks never wrote, sitting where a vendored file has to go.
//     That is refused outright and --force does not apply to it: --force means
//     "discard my own edits to barracks' files", never "delete a file barracks
//     has no record of".
func planWrite(req Request, prev *Garrison, next Garrison, places []placement) (*writePlan, error) {
	stores := map[string]string{}
	for _, p := range places {
		stores[p.name] = p.storeDir
	}

	type want struct {
		op  writeOp
		rec File
	}
	desired := map[string]want{}
	for _, s := range next.Skills {
		storeDir, ok := stores[s.Name]
		if !ok {
			return nil, fmt.Errorf("internal: no store directory resolved for skill %s", s.Name)
		}
		for _, f := range s.Files {
			rel := s.Dir + "/" + f.Path
			desired[rel] = want{
				op: writeOp{
					rel:  rel,
					abs:  filepath.Join(req.Root, filepath.FromSlash(rel)),
					src:  filepath.Join(storeDir, filepath.FromSlash(f.Path)),
					mode: f.mode(),
				},
				rec: f,
			}
		}
	}

	plan := &writePlan{root: req.Root}
	var modified, foreign []string

	recorded := recordedFiles(prev)
	for _, rel := range sortedKeys(desired) {
		w := desired[rel]
		was, mine := recorded[rel]
		// Which record a path is judged against is the whole of the decision, and
		// the two questions are different. "Has somebody edited this?" is asked
		// against what barracks last wrote there. "Does it need writing?" is
		// asked against what it should hold now. Comparing the file on disk with
		// the new content and calling the difference a local edit would refuse
		// every legitimate update.
		if !mine {
			was = w.rec
		}
		state, detail := inspect(w.op.abs, was)
		switch {
		case state == StateMissing:
			plan.ops = append(plan.ops, w.op)
			plan.wrote = append(plan.wrote, rel)
		case state == StateMatches && was.Digest == w.rec.Digest:
			plan.unchanged = append(plan.unchanged, rel)
		case state == StateMatches:
			plan.ops = append(plan.ops, w.op)
			plan.wrote = append(plan.wrote, rel)
		case mine && state == StateModified && req.Force:
			plan.ops = append(plan.ops, w.op)
			plan.wrote = append(plan.wrote, rel)
		case mine && state == StateModified:
			modified = append(modified, rel)
		default:
			foreign = append(foreign, rel+" ("+stateReason(state, detail)+")")
		}
	}

	// Everything the previous garrison put there that is no longer wanted: a
	// skill dropped upstream, or a target this install no longer covers.
	for _, rel := range sortedKeys(recorded) {
		if _, keep := desired[rel]; keep {
			continue
		}
		abs := filepath.Join(req.Root, filepath.FromSlash(rel))
		state, detail := inspect(abs, recorded[rel])
		switch {
		case state == StateMissing:
			// Already gone: nothing to do and nothing to report.
		case state == StateMatches:
			plan.ops = append(plan.ops, writeOp{rel: rel, abs: abs, remove: true})
			plan.deleted = append(plan.deleted, rel)
		case state == StateModified && req.Force:
			plan.ops = append(plan.ops, writeOp{rel: rel, abs: abs, remove: true})
			plan.deleted = append(plan.deleted, rel)
		case state == StateModified:
			modified = append(modified, rel)
		default:
			plan.notices = append(plan.notices, fmt.Sprintf("left in place (barracks did not write what is there now): %s - %s", rel, stateReason(state, detail)))
		}
	}

	if len(foreign) > 0 {
		return nil, fmt.Errorf("%w: %s\nbarracks will not write over a file it has no record of; move or delete it and run this again",
			ErrOccupied, strings.Join(foreign, ", "))
	}
	if len(modified) > 0 {
		return nil, fmt.Errorf("%w: %s\nthese files have been edited since they were committed. Restore them (git checkout -- <path>) to keep the edit out of the way, or pass --force to replace them with the recorded source content",
			ErrLocallyModified, strings.Join(dedupe(modified), ", "))
	}

	// Files sitting inside a vendored skill directory that the lockfile does not
	// account for. They are never touched - reported only, because an untracked
	// file inside a managed directory is exactly what `barracks inspect` exists
	// to make visible.
	known := make(map[string]bool, len(desired))
	for rel := range desired {
		known[rel] = true
	}
	plan.notices = append(plan.notices, strayNotices(req.Root, next, known)...)

	dirs, err := planDirs(req.Root, prev, next)
	if err != nil {
		return nil, err
	}
	plan.dirs = dirs
	// A skill dropped upstream leaves its directory behind once its files are
	// gone. Leaving an empty .claude/skills/css there would make the update's
	// diff say a skill was removed while the tree still showed it.
	plan.prune = droppedDirs(prev, next)
	return plan, nil
}

// droppedDirs is the skill directories the previous garrison had that this one
// does not.
func droppedDirs(prev *Garrison, next Garrison) []string {
	if prev == nil {
		return nil
	}
	keep := map[string]bool{}
	for _, s := range next.Skills {
		keep[s.Dir] = true
	}
	var out []string
	for _, s := range prev.Skills {
		if !keep[s.Dir] {
			out = append(out, s.Dir)
		}
	}
	return deepestFirst(dedupe(out))
}

// stateReason renders why a path was refused.
func stateReason(state State, detail string) string {
	switch state {
	case StateNotRegular:
		return detail
	case StateUnreadable:
		return "cannot inspect: " + detail
	default:
		return string(state)
	}
}

// recordedFiles indexes the previous garrison's files by repo-relative path.
func recordedFiles(prev *Garrison) map[string]File {
	out := map[string]File{}
	if prev == nil {
		return out
	}
	for _, s := range prev.Skills {
		for _, f := range s.Files {
			out[s.Dir+"/"+f.Path] = f
		}
	}
	return out
}

// strayNotices reports files inside a vendored skill directory that the new
// lockfile does not record.
func strayNotices(root string, next Garrison, known map[string]bool) []string {
	var out []string
	for _, s := range next.Skills {
		dir := filepath.Join(root, filepath.FromSlash(s.Dir))
		found, err := walkFiles(dir)
		if err != nil {
			continue
		}
		for _, rel := range found {
			full := s.Dir + "/" + rel
			if !known[full] {
				out = append(out, "not part of this garrison and left alone: "+full)
			}
		}
	}
	sort.Strings(out)
	return out
}

// walkFiles lists every entry under dir, slash-separated and dir-relative.
func walkFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// planDirs works out which directories the install has to create, and which
// previously recorded ones it must keep claiming.
//
// Inheriting another record's ancestors is the same rule internal/spawn follows:
// when two records install into one directory, only the first one creates it, so
// whichever is removed last has to be the one that can prune it. Pruning still
// only ever removes an empty directory some barracks record claims, so nothing
// the user made is at risk.
func planDirs(root string, prev *Garrison, next Garrison) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(rel string) {
		if rel == "" || rel == "." || seen[rel] {
			return
		}
		seen[rel] = true
		out = append(out, rel)
	}

	for _, s := range next.Skills {
		abs := filepath.Join(root, filepath.FromSlash(s.Dir))
		missing, err := missingDirs(abs)
		if err != nil {
			return nil, err
		}
		for _, d := range missing {
			rel, err := relDir(root, d)
			if err != nil {
				continue
			}
			add(rel)
		}
	}
	if prev != nil {
		for _, d := range prev.Dirs {
			if ancestorOfAny(d, next.Skills) {
				add(d)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) < len(out[j]) })
	return out, nil
}

// inheritDirs adds ancestor directories other barracks records already claim, so
// the last record removed can finish the cleanup.
func inheritDirs(dirs []string, others [][]string, skills []Skill) []string {
	seen := map[string]bool{}
	for _, d := range dirs {
		seen[d] = true
	}
	out := append([]string(nil), dirs...)
	for _, list := range others {
		for _, d := range list {
			if seen[d] || !ancestorOfAny(d, skills) {
				continue
			}
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) < len(out[j]) })
	return out
}

// ancestorOfAny reports whether rel is a skill directory or one of its parents.
func ancestorOfAny(rel string, skills []Skill) bool {
	for _, s := range skills {
		if s.Dir == rel || strings.HasPrefix(s.Dir, rel+"/") {
			return true
		}
	}
	return false
}

// apply performs the plan, keeping enough behind to undo it exactly.
func (p *writePlan) apply(root string) error {
	j, err := newJournal(root)
	if err != nil {
		return err
	}
	p.journal = j

	for _, rel := range p.dirs {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Stat(abs); err == nil {
			continue
		}
		if err := os.Mkdir(abs, 0o755); err != nil && !os.IsExist(err) {
			p.undo()
			return fmt.Errorf("create %s: %w", rel, err)
		}
		p.made = append(p.made, abs)
	}

	for _, op := range p.ops {
		if err := j.stash(op.abs); err != nil {
			p.undo()
			return err
		}
		if op.remove {
			continue // stashing already took it out of the tree
		}
		if err := copyFile(op.src, op.abs, op.mode); err != nil {
			p.undo()
			return fmt.Errorf("write %s: %w", op.rel, err)
		}
	}

	// Only while empty, as everywhere else: anything the user left in a dropped
	// skill's directory holds it open, and that is the right outcome.
	for _, rel := range p.prune {
		_ = os.Remove(filepath.Join(root, filepath.FromSlash(rel)))
	}
	return nil
}

// undo puts the tree back exactly as it was before apply ran.
func (p *writePlan) undo() {
	if p.journal != nil {
		p.journal.restore()
	}
	for i := len(p.made) - 1; i >= 0; i-- {
		_ = os.Remove(p.made[i]) // only ever succeeds while still empty
	}
	p.made = nil
}

// done discards the undo material once the lockfile is safely written.
func (p *writePlan) done() {
	if p.journal != nil {
		_ = p.journal.discard()
	}
}

// journal moves files aside instead of destroying them, so a failed install can
// be undone without keeping file contents in memory.
type journal struct {
	dir     string
	n       int
	entries []stashed
}

type stashed struct {
	abs string
	// backup is empty when nothing was there, meaning undo removes abs instead
	// of restoring it.
	backup string
}

func newJournal(root string) (*journal, error) {
	// Inside the repository on purpose: a rename has to stay on one filesystem,
	// and the temp directory is gone again before the command returns.
	dir, err := os.MkdirTemp(root, ".barracks-undo-*")
	if err != nil {
		return nil, err
	}
	return &journal{dir: dir}, nil
}

func (j *journal) stash(abs string) error {
	if _, err := os.Lstat(abs); err != nil {
		if os.IsNotExist(err) {
			j.entries = append(j.entries, stashed{abs: abs})
			return nil
		}
		return err
	}
	j.n++
	backup := filepath.Join(j.dir, fmt.Sprintf("%d-%s", j.n, filepath.Base(abs)))
	if err := os.Rename(abs, backup); err != nil {
		return fmt.Errorf("set aside %s: %w", abs, err)
	}
	j.entries = append(j.entries, stashed{abs: abs, backup: backup})
	return nil
}

func (j *journal) restore() {
	for i := len(j.entries) - 1; i >= 0; i-- {
		e := j.entries[i]
		if e.backup == "" {
			_ = os.Remove(e.abs)
			continue
		}
		_ = os.Remove(e.abs)
		// The directory may have been pruned after this file was moved out, so
		// it has to be there again before the file can go back.
		_ = os.MkdirAll(filepath.Dir(e.abs), 0o755)
		_ = os.Rename(e.backup, e.abs)
	}
	j.entries = nil
	_ = os.RemoveAll(j.dir)
}

func (j *journal) discard() error { return os.RemoveAll(j.dir) }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
