package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobi404/barracks/internal/garrison"
	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/testutil"
)

// collidingLoadout writes a definition in which two sources provide the same
// skills, the state an earlier build's `equip` accepted silently. It goes
// around `equip` on purpose: the guard now refuses to build it, and a spawn
// or garrison must still explain a definition that already is this way.
func collidingLoadout(t *testing.T, h *harness, name string, rootFlags ...string) {
	t.Helper()
	h.mustRun("train", name)
	h.mustRun(append([]string{"equip", name, h.src.Dir}, rootFlags...)...)
	store := loadout.NewStore(h.layout.LoadoutsDir())
	l, err := store.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	dup := l.Equipment[0]
	dup.Ref = "main"
	dup.Subpath = "skills"
	dup.Raw = h.sourceArg("skills")
	dup.Except = nil
	l.Equipment = append(l.Equipment, dup)
	if err := store.Save(l); err != nil {
		t.Fatal(err)
	}
}

// TestEquipRefusesASourceThatOnlyRepeatsWhatTheLoadoutCarries is the retry
// the UX review reproduced: the repository root, then the same repository by
// its subpath. Before, the second equip printed success and the loadout could
// never be deployed again; now it is refused, nothing is saved, and the refusal
// names the strip that switches sources.
func TestEquipRefusesASourceThatOnlyRepeatsWhatTheLoadoutCarries(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.src.Dir)

	_, _, err := h.run("equip", "frontend", h.sourceArg("skills"))
	if err == nil {
		t.Fatal("equipping a source whose every skill is already provided was accepted")
	}
	msg := err.Error()
	for _, want := range []string{"every skill", "css, legacy, react", "barracks strip frontend " + h.src.Dir, "first, then equip it again"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal is missing %q:\n%s", want, msg)
		}
	}
	if list := h.mustRun("list"); !strings.Contains(list, "1 source") {
		t.Errorf("a refused equip still changed the definition:\n%s", list)
	}
	h.mustRun("spawn", "frontend")

	// The command the refusal names is one that works, from any directory.
	h.mustRun("recall", "frontend")
	h.mustRun("strip", "frontend", h.src.Dir)
	h.mustRun("equip", "frontend", h.sourceArg("skills"))
}

// A source that shares some skills and brings others is kept, but the user is
// told which ones clash and given the one --except that repairs it without
// losing anything - and that command, run as printed, does repair it.
func TestEquipWarnsWhenASourceSharesSomeSkills(t *testing.T) {
	h := newHarness(t)
	other := testutil.NewSkillRepo(t, filepath.Join(h.root, "other"),
		testutil.Skill{Path: "react"}, testutil.Skill{Path: "css"}, testutil.Skill{Path: "forms"})
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.src.Dir)

	out, errOut, err := h.run("equip", "frontend", other.Dir, "--except", "stale")
	if err != nil {
		t.Fatalf("a partly overlapping source was refused: %v", err)
	}
	if !strings.Contains(out, "equipped frontend with") {
		t.Errorf("the source was not equipped:\n%s", out)
	}
	repair := "barracks equip frontend " + other.Dir + " --except stale,css,react"
	for _, want := range []string{"2 skills", "also provided by", "css, react", repair} {
		if !strings.Contains(errOut, want) {
			t.Errorf("warning is missing %q:\n%s", want, errOut)
		}
	}
	if strings.Contains(out, "also provided") {
		t.Error("the warning went to stdout rather than stderr")
	}

	args := strings.Fields(strings.TrimPrefix(repair, "barracks "))
	if _, errOut, err := h.run(args...); err != nil || errOut != "" {
		t.Fatalf("the printed repair did not apply cleanly: %v\n%s", err, errOut)
	}
	h.mustRun("spawn", "frontend")
	if !testutil.IsSymlink(t, filepath.Join(h.skillsDir(), "forms")) {
		t.Error("the repaired source no longer contributes the skill only it provides")
	}
}

// Re-equipping the same source is a re-pin, never a clash with itself.
func TestReEquippingASourceIsNotAnOverlapWithItself(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"))
	if _, errOut, err := h.run("equip", "frontend", h.sourceArg("skills")); err != nil || errOut != "" {
		t.Fatalf("re-equipping the same source was treated as an overlap: %v\n%s", err, errOut)
	}
}

// TestACollisionNamesEverySkillAndAFixThatWorks is the old message's two
// faults: it named only the first clash, and it prescribed --only/--except on
// spawn, which no selection can use when every skill clashes.
func TestACollisionNamesEverySkillAndAFixThatWorks(t *testing.T) {
	for _, verb := range []string{"spawn", "garrison"} {
		t.Run(verb, func(t *testing.T) {
			h := newHarness(t)
			collidingLoadout(t, h, "frontend")

			_, _, err := h.run(verb, "frontend")
			var clash *loadout.CollisionError
			if !errors.As(err, &clash) {
				t.Fatalf("%s of a colliding loadout = %v, want a collision error", verb, err)
			}
			msg := err.Error()
			for _, want := range []string{"3 skills", "css, legacy, react", "barracks strip frontend " + h.sourceArg("skills")} {
				if !strings.Contains(msg, want) {
					t.Errorf("error is missing %q:\n%s", want, msg)
				}
			}
			if strings.Contains(msg, "--only") || strings.Contains(msg, "--except") {
				t.Errorf("error still prescribes flags:\n%s", msg)
			}
			if testutil.Exists(filepath.Join(h.work.Dir, garrison.LockName)) {
				t.Error("a refused garrison wrote a lockfile")
			}

			// Every skill clashes, so there is no --except to offer.
			var printed bytes.Buffer
			printError(&printed, err)
			if strings.Contains(printed.String(), "--except") {
				t.Errorf("a total overlap was offered an --except that would leave nothing:\n%s", printed.String())
			}

			h.mustRun("strip", "frontend", h.sourceArg("skills"))
			h.mustRun(verb, "frontend")
		})
	}
}

// When the later source has skills of its own, the command line adds the
// --except that keeps them; the error itself stays free of flags.
func TestTheCommandLineAddsTheExceptRepairForAPartialClash(t *testing.T) {
	h := newHarness(t, testutil.Skill{Path: "skills/react"}, testutil.Skill{Path: "skills/css"},
		testutil.Skill{Path: "skills/legacy"}, testutil.Skill{Path: "skills/forms"})
	collidingLoadout(t, h, "frontend", "--except", "forms")

	_, _, err := h.run("spawn", "frontend")
	if err == nil {
		t.Fatal("a colliding loadout was spawned")
	}
	var printed bytes.Buffer
	printError(&printed, err)
	want := "barracks equip frontend " + h.sourceArg("skills") + " --except css,legacy,react"
	if !strings.Contains(printed.String(), want) {
		t.Errorf("printed error is missing %q:\n%s", want, printed.String())
	}
	if strings.Contains(err.Error(), "--except") {
		t.Errorf("the flag advice leaked into the surface-neutral message:\n%s", err)
	}
}

// The roster shows the same refusal, and must not recommend command-line flags
// on a screen that has none; it points to strip.
func TestTheRostersCollisionRefusalPointsToStrip(t *testing.T) {
	for _, key := range []string{"s", "g"} {
		t.Run(key, func(t *testing.T) {
			h := newHarness(t)
			collidingLoadout(t, h, "frontline")

			got := h.frame(120, 32, key, "y", "@pump")
			flat := strings.Join(strings.Fields(strings.ReplaceAll(got, "║", " ")), " ")
			for _, want := range []string{"REFUSED", "3 skills", "barracks strip"} {
				if !strings.Contains(flat, want) {
					t.Errorf("refusal card is missing %q:\n%s", want, got)
				}
			}
			for _, flag := range []string{"--only", "--except"} {
				if strings.Contains(flat, flag) {
					t.Errorf("refusal card recommends %s on a surface with no flags:\n%s", flag, got)
				}
			}
			if _, err := os.Lstat(filepath.Join(h.skillsDir(), "react")); err == nil {
				t.Error("a refused order still deployed a skill")
			}
		})
	}
}
