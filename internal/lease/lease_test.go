package lease

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobi404/barracks/internal/testutil"
)

func TestNewIDIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := NewID()
		if id == "" {
			t.Fatal("NewID returned an empty id")
		}
		if seen[id] {
			t.Fatalf("NewID repeated %q", id)
		}
		seen[id] = true
	}
}

func TestExpired(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	tests := []struct {
		name string
		l    Lease
		want bool
	}{
		{"manual never expires", Lease{Kind: KindManual}, false},
		{"process never expires by clock", Lease{Kind: KindProcess, ExpiresAt: &past}, false},
		{"deadline in the past", Lease{Kind: KindDeadline, ExpiresAt: &past}, true},
		{"deadline in the future", Lease{Kind: KindDeadline, ExpiresAt: &future}, false},
		{"deadline exactly now", Lease{Kind: KindDeadline, ExpiresAt: &now}, true},
		{"deadline with no timestamp", Lease{Kind: KindDeadline}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.l.Expired(now); got != tt.want {
				t.Errorf("Expired = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDescribe(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	in90m := now.Add(90 * time.Minute)
	past := now.Add(-time.Minute)

	tests := []struct {
		name string
		l    Lease
		want string
	}{
		{"manual", Lease{Kind: KindManual}, "until recalled"},
		{"deadline ahead", Lease{Kind: KindDeadline, ExpiresAt: &in90m}, "expires in 1h30m0s"},
		{"deadline passed", Lease{Kind: KindDeadline, ExpiresAt: &past}, "expired"},
		{"deadline unset", Lease{Kind: KindDeadline}, "until deadline"},
		{"process", Lease{Kind: KindProcess, Owner: &Owner{PID: 42, Command: "claude"}}, "while pid 42 (claude) runs"},
		{"process without a command", Lease{Kind: KindProcess, Owner: &Owner{PID: 42}}, "while pid 42 (process) runs"},
		{"process without an owner", Lease{Kind: KindProcess}, "tied to a process"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.l.Describe(now); got != tt.want {
				t.Errorf("Describe = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStoreRoundTrip(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "leases"))
	exp := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	want := &Lease{
		ID:        "abc123",
		Loadout:   "frontend",
		Target:    "claude",
		Scope:     ScopeRepo,
		Root:      "/repo",
		Dir:       "/repo/.claude/skills",
		Kind:      KindDeadline,
		CreatedAt: time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC),
		ExpiresAt: &exp,
		Owner:     &Owner{PID: 7, StartToken: "tok", Command: "claude"},
		Links:     []Link{{Path: "/repo/.claude/skills/react", Target: "/store/x/react", Skill: "react", Source: "github.com/o/r"}},
	}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got.Loadout != want.Loadout || got.Kind != want.Kind || got.Scope != want.Scope {
		t.Errorf("round trip lost fields: %+v", got)
	}
	if got.Owner == nil || got.Owner.StartToken != "tok" {
		t.Errorf("owner start token lost: %+v", got.Owner)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) {
		t.Errorf("expiry lost: %+v", got.ExpiresAt)
	}
	if len(got.Links) != 1 || got.Links[0].Skill != "react" {
		t.Errorf("links lost: %+v", got.Links)
	}
}

func TestStoreSaveRejectsMissingID(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Save(&Lease{}); err == nil {
		t.Fatal("saving a lease without an id should fail")
	}
}

func TestStoreGetMissing(t *testing.T) {
	s := NewStore(t.TempDir())
	_, err := s.Get("nope")
	if err == nil || !strings.Contains(err.Error(), "lease not found") {
		t.Fatalf("err = %v, want a not-found error", err)
	}
}

func TestStoreDeleteIsIdempotent(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Delete("never-existed"); err != nil {
		t.Fatalf("deleting a missing lease should be a no-op, got %v", err)
	}
}

func TestStoreListOrdersByCreation(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "leases"))
	base := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	for i, id := range []string{"third", "first", "second"} {
		offsets := map[string]int{"first": 0, "second": 1, "third": 2}
		_ = i
		if err := s.Save(&Lease{ID: id, CreatedAt: base.Add(time.Duration(offsets[id]) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	got, problems := s.List()
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("got %d leases, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("order = %v, want %v", ids(got), want)
		}
	}
}

func TestStoreListOnMissingDirectory(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "not-created-yet"))
	got, problems := s.List()
	if len(got) != 0 || len(problems) != 0 {
		t.Fatalf("List on a missing directory = %v, %v; want empty and no error", got, problems)
	}
}

func TestStoreListIgnoresNonYAML(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "leases")
	s := NewStore(dir)
	if err := s.Save(&Lease{ID: "real"}); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(dir, "notes.txt"), "ignore me")

	got, problems := s.List()
	if len(got) != 1 || len(problems) != 0 {
		t.Fatalf("List = %v, %v; want only the yaml record", ids(got), problems)
	}
}

func ids(ls []*Lease) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.ID
	}
	return out
}

func TestFindInScope(t *testing.T) {
	leases := []*Lease{
		{ID: "claude-here", Scope: ScopeRepo, Root: "/repo", Dir: "/repo/.claude/skills", Target: "claude"},
		{ID: "cursor-here", Scope: ScopeRepo, Root: "/repo/", Dir: "/repo/.cursor/skills", Target: "cursor"},
		{ID: "other-repo", Scope: ScopeRepo, Root: "/other", Dir: "/other/.claude/skills", Target: "claude"},
		{ID: "global", Scope: ScopeGlobal, Dir: "/home/me/.claude/skills", Target: "claude"},
	}

	// One repository, every agent in it: that is what makes a single recall
	// undo a spawn that reached two targets.
	got := FindInScope(leases, ScopeRepo, "/repo")
	if strings.Join(ids(got), ",") != "claude-here,cursor-here" {
		t.Errorf("FindInScope(repo) = %v, want both agents in that repo and nothing else", ids(got))
	}

	got = FindInScope(leases, ScopeGlobal, "")
	if strings.Join(ids(got), ",") != "global" {
		t.Errorf("FindInScope(global) = %v, want only the user-level spawn", ids(got))
	}
}

func TestWithTargets(t *testing.T) {
	leases := []*Lease{
		{ID: "a", Target: "claude"},
		{ID: "b", Target: "cursor"},
	}
	if got := WithTargets(leases, nil); len(got) != 2 {
		t.Errorf("WithTargets with no filter = %v, want every lease", ids(got))
	}
	got := WithTargets(leases, []string{"cursor"})
	if strings.Join(ids(got), ",") != "b" {
		t.Errorf("WithTargets(cursor) = %v, want only the cursor lease", ids(got))
	}
	if got := WithTargets(leases, []string{"windsurf"}); len(got) != 0 {
		t.Errorf("WithTargets on an unused target = %v, want nothing", ids(got))
	}
}
