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
