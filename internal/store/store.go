// Package store implements the content-addressed source store.
//
// A source at a given commit is fetched at most once, into
// <store>/<host>/<owner>/<repo>@<commit>/, and shared by every loadout and
// every repo that references it.
package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tobi404/barracks/internal/gitcmd"
	"github.com/tobi404/barracks/internal/progress"
	"github.com/tobi404/barracks/internal/source"
)

// Store owns the store and mirror trees.
type Store struct {
	Root    string
	Mirrors string
	Git     gitcmd.Git

	// Progress is where the slow half of barracks announces itself. Every
	// command that can sit waiting - equip, upgrade, and the first spawn,
	// garrison, strip or run that has to populate the store rather than reuse it -
	// reaches the network through this package and nowhere else, which is why
	// one reporter here covers all of them. A nil reporter is silent.
	Progress *progress.Reporter

	// Workdir is where git configuration is read from when the display needs to
	// know whether a credential helper could reach the terminal, so a
	// repository's local scope is seen alongside the global and system ones.
	// Empty means the process's own working directory.
	Workdir string

	// The one credential-helper read a run makes. See
	// credentialHelpersStaySilent; unset means "not known to stay silent",
	// which is the safe answer for a read that never happened or failed.
	helpersOnce   sync.Once
	helpersSilent bool
}

// New builds a Store over the given roots.
func New(root, mirrors string, g gitcmd.Git) *Store {
	return &Store{Root: root, Mirrors: mirrors, Git: g}
}

// Path is where a source at commit lives, whether or not it has been fetched.
func (s *Store) Path(src source.Source, commit string) string {
	return filepath.Join(s.Root, src.StoreKey(commit))
}

// Has reports whether the source at commit is already materialised.
func (s *Store) Has(src source.Source, commit string) bool {
	fi, err := os.Stat(s.Path(src, commit))
	return err == nil && fi.IsDir()
}

// Resolve turns the source's ref into a concrete commit SHA. A source already
// pinned to a full SHA resolves offline.
func (s *Store) Resolve(ctx context.Context, src source.Source) (string, error) {
	if source.IsFullSHA(src.Ref) {
		return src.Ref, nil
	}
	step := s.step(ctx, src, "resolving")
	defer step.Fail() // no-op once Done has run; the guarantee is on the panic path
	commit, err := s.Git.ResolveRef(ctx, src.CloneURL, src.Ref)
	if err != nil {
		return "", err
	}
	step.Done("resolved " + short(commit))
	return commit, nil
}

// Ensure materialises the source at commit and returns its directory.
//
// fetched reports whether this call did the work; a second Ensure for the same
// source and commit returns fetched=false without touching the network. That is
// what makes two loadouts sharing a source cost exactly one fetch.
func (s *Store) Ensure(ctx context.Context, src source.Source, commit string) (dir string, fetched bool, err error) {
	if err := src.Validate(); err != nil {
		return "", false, err
	}
	if commit == "" {
		return "", false, fmt.Errorf("source %s: no commit to materialise", src.Ident())
	}
	dir = s.Path(src, commit)
	if s.Has(src, commit) {
		return dir, false, nil
	}

	// Everything past here is the slow half: a network fetch, then unpacking a
	// tree. The early return above is why a warm store stays silent - it never
	// gets as far as announcing anything.
	step := s.step(ctx, src, "fetching")
	defer step.Fail() // no-op once Done has run; restores the terminal on every other path

	mirror := filepath.Join(s.Mirrors, src.MirrorKey())
	if !s.Git.HasCommit(ctx, mirror, commit) {
		if err := s.Git.EnsureMirror(ctx, mirror, src.CloneURL); err != nil {
			return "", false, fmt.Errorf("fetch %s: %w", src.Ident(), err)
		}
	}
	if !s.Git.HasCommit(ctx, mirror, commit) {
		return "", false, fmt.Errorf("fetch %s: commit %s not found in %s", src.Ident(), short(commit), src.CloneURL)
	}

	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", false, err
	}
	// Export to a sibling temp dir and rename, so the final path only ever
	// exists complete. A half-written store entry would be indistinguishable
	// from a good one.
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".partial-*")
	if err != nil {
		return "", false, err
	}
	defer os.RemoveAll(tmp)

	step.Phase("unpacking")
	if err := s.Git.ExportTree(ctx, mirror, commit, tmp); err != nil {
		return "", false, fmt.Errorf("export %s@%s: %w", src.Ident(), short(commit), err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		if s.Has(src, commit) { // lost a race with a concurrent barracks; fine.
			step.Done("already in the store")
			return dir, false, nil
		}
		return "", false, fmt.Errorf("install %s: %w", src.Ident(), err)
	}
	step.Done("fetched " + short(commit))
	return dir, true, nil
}

// step announces work on src.
//
// The subject is the repository alone: one mirror serves every ref of it, so
// that is the thing actually being fetched, and it is short enough to leave the
// phase and the summary room on the line.
//
// Resolve and Ensure announce separately rather than sharing one line for the
// source. Merging them would mean holding a completed line back until something
// later decided nothing else was coming, and every caller would then owe the
// display a flush before printing its own report. Separate steps cost a second
// line only when resolving and fetching were each slow enough to be announced -
// and when that happens, "resolving took 30s of this" is worth saying.
func (s *Store) step(ctx context.Context, src source.Source, phase string) *progress.Step {
	work := progress.Work{Subject: src.RepoKey(), Phase: phase}
	// Only an animated display can paint over somebody, and answering this can
	// cost a git subprocess - so a run that is not animating anyway (redirected,
	// quiet, in CI) neither needs the answer nor pays for it.
	if s.Progress.Animates() {
		work.SharesTerminal = s.sharesTerminal(ctx, src.CloneURL)
	}
	return s.Progress.Step(work)
}

// Locate reads a store path back: given a source, it reports which commit of
// that source the path belongs to and where inside it the path sits.
//
// It is what lets an upgrade recognise a spawned link as "this source, but an
// older commit" without trusting the label recorded beside it. The label is a
// string that --pin rewrites; the path is a fact.
func (s *Store) Locate(src source.Source, path string) (commit, rel string, ok bool) {
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return "", "", false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", "", false
	}
	r, err := filepath.Rel(root, filepath.Clean(abs))
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	prefix := filepath.Join(src.Host, filepath.FromSlash(src.Owner), src.Repo) + "@"
	if !strings.HasPrefix(r, prefix) {
		return "", "", false
	}
	commit, rest, _ := strings.Cut(r[len(prefix):], string(filepath.Separator))
	if commit == "" {
		return "", "", false
	}
	return commit, filepath.ToSlash(rest), true
}

// Contains reports whether p resolves to something inside the store. Revoking a
// link is only ever allowed when this holds.
func (s *Store) Contains(p string) bool {
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func short(commit string) string {
	if len(commit) > 12 {
		return commit[:12]
	}
	return commit
}
