package lease

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobi404/barracks/internal/gitexclude"
	"github.com/tobi404/barracks/internal/testutil"
)

// fakeGuard treats everything under Root as belonging to the store.
type fakeGuard struct{ Root string }

func (g fakeGuard) Contains(p string) bool {
	rel, err := filepath.Rel(g.Root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// scene is a store directory plus a skills directory with links into it.
type scene struct {
	store  string
	skills string
	guard  fakeGuard
}

func newScene(t *testing.T) *scene {
	t.Helper()
	root := t.TempDir()
	s := &scene{
		store:  filepath.Join(root, "store"),
		skills: filepath.Join(root, "repo", ".claude", "skills"),
	}
	s.guard = fakeGuard{Root: s.store}
	if err := os.MkdirAll(s.skills, 0o755); err != nil {
		t.Fatal(err)
	}
	return s
}

// link creates a store skill and a symlink to it, returning the Link record.
func (s *scene) link(t *testing.T, name string) Link {
	t.Helper()
	target := filepath.Join(s.store, "src@abc", name)
	testutil.WriteFile(t, filepath.Join(target, "SKILL.md"), "# "+name)
	path := filepath.Join(s.skills, name)
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	return Link{Path: path, Target: target, Skill: name}
}

// TestRevokeRemovesOnlyItsOwnLinks is the core safety guarantee: revocation
// touches a path only when it is still a symlink pointing at the exact store
// directory the lease recorded.
func TestRevokeRemovesOnlyItsOwnLinks(t *testing.T) {
	tests := []struct {
		name string
		// tamper mutates the link path after it was created.
		tamper      func(t *testing.T, s *scene, l Link)
		wantRemoved bool
		wantReason  string
		// wantSurvives is the path that must still exist afterwards.
		wantSurvives bool
	}{
		{
			name:        "untouched barracks link is removed",
			tamper:      func(*testing.T, *scene, Link) {},
			wantRemoved: true,
		},
		{
			name: "user replaced the link with a real directory",
			tamper: func(t *testing.T, s *scene, l Link) {
				if err := os.Remove(l.Path); err != nil {
					t.Fatal(err)
				}
				testutil.WriteFile(t, filepath.Join(l.Path, "SKILL.md"), "mine")
			},
			wantReason:   "real directory",
			wantSurvives: true,
		},
		{
			name: "user replaced the link with a regular file",
			tamper: func(t *testing.T, s *scene, l Link) {
				if err := os.Remove(l.Path); err != nil {
					t.Fatal(err)
				}
				testutil.WriteFile(t, l.Path, "mine")
			},
			wantReason:   "regular file",
			wantSurvives: true,
		},
		{
			name: "link was re-pointed somewhere else",
			tamper: func(t *testing.T, s *scene, l Link) {
				elsewhere := filepath.Join(t.TempDir(), "elsewhere")
				testutil.WriteFile(t, filepath.Join(elsewhere, "SKILL.md"), "elsewhere")
				if err := os.Remove(l.Path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(elsewhere, l.Path); err != nil {
					t.Fatal(err)
				}
			},
			wantReason:   "points at",
			wantSurvives: true,
		},
		{
			name: "link already gone is silently fine",
			tamper: func(t *testing.T, s *scene, l Link) {
				if err := os.Remove(l.Path); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScene(t)
			link := s.link(t, "react")
			tt.tamper(t, s, link)

			l := &Lease{ID: "abc", Loadout: "frontend", Dir: s.skills, Links: []Link{link}}
			rep := Revoke(l, s.guard, nil, "test")

			gotRemoved := len(rep.Removed) == 1
			if gotRemoved != tt.wantRemoved {
				t.Errorf("removed = %v (%v), want %v", gotRemoved, rep.Removed, tt.wantRemoved)
			}
			if tt.wantReason != "" {
				if len(rep.Kept) != 1 {
					t.Fatalf("expected one kept path, got %+v", rep.Kept)
				}
				if !strings.Contains(rep.Kept[0].Reason, tt.wantReason) {
					t.Errorf("reason = %q, want it to mention %q", rep.Kept[0].Reason, tt.wantReason)
				}
				if !rep.Foreign() {
					t.Error("Foreign() = false, want true when a path was kept")
				}
			} else if len(rep.Kept) != 0 {
				t.Errorf("unexpected kept paths: %+v", rep.Kept)
			}
			if got := testutil.Exists(link.Path); got != tt.wantSurvives {
				t.Errorf("path exists = %v, want %v", got, tt.wantSurvives)
			}
		})
	}
}

// TestRevokeRefusesLinksOutsideTheStore covers a lease record whose target was
// tampered with to point outside the store. Even an exact-match symlink must
// fail the store containment check.
func TestRevokeRefusesLinksOutsideTheStore(t *testing.T) {
	s := newScene(t)
	outside := filepath.Join(t.TempDir(), "precious")
	testutil.WriteFile(t, filepath.Join(outside, "data.txt"), "important")

	path := filepath.Join(s.skills, "evil")
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	l := &Lease{ID: "abc", Links: []Link{{Path: path, Target: outside, Skill: "evil"}}}

	rep := Revoke(l, s.guard, nil, "test")
	if len(rep.Removed) != 0 {
		t.Fatalf("removed %v, want nothing outside the store to be touched", rep.Removed)
	}
	if len(rep.Kept) != 1 || !strings.Contains(rep.Kept[0].Reason, "outside the barracks store") {
		t.Fatalf("kept = %+v, want a store-containment refusal", rep.Kept)
	}
	if !testutil.Exists(filepath.Join(outside, "data.txt")) {
		t.Fatal("revocation deleted a path outside the store")
	}
}

// TestRevokeNeverPrunesNonEmptyDirectories makes sure a user's own skill in the
// same directory keeps that directory alive.
func TestRevokeNeverPrunesNonEmptyDirectories(t *testing.T) {
	s := newScene(t)
	link := s.link(t, "react")
	mine := filepath.Join(s.skills, "my-own-skill")
	testutil.WriteFile(t, filepath.Join(mine, "SKILL.md"), "mine")

	l := &Lease{
		ID:          "abc",
		Dir:         s.skills,
		Links:       []Link{link},
		CreatedDirs: []string{filepath.Dir(s.skills), s.skills},
	}
	rep := Revoke(l, s.guard, nil, "test")

	if len(rep.Removed) != 1 {
		t.Fatalf("removed = %v, want the one barracks link", rep.Removed)
	}
	if !testutil.Exists(mine) {
		t.Fatal("a user-created skill directory was destroyed")
	}
	if !testutil.Exists(s.skills) {
		t.Fatal("a non-empty directory was pruned")
	}
}

func TestRevokePrunesOnlyTheDirectoriesItCreated(t *testing.T) {
	s := newScene(t)
	link := s.link(t, "react")
	dotClaude := filepath.Dir(s.skills)

	l := &Lease{
		ID:          "abc",
		Dir:         s.skills,
		Links:       []Link{link},
		CreatedDirs: []string{dotClaude, s.skills},
	}
	Revoke(l, s.guard, nil, "test")

	if testutil.Exists(s.skills) {
		t.Error("empty skills directory should have been pruned")
	}
	if testutil.Exists(dotClaude) {
		t.Error("empty .claude directory should have been pruned")
	}
	// The repo directory was never recorded as created, so it stays.
	if !testutil.Exists(filepath.Dir(dotClaude)) {
		t.Error("pruning climbed past the directories barracks created")
	}
}

func TestRevokeRemovesExcludeBlockAndLeaseRecord(t *testing.T) {
	dir := t.TempDir()
	repo := testutil.NewGitRepo(t, filepath.Join(dir, "repo"))
	before := repo.ReadExclude(t)

	rec, err := gitexclude.Add(filepath.Join(repo.Dir, ".git"), "lease1", []string{"/.claude/skills/react"})
	if err != nil {
		t.Fatal(err)
	}

	store := NewStore(filepath.Join(dir, "leases"))
	l := &Lease{ID: "lease1", Loadout: "frontend", Dir: dir, Exclude: rec}
	if err := store.Save(l); err != nil {
		t.Fatal(err)
	}

	rep := Revoke(l, fakeGuard{Root: dir}, store, "test")
	if len(rep.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", rep.Errors)
	}
	if got := repo.ReadExclude(t); got != before {
		t.Errorf("exclude file not restored\n got: %q\nwant: %q", got, before)
	}
	if _, err := store.Get("lease1"); err == nil {
		t.Error("lease record should be gone after revocation")
	}
}

func TestDeepestFirst(t *testing.T) {
	got := deepestFirst([]string{"/a", "/a/b/c", "/a/b"})
	want := []string{"/a/b/c", "/a/b", "/a"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("deepestFirst = %v, want %v", got, want)
		}
	}
}
