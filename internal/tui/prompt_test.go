package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/tobi404/barracks/internal/loadout"
	"github.com/tobi404/barracks/internal/source"
)

// desk is a set of records the train and equip orders can write to, so a test
// can see the roster re-read what an order left behind. The records are held by
// pointer: fakeRecords on its own is a value, and a roster reading a copy would
// never see a unit arrive.
type desk struct {
	*fakeRecords
	trained  []string
	equipped []string
	// failEquip is what the next equip reports, as a fetch that failed would.
	failEquip error
}

func newDesk(units ...*loadout.Loadout) *desk {
	return &desk{fakeRecords: &fakeRecords{root: "/repo", loadouts: units}}
}

// config wires the desk's two orders over the stand-ins every other order has.
func (d *desk) config() Config {
	cfg := withActions(cfgFor(d))
	cfg.Train = func(_ context.Context, name string) Outcome {
		d.trained = append(d.trained, name)
		if err := loadout.ValidateName(name); err != nil {
			return Outcome{Err: err}
		}
		for _, l := range d.loadouts {
			if l.Name == name {
				return Outcome{Err: fmt.Errorf("%w: %s", loadout.ErrExists, name)}
			}
		}
		d.loadouts = append(d.loadouts, unitLoadout(name))
		return Outcome{Title: name + " trained"}
	}
	cfg.Equip = func(_ context.Context, l *loadout.Loadout, raw string, s Session) Outcome {
		d.equipped = append(d.equipped, l.Name+" "+raw)
		// Said on the terminal the order was handed, as the real fetch reports.
		fmt.Fprintf(s.Out, "resolving %s\n", raw)
		if d.failEquip != nil {
			return Outcome{Err: d.failEquip}
		}
		for _, have := range d.loadouts {
			if have.Name == l.Name {
				have.Equipment = unitLoadout(l.Name, "react", "css").Equipment
			}
		}
		return Outcome{Title: l.Name + " equipped", Lines: []string{"  + react", "  + css"}}
	}
	return cfg
}

// The first thing an empty roster offers is the key that fills it, and that key
// trains a unit without leaving the screen: the new unit is on the roster, under
// the cursor, and the status line names the order it needs next.
func TestNewTrainsALoadoutAndLandsOnIt(t *testing.T) {
	d := newDesk(unitLoadout("alpha", "a"), unitLoadout("delta", "d"))

	prompt := plain(Frame(d.config(), 100, 24, "n"))
	for _, want := range []string{"TRAIN ORDER", "Name the new loadout", "enter train", "esc stand down"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("n did not raise the train prompt (missing %q):\n%s", want, prompt)
		}
	}

	got := plain(Frame(d.config(), 100, 24, "n", "@type:charlie", "enter", "@pump"))
	if strings.Join(d.trained, ",") != "charlie" {
		t.Fatalf("the prompt trained %v, not charlie", d.trained)
	}
	if strings.Contains(got, "TRAIN ORDER") {
		t.Errorf("the prompt stayed up after the loadout was trained:\n%s", got)
	}
	if !strings.Contains(got, "▸ charlie") {
		t.Errorf("the cursor did not land on the unit just trained:\n%s", got)
	}
	if !strings.Contains(got, "Trained charlie. Press e to equip it.") {
		t.Errorf("the status line did not name the order the new unit needs next:\n%s", got)
	}
}

// Every letter is text while the prompt is up. The roster's own keys - quit,
// the orders, the card's yes and no - are all letters somebody may be spelling a
// name with, and a prompt that answered any of them would lose what was typed or
// quit barracks halfway through a word.
func TestThePromptTakesEveryLetterAsText(t *testing.T) {
	d := newDesk(unitLoadout("alpha", "a"))
	const name = "squad-nygeLRu?"

	got := plain(Frame(d.config(), 100, 24, "n", "@type:"+name))
	if !strings.Contains(got, "TRAIN ORDER") || !strings.Contains(got, "> "+name) {
		t.Errorf("typing %q into the prompt did something other than type it:\n%s", name, got)
	}

	m := newModel(d.config())
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m.Update(keyPress("n"))
	for _, r := range "sq" {
		if _, cmd := m.Update(tea.KeyPressMsg{Code: r, Text: string(r)}); cmd != nil {
			if _, quit := cmd().(tea.QuitMsg); quit {
				t.Fatalf("%q quit barracks from inside the prompt", r)
			}
		}
	}
	if got := m.input.Value(); got != "sq" {
		t.Errorf("the field holds %q after typing \"sq\"", got)
	}
}

// Esc leaves the prompt with nothing trained and nothing typed kept, and ctrl+c
// - the key that quits the roster - is the same withdrawal here rather than a
// quit, because it is the key people press to get out of a field.
func TestThePromptStandsDownOnEscAndTrainsNothing(t *testing.T) {
	for _, withdraw := range []string{"esc", "ctrl+c"} {
		d := newDesk(unitLoadout("alpha", "a"))
		got := plain(Frame(d.config(), 100, 24, "n", "@type:charlie", withdraw, "@pump"))
		if strings.Contains(got, "TRAIN ORDER") {
			t.Errorf("%s did not withdraw the prompt:\n%s", withdraw, got)
		}
		if !strings.Contains(got, "Order withdrawn.") {
			t.Errorf("%s withdrew the prompt without saying so:\n%s", withdraw, got)
		}
		if len(d.trained) != 0 {
			t.Errorf("%s still trained %v", withdraw, d.trained)
		}

		// And a prompt opened again starts empty rather than on the last one.
		again := plain(Frame(d.config(), 100, 24, "n", "@type:charlie", withdraw, "n"))
		if strings.Contains(again, "charlie") {
			t.Errorf("a prompt reopened after %s still held what was typed:\n%s", withdraw, again)
		}
	}
}

// A name barracks refuses is refused on the prompt, in barracks' own words, with
// what was typed still in the field to be corrected - never on a card that sends
// the user back out to the roster to start again.
func TestARefusedNameStaysOnThePromptToBeCorrected(t *testing.T) {
	d := newDesk(unitLoadout("alpha", "a"))

	got := plain(Frame(d.config(), 100, 24, "n", "@type:no good", "enter", "@pump"))
	if !strings.Contains(got, "TRAIN ORDER") {
		t.Fatalf("a refused name left the prompt:\n%s", got)
	}
	if !strings.Contains(got, "invalid loadout name") {
		t.Errorf("the refusal did not reach the prompt:\n%s", got)
	}
	if !strings.Contains(got, "> no good") {
		t.Errorf("what was typed was lost with the refusal:\n%s", got)
	}
	if strings.Contains(got, "REFUSED") {
		t.Errorf("a refused name ended on an outcome card instead of the prompt:\n%s", got)
	}

	// Corrected in place, it trains - and the refusal goes with the edit.
	fixed := plain(Frame(d.config(), 100, 24, "n", "@type:no good", "enter", "@pump",
		"backspace", "backspace", "backspace", "backspace", "backspace", "@type:-go", "enter", "@pump"))
	if !strings.Contains(fixed, "▸ no-go") {
		t.Errorf("a corrected name did not train:\n%s", fixed)
	}

	// A name that is taken is the same kind of answer.
	taken := plain(Frame(d.config(), 100, 24, "n", "@type:alpha", "enter", "@pump"))
	if !strings.Contains(taken, "TRAIN ORDER") || !strings.Contains(taken, "already exists") {
		t.Errorf("training over an existing loadout was not refused on the prompt:\n%s", taken)
	}

	// And an empty field says what it wants rather than sending nothing.
	empty := plain(Frame(d.config(), 100, 24, "n", "enter"))
	if !strings.Contains(empty, "Type a name, or esc to stand down.") {
		t.Errorf("an empty name was not answered on the prompt:\n%s", empty)
	}
}

// e equips the unit under the cursor with what was typed, through the handover
// every fetching order goes through, and ends on the report of what it carried.
func TestEquipGivesTheSelectedUnitASource(t *testing.T) {
	d := newDesk(unitLoadout("alpha", "a"), unitLoadout("bravo"))

	prompt := plain(Frame(d.config(), 100, 24, "j", "e"))
	for _, want := range []string{"EQUIP ORDER", "Equip bravo with which source?", "enter equip", "#ref:subpath"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("e did not raise the equip prompt for the selected unit (missing %q):\n%s", want, prompt)
		}
	}

	const src = "gh:unit/kit#v1.2.0:skills"
	got, terminal := FrameAndTerminal(d.config(), 100, 24, "j", "e", "@type:"+src, "enter", "@pump")
	got = plain(got)
	if strings.Join(d.equipped, ",") != "bravo "+src {
		t.Fatalf("the prompt equipped %v, not bravo with %s", d.equipped, src)
	}
	if !strings.Contains(terminal, "resolving "+src) {
		t.Errorf("the equip did not run with the terminal handed back to it: %q", terminal)
	}
	for _, want := range []string{"BRAVO EQUIPPED", "+ react", "+ css"} {
		if !strings.Contains(got, want) {
			t.Errorf("the outcome is missing %q:\n%s", want, got)
		}
	}
	// The roster behind the card has been re-read: bravo now carries two.
	back := plain(Frame(d.config(), 100, 24, "j", "e", "@type:"+src, "enter", "@pump", "x"))
	if !strings.Contains(back, "reserve") || strings.Contains(back, "unequipped") {
		t.Errorf("the roster was not re-read after the equip:\n%s", back)
	}
}

// A source barracks cannot even parse is refused before the terminal is handed
// over: nothing is fetched, the screen never steps aside, and the refusal is on
// the prompt with the source still in it.
func TestAnUnreadableSourceIsRefusedBeforeAnyFetch(t *testing.T) {
	d := newDesk(unitLoadout("alpha"))

	got, terminal := FrameAndTerminal(d.config(), 100, 24, "e", "@type:gh:", "enter", "@pump")
	got = plain(got)
	if len(d.equipped) != 0 {
		t.Fatalf("an unreadable source was still handed to equip: %v", d.equipped)
	}
	if terminal != "" {
		t.Errorf("the terminal was handed over for a source that could not be read: %q", terminal)
	}
	if !strings.Contains(got, "EQUIP ORDER") || !strings.Contains(got, "> gh:") {
		t.Errorf("the refusal did not stay on the prompt with the source in it:\n%s", got)
	}
	_, err := source.Parse("gh:")
	if err == nil {
		t.Fatal("gh: parses, so this proved nothing")
	}
	if !strings.Contains(got, strings.Fields(err.Error())[0]) {
		t.Errorf("the prompt does not carry the parser's own refusal %q:\n%s", err, got)
	}
}

// A source that parses but cannot be fetched - an unknown ref, a repository that
// is not there - comes back to the prompt too, because the correction is the
// same: change what was typed.
func TestAFailedFetchComesBackToThePrompt(t *testing.T) {
	d := newDesk(unitLoadout("alpha"))
	d.failEquip = errors.New("resolve gh:unit/kit#mian: no ref named mian")

	got := plain(Frame(d.config(), 100, 24, "e", "@type:gh:unit/kit#mian", "enter", "@pump"))
	if !strings.Contains(got, "EQUIP ORDER") || !strings.Contains(got, "no ref named mian") {
		t.Errorf("a failed fetch did not come back to the prompt with its reason:\n%s", got)
	}
	if !strings.Contains(got, "> gh:unit/kit#mian") {
		t.Errorf("what was typed was lost with the refusal:\n%s", got)
	}
}

func TestEquipStandsDownOnEsc(t *testing.T) {
	d := newDesk(unitLoadout("alpha"))
	got := plain(Frame(d.config(), 100, 24, "e", "@type:gh:unit/kit", "esc", "@pump"))
	if strings.Contains(got, "EQUIP ORDER") || len(d.equipped) != 0 {
		t.Errorf("esc did not withdraw the equip prompt (equipped %v):\n%s", d.equipped, got)
	}
}

// An unequipped unit's refusals name the key that equips it, rather than
// sending the user to the shell for it. This is the dead end the equip key
// exists to close: before it, every order on such a unit answered "equip it
// first" and nothing on the screen could.
func TestAnUnequippedUnitNamesTheKeyThatEquipsIt(t *testing.T) {
	d := newDesk(unitLoadout("alpha"))
	for _, order := range []string{"s", "g", "u", "L"} {
		got := plain(Frame(d.config(), 100, 24, order))
		if !strings.Contains(got, "alpha carries nothing - press e to equip it.") {
			t.Errorf("%s on an unequipped unit did not name the equip key:\n%s", order, got)
		}
	}
	// Whole, on the narrowest terminal the roster claims: a hint cut to "press
	// e to equ" names no key at all.
	for _, w := range []int{60, 80, 100} {
		dossier := plain(Frame(d.config(), w, 24))
		if !strings.Contains(dossier, "none - press e to equip") || strings.Contains(dossier, "barracks equip") {
			t.Errorf("%d columns: the dossier of an unequipped unit does not name the equip key:\n%s", w, dossier)
		}
	}
}

// e with nothing on the roster has nobody to equip, and says how to fix that.
func TestEquipOnAnEmptyRosterNamesTheTrainKey(t *testing.T) {
	got := plain(Frame(newDesk().config(), 100, 24, "e"))
	if strings.Contains(got, "EQUIP ORDER") || !strings.Contains(got, "No units yet - press n to train one.") {
		t.Errorf("e on an empty roster did not point at n:\n%s", got)
	}
}

// A paste is text for the field, and nothing at all anywhere else.
func TestAPasteLandsInThePromptAndNowhereElse(t *testing.T) {
	d := newDesk(unitLoadout("alpha"))
	m := newModel(d.config())
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})

	idle := plain(m.View().Content)
	m.Update(tea.PasteMsg{Content: "s"})
	if got := plain(m.View().Content); got != idle {
		t.Errorf("a paste on the roster acted as a key:\n%s", got)
	}

	m.Update(keyPress("e"))
	m.Update(tea.PasteMsg{Content: "gh:unit/kit#main"})
	if got := m.input.Value(); got != "gh:unit/kit#main" {
		t.Errorf("a paste into the prompt left %q in the field", got)
	}
}

// The prompt's refusal is barracks' own sentence and names what was wrong, so it
// is wrapped rather than cut - and on a terminal too short for all of it, the
// field and the way out are what stay.
func TestTheEquipPromptHoldsALongRefusalOnASmallTerminal(t *testing.T) {
	d := newDesk(unitLoadout("alpha"))
	d.failEquip = errors.New("resolve /private/var/folders/xx/an/extremely/long/path/that/names/a/repository/somebody/typed: " +
		"fatal: not a git repository (or any of the parent directories): .git")

	for _, size := range [][2]int{{60, 16}, {80, 24}, {60, 12}} {
		w, h := size[0], size[1]
		got := plain(Frame(d.config(), w, h, "e", "@type:./kit", "enter", "@pump"))
		fits(t, got, w, h, fmt.Sprintf("%dx%d equip refusal", w, h))
		for _, want := range []string{"EQUIP ORDER", "> ./kit", "esc stand down"} {
			if !strings.Contains(got, want) {
				t.Errorf("%dx%d: the prompt lost %q to a long refusal:\n%s", w, h, want, got)
			}
		}
	}
}

// The orders overlay advertises the two keys, and holds its own width while it
// does: the frame is clipped to the terminal, so a card grown one column too
// wide still "fits" - it just loses its right edge, and the fitting test cannot
// see that. The card's border on the row that names the keys is what can.
func TestTheOrdersOverlayNamesNewAndEquipAndKeepsItsEdge(t *testing.T) {
	d := newDesk(unitLoadout("alpha", "a"))
	for _, w := range []int{80, 120} {
		got := plain(Frame(d.config(), w, 24, "?"))
		for _, want := range []string{"n new loadout", "e equip"} {
			var row string
			for _, line := range strings.Split(got, "\n") {
				if strings.Contains(line, want) && strings.Contains(line, "║") {
					row = line
					break
				}
			}
			if row == "" {
				t.Errorf("%d columns: the orders overlay does not advertise %q:\n%s", w, want, got)
				continue
			}
			if strings.Count(row, "║") != 2 || strings.Contains(row, "…") {
				t.Errorf("%d columns: the overlay row naming %q has lost its edge: %q", w, want, row)
			}
		}
	}
}

// A train that worked but had something to say goes to the outcome card rather
// than the status line, because a notice is a thing barracks declined to do and
// a status line is gone at the next key. The cursor still lands on the unit.
func TestATrainWithANoticeIsNotReducedToAStatusLine(t *testing.T) {
	noisy := func() Config {
		cfg := newDesk(unitLoadout("alpha", "a")).config()
		train := cfg.Train
		cfg.Train = func(ctx context.Context, name string) Outcome {
			out := train(ctx, name)
			out.Notices = []string{"declared target \"vim\" is not one barracks knows"}
			return out
		}
		return cfg
	}

	got := plain(Frame(noisy(), 100, 24, "n", "@type:charlie", "enter", "@pump"))
	if !strings.Contains(got, "CHARLIE TRAINED") || !strings.Contains(got, "not one barracks knows") {
		t.Errorf("a train's notice was not shown on the outcome card:\n%s", got)
	}
	if back := plain(Frame(noisy(), 100, 24, "n", "@type:charlie", "enter", "@pump", "x")); !strings.Contains(back, "▸ charlie") {
		t.Errorf("the cursor did not land on the unit trained:\n%s", back)
	}
}
