package loadout

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// collisionListMax is how many colliding skill names a message spells out
// before it counts the rest. Enough to recognise the overlap by, few enough
// that a forty-skill monorepo equipped twice still fits on a terminal.
const collisionListMax = 8

// Provision is what one equipped source contributes to a deployment: the entry
// itself and the skill names it offers once every filter has been applied.
type Provision struct {
	Equipment Equipment
	Skills    []string
}

// Collision is one skill name that more than one equipped source provides.
type Collision struct {
	Skill string
	// Sources are the entries providing it, in equipment order.
	Sources []Equipment
}

// CollisionError refuses a deployment in which two sources provide one skill.
//
// A skill is installed under its name, so two sources providing the same name
// is a question barracks cannot answer for the user. The error names every
// clash at once rather than the first, because a user fixing them one message
// at a time learns the overlap one skill per attempt.
//
// Error is worded for any surface: it names `barracks strip`, which is a
// command whichever screen the user reads it on. Flag advice - re-equipping a
// source with --except - is EquipHint's, and only the command line prints it,
// because the roster has no flags to offer.
type CollisionError struct {
	Loadout    string
	Collisions []Collision
	provisions []Provision
}

// Collisions reports every skill name more than one provision offers, or nil
// when the provisions can be deployed side by side.
func Collisions(loadout string, provided []Provision) *CollisionError {
	by := map[string][]Equipment{}
	for _, p := range provided {
		for _, name := range p.Skills {
			by[name] = append(by[name], p.Equipment)
		}
	}
	var out []Collision
	for name, sources := range by {
		if len(sources) > 1 {
			out = append(out, Collision{Skill: name, Sources: sources})
		}
	}
	if len(out) == 0 {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Skill < out[j].Skill })
	return &CollisionError{Loadout: loadout, Collisions: out, provisions: provided}
}

// Overlap reports which of eq's skills the loadout's other equipment already
// provides, read from the skills each entry recorded when it was equipped.
//
// An entry with eq's own identity is not "other": equipping it again replaces
// it, which is how a re-pin works and how re-equipping with --except repairs an
// overlap. Each collision lists the existing providers first and eq last.
func (l *Loadout) Overlap(eq Equipment) []Collision {
	provided := make([]Provision, 0, len(l.Equipment)+1)
	for _, other := range l.Equipment {
		if other.Ident() != eq.Ident() {
			provided = append(provided, Provision{Equipment: other, Skills: other.Skills})
		}
	}
	ce := Collisions(l.Name, append(provided, Provision{Equipment: eq, Skills: eq.Skills}))
	if ce == nil {
		return nil
	}
	// Only a clash with another entry is this source's overlap. Two skills of
	// one name inside eq itself are the source's own affair, and counting them
	// here would refuse a source for colliding with itself.
	var out []Collision
	for _, c := range ce.Collisions {
		others := distinct(c.Sources[:len(c.Sources)-1], eq.Ident())
		if c.Sources[len(c.Sources)-1].Ident() == eq.Ident() && len(others) > 0 {
			out = append(out, Collision{Skill: c.Skill, Sources: append(others, eq)})
		}
	}
	return out
}

// distinct is sources without repeats and without the entry skip names.
func distinct(sources []Equipment, skip string) []Equipment {
	var out []Equipment
	seen := map[string]bool{skip: true}
	for _, s := range sources {
		if !seen[s.Ident()] {
			seen[s.Ident()] = true
			out = append(out, s)
		}
	}
	return out
}

func (e *CollisionError) Error() string {
	return e.fact() + "; " + e.remedy()
}

// fact says which skills clash and between which sources.
func (e *CollisionError) fact() string {
	if key := sourcesKey(e.Collisions[0].Sources); e.sharedBy(key) {
		between := joinIdents(e.Collisions[0].Sources)
		if len(e.Collisions) == 1 {
			return fmt.Sprintf("skill %q is provided %s", e.Collisions[0].Skill, between)
		}
		return fmt.Sprintf("%d skills are provided %s: %s",
			len(e.Collisions), between, ListNames(SkillsOf(e.Collisions)))
	}
	parts := make([]string, len(e.Collisions))
	for i, c := range e.Collisions {
		parts[i] = fmt.Sprintf("%s (%s)", c.Skill, joinIdents(c.Sources))
	}
	return fmt.Sprintf("%d skills are provided by more than one source: %s",
		len(e.Collisions), ListNames(parts))
}

// remedy names the command that resolves the clash. The source to drop is the
// one equipped last - almost always the retry that caused it - when removing it
// alone clears every clash; otherwise there is no one command to name.
func (e *CollisionError) remedy() string {
	if e.withinOneSource() {
		// No strip clears this one: dropping the source takes every skill it
		// has with it, and another source is not what put the second copy there.
		return "a skill can come from one place only, and two directories in one source share that name - one of them has to be renamed there or skipped by its path"
	}
	if eq, ok := e.redundant(); ok {
		return fmt.Sprintf("a skill can come from one source only - drop one with `barracks strip %s %s`",
			e.Loadout, ShellArg(eq.Spelling()))
	}
	return fmt.Sprintf("a skill can come from one source only - drop all but one of each with `barracks strip %s <source>`",
		e.Loadout)
}

// EquipHint is the command-line-only half of the advice: keep the redundant
// source for the skills only it provides, and skip the ones it shares. Empty
// when there is no single such source, or when it provides nothing else - an
// --except that excludes everything would only be refused.
func (e *CollisionError) EquipHint() string {
	eq, ok := e.redundant()
	if !ok || e.withinOneSource() {
		return ""
	}
	var shared []string
	for _, c := range e.Collisions {
		shared = append(shared, c.Skill)
	}
	for _, p := range e.provisions {
		if p.Equipment.Ident() != eq.Ident() || len(p.Skills) <= len(shared) {
			continue
		}
		return fmt.Sprintf("to keep %s for its other skills, skip the shared ones with `%s`",
			eq.Ident(), ReEquipCommand(e.Loadout, eq, shared))
	}
	return ""
}

// ReEquipCommand is the `barracks equip` line that re-equips eq with skip added
// to its --except, keeping the filters it already has so the repair does not
// quietly widen what the source contributes.
func ReEquipCommand(loadout string, eq Equipment, skip []string) string {
	args := []string{"barracks", "equip", ShellArg(loadout), ShellArg(eq.Spelling())}
	if len(eq.Only) > 0 {
		args = append(args, "--only", ShellArg(strings.Join(eq.Only, ",")))
	}
	except := append(append([]string(nil), eq.Except...), skip...)
	args = append(args, "--except", ShellArg(strings.Join(except, ",")))
	return strings.Join(args, " ")
}

// withinOneSource reports whether any clash is one source providing a skill
// name twice: skill.Discover allows it, because two directories in different
// places may share a base name.
func (e *CollisionError) withinOneSource() bool {
	for _, c := range e.Collisions {
		if len(distinct(c.Sources, "")) < len(c.Sources) {
			return true
		}
	}
	return false
}

// redundant is the one source whose removal would clear every clash: each is
// between exactly two sources, and this one is the later of them every time.
// A skill three sources provide is still shared after one of them goes, so a
// command naming one source would be advice that does not work.
func (e *CollisionError) redundant() (Equipment, bool) {
	last := e.Collisions[0].Sources[len(e.Collisions[0].Sources)-1]
	for _, c := range e.Collisions {
		if len(c.Sources) != 2 || c.Sources[0].Ident() == last.Ident() || c.Sources[1].Ident() != last.Ident() {
			return Equipment{}, false
		}
	}
	return last, true
}

// sharedBy reports whether every collision is between the same sources.
func (e *CollisionError) sharedBy(key string) bool {
	for _, c := range e.Collisions {
		if sourcesKey(c.Sources) != key {
			return false
		}
	}
	return true
}

func sourcesKey(sources []Equipment) string {
	ids := make([]string, len(sources))
	for i, s := range sources {
		ids[i] = s.Ident()
	}
	return strings.Join(ids, "\x00")
}

// joinIdents reads "by both A and B", "by A, B and C" for more than two, and
// "more than once by A" when the only source is one providing the name twice.
func joinIdents(sources []Equipment) string {
	var ids []string
	for _, s := range distinct(sources, "") {
		ids = append(ids, s.Ident())
	}
	if len(ids) == 1 {
		return "more than once by " + ids[0]
	}
	return "by " + JoinAnd(ids)
}

// JoinAnd reads "both A and B" for two items and "A, B and C" for more.
func JoinAnd(items []string) string {
	switch len(items) {
	case 1:
		return items[0]
	case 2:
		return "both " + items[0] + " and " + items[1]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

// SkillsOf is the skill name of every collision, in order.
func SkillsOf(cs []Collision) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Skill
	}
	return out
}

// ListNames joins names, spelling out at most collisionListMax and counting the
// rest, so that what is left out is still said.
func ListNames(names []string) string {
	if len(names) <= collisionListMax {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:collisionListMax], ", "), len(names)-collisionListMax)
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9._/:#@+=,%-]+$`)

// ShellArg quotes s for a POSIX shell when it needs quoting, so a suggested
// command can be pasted as printed - a local source's path may hold a space.
func ShellArg(s string) string {
	if shellSafe.MatchString(s) && !strings.HasPrefix(s, "#") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
