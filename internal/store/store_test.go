package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tobi404/barracks/internal/gitcmd"
	"github.com/tobi404/barracks/internal/source"
	"github.com/tobi404/barracks/internal/testutil"
)

func ctx() context.Context { return context.Background() }

// scene is a fixture repository plus a fresh store.
type scene struct {
	repo   *testutil.GitRepo
	store  *Store
	src    source.Source
	commit string
}

func newScene(t *testing.T, skills ...testutil.Skill) *scene {
	t.Helper()
	if len(skills) == 0 {
		skills = []testutil.Skill{{Path: "skills/react"}, {Path: "skills/css"}}
	}
	dir := t.TempDir()
	repo := testutil.NewSkillRepo(t, filepath.Join(dir, "src"), skills...)
	st := New(filepath.Join(dir, "store"), filepath.Join(dir, "mirrors"), gitcmd.Git{})

	src, err := source.Parse(repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return &scene{repo: repo, store: st, src: src, commit: repo.Head(t)}
}

func TestResolveAndEnsure(t *testing.T) {
	s := newScene(t)

	commit, err := s.store.Resolve(ctx(), s.src)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if commit != s.commit {
		t.Fatalf("Resolve = %q, want %q", commit, s.commit)
	}

	dir, fetched, err := s.store.Ensure(ctx(), s.src, commit)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !fetched {
		t.Error("the first Ensure should report that it fetched")
	}
	if _, err := os.Stat(filepath.Join(dir, "skills", "react", "SKILL.md")); err != nil {
		t.Fatalf("store entry is missing content: %v", err)
	}
	if !s.store.Has(s.src, commit) {
		t.Error("Has should be true after Ensure")
	}
}

// TestEnsureFetchesExactlyOnce is acceptance criterion 6: two loadouts sharing
// a source cost one fetch, not two.
func TestEnsureFetchesExactlyOnce(t *testing.T) {
	s := newScene(t)

	first, fetched, err := s.store.Ensure(ctx(), s.src, s.commit)
	if err != nil {
		t.Fatal(err)
	}
	if !fetched {
		t.Fatal("the first Ensure should fetch")
	}

	// A second loadout equipping the same source at the same commit.
	second, fetched, err := s.store.Ensure(ctx(), s.src, s.commit)
	if err != nil {
		t.Fatal(err)
	}
	if fetched {
		t.Error("the second Ensure fetched again; the store is meant to be shared")
	}
	if second != first {
		t.Errorf("second Ensure returned %q, want the same directory %q", second, first)
	}

	// A different source object pointing at the same repository and commit
	// must also land on the same store entry.
	other, err := source.Parse(s.repo.Dir + "#main:skills")
	if err != nil {
		t.Fatal(err)
	}
	third, fetched, err := s.store.Ensure(ctx(), other, s.commit)
	if err != nil {
		t.Fatal(err)
	}
	if fetched {
		t.Error("a subpath variant of the same source refetched it")
	}
	if third != first {
		t.Errorf("subpath variant landed at %q, want %q", third, first)
	}
}

func TestEnsureSeparatesCommits(t *testing.T) {
	s := newScene(t)
	first := s.commit

	s.repo.AddSkills(t, testutil.Skill{Path: "skills/new"})
	second := s.repo.Commit(t, "add new skill")

	dirA, _, err := s.store.Ensure(ctx(), s.src, first)
	if err != nil {
		t.Fatal(err)
	}
	dirB, fetched, err := s.store.Ensure(ctx(), s.src, second)
	if err != nil {
		t.Fatal(err)
	}
	if !fetched {
		t.Error("a new commit should be fetched")
	}
	if dirA == dirB {
		t.Fatal("two commits share one store directory")
	}
	if testutil.Exists(filepath.Join(dirA, "skills", "new")) {
		t.Error("the older commit's store entry contains a later commit's content")
	}
	if !testutil.Exists(filepath.Join(dirB, "skills", "new")) {
		t.Error("the newer commit's store entry is missing its content")
	}
}

func TestResolvePinnedSHAWorksOffline(t *testing.T) {
	dir := t.TempDir()
	st := New(filepath.Join(dir, "store"), filepath.Join(dir, "mirrors"), gitcmd.Git{})

	sha := "0123456789abcdef0123456789abcdef01234567"
	src, err := source.Parse("gh:owner/repo#" + sha)
	if err != nil {
		t.Fatal(err)
	}
	// No network is reachable here; a full SHA must resolve to itself.
	got, err := st.Resolve(ctx(), src)
	if err != nil {
		t.Fatalf("Resolve of a pinned SHA: %v", err)
	}
	if got != sha {
		t.Errorf("Resolve = %q, want %q", got, sha)
	}
}

func TestEnsureErrors(t *testing.T) {
	s := newScene(t)

	t.Run("no commit given", func(t *testing.T) {
		if _, _, err := s.store.Ensure(ctx(), s.src, ""); err == nil {
			t.Fatal("Ensure without a commit should fail")
		}
	})

	t.Run("unsafe source", func(t *testing.T) {
		bad := s.src
		bad.Repo = ".."
		if _, _, err := s.store.Ensure(ctx(), bad, s.commit); err == nil {
			t.Fatal("Ensure should reject a source that escapes the store")
		}
	})

	t.Run("commit not in the source", func(t *testing.T) {
		_, _, err := s.store.Ensure(ctx(), s.src, "0000000000000000000000000000000000000000")
		if err == nil {
			t.Fatal("Ensure of a commit the repository does not have should fail")
		}
	})

	t.Run("unreachable source", func(t *testing.T) {
		// A source barracks has never mirrored, pointing at nothing.
		gone, err := source.Parse(filepath.Join(t.TempDir(), "not-a-repo"))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.store.Ensure(ctx(), gone, s.commit); err == nil {
			t.Fatal("Ensure of an unreachable source should fail")
		}
	})
}

// TestEnsureLeavesNoPartialEntry checks that a failed export cannot leave a
// half-written store entry that later looks complete.
func TestEnsureLeavesNoPartialEntry(t *testing.T) {
	s := newScene(t)
	missing := "0000000000000000000000000000000000000000"

	_, _, err := s.store.Ensure(ctx(), s.src, missing)
	if err == nil {
		t.Fatal("expected the fetch to fail")
	}
	if s.store.Has(s.src, missing) {
		t.Fatal("a failed fetch left a store entry behind")
	}
	entries := testutil.Entries(t, filepath.Dir(s.store.Path(s.src, missing)))
	for _, e := range entries {
		if len(e) > 0 && e[0] == '.' {
			t.Errorf("a partial export directory %q was left behind", e)
		}
	}
}

func TestPath(t *testing.T) {
	dir := t.TempDir()
	st := New(filepath.Join(dir, "store"), filepath.Join(dir, "mirrors"), gitcmd.Git{})
	src, err := source.Parse("gh:owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	commit := "0123456789abcdef0123456789abcdef01234567"
	want := filepath.Join(st.Root, "github.com", "owner", "repo@"+commit)
	if got := st.Path(src, commit); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
	if st.Has(src, commit) {
		t.Error("Has should be false for a source that was never fetched")
	}
}

// TestLocate is what an upgrade reads a spawned link back with: which commit of
// a source a store path belongs to, taken from the path rather than from any
// label recorded beside it.
func TestLocate(t *testing.T) {
	root := t.TempDir()
	st := New(filepath.Join(root, "store"), filepath.Join(root, "mirrors"), gitcmd.Git{})
	src, err := source.Parse("gh:owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	other, err := source.Parse("gh:owner/elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	const commit = "0123456789abcdef0123456789abcdef01234567"
	entry := st.Path(src, commit)

	tests := []struct {
		name       string
		src        source.Source
		path       string
		wantCommit string
		wantRel    string
		wantOK     bool
	}{
		{"a skill inside the entry", src, filepath.Join(entry, "skills", "react"), commit, "skills/react", true},
		{"the entry itself", src, entry, commit, "", true},
		{"a different repository", other, filepath.Join(entry, "skills"), "", "", false},
		{"outside the store", src, filepath.Join(root, "elsewhere"), "", "", false},
		{"the store root", src, st.Root, "", "", false},
		{"a repository name that merely shares a prefix", src, filepath.Join(st.Root, "github.com", "owner", "repository@"+commit), "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCommit, gotRel, ok := st.Locate(tt.src, tt.path)
			if ok != tt.wantOK || gotCommit != tt.wantCommit || gotRel != tt.wantRel {
				t.Errorf("Locate(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.path, gotCommit, gotRel, ok, tt.wantCommit, tt.wantRel, tt.wantOK)
			}
		})
	}
}

// TestContains is the guard revocation relies on: nothing outside the store may
// ever be removed.
func TestContains(t *testing.T) {
	root := t.TempDir()
	st := New(filepath.Join(root, "store"), filepath.Join(root, "mirrors"), gitcmd.Git{})

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"inside the store", filepath.Join(st.Root, "github.com", "o", "r@abc", "skill"), true},
		{"the store root itself", st.Root, true},
		{"a sibling of the store", filepath.Join(root, "mirrors", "x"), false},
		{"somewhere else entirely", "/etc/passwd", false},
		{"traversal out of the store", filepath.Join(st.Root, "..", "..", "etc"), false},
		{"a path that merely shares a prefix", st.Root + "-evil", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := st.Contains(tt.path); got != tt.want {
				t.Errorf("Contains(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
