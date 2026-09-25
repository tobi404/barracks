package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobi404/barracks/internal/garrison"
	"github.com/tobi404/barracks/internal/testutil"
)

// teamRepo makes the harness repository one a team already works in with
// Claude Code alone: a committed .claude and nothing else.
func (h *harness) teamRepo() {
	h.t.Helper()
	testutil.WriteFile(h.t, filepath.Join(h.work.Dir, ".claude", "settings.json"), "{}\n")
	h.work.Commit(h.t, "the team uses Claude Code")
}

// TestAPersonalSpawnDoesNotSteerWhereOtherLoadoutsGo is the leak as it was
// found: one loadout spawned into a second agent by hand, and from then on every
// other loadout that declares nothing followed it there - including a garrison,
// which committed that agent's files for the whole team on the strength of a
// symlink directory nobody else can see.
func TestAPersonalSpawnDoesNotSteerWhereOtherLoadoutsGo(t *testing.T) {
	h := newHarness(t)
	h.teamRepo()
	h.equipped("scout", "--only", "react")
	h.equipped("second", "--only", "css")
	h.equipped("alpha", "--only", "legacy")

	h.mustRun("spawn", "scout", "--target", "claude", "--target", "cursor")
	if !testutil.IsSymlink(t, filepath.Join(h.repoDir(t, "cursor"), "react")) {
		t.Fatal("the explicit spawn never reached Cursor, so there is nothing to leak")
	}

	out := h.mustRun("spawn", "second")
	if !strings.Contains(out, "targets: claude (detected in this repository)") {
		t.Errorf("a loadout declaring nothing did not go only where the repository points:\n%s", out)
	}
	if testutil.Exists(filepath.Join(h.repoDir(t, "cursor"), "css")) {
		t.Error("another loadout's explicit --target cursor decided where this spawn went")
	}

	statusBefore := h.work.Status(t)
	out = h.mustRun("garrison", "alpha")
	if !strings.Contains(out, "targets: claude (detected in this repository)") {
		t.Errorf("the garrison did not commit only where the repository points:\n%s", out)
	}
	if testutil.Exists(filepath.Join(h.repoDir(t, "cursor"), "legacy")) {
		t.Error("a personal spawn leaked into the committed tier: the garrison wrote Cursor files")
	}
	if !testutil.Exists(h.garrisonPath(".claude/skills/legacy/SKILL.md")) {
		t.Error("the garrison did not commit into the agent the team does use")
	}
	if status := h.work.Status(t); strings.Contains(status, ".cursor") {
		t.Errorf("the garrison left Cursor files for the team to commit:\n%s\n(before: %s)", status, statusBefore)
	}
	if g := h.lockfile(t).FindFor("", "alpha"); g == nil || strings.Join(g.Targets, ",") != "claude" {
		t.Errorf("%s records the wrong targets for alpha: %+v", garrison.LockName, g)
	}

	// And the listing that says what is present agrees with where a spawn goes.
	out = h.mustRun("targets")
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "Cursor") && strings.Contains(line, "[present here]") {
			t.Errorf("barracks targets calls Cursor present on the strength of a spawn:\n%s", out)
		}
	}
}

// TestASpawnIntoAnAgentAlreadyInUseStillCountsIt is the other half: a spawn
// into an agent the repository already shows records none of its markers as
// its own, and one the agent itself has started writing into is taken up for
// real. Neither may be discounted.
func TestASpawnIntoAnAgentAlreadyInUseStillCountsIt(t *testing.T) {
	h := newHarness(t)
	h.teamRepo()
	h.equipped("scout", "--only", "react")
	h.equipped("second", "--only", "css")

	testutil.MkDir(t, h.markerDir(t, "windsurf"))
	h.mustRun("spawn", "scout", "--target", "windsurf", "--target", "cursor")
	testutil.WriteFile(t, filepath.Join(h.markerDir(t, "cursor"), "mcp.json"), "{}\n")

	out := h.mustRun("spawn", "second")
	if !strings.Contains(out, "targets: claude, cursor, windsurf (detected in this repository)") {
		t.Errorf("an agent the repository really uses was discounted:\n%s", out)
	}
}

// TestAGlobalSpawnDoesNotSteerOtherGlobalSpawns is the same rule one scope
// over: a global spawn creates the agent's directory in the home when it is
// missing, and that is not the agent being installed.
func TestAGlobalSpawnDoesNotSteerOtherGlobalSpawns(t *testing.T) {
	h := newHarness(t)
	h.equipped("scout", "--only", "react")
	h.equipped("second", "--only", "css")

	h.mustRun("spawn", "scout", "--global", "--target", "cursor")
	if !testutil.IsSymlink(t, filepath.Join(h.home, ".cursor", "skills", "react")) {
		t.Fatal("the explicit global spawn never reached Cursor")
	}

	out := h.mustRun("spawn", "second", "--global")
	if !strings.Contains(out, "the default target") {
		t.Errorf("another global spawn followed the first into Cursor:\n%s", out)
	}
	if testutil.Exists(filepath.Join(h.home, ".cursor", "skills", "css")) {
		t.Error("a global spawn went into Cursor on the strength of another loadout's --target")
	}
}

// cardProse is the text inside the card in a frame, one space between rows,
// so a sentence the card wrapped reads back whole.
func cardProse(frame string) string {
	var rows []string
	for _, line := range strings.Split(frame, "\n") {
		first, last := strings.Index(line, "║"), strings.LastIndex(line, "║")
		if first < 0 || last <= first {
			continue
		}
		if row := strings.TrimSpace(line[first+len("║") : last]); row != "" {
			rows = append(rows, row)
		}
	}
	return strings.Join(rows, " ")
}

// TestTheGarrisonCardSaysWhereItWillCommit is what would have let the leak be
// caught before it was confirmed: the card names every agent the garrison is
// about to commit into, and why, by the rule the order then carries out.
func TestTheGarrisonCardSaysWhereItWillCommit(t *testing.T) {
	h := newHarness(t)
	h.teamRepo()
	h.equipped("alpha", "--only", "legacy")
	h.equipped("scout", "--only", "react")
	h.mustRun("spawn", "scout", "--target", "claude", "--target", "cursor")

	card := cardProse(h.frame(120, 32, "g"))
	if !strings.Contains(card, "Into: Claude Code (detected in this repository).") {
		t.Errorf("the garrison card does not say where it will commit:\n%s", card)
	}
	if strings.Contains(card, "Cursor") {
		t.Errorf("the garrison card offers to commit into an agent only a spawn put here:\n%s", card)
	}
	// The command, asked the same question in the same repository, gives the
	// same answer.
	if out := h.mustRun("garrison", "alpha"); !strings.Contains(out, "targets: claude (detected in this repository)") {
		t.Errorf("the command and the card disagree about where alpha goes:\n%s", out)
	}
	h.mustRun("recall", "alpha")

	// An existing garrison keeps what its lockfile records, and the card says
	// that is why - and names every agent, however narrow the terminal.
	h.mustRun("garrison", "alpha", "--target", "claude", "--target", "agents")
	for _, w := range []int{60, 120} {
		card = cardProse(h.frame(w, 32, "g"))
		want := "Into: Claude Code, AGENTS.md agents (Codex, opencode, Cursor) (recorded in " + garrison.LockName + ")."
		if !strings.Contains(card, want) {
			t.Errorf("%d columns: the card does not name the lockfile's agents:\n%s", w, card)
		}
		if strings.Contains(card, "…") {
			t.Errorf("%d columns: the card cut where it will commit:\n%s", w, card)
		}
	}
}

// TestTheGarrisonCardRefusesWhatTheCommandRefuses keeps the card from naming a
// destination for a loadout the order itself would stop on.
func TestTheGarrisonCardRefusesWhatTheCommandRefuses(t *testing.T) {
	h := newHarness(t)
	h.equipped("alpha", "--only", "legacy")
	path := filepath.Join(h.layout.LoadoutsDir(), "alpha.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, path, string(body)+"\ntargets:\n  - not-an-agent\n")

	got := h.frame(120, 32, "g")
	if !strings.Contains(got, "REFUSED") || !strings.Contains(got, "not-an-agent") {
		t.Errorf("the card opened on a loadout the command refuses:\n%s", got)
	}
}
