package loadout

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tobi404/barracks/internal/source"
	"github.com/tobi404/barracks/internal/testutil"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "loadouts"))
}

var testTime = time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

func TestValidateName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"simple", "frontend", true},
		{"with dash", "front-end", true},
		{"with underscore", "front_end", true},
		{"with dot", "front.end", true},
		{"with digits", "v2skills", true},
		{"empty", "", false},
		{"leading dash", "-frontend", false},
		{"leading dot", ".frontend", false},
		{"path separator", "front/end", false},
		{"traversal", "..", false},
		{"space", "front end", false},
		{"too long", strings.Repeat("a", 65), false},
		{"exactly 64", strings.Repeat("a", 64), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateName(tt.in)
			if tt.ok && err != nil {
				t.Fatalf("ValidateName(%q) = %v, want nil", tt.in, err)
			}
			if !tt.ok && err == nil {
				t.Fatalf("ValidateName(%q) = nil, want error", tt.in)
			}
		})
	}
}

func TestCreateAndGet(t *testing.T) {
	s := newStore(t)
	created, err := s.Create("frontend", "web skills", testTime)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Name != "frontend" || created.Description != "web skills" {
		t.Errorf("created = %+v", created)
	}

	got, err := s.Get("frontend")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "frontend" || got.Description != "web skills" {
		t.Errorf("Get = %+v", got)
	}
	if !got.CreatedAt.Equal(testTime) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, testTime)
	}
}

func TestCreateRejectsDuplicates(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("frontend", "", testTime); err != nil {
		t.Fatal(err)
	}
	_, err := s.Create("frontend", "", testTime)
	if !errors.Is(err, ErrExists) {
		t.Fatalf("second Create = %v, want ErrExists", err)
	}
}

func TestCreateRejectsBadNames(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("../escape", "", testTime); err == nil {
		t.Fatal("Create should reject a name that escapes the loadouts directory")
	}
}

func TestGetMissing(t *testing.T) {
	s := newStore(t)
	_, err := s.Get("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v, want ErrNotFound", err)
	}
	if _, err := s.Get("../etc/passwd"); err == nil {
		t.Fatal("Get should reject an escaping name")
	}
}

func TestSaveRoundTripsEquipment(t *testing.T) {
	s := newStore(t)
	l, err := s.Create("frontend", "", testTime)
	if err != nil {
		t.Fatal(err)
	}
	src, err := source.Parse("gh:owner/repo#main:skills")
	if err != nil {
		t.Fatal(err)
	}
	l.Equipment = append(l.Equipment, Equipment{
		Source:     src,
		Commit:     "0123456789abcdef0123456789abcdef01234567",
		Only:       []string{"react-*"},
		Except:     []string{"react-legacy"},
		Skills:     []string{"react-hooks", "react-forms"},
		EquippedAt: testTime,
	})
	if err := s.Save(l); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get("frontend")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Equipment) != 1 {
		t.Fatalf("equipment = %+v, want one entry", got.Equipment)
	}
	eq := got.Equipment[0]
	if eq.Host != "github.com" || eq.Owner != "owner" || eq.Repo != "repo" {
		t.Errorf("source not round tripped: %+v", eq.Source)
	}
	if eq.Ref != "main" || eq.Subpath != "skills" {
		t.Errorf("ref/subpath not round tripped: %+v", eq.Source)
	}
	if eq.Commit != "0123456789abcdef0123456789abcdef01234567" {
		t.Errorf("commit pin lost: %q", eq.Commit)
	}
	if len(eq.Only) != 1 || len(eq.Except) != 1 {
		t.Errorf("filters lost: only=%v except=%v", eq.Only, eq.Except)
	}
	if got.SkillCount() != 2 {
		t.Errorf("SkillCount = %d, want 2", got.SkillCount())
	}
}

// TestEquipReplacesTheSameSourceInPlace pins down what counts as the same
// source. Attaching one twice would make every skill it provides collide with
// itself, so a repeat is a re-pin - but only when the ref and subpath match
// too, because those select different content.
func TestEquipReplacesTheSameSourceInPlace(t *testing.T) {
	equipmentFor := func(t *testing.T, raw, commit string) Equipment {
		t.Helper()
		src, err := source.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return Equipment{Source: src, Commit: commit, EquippedAt: testTime}
	}

	tests := []struct {
		name         string
		second       string
		wantReplaced bool
		wantEntries  int
	}{
		{"same source again", "gh:owner/repo#main:skills", true, 1},
		{"different ref", "gh:owner/repo#v2:skills", false, 2},
		{"no ref at all", "gh:owner/repo", false, 2},
		{"different subpath", "gh:owner/repo#main:other", false, 2},
		{"different repo", "gh:owner/other#main:skills", false, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &Loadout{Name: "frontend", CreatedAt: testTime}
			first := equipmentFor(t, "gh:owner/repo#main:skills", "aaaaaaa")
			if replaced := l.Equip(first); replaced != nil {
				t.Fatalf("first Equip replaced %+v, want nil", replaced)
			}

			replaced := l.Equip(equipmentFor(t, tt.second, "bbbbbbb"))
			if tt.wantReplaced {
				if replaced == nil {
					t.Fatal("Equip attached a second copy of the same source")
				}
				if replaced.Commit != "aaaaaaa" {
					t.Errorf("replaced entry = %q, want the previous pin", replaced.Commit)
				}
				if l.Equipment[0].Commit != "bbbbbbb" {
					t.Errorf("pin = %q, want the new commit", l.Equipment[0].Commit)
				}
			} else if replaced != nil {
				t.Errorf("Equip collapsed %s into the existing source", tt.second)
			}
			if len(l.Equipment) != tt.wantEntries {
				t.Errorf("equipment = %d entries, want %d", len(l.Equipment), tt.wantEntries)
			}
		})
	}
}

// TestSavedFileIsHandEditable checks the promise made in the docs: a user can
// open the definition and understand it.
func TestSavedFileIsHandEditable(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("frontend", "web skills", testTime); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.Dir, "frontend.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.HasPrefix(text, "# barracks loadout") {
		t.Errorf("saved file should open with an explanatory comment, got %q", firstLine(text))
	}
	for _, want := range []string{"name: frontend", "description: web skills"} {
		if !strings.Contains(text, want) {
			t.Errorf("saved file missing %q:\n%s", want, text)
		}
	}
}

// TestHandEditedFileIsRead is the other half of that promise.
func TestHandEditedFileIsRead(t *testing.T) {
	s := newStore(t)
	testutil.WriteFile(t, filepath.Join(s.Dir, "manual.yaml"), strings.TrimSpace(`
description: written by hand
equipment:
  - host: github.com
    owner: someone
    repo: skills
    clone_url: https://github.com/someone/skills.git
    commit: 0123456789abcdef0123456789abcdef01234567
    skills:
      - alpha
`)+"\n")

	got, err := s.Get("manual")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	// The name is inferred from the filename when it was left out.
	if got.Name != "manual" {
		t.Errorf("Name = %q, want it inferred from the filename", got.Name)
	}
	if got.Description != "written by hand" || got.SkillCount() != 1 {
		t.Errorf("hand-written definition not read: %+v", got)
	}
}

func TestDelete(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("frontend", "", testTime); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("frontend"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("frontend"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after Delete, Get = %v, want ErrNotFound", err)
	}
	if err := s.Delete("frontend"); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete = %v, want ErrNotFound", err)
	}
	if err := s.Delete("../escape"); err == nil {
		t.Error("Delete should reject an escaping name")
	}
}

func TestList(t *testing.T) {
	s := newStore(t)
	for _, n := range []string{"zeta", "alpha", "middle"} {
		if _, err := s.Create(n, "", testTime); err != nil {
			t.Fatal(err)
		}
	}
	got, problems := s.List()
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
	want := []string{"alpha", "middle", "zeta"}
	for i := range want {
		if got[i].Name != want[i] {
			t.Fatalf("List order = %v, want %v", names(got), want)
		}
	}
}

func TestListReportsBrokenFilesWithoutAborting(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("good", "", testTime); err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, filepath.Join(s.Dir, "broken.yaml"), "equipment: [oh no")
	testutil.WriteFile(t, filepath.Join(s.Dir, "notes.txt"), "not a loadout")

	got, problems := s.List()
	if len(got) != 1 || got[0].Name != "good" {
		t.Errorf("List = %v, want just the readable loadout", names(got))
	}
	if len(problems) != 1 {
		t.Errorf("problems = %v, want the broken file reported", problems)
	}
}

func TestListOnMissingDirectory(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "never-made"))
	got, problems := s.List()
	if len(got) != 0 || len(problems) != 0 {
		t.Fatalf("List = %v, %v; want empty and no error", got, problems)
	}
}

func TestSaveRejectsBadNames(t *testing.T) {
	s := newStore(t)
	if err := s.Save(&Loadout{Name: "../evil"}); err == nil {
		t.Fatal("Save should reject an escaping name")
	}
}

func names(ls []*Loadout) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.Name
	}
	return out
}

func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}

func TestSetTargets(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"plain list", []string{"claude", "cursor"}, []string{"claude", "cursor"}},
		{"repeats collapse", []string{"claude", "claude"}, []string{"claude"}},
		{"blanks are dropped", []string{" ", "claude", ""}, []string{"claude"}},
		{"surrounding space is trimmed", []string{" cursor "}, []string{"cursor"}},
		{"an empty list clears the declaration", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &Loadout{Name: "x", Targets: []string{"stale"}}
			l.SetTargets(tt.in)
			if strings.Join(l.Targets, ",") != strings.Join(tt.want, ",") {
				t.Errorf("SetTargets(%v) = %v, want %v", tt.in, l.Targets, tt.want)
			}
		})
	}
}

// TestTargetsSurviveARoundTrip is what makes the declaration belong to the
// loadout: it is written into the hand-editable YAML, not held in memory.
func TestTargetsSurviveARoundTrip(t *testing.T) {
	s := newStore(t)
	l, err := s.Create("frontend", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	l.SetTargets([]string{"cursor", "windsurf"})
	if err := s.Save(l); err != nil {
		t.Fatal(err)
	}

	got, err := s.Get("frontend")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Targets, ",") != "cursor,windsurf" {
		t.Errorf("targets after a round trip = %v, want cursor,windsurf", got.Targets)
	}

	// And a loadout with no declaration writes no targets key at all, so the
	// file stays as plain as it was before targets existed.
	bare, err := s.Create("bare", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(s.Dir, bare.Name+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "targets:") {
		t.Errorf("a loadout declaring no targets wrote a targets key:\n%s", b)
	}
}

// TestIdentityIsMintedOnceAndSurvivesEverything: the identity is what a
// committed lockfile keys on, so it has to be stable across reads and saves and
// deliberately unrelated to the name.
func TestIdentityIsMintedOnceAndSurvivesEverything(t *testing.T) {
	s := newStore(t)
	a, err := s.Create("frontend", "", testTime)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == "" {
		t.Fatal("Create minted no identity")
	}

	b, err := s.Create("backend", "", testTime)
	if err != nil {
		t.Fatal(err)
	}
	if b.ID == a.ID {
		t.Errorf("two loadouts share the identity %q", a.ID)
	}

	// Reading it back, and saving it again, must not move it.
	for i := 0; i < 3; i++ {
		got, err := s.Get("frontend")
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != a.ID {
			t.Fatalf("read %d gave identity %q, want %q", i, got.ID, a.ID)
		}
		if err := s.Save(got); err != nil {
			t.Fatal(err)
		}
	}
}

// TestALoadoutWrittenBeforeIdentitiesGetsOneOnRead is the migration path for a
// definition already on disk. It must be given an identity exactly once, and the
// same one every time after that.
func TestALoadoutWrittenBeforeIdentitiesGetsOneOnRead(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old := "name: legacy\ncreated_at: 2026-01-01T00:00:00Z\nequipment: []\n"
	if err := os.WriteFile(filepath.Join(s.Dir, "legacy.yaml"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := s.Get("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" {
		t.Fatal("a pre-identity definition was not given one")
	}
	second, err := s.Get("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Errorf("the backfilled identity moved between reads: %q then %q", first.ID, second.ID)
	}
	// It has to be on disk, not only in memory: a lockfile stamped with an
	// identity nothing else remembers could never be matched again.
	raw, err := os.ReadFile(filepath.Join(s.Dir, "legacy.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), first.ID) {
		t.Errorf("the identity was not persisted:\n%s", raw)
	}
}

// TestRenameKeepsTheIdentityAndRefusesAnExistingName.
func TestRenameKeepsTheIdentityAndRefusesAnExistingName(t *testing.T) {
	s := newStore(t)
	l, err := s.Create("frontend", "web skills", testTime)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("taken", "", testTime); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Rename("frontend", "taken"); !errors.Is(err, ErrExists) {
		t.Fatalf("rename onto an existing name = %v, want ErrExists", err)
	}
	for _, name := range []string{"frontend", "taken"} {
		if _, err := s.Get(name); err != nil {
			t.Errorf("%s did not survive a refused rename: %v", name, err)
		}
	}

	renamed, err := s.Rename("frontend", "web")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ID != l.ID {
		t.Errorf("identity changed with the name: %q -> %q", l.ID, renamed.ID)
	}
	if renamed.Description != "web skills" {
		t.Errorf("description lost: %q", renamed.Description)
	}
	if _, err := s.Get("frontend"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the old definition survived: %v", err)
	}
	got, err := s.Get("web")
	if err != nil || got.Name != "web" || got.ID != l.ID {
		t.Errorf("renamed definition = %+v, %v", got, err)
	}
	// Exactly one definition answers to this loadout afterwards.
	all, problems := s.List()
	if len(problems) != 0 || len(all) != 2 {
		t.Errorf("store holds %d loadouts (%v), want frontend gone and web + taken", len(all), problems)
	}
}

// TestFindResolvesASourceSpellingOrRefuses: removal never guesses which entry a
// spelling meant.
func TestFindResolvesASourceSpellingOrRefuses(t *testing.T) {
	repo := testutil.NewSkillRepo(t, filepath.Join(t.TempDir(), "src"), testutil.Skill{Path: "skills/react"})
	parse := func(raw string) source.Source {
		t.Helper()
		src, err := source.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return src
	}

	l := &Loadout{Name: "frontend"}
	l.Equip(Equipment{Source: parse(repo.Dir + "#main:skills"), Commit: "aaa"})
	l.Equip(Equipment{Source: parse(repo.Dir + "#v2:skills"), Commit: "bbb"})
	l.Equip(Equipment{Source: parse("gh:owner/other"), Commit: "ccc"})
	// As `upgrade --pin` leaves a source: a declared ref nobody ever typed.
	l.Equip(Equipment{Source: parse("gh:owner/pinned#0123456789abcdef0123456789abcdef01234567:pkg"), Commit: "ddd"})

	if i, err := l.Find(parse(repo.Dir + "#v2:skills")); err != nil || i != 1 {
		t.Errorf("exact ident = %d, %v; want entry 1", i, err)
	}
	// The ref is the first part dropped, precisely because --pin owns it.
	if i, err := l.Find(parse("gh:owner/pinned#main:pkg")); err != nil || i != 3 {
		t.Errorf("a pinned source found by its original ref = %d, %v; want entry 3", i, err)
	}
	if i, err := l.Find(parse("gh:owner/other")); err != nil || i != 2 {
		t.Errorf("shorthand = %d, %v; want entry 2", i, err)
	}

	// A spelling covering two entries is refused, and both are named.
	_, err := l.Find(parse(repo.Dir))
	if !errors.Is(err, ErrAmbiguousSource) {
		t.Fatalf("ambiguous spelling = %v, want ErrAmbiguousSource", err)
	}
	for _, want := range []string{"#main:skills", "#v2:skills"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %s: %v", want, err)
		}
	}

	// A subpath that was typed and matches nothing is not widened away: giving
	// back the source the user did not name would be the worst kind of helpful.
	if _, err := l.Find(parse(repo.Dir + "#main:nowhere")); !errors.Is(err, ErrNoSuchSource) {
		t.Errorf("a subpath that is not equipped = %v, want ErrNoSuchSource", err)
	}
	if _, err := l.Find(parse("gh:nobody/nothing")); !errors.Is(err, ErrNoSuchSource) {
		t.Errorf("an unequipped source = %v, want ErrNoSuchSource", err)
	}
	if _, err := (&Loadout{Name: "empty"}).Find(parse("gh:owner/x")); !errors.Is(err, ErrNoSuchSource) {
		t.Errorf("an empty loadout = %v, want ErrNoSuchSource", err)
	}
}

// TestStripDetachesOneEntryAndLeavesTheRest.
func TestStripDetachesOneEntryAndLeavesTheRest(t *testing.T) {
	src := func(raw string) source.Source {
		t.Helper()
		s, err := source.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	l := &Loadout{Name: "frontend"}
	l.Equip(Equipment{Source: src("gh:owner/a"), Commit: "aaa", Skills: []string{"react"}})
	l.Equip(Equipment{Source: src("gh:owner/b"), Commit: "bbb", Skills: []string{"css"}})
	l.Equip(Equipment{Source: src("gh:owner/c"), Commit: "ccc", Skills: []string{"hooks"}})

	dropped := l.Strip(1)
	if dropped.Ident() != "github.com/owner/b" {
		t.Errorf("dropped %q", dropped.Ident())
	}
	if got := strings.Join(l.Idents(), ","); got != "github.com/owner/a,github.com/owner/c" {
		t.Errorf("kept %q", got)
	}
	if l.SkillCount() != 2 {
		t.Errorf("skill count = %d, want 2", l.SkillCount())
	}
	// Down to nothing is a legitimate state, not an error.
	l.Strip(0)
	l.Strip(0)
	if len(l.Equipment) != 0 {
		t.Errorf("%d entries left", len(l.Equipment))
	}
}
