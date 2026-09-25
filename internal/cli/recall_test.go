package cli

import (
	"strings"
	"testing"

	"github.com/tobi404/barracks/internal/testutil"
)

// garrisonedAndSpawned leaves frontend committed into the work repository
// (react and css, one file each) and spawned into cursor beside it, so a
// recall has both tiers to reach.
func garrisonedAndSpawned(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.equipped("frontend", "--except", "legacy")
	h.mustRun("garrison", "frontend", "--target", "claude")
	h.mustRun("spawn", "frontend", "--target", "cursor")
	return h
}

// untouched fails the test unless both tiers are exactly where they were: the
// committed files, the lockfile entry, and the spawn.
func (h *harness) untouched(t *testing.T, what string) {
	t.Helper()
	for _, rel := range []string{".claude/skills/react/SKILL.md", ".claude/skills/css/SKILL.md"} {
		if !testutil.Exists(h.garrisonPath(rel)) {
			t.Errorf("%s: committed file %s was removed", what, rel)
		}
	}
	if out := h.mustRun("inspect"); !strings.Contains(out, "frontend") {
		t.Errorf("%s: barracks.lock no longer records the garrison:\n%s", what, out)
	}
	if out := h.mustRun("deployed"); !strings.Contains(out, "cursor") {
		t.Errorf("%s: the spawn was recalled although the garrison was not:\n%s", what, out)
	}
}

// A recall off a terminal removed committed files with nobody asked, while the
// roster refused to do the same thing behind a single key. Off a terminal it
// now refuses and names the flag a script uses to say it meant it - and it
// refuses before touching anything, so the spawn beside the garrison stays too.
func TestRecallRefusesToRemoveAGarrisonWithNobodyToAsk(t *testing.T) {
	h := garrisonedAndSpawned(t)

	out, _, err := h.run("recall", "frontend")
	if err == nil {
		t.Fatalf("a recall off a terminal removed a garrison without asking:\n%s", out)
	}
	for _, want := range []string{"--yes", "2 committed files", "barracks.lock"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
	if strings.Contains(out, "[y/N]") {
		t.Errorf("a prompt was printed with nobody to answer it:\n%s", out)
	}
	h.untouched(t, "refused")

	// A terminal on stdout is not enough: the answer is read from stdin, and a
	// pipe there would answer the question with whatever it happened to hold.
	h.tty, h.in = true, "y\n"
	if _, _, err := h.run("recall", "frontend"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("a recall with stdin piped removed a garrison: %v", err)
	}
	h.untouched(t, "stdin piped")
}

func TestRecallAsksBeforeRemovingAGarrison(t *testing.T) {
	h := garrisonedAndSpawned(t)
	h.tty, h.inTty, h.in = true, true, "y\n"

	out := h.mustRun("recall", "frontend")
	if !strings.Contains(out, "frontend garrison: remove 2 committed files and rewrite barracks.lock? [y/N] ") {
		t.Errorf("the prompt does not say what is about to go:\n%s", out)
	}
	if !strings.Contains(out, "recalled the frontend garrison (2 files removed") {
		t.Errorf("a yes did not remove the garrison:\n%s", out)
	}
	if !strings.Contains(out, "recalled frontend from") {
		t.Errorf("a yes did not recall the spawn beside it:\n%s", out)
	}
	if testutil.Exists(h.garrisonPath(".claude/skills/react/SKILL.md")) {
		t.Error("the committed files are still there after a yes")
	}
}

// Anything but a yes is a no, and a no changes nothing - including no answer at
// all, which is what a terminal closed on the prompt gives.
func TestRecallStandsDownOnAnythingButYes(t *testing.T) {
	for _, answer := range []string{"n\n", "\n", "nope\n", ""} {
		h := garrisonedAndSpawned(t)
		h.tty, h.inTty, h.in = true, true, answer

		out, _, err := h.run("recall", "frontend")
		if err == nil || !strings.Contains(err.Error(), "nothing was recalled") {
			t.Errorf("answer %q: want a refusal saying nothing was recalled, got %v", answer, err)
		}
		if !strings.Contains(out, "[y/N]") {
			t.Errorf("answer %q: the question was never asked:\n%s", answer, out)
		}
		h.untouched(t, "answer "+strings.TrimSpace(answer))
	}
}

func TestRecallYesSkipsTheQuestion(t *testing.T) {
	h := garrisonedAndSpawned(t)
	out := h.mustRun("recall", "frontend", "--yes")
	if strings.Contains(out, "[y/N]") {
		t.Errorf("--yes still asked:\n%s", out)
	}
	if !strings.Contains(out, "recalled the frontend garrison (2 files removed") {
		t.Errorf("--yes did not remove the garrison:\n%s", out)
	}

	// And on a terminal too: a person who passed it has already answered.
	h = garrisonedAndSpawned(t)
	h.tty, h.inTty = true, true
	if out := h.mustRun("recall", "frontend", "-y"); strings.Contains(out, "[y/N]") {
		t.Errorf("-y still asked on a terminal:\n%s", out)
	}
}

// Only the committed tier is asked about. A spawn is barracks' own symlinks and
// comes back with one command, so recalling one stays a single step anywhere.
func TestRecallOfSpawnsAloneNeverAsks(t *testing.T) {
	h := newHarness(t)
	h.equipped("frontend", "--except", "legacy")
	h.mustRun("spawn", "frontend")

	out := h.mustRun("recall", "frontend")
	if strings.Contains(out, "[y/N]") {
		t.Errorf("a spawn-only recall asked a question:\n%s", out)
	}
	if !strings.Contains(out, "recalled frontend from") {
		t.Errorf("the spawn was not recalled:\n%s", out)
	}
}
