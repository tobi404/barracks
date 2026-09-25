package cli

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/source"
)

// The roster's train and equip orders, driven against the real stores and the
// real store of fetched sources. internal/tui holds how the prompt behaves;
// these hold that what it runs is the command, and that the disk agrees with
// what the screen says.

// n on an empty roster is the whole of `barracks train`: a definition on disk,
// the unit on the roster under the cursor, and the next key named.
func TestRosterTrainsALoadoutThroughTheCommandsPath(t *testing.T) {
	h := newHarness(t)

	got := h.frame(120, 32, "n", "@type:frontline", "enter", "@pump")
	l, err := h.loadout("frontline")
	if err != nil {
		t.Fatalf("the roster said nothing was wrong but trained no loadout: %v\n%s", err, got)
	}
	if l.ID == "" {
		t.Error("a loadout trained from the roster carries no identity, which `barracks train` always mints")
	}
	if len(l.Equipment) != 0 || len(l.Targets) != 0 {
		t.Errorf("a loadout trained from the roster is not the empty one the command trains: %+v", l)
	}
	for _, want := range []string{"▸ frontline", "Trained frontline. Press e to equip it."} {
		if !strings.Contains(got, want) {
			t.Errorf("the roster after training is missing %q:\n%s", want, got)
		}
	}
	// The command's own report is not the roster's: it ends by naming the next
	// command to type, and on this screen the next thing is a key.
	if strings.Contains(got, "barracks equip") {
		t.Errorf("the roster passed on the command's shell hint:\n%s", got)
	}

	// And the command sees exactly what the roster made.
	if out := h.mustRun("list"); !strings.Contains(out, "frontline") {
		t.Errorf("barracks list does not see the loadout the roster trained:\n%s", out)
	}
}

// Every refusal `barracks train` has is the prompt's refusal too, in the
// command's words, and nothing is written.
func TestRosterTrainRefusesWhatTheCommandRefuses(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")

	for _, tc := range []struct{ name, want string }{
		{"no/good", "invalid loadout name"},
		{"frontline", "already exists"},
	} {
		got := h.frame(120, 32, "n", "@type:"+tc.name, "enter", "@pump")
		if !strings.Contains(got, "TRAIN ORDER") || !strings.Contains(got, tc.want) {
			t.Errorf("training %q was not refused on the prompt with %q:\n%s", tc.name, tc.want, got)
		}
		_, _, err := h.run("train", tc.name)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("the command does not refuse %q the same way (got %v), so this compared nothing", tc.name, err)
		}
	}
	if _, err := h.loadout("no"); !errors.Is(err, loadout.ErrNotFound) {
		t.Errorf("a refused name still left a definition behind: %v", err)
	}

	// Withdrawn, nothing is trained at all.
	h.frame(120, 32, "n", "@type:scouts", "esc", "@pump")
	if _, err := h.loadout("scouts"); !errors.Is(err, loadout.ErrNotFound) {
		t.Errorf("a withdrawn train order still trained the loadout: %v", err)
	}
}

// e is `barracks equip`: the same source syntax, the same fetch into the same
// store, the same pinned commit and skill list in the definition - so a unit
// equipped from the roster is indistinguishable from one equipped at the prompt.
func TestRosterEquipsThroughTheCommandsPath(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")
	h.mustRun("train", "twin")
	h.mustRun("equip", "twin", h.sourceArg("skills"))

	got, released := h.frameAndTerminal(120, 32, "e", "@type:"+h.sourceArg("skills"), "enter", "@pump")
	for _, want := range []string{"FRONTLINE EQUIPPED", "equipped frontline with", "+ react", "+ css", "+ legacy"} {
		if !strings.Contains(got, want) {
			t.Errorf("the equip outcome is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(released, "\x1b") {
		t.Errorf("the equip wrote an escape sequence onto the terminal it was handed: %q", released)
	}

	viaRoster, err := h.loadout("frontline")
	if err != nil {
		t.Fatal(err)
	}
	viaCommand, err := h.loadout("twin")
	if err != nil {
		t.Fatal(err)
	}
	if len(viaRoster.Equipment) != 1 || len(viaCommand.Equipment) != 1 {
		t.Fatalf("equipment roster %d, command %d, want one each", len(viaRoster.Equipment), len(viaCommand.Equipment))
	}
	r, c := viaRoster.Equipment[0], viaCommand.Equipment[0]
	if r.Source != c.Source || r.Commit != c.Commit || !reflect.DeepEqual(r.Skills, c.Skills) {
		t.Errorf("the roster equipped something other than the command does:\nroster  %+v\ncommand %+v", r, c)
	}

	// The unit is no longer the dead end it was: its orders are there to give.
	if deploy := h.frame(120, 32, "s"); !strings.Contains(deploy, "DEPLOY ORDER") {
		t.Errorf("the unit equipped from the roster still cannot be deployed:\n%s", deploy)
	}
}

// A source the command's own parser refuses never reaches a fetch, and the
// refusal is the parser's own sentence on the prompt.
func TestRosterEquipRefusesAnUnreadableSourceOnThePrompt(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")

	const bad = "gh:owner/../escape"
	_, perr := parseSource(bad)
	if perr == nil {
		t.Fatalf("%q is a source barracks accepts, so this proved nothing", bad)
	}
	got, released := h.frameAndTerminal(120, 32, "e", "@type:"+bad, "enter", "@pump")
	if released != "" {
		t.Errorf("the terminal was handed over for a source that could not be read: %q", released)
	}
	if !strings.Contains(got, "EQUIP ORDER") || !strings.Contains(got, "> "+bad) {
		t.Errorf("the refusal did not stay on the prompt with the source in it:\n%s", got)
	}
	if !strings.Contains(strings.Join(strings.Fields(got), " "), strings.Fields(perr.Error())[0]) {
		t.Errorf("the prompt does not carry the command's refusal %q:\n%s", perr, got)
	}
	if l, err := h.loadout("frontline"); err != nil || len(l.Equipment) != 0 {
		t.Errorf("a refused source still changed the definition: %+v %v", l, err)
	}
}

// A source that parses but cannot be fetched fails where the command fails, and
// comes back to the prompt with what was typed and the command's reason.
func TestRosterEquipFromAMissingRepositoryComesBackToThePrompt(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontline")

	missing := filepath.Join(h.root, "no-such-repo")
	if _, err := source.Parse(missing); err != nil {
		t.Fatalf("%s is not even a source: %v", missing, err)
	}
	got := h.frame(160, 40, "e", "@type:"+missing, "enter", "@pump")
	if !strings.Contains(got, "EQUIP ORDER") || !strings.Contains(got, "no-such-repo") {
		t.Errorf("a failed fetch did not come back to the prompt:\n%s", got)
	}
	if strings.Contains(got, "FRONTLINE EQUIPPED") {
		t.Errorf("a failed fetch was reported as an equip:\n%s", got)
	}
	if l, err := h.loadout("frontline"); err != nil || len(l.Equipment) != 0 {
		t.Errorf("a failed fetch still changed the definition: %+v %v", l, err)
	}
}
