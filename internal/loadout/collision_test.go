package loadout

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tobi404/barracks/internal/source"
)

func equipment(t *testing.T, raw string, skills ...string) Equipment {
	t.Helper()
	src, err := source.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return Equipment{Source: src, Skills: skills}
}

func provide(eqs ...Equipment) []Provision {
	out := make([]Provision, len(eqs))
	for i, eq := range eqs {
		out[i] = Provision{Equipment: eq, Skills: eq.Skills}
	}
	return out
}

func TestNoCollisionIsNil(t *testing.T) {
	a := equipment(t, "gh:o/a", "react")
	b := equipment(t, "gh:o/b", "css")
	if ce := Collisions("frontend", provide(a, b)); ce != nil {
		t.Errorf("Collisions = %v, want nil", ce)
	}
}

func TestACollisionNamesEverySkillAndTheSourceToStrip(t *testing.T) {
	a := equipment(t, "gh:o/skills", "react", "css", "forms")
	b := equipment(t, "gh:o/skills#main", "css", "react")
	ce := Collisions("frontend", provide(a, b))
	if ce == nil {
		t.Fatal("no collision reported")
	}
	want := "2 skills are provided by both github.com/o/skills and github.com/o/skills#main: css, react; " +
		"a skill can come from one source only - drop one with `barracks strip frontend gh:o/skills#main`"
	if ce.Error() != want {
		t.Errorf("Error =\n%s\nwant\n%s", ce.Error(), want)
	}
	if hint := ce.EquipHint(); hint != "" {
		t.Errorf("EquipHint = %q, want none: the later source provides nothing else", hint)
	}
}

func TestOneCollisionIsNamedInTheSingular(t *testing.T) {
	a := equipment(t, "gh:o/a", "react")
	b := equipment(t, "gh:o/b", "react", "css")
	ce := Collisions("frontend", provide(a, b))
	if !strings.HasPrefix(ce.Error(), `skill "react" is provided by both github.com/o/a and github.com/o/b;`) {
		t.Errorf("Error = %s", ce.Error())
	}
	want := "to keep github.com/o/b for its other skills, skip the shared ones with `barracks equip frontend gh:o/b --except react`"
	if hint := ce.EquipHint(); hint != want {
		t.Errorf("EquipHint =\n%s\nwant\n%s", hint, want)
	}
}

func TestTheEquipHintKeepsTheFiltersTheSourceHas(t *testing.T) {
	a := equipment(t, "gh:o/a", "react")
	b := equipment(t, "gh:o/b", "react", "css")
	b.Only = []string{"react", "css"}
	b.Except = []string{"old"}
	hint := Collisions("fe", provide(a, b)).EquipHint()
	if !strings.Contains(hint, "barracks equip fe gh:o/b --only react,css --except old,react") {
		t.Errorf("EquipHint = %s", hint)
	}
}

// A skill three sources provide is still shared after one goes, so no single
// strip is named - naming one would be advice that does not work.
func TestAThreeWayClashNamesNoSingleSource(t *testing.T) {
	a := equipment(t, "gh:o/a", "react")
	b := equipment(t, "gh:o/b", "react")
	c := equipment(t, "gh:o/c", "react", "css")
	ce := Collisions("fe", provide(a, b, c))
	for _, want := range []string{"github.com/o/a, github.com/o/b and github.com/o/c", "`barracks strip fe <source>`"} {
		if !strings.Contains(ce.Error(), want) {
			t.Errorf("Error is missing %q:\n%s", want, ce.Error())
		}
	}
	if hint := ce.EquipHint(); hint != "" {
		t.Errorf("EquipHint = %q, want none", hint)
	}
}

func TestClashesBetweenDifferentPairsAreEachAttributed(t *testing.T) {
	a := equipment(t, "gh:o/a", "react", "css")
	b := equipment(t, "gh:o/b", "react")
	c := equipment(t, "gh:o/c", "css")
	msg := Collisions("fe", provide(a, b, c)).Error()
	for _, want := range []string{
		"2 skills are provided by more than one source",
		"css (by both github.com/o/a and github.com/o/c)",
		"react (by both github.com/o/a and github.com/o/b)",
		"`barracks strip fe <source>`",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error is missing %q:\n%s", want, msg)
		}
	}
}

func TestALongClashIsCountedRatherThanListed(t *testing.T) {
	var names []string
	for i := 0; i < 20; i++ {
		names = append(names, fmt.Sprintf("s%02d", i))
	}
	a := equipment(t, "gh:o/a", names...)
	b := equipment(t, "gh:o/b", names...)
	msg := Collisions("fe", provide(a, b)).Error()
	if !strings.Contains(msg, "20 skills") || !strings.Contains(msg, "s07 and 12 more") || strings.Contains(msg, "s08") {
		t.Errorf("Error = %s", msg)
	}
}

func TestOverlapIgnoresTheEntryBeingReplaced(t *testing.T) {
	root := equipment(t, "gh:o/skills", "react", "css")
	l := &Loadout{Name: "fe", Equipment: []Equipment{root}}

	if got := l.Overlap(equipment(t, "gh:o/skills", "react", "css")); got != nil {
		t.Errorf("re-equipping the same source overlapped with itself: %v", got)
	}
	got := l.Overlap(equipment(t, "gh:o/skills#main", "css", "forms"))
	if len(got) != 1 || got[0].Skill != "css" || got[0].Sources[0].Ident() != root.Ident() {
		t.Errorf("Overlap = %+v, want css provided by the root first", got)
	}
}

func TestShellArg(t *testing.T) {
	for in, want := range map[string]string{
		"gh:o/skills#main:sub": "gh:o/skills#main:sub",
		"/a b/repo":            "'/a b/repo'",
		"it's":                 `'it'\''s'`,
		"#main":                "'#main'",
		"report[1]":            "'report[1]'",
	} {
		if got := ShellArg(in); got != want {
			t.Errorf("ShellArg(%q) = %s, want %s", in, got, want)
		}
	}
}

// skill.Discover allows two directories of one name in one source. That is a
// clash no strip clears - dropping the source takes everything it has - so it
// is named as the source's own and no source is offered up for removal.
func TestASkillTwiceInOneSourceIsNotBlamedOnAnother(t *testing.T) {
	a := equipment(t, "gh:o/a", "react", "react", "css")
	ce := Collisions("fe", provide(a))
	if ce == nil {
		t.Fatal("a name one source provides twice was not reported")
	}
	msg := ce.Error()
	for _, want := range []string{`skill "react" is provided more than once by github.com/o/a`, "renamed there or skipped by its path"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error is missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "barracks strip") || ce.EquipHint() != "" {
		t.Errorf("a clash inside one source was answered by removing it:\n%s\n%s", msg, ce.EquipHint())
	}
}

func TestOverlapIsNotASourceCollidingWithItself(t *testing.T) {
	l := &Loadout{Name: "fe", Equipment: []Equipment{equipment(t, "gh:o/b", "css")}}
	got := l.Overlap(equipment(t, "gh:o/a", "react", "react", "css"))
	if len(got) != 1 || got[0].Skill != "css" || len(got[0].Sources) != 2 {
		t.Errorf("Overlap = %+v, want only css, between the two sources", got)
	}
}
