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

	"github.com/tobi404/barracks/internal/gitcmd"
	"github.com/tobi404/barracks/internal/source"
)

// Store owns the store and mirror trees.
type Store struct {
	Root    string
	Mirrors string
	Git     gitcmd.Git
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
	return s.Git.ResolveRef(ctx, src.CloneURL, src.Ref)
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

	if err := s.Git.ExportTree(ctx, mirror, commit, tmp); err != nil {
		return "", false, fmt.Errorf("export %s@%s: %w", src.Ident(), short(commit), err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		if s.Has(src, commit) { // lost a race with a concurrent barracks; fine.
			return dir, false, nil
		}
		return "", false, fmt.Errorf("install %s: %w", src.Ident(), err)
	}
	return dir, true, nil
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
