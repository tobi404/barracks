package voice

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newSpeaker(t *testing.T, now *time.Time, pick uint64) *Speaker {
	t.Helper()
	return &Speaker{
		Path: StatePath(t.TempDir()),
		Now:  func() time.Time { return *now },
		Rand: func() uint64 { return pick },
	}
}

// TestSilentCommandsHaveNoPool is the whole "which commands speak" rule: a
// command speaks because it has a pool, and the data-only commands have none.
func TestSilentCommandsHaveNoPool(t *testing.T) {
	speaks := []string{"train", "equip", "spawn", "recall", "upgrade", "garrison", "run"}
	silent := []string{"list", "deployed", "inspect", "targets", "assign", "disband", "barracks", ""}

	for _, c := range speaks {
		if !Speaks(c) {
			t.Errorf("%s should speak", c)
		}
	}
	for _, c := range silent {
		if Speaks(c) {
			t.Errorf("%s should stay silent", c)
		}
	}

	now := time.Now()
	s := newSpeaker(t, &now, 0)
	for _, c := range silent {
		if got := s.Line(c, "frontend", "repo"); got != "" {
			t.Errorf("Line(%q) = %q, want no line", c, got)
		}
	}
}

// TestEscalationClimbsThenResets is acceptance criterion 5.
func TestEscalationClimbsThenResets(t *testing.T) {
	now := time.Now()
	s := newSpeaker(t, &now, 0)

	var seen []string
	for i := 0; i < steps+2; i++ {
		seen = append(seen, s.Line("spawn", "frontend", "repo"))
		now = now.Add(time.Second)
	}

	pool := pools["spawn"]
	for i := 0; i < steps; i++ {
		if seen[i] != pool[i][0] {
			t.Errorf("repeat %d said %q, want the step-%d line %q", i, seen[i], i, pool[i][0])
		}
	}
	// Past the last step it stays put-upon rather than wrapping back to fresh.
	for i := steps; i < len(seen); i++ {
		if seen[i] != pool[steps-1][0] {
			t.Errorf("repeat %d said %q, want it held at the last step %q", i, seen[i], pool[steps-1][0])
		}
	}

	// A quiet period puts it back at the top.
	now = now.Add(Window)
	if got := s.Line("spawn", "frontend", "repo"); got != pool[0][0] {
		t.Errorf("after the quiet window: %q, want a fresh %q", got, pool[0][0])
	}
}

// TestEscalationIsPerCommandAndLoadout keeps one loadout's pestering from
// making another loadout weary.
func TestEscalationIsPerCommandAndLoadout(t *testing.T) {
	now := time.Now()
	s := newSpeaker(t, &now, 0)

	s.Line("spawn", "frontend", "repo")
	s.Line("spawn", "frontend", "repo")

	if got, want := s.Line("spawn", "backend", "repo"), pools["spawn"][0][0]; got != want {
		t.Errorf("a different loadout said %q, want a fresh %q", got, want)
	}
	if got, want := s.Line("recall", "frontend", "repo"), pools["recall"][0][0]; got != want {
		t.Errorf("a different command said %q, want a fresh %q", got, want)
	}
}

// TestEscalationIsPerPlace: the same command on the same loadout somewhere else
// is a genuine first time there, and must not inherit another place's weariness.
func TestEscalationIsPerPlace(t *testing.T) {
	now := time.Now()
	s := newSpeaker(t, &now, 0)
	fresh := pools["spawn"][0][0]

	if got := s.Line("spawn", "frontend", "/home/me/project-a"); got != fresh {
		t.Fatalf("first spawn said %q, want %q", got, fresh)
	}
	if got := s.Line("spawn", "frontend", "/home/me/project-b"); got != fresh {
		t.Errorf("a different repository said %q, want a fresh %q", got, fresh)
	}
	// A global install is its own place, not whichever directory it ran from.
	if got := s.Line("spawn", "frontend", "global"); got != fresh {
		t.Errorf("a global spawn said %q, want a fresh %q", got, fresh)
	}
	// ...and repeating in the first repository still escalates.
	if got := s.Line("spawn", "frontend", "/home/me/project-a"); got != pools["spawn"][1][0] {
		t.Errorf("a repeat in the same repository said %q, want the next step", got)
	}
}

// TestStateDoesNotGrowForever: the file only ever holds keys still inside the
// window, so a machine that spawns a thousand loadouts over a week does not
// accumulate a thousand records.
func TestStateDoesNotGrowForever(t *testing.T) {
	now := time.Now()
	s := newSpeaker(t, &now, 0)

	for i := 0; i < 50; i++ {
		s.Line("spawn", string(rune('a'+i%26))+string(rune('a'+i/26)), "repo")
		now = now.Add(Window)
	}
	st := load(s.Path)
	if len(st.Records) != 1 {
		t.Fatalf("state holds %d records, want only the one still inside the window", len(st.Records))
	}
}

// TestPickVariesWithinAStep proves the pools are pools: the same step can say
// more than one thing.
func TestPickVariesWithinAStep(t *testing.T) {
	now := time.Now()
	pool := pools["train"][0]
	for i := range pool {
		s := newSpeaker(t, &now, uint64(i))
		if got, want := s.Line("train", "x", "repo"), pool[i]; got != want {
			t.Errorf("pick %d said %q, want %q", i, got, want)
		}
	}
	// An arbitrarily large source still lands inside the pool.
	s := newSpeaker(t, &now, ^uint64(0))
	if got := s.Line("train", "x", "repo"); !contains(pool, got) {
		t.Errorf("line %q is not in the step's pool", got)
	}
}

// TestBrokenStateCostsOnlyTheEscalation: flavor must never be the reason a
// command misbehaves, so every state failure degrades to a fresh line.
func TestBrokenStateCostsOnlyTheEscalation(t *testing.T) {
	dir := t.TempDir()
	corrupt := filepath.Join(dir, StateFile)
	if err := os.WriteFile(corrupt, []byte("{{ not yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cases := map[string]string{
		"corrupt file":   corrupt,
		"no path at all": "",
		"unwritable dir": filepath.Join(dir, "does", "not", "exist", StateFile),
	}
	for name, path := range cases {
		s := &Speaker{Path: path, Now: func() time.Time { return now }, Rand: func() uint64 { return 0 }}
		if got, want := s.Line("spawn", "frontend", "repo"), pools["spawn"][0][0]; got != want {
			t.Errorf("%s: %q, want a fresh %q", name, got, want)
		}
	}
}

// TestOutOfRangeStateCostsOnlyTheEscalation is the same promise as the test
// above for a file that parses: a recorded step outside the escalation is a step
// nothing here wrote, so it degrades to a fresh line instead of indexing off the
// end of a pool. Reached two ways - a negative step written straight into the
// file, and one large enough that the increment wraps negative on its own.
func TestOutOfRangeStateCostsOnlyTheEscalation(t *testing.T) {
	now := time.Now()
	fresh := pools["spawn"][0][0]
	cases := map[string]int{
		"negative step":     -5,
		"overflowing step":  math.MaxInt64,
		"step past the end": steps + 3,
	}
	for name, recorded := range cases {
		t.Run(name, func(t *testing.T) {
			path := StatePath(t.TempDir())
			save(path, state{Records: []record{{
				Key:  key("spawn", "frontend", "repo"),
				Step: recorded,
				Seen: now,
			}}})

			s := &Speaker{Path: path, Now: func() time.Time { return now }, Rand: func() uint64 { return 0 }}
			if got := s.Line("spawn", "frontend", "repo"); got != fresh {
				t.Errorf("%q, want a fresh %q", got, fresh)
			}
			// ...and the escalation carries on from there rather than staying stuck.
			if got, want := s.Line("spawn", "frontend", "repo"), pools["spawn"][1][0]; got != want {
				t.Errorf("the repeat after it said %q, want the next step %q", got, want)
			}
		})
	}
}

// TestStepInHoldsWhateverReachesIt pins the guard at the point of use directly,
// since bump is now careful enough never to hand it a bad step itself.
func TestStepInHoldsWhateverReachesIt(t *testing.T) {
	pool := []step{{"first"}, {"second"}}
	for _, bad := range []int{-1, -9999, 2, math.MaxInt64, math.MinInt64} {
		if got := stepIn(pool, bad); len(got) != 1 || got[0] != "first" {
			t.Errorf("step %d gave %q, want the fresh step", bad, got)
		}
	}
	if got := stepIn(pool, 1); len(got) != 1 || got[0] != "second" {
		t.Errorf("step 1 gave %q, want the second step", got)
	}
	if got := stepIn(nil, 0); got != nil {
		t.Errorf("an empty pool gave %q, want nothing to say", got)
	}
}

// TestZeroSpeakerWorks: the defaults are real, not placeholders.
func TestZeroSpeakerWorks(t *testing.T) {
	var s Speaker
	if got := s.Line("spawn", "frontend", "repo"); !contains(pools["spawn"][0], got) {
		t.Errorf("zero Speaker said %q, want a step-0 spawn line", got)
	}
	if got := s.Line("list", "frontend", "repo"); got != "" {
		t.Errorf("zero Speaker said %q for a silent command", got)
	}
}

// TestLinesMeetTheHouseStyle guards the bar the pools were written to. A line
// that drifts long, or starts explaining itself, stops being a voice.
func TestLinesMeetTheHouseStyle(t *testing.T) {
	// Words the unit must never reach for: it talks about itself and its task,
	// never about the reader or the machinery.
	banned := []string{"you", "your", "file", "files", "skill", "skills", "path", "flag", "repo", "loadout", "barracks", "success", "successfully"}

	seen := map[string]string{}
	for command, pool := range pools {
		if len(pool) != steps {
			t.Errorf("%s has %d steps, want %d", command, len(pool), steps)
		}
		for i, st := range pool {
			if len(st) == 0 {
				t.Errorf("%s step %d is empty", command, i)
			}
			for _, line := range st {
				where := command + " step " + string(rune('0'+i))
				words := strings.Fields(line)
				if n := len(words); n < 2 || n > 5 {
					t.Errorf("%s: %q is %d words, want 2-5", where, line, n)
				}
				for _, w := range words {
					if banned := bannedWord(w, banned); banned != "" {
						t.Errorf("%s: %q reaches outside the world with %q", where, line, banned)
					}
				}
				if prev, dup := seen[line]; dup {
					t.Errorf("%s repeats a line already used by %s: %q", where, prev, line)
				}
				seen[line] = where
			}
		}
	}

	// The one house rule the exact-uniqueness check above cannot see: a line
	// must never restate the one before it. A restatement is rarely a
	// duplicate - bolt an ellipsis onto the previous step and the strings
	// differ while the unit says the same sentence twice - so consecutive
	// steps are compared word by word instead.
	//
	// Four consecutive words, and the threshold is deliberate. Over these pools
	// four flags a genuine restatement and nothing else. Three also flags "To
	// the depot." against "Back to the depot.", where "back" is the
	// progression - first the trip out, then the return - which is the opposite
	// of a restatement. There are no exceptions and no allow-list: a failure
	// here means the line should change, never that the number should.
	for command, pool := range pools {
		for i := 0; i+1 < len(pool); i++ {
			for _, earlier := range pool[i] {
				for _, later := range pool[i+1] {
					a, b := normalizedWords(earlier), normalizedWords(later)
					where := command + " steps " + string(rune('0'+i)) + " and " + string(rune('0'+i+1))
					if run := sharedRun(a, b, 4); run != "" {
						t.Errorf("%s: %q restates %q - both carry %q", where, later, earlier, run)
					}
					if strings.Join(a, " ") == strings.Join(b, " ") {
						t.Errorf("%s: %q is %q again", where, later, earlier)
					}
				}
			}
		}
	}
}

// normalizedWords lowercases a line, drops the leading ellipsis every step-2
// slot opens with, and strips the punctuation from each word's edges.
func normalizedWords(line string) []string {
	var words []string
	for _, w := range strings.Fields(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(line)), "...")) {
		if w = strings.Trim(w, ".!?,'\""); w != "" {
			words = append(words, w)
		}
	}
	return words
}

// sharedRun returns the first run of n consecutive words a and b have in
// common, or "" when they share none that long.
func sharedRun(a, b []string, n int) string {
	for i := 0; i+n <= len(a); i++ {
		run := strings.Join(a[i:i+n], " ")
		for j := 0; j+n <= len(b); j++ {
			if run == strings.Join(b[j:j+n], " ") {
				return run
			}
		}
	}
	return ""
}

func bannedWord(word string, banned []string) string {
	w := strings.ToLower(strings.Trim(word, ".!?,'\""))
	for _, b := range banned {
		if w == b {
			return b
		}
	}
	return ""
}

func contains(pool []string, line string) bool {
	for _, l := range pool {
		if l == line {
			return true
		}
	}
	return false
}
