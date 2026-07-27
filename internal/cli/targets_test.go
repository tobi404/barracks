package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobi404/barracks/internal/target"
	"github.com/tobi404/barracks/internal/testutil"
)

// repoDir is where a target would put its skills inside the harness repo.
func (h *harness) repoDir(t *testing.T, id string) string {
	t.Helper()
	tgt, err := target.Lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	return tgt.RepoPath(h.work.Dir)
}

// markerDir is the configuration directory whose presence makes a target
// detected in this repository.
func (h *harness) markerDir(t *testing.T, id string) string {
	t.Helper()
	tgt, err := target.Lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(tgt.Markers) == 0 {
		t.Fatalf("target %q declares no markers", id)
	}
	return filepath.Join(h.work.Dir, tgt.Markers[0])
}

// TestEverySupportedTargetSpawnsAndRecalls is acceptance criterion 1, run over
// the whole map rather than one hand-picked entry: a loadout can be spawned
// into each supported target and recalled from it, leaving the repository
// exactly as it started.
func TestEverySupportedTargetSpawnsAndRecalls(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--only", "react")

	excludeBefore := h.work.ReadExclude(t)
	statusBefore := h.work.Status(t)

	for _, tgt := range target.Registry {
		t.Run(tgt.ID, func(t *testing.T) {
			link := filepath.Join(tgt.RepoPath(h.work.Dir), "react")

			out := h.mustRun("spawn", "frontend", "--target", tgt.ID)
			if !strings.Contains(out, tgt.Display) {
				t.Errorf("spawn output does not name the agent it went into:\n%s", out)
			}
			if !testutil.IsSymlink(t, link) {
				t.Fatalf("spawn --target %s did not create %s", tgt.ID, link)
			}
			// The whole point of registering in .git/info/exclude: a spawn into
			// any agent leaves the working tree looking untouched.
			if status := h.work.Status(t); status != statusBefore {
				t.Errorf("git status changed after spawning into %s:\n%s", tgt.ID, status)
			}

			h.mustRun("recall", "frontend", "--target", tgt.ID)
			if testutil.Exists(link) {
				t.Errorf("recall from %s left %s behind", tgt.ID, link)
			}
			if testutil.Exists(filepath.Join(h.work.Dir, tgt.Markers[0])) {
				t.Errorf("recall from %s left %s behind", tgt.ID, tgt.Markers[0])
			}
			if got := h.work.ReadExclude(t); got != excludeBefore {
				t.Errorf(".git/info/exclude not restored after %s\n got: %q\nwant: %q", tgt.ID, got, excludeBefore)
			}
			if status := h.work.Status(t); status != statusBefore {
				t.Errorf("git status not restored after recalling from %s:\n%s", tgt.ID, status)
			}
		})
	}
}

// TestTwoTargetsSpawnTogetherAndRecallTogether is acceptance criterion 2: a
// loadout declaring two targets spawns into both in one command, and one recall
// removes both.
func TestTwoTargetsSpawnTogetherAndRecallTogether(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend", "--target", "claude", "--target", "cursor")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--only", "react")

	statusBefore := h.work.Status(t)
	excludeBefore := h.work.ReadExclude(t)

	out := h.mustRun("spawn", "frontend")
	claude := filepath.Join(h.repoDir(t, "claude"), "react")
	cursor := filepath.Join(h.repoDir(t, "cursor"), "react")
	for _, p := range []string{claude, cursor} {
		if !testutil.IsSymlink(t, p) {
			t.Fatalf("one spawn did not reach %s:\n%s", p, out)
		}
	}
	if status := h.work.Status(t); status != statusBefore {
		t.Errorf("a two-target spawn dirtied the repo:\n%s", status)
	}

	// Both show up, each named by its agent.
	out = h.mustRun("deployed")
	for _, want := range []string{"claude", "cursor", "Claude Code", "Cursor"} {
		if !strings.Contains(out, want) {
			t.Errorf("deployed does not distinguish the two spawns, missing %q:\n%s", want, out)
		}
	}

	// One recall, both gone.
	out = h.mustRun("recall", "frontend")
	if strings.Count(out, "recalled frontend") != 2 {
		t.Errorf("one recall should have undone both halves of one spawn:\n%s", out)
	}
	for _, p := range []string{claude, cursor} {
		if testutil.Exists(p) {
			t.Errorf("%s survived the recall", p)
		}
	}
	if got := h.work.ReadExclude(t); got != excludeBefore {
		t.Errorf(".git/info/exclude not restored\n got: %q\nwant: %q", got, excludeBefore)
	}
	if status := h.work.Status(t); status != statusBefore {
		t.Errorf("git status not restored:\n%s", status)
	}
}

// TestRecallCanBeNarrowedToOneTarget is the other half of the multi-target
// story: recalling every agent by default must not make it impossible to
// recall just one.
func TestRecallCanBeNarrowedToOneTarget(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend", "--target", "claude,cursor")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--only", "react")
	h.mustRun("spawn", "frontend")

	h.mustRun("recall", "frontend", "--target", "cursor")
	if testutil.Exists(filepath.Join(h.repoDir(t, "cursor"), "react")) {
		t.Error("the narrowed recall did not remove the cursor spawn")
	}
	if !testutil.IsSymlink(t, filepath.Join(h.repoDir(t, "claude"), "react")) {
		t.Error("the narrowed recall removed the claude spawn as well")
	}

	h.mustRun("recall", "frontend")
	if testutil.Exists(filepath.Join(h.repoDir(t, "claude"), "react")) {
		t.Error("the follow-up recall did not remove what was left")
	}
}

// TestPerSpawnTargetOverrideDoesNotChangeTheDeclaration is acceptance
// criterion 3.
func TestPerSpawnTargetOverrideDoesNotChangeTheDeclaration(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend", "--target", "claude")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--only", "react")

	h.mustRun("spawn", "frontend", "--target", "windsurf")
	if !testutil.IsSymlink(t, filepath.Join(h.repoDir(t, "windsurf"), "react")) {
		t.Fatal("--target did not override the loadout's declaration")
	}
	if testutil.Exists(h.repoDir(t, "claude")) {
		t.Error("--target should replace the declaration for this spawn, not add to it")
	}

	// The declaration on disk is untouched.
	out := h.mustRun("assign", "frontend")
	if !strings.Contains(out, "claude") || strings.Contains(out, "windsurf") {
		t.Errorf("the spawn override edited the stored declaration:\n%s", out)
	}
	h.mustRun("recall", "frontend")

	// And the next spawn goes back to what the loadout declares.
	h.mustRun("spawn", "frontend")
	if !testutil.IsSymlink(t, filepath.Join(h.repoDir(t, "claude"), "react")) {
		t.Error("the spawn after an override did not follow the declaration again")
	}
}

// TestDeployedShowsTheTargetOfEachSpawn is acceptance criterion 4, including
// the case the field exists for: one loadout in two agents at once.
func TestDeployedShowsTheTargetOfEachSpawn(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--only", "react")
	h.mustRun("spawn", "frontend", "--target", "claude")
	h.mustRun("spawn", "frontend", "--target", "opencode")

	out := h.mustRun("deployed")
	for _, want := range []string{"target: claude (Claude Code)", "target: opencode (OpenCode)"} {
		if !strings.Contains(out, want) {
			t.Errorf("deployed output does not say which agent a spawn went into, missing %q:\n%s", want, out)
		}
	}

	// And it can be narrowed to one.
	out = h.mustRun("deployed", "--target", "opencode")
	if !strings.Contains(out, "opencode") || strings.Contains(out, "Claude Code") {
		t.Errorf("deployed --target did not narrow to one agent:\n%s", out)
	}
	h.mustRun("recall", "frontend")
}

// TestAddingATargetIsDataNotCode is acceptance criterion 5.
//
// It adds an agent barracks has never heard of by appending one entry to the
// registry - no command logic, no new flag, no new branch anywhere - and then
// drives the whole lifecycle through it.
func TestAddingATargetIsDataNotCode(t *testing.T) {
	original := append([]target.Target(nil), target.Registry...)
	t.Cleanup(func() { target.Registry = original })
	target.Registry = append(append([]target.Target(nil), original...), target.Target{
		ID:             "hypothetical",
		Aliases:        []string{"hypo"},
		Display:        "Hypothetical Agent",
		RepoDir:        filepath.Join(".hypothetical", "agent-skills"),
		GlobalDir:      filepath.Join("~", ".hypothetical", "agent-skills"),
		GlobalFallback: filepath.Join("~", ".hypothetical", "agent-skills"),
		Unit:           "skill",
		Markers:        []string{".hypothetical"},
		Docs:           "https://example.invalid/docs/skills",
	})

	h := newHarness(t)
	statusBefore := h.work.Status(t)
	excludeBefore := h.work.ReadExclude(t)

	// It is offered like any other agent.
	if out := h.mustRun("targets"); !strings.Contains(out, "Hypothetical Agent") {
		t.Errorf("a new map entry is not listed by `barracks targets`:\n%s", out)
	}

	h.mustRun("train", "experiment", "--target", "hypothetical")
	h.mustRun("equip", "experiment", h.sourceArg("skills"), "--only", "react")

	link := filepath.Join(h.work.Dir, ".hypothetical", "agent-skills", "react")
	h.mustRun("spawn", "experiment")
	if !testutil.IsSymlink(t, link) {
		t.Fatalf("spawning into a map-only target did not create %s", link)
	}
	if status := h.work.Status(t); status != statusBefore {
		t.Errorf("a map-only target did not get the same git-status guarantee:\n%s", status)
	}
	if out := h.mustRun("deployed"); !strings.Contains(out, "hypothetical") {
		t.Errorf("deployed does not report the new target:\n%s", out)
	}

	// The alias resolves too, and the global location comes from the map.
	h.mustRun("spawn", "experiment", "--global", "--target", "hypo")
	globalLink := filepath.Join(h.home, ".hypothetical", "agent-skills", "react")
	if !testutil.IsSymlink(t, globalLink) {
		t.Errorf("a global spawn for a map-only target did not land at %s", globalLink)
	}
	h.mustRun("recall", "experiment", "--global")
	if testutil.Exists(globalLink) {
		t.Errorf("global recall left %s behind", globalLink)
	}

	h.mustRun("recall", "experiment")
	if testutil.Exists(filepath.Join(h.work.Dir, ".hypothetical")) {
		t.Error("recall from a map-only target left its directory behind")
	}
	if got := h.work.ReadExclude(t); got != excludeBefore {
		t.Errorf(".git/info/exclude not restored\n got: %q\nwant: %q", got, excludeBefore)
	}
	if status := h.work.Status(t); status != statusBefore {
		t.Errorf("git status not restored:\n%s", status)
	}
}

// TestUndeclaredLoadoutDetectsWhatTheRepoUses covers the fallback rule: a
// loadout that declares nothing must look at the repository rather than assume.
func TestUndeclaredLoadoutDetectsWhatTheRepoUses(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--only", "react")

	// Nothing here yet, so the default target is used - and said out loud.
	out := h.mustRun("spawn", "frontend")
	if !strings.Contains(out, "the default target") {
		t.Errorf("a spawn nobody asked to place should say why it landed where it did:\n%s", out)
	}
	if !testutil.IsSymlink(t, filepath.Join(h.repoDir(t, target.DefaultID), "react")) {
		t.Error("the default target was not used")
	}
	h.mustRun("recall", "frontend")

	// Now the repository shows which agents it is set up for.
	testutil.MkDir(t, h.markerDir(t, "cursor"))
	testutil.MkDir(t, h.markerDir(t, "windsurf"))

	out = h.mustRun("spawn", "frontend")
	if !strings.Contains(out, "detected in this repository") {
		t.Errorf("spawn did not report that it detected the targets:\n%s", out)
	}
	for _, id := range []string{"cursor", "windsurf"} {
		if !testutil.IsSymlink(t, filepath.Join(h.repoDir(t, id), "react")) {
			t.Errorf("detection missed %s, which this repo is configured for", id)
		}
	}
	if testutil.Exists(h.repoDir(t, "claude")) {
		t.Error("detection fell back to the default even though it found agents")
	}
	h.mustRun("recall", "frontend")
}

// TestAssignChangesTheDeclarationAfterwards covers the second half of the
// per-loadout requirement: the choice can be changed after training.
func TestAssignChangesTheDeclarationAfterwards(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--only", "react")

	out := h.mustRun("assign", "frontend")
	if !strings.Contains(out, "declares no targets") {
		t.Errorf("assign with no arguments should show the current state:\n%s", out)
	}

	out = h.mustRun("assign", "frontend", "cursor", "codex")
	if !strings.Contains(out, "cursor") || !strings.Contains(out, "agents") {
		t.Errorf("assign output = %q, want the resolved targets including the aliased one", out)
	}

	// What is stored is the canonical ID, not the spelling that was typed, so
	// the file, `barracks list`, and the command's own output cannot disagree.
	body, err := os.ReadFile(filepath.Join(h.layout.LoadoutsDir(), "frontend.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "agents") || strings.Contains(string(body), "codex") {
		t.Errorf("the loadout file stores the raw alias rather than the target it resolves to:\n%s", body)
	}

	h.mustRun("spawn", "frontend")
	for _, id := range []string{"cursor", "agents"} {
		if !testutil.IsSymlink(t, filepath.Join(h.repoDir(t, id), "react")) {
			t.Errorf("the reassigned loadout did not spawn into %s", id)
		}
	}
	h.mustRun("recall", "frontend")

	// list reflects it without --verbose, because it changes where a spawn goes.
	if out := h.mustRun("list"); !strings.Contains(out, "cursor") {
		t.Errorf("list does not show which agents a loadout installs into:\n%s", out)
	}

	// --auto clears it, and detection takes over again.
	h.mustRun("assign", "frontend", "--auto")
	out = h.mustRun("spawn", "frontend")
	if !strings.Contains(out, "the default target") {
		t.Errorf("--auto did not clear the declaration:\n%s", out)
	}
	h.mustRun("recall", "frontend")
}

func TestAssignErrors(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"an unknown loadout", []string{"assign", "nope", "claude"}, "loadout not found"},
		{"an unknown target", []string{"assign", "frontend", "emacs"}, "unknown target"},
		{"names together with --auto", []string{"assign", "frontend", "claude", "--auto"}, "cannot combine"},
		{"train with an unknown target", []string{"train", "other", "--target", "emacs"}, "unknown target"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := h.run(tt.args...)
			if err == nil {
				t.Fatalf("barracks %s should have failed", strings.Join(tt.args, " "))
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}
		})
	}
	// A refused train must not have left a loadout behind.
	if out := h.mustRun("list"); strings.Contains(out, "other") {
		t.Errorf("a train refused for a bad target created the loadout anyway:\n%s", out)
	}
}

// TestHandEditedLoadoutWithAnUnknownTargetIsReported covers the file being
// hand-editable: a typo there must name the loadout, not fail obscurely.
func TestHandEditedLoadoutWithAnUnknownTargetIsReported(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--only", "react")

	path := filepath.Join(h.layout.LoadoutsDir(), "frontend.yaml")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	testutil.WriteFile(t, path, string(body)+"\ntargets:\n  - emacs\n")

	_, _, err = h.run("spawn", "frontend")
	if err == nil || !strings.Contains(err.Error(), "loadout declares") {
		t.Fatalf("spawn = %v, want an error naming the loadout's declaration", err)
	}
	if testutil.Exists(h.repoDir(t, "claude")) {
		t.Error("the refused spawn created something anyway")
	}
}

// TestRunReachesEveryDeclaredTarget makes sure the throwaway-session path is
// multi-target too, and cleans all of it up.
func TestRunReachesEveryDeclaredTarget(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend", "--target", "claude,windsurf")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--only", "react")

	statusBefore := h.work.Status(t)
	marker := filepath.Join(h.root, "seen.txt")
	out := h.mustRun("run", "frontend", "--", "sh", "-c",
		"ls .claude/skills .windsurf/skills > "+marker)

	seen, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the command did not run: %v", err)
	}
	if strings.Count(string(seen), "react") != 2 {
		t.Errorf("the command did not see the skill in both agents:\n%s", seen)
	}
	if strings.Count(out, "recalled frontend") != 2 {
		t.Errorf("run did not recall both halves on exit:\n%s", out)
	}
	for _, id := range []string{"claude", "windsurf"} {
		if testutil.Exists(h.repoDir(t, id)) {
			t.Errorf("run left %s behind", id)
		}
	}
	if status := h.work.Status(t); status != statusBefore {
		t.Errorf("run did not restore git status:\n%s", status)
	}
}

// agentScript writes an executable standing in for an agent's own CLI, named
// exactly as that agent's registry entry declares. It is created outside the
// repository and invoked by absolute path, which is also how the base-name
// match is exercised: /some/where/claude must resolve like claude.
func (h *harness) agentScript(t *testing.T, id, body string) string {
	t.Helper()
	tgt, err := target.Lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(tgt.Binaries) == 0 {
		t.Fatalf("target %q declares no binaries to stand in for", id)
	}
	return testutil.WriteScript(t, filepath.Join(h.root, "bin", tgt.Binaries[0]), body)
}

// TestRunEquipsTheAgentItLaunches is the point of `run`: the user names the
// agent, so the skills must land where that agent reads them even when the
// repository is set up for a different one.
func TestRunEquipsTheAgentItLaunches(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--only", "react")

	// This repository looks like a Cursor repository and nothing else.
	testutil.MkDir(t, h.markerDir(t, "cursor"))

	marker := filepath.Join(h.root, "seen.txt")
	script := h.agentScript(t, "claude", "ls .claude/skills > "+marker)

	out, errb, err := h.run("run", "frontend", "--", script)
	if err != nil {
		t.Fatalf("run failed: %v\n%s\n%s", err, out, errb)
	}
	seen, rerr := os.ReadFile(marker)
	if rerr != nil {
		t.Fatalf("the launched agent could not see its own skills directory: %v", rerr)
	}
	if !strings.Contains(string(seen), "react") {
		t.Errorf("the agent barracks launched did not see the skill:\n%s", seen)
	}
	if !strings.Contains(out, "for the agent this command launches") {
		t.Errorf("run did not say the launched agent decided where the skills went:\n%s", out)
	}
	// Detection still contributes; the launched agent joins it, not replaces it.
	if !strings.Contains(out, "cursor") {
		t.Errorf("the launched agent should join what the repository uses, not replace it:\n%s", out)
	}
	if errb != "" {
		t.Errorf("nothing here warrants a warning:\n%s", errb)
	}
	for _, id := range []string{"claude", "cursor"} {
		if testutil.Exists(h.repoDir(t, id)) {
			t.Errorf("run left %s behind", id)
		}
	}
}

// TestRunFallsBackToDetectionForAnUnknownCommand is the other half: a wrapper,
// a shell, or anything barracks does not recognise must behave exactly as it
// did before, with no guessing and no warning.
func TestRunFallsBackToDetectionForAnUnknownCommand(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--only", "react")
	testutil.MkDir(t, h.markerDir(t, "cursor"))

	marker := filepath.Join(h.root, "seen.txt")
	out, errb, err := h.run("run", "frontend", "--", "sh", "-c", "ls .cursor/skills > "+marker)
	if err != nil {
		t.Fatalf("run failed: %v\n%s\n%s", err, out, errb)
	}
	seen, rerr := os.ReadFile(marker)
	if rerr != nil || !strings.Contains(string(seen), "react") {
		t.Fatalf("an unrecognised command did not fall back to detection: %v\n%s", rerr, seen)
	}
	if !strings.Contains(out, "detected in this repository") {
		t.Errorf("an unrecognised command should still resolve by detection:\n%s", out)
	}
	if testutil.Exists(h.repoDir(t, "claude")) {
		t.Error("an unrecognised command was guessed at and spawned into an agent nobody asked for")
	}
	if errb != "" {
		t.Errorf("an unrecognised command must not warn:\n%s", errb)
	}
}

// TestRunWarnsWhenAnExplicitTargetExcludesTheLaunchedAgent covers both forms of
// explicit choice. Neither is overruled - but starting an agent that cannot see
// one of the skills just installed is never allowed to happen in silence.
func TestRunWarnsWhenAnExplicitTargetExcludesTheLaunchedAgent(t *testing.T) {
	script := func(h *harness, t *testing.T) string {
		return h.agentScript(t, "claude", "true")
	}

	tests := []struct {
		name  string
		setup func(h *harness)
		args  []string
		want  string
	}{
		{
			name:  "a --target flag",
			setup: func(h *harness) {},
			args:  []string{"--target", "windsurf"},
			want:  "windsurf",
		},
		{
			name:  "a loadout declaration",
			setup: func(h *harness) { h.mustRun("assign", "frontend", "cursor") },
			args:  nil,
			want:  "cursor",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.mustRun("train", "frontend")
			h.mustRun("equip", "frontend", h.sourceArg("skills"), "--only", "react")
			tt.setup(h)

			args := append([]string{"run", "frontend"}, tt.args...)
			args = append(args, "--", script(h, t))
			out, errb, err := h.run(args...)
			if err != nil {
				t.Fatalf("run failed: %v\n%s\n%s", err, out, errb)
			}
			// Warned about, and named.
			claude, lerr := target.Lookup("claude")
			if lerr != nil {
				t.Fatal(lerr)
			}
			if !strings.Contains(errb, claude.Display) {
				t.Errorf("run did not warn that the launched agent was left out:\n%s", errb)
			}
			if !strings.Contains(errb, tt.want) {
				t.Errorf("the warning does not say where the skills went instead:\n%s", errb)
			}
			// And obeyed: the explicit choice stands, argv never widened it.
			if testutil.Exists(h.repoDir(t, "claude")) {
				t.Error("argv overruled the user's explicit choice")
			}
		})
	}
}

// TestGlobalSpawnIsPerTargetToo covers the requirement that --global works for
// every supported target using that target's own user-level location.
func TestGlobalSpawnIsPerTargetToo(t *testing.T) {
	h := newHarness(t)
	h.mustRun("train", "frontend", "--target", "cursor,windsurf")
	h.mustRun("equip", "frontend", h.sourceArg("skills"), "--only", "react")

	h.mustRun("spawn", "frontend", "--global")
	for _, want := range []string{
		filepath.Join(h.home, ".cursor", "skills", "react"),
		filepath.Join(h.home, ".codeium", "windsurf", "skills", "react"),
	} {
		if !testutil.IsSymlink(t, want) {
			t.Errorf("a two-target global spawn did not land at %s", want)
		}
	}

	out := h.mustRun("deployed", "--global")
	if !strings.Contains(out, "cursor") || !strings.Contains(out, "windsurf") {
		t.Errorf("deployed --global does not show both agents:\n%s", out)
	}

	h.mustRun("recall", "frontend", "--global")
	for _, gone := range []string{
		filepath.Join(h.home, ".cursor", "skills"),
		filepath.Join(h.home, ".codeium"),
	} {
		if testutil.Exists(gone) {
			t.Errorf("global recall left %s behind", gone)
		}
	}
}
