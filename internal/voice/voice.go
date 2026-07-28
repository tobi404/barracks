// Package voice gives barracks its unit voice: one short line after a
// successful command that changed something, in the spirit of a soldier
// acknowledging an order.
//
// The rules it exists to keep are all about staying out of the way. The line is
// flavor, never information: it goes to stderr so stdout is never polluted, it
// is printed only when stdout is a terminal so scripts, logs and CI never see
// it, and it never appears on a failure - an error message the tool decorates
// reads as the tool laughing at the user.
//
// Nothing here may affect anything but which string is printed.
package voice

import (
	"math/rand/v2"
	"strings"
	"time"
)

// Speaker picks the line for one invocation.
//
// A zero Speaker still works: it speaks the first step of every pool with no
// memory between invocations, which is what an unwritable data directory
// degrades to.
type Speaker struct {
	// Path is the escalation state file. Empty means no memory: every
	// invocation is treated as the first.
	Path string
	// Now is the clock the escalation window is measured against.
	Now func() time.Time
	// Rand picks between the interchangeable lines of one step.
	Rand func() uint64
}

// Line returns what the unit says after command succeeded, or "" when this
// command has no voice.
//
// subject is the loadout the command acted on, and place is where it acted -
// both are what make two invocations "the same". Either may be empty for a
// command that has no such thing.
func (s *Speaker) Line(command, subject, place string) string {
	pool, ok := pools[command]
	if !ok {
		return ""
	}
	lines := pool[bump(s.Path, key(command, subject, place), s.now())]
	if len(lines) == 0 {
		return ""
	}
	return lines[int(s.pick()%uint64(len(lines)))]
}

// key is what the escalation counts repeats of.
//
// Two spawns of different loadouts are not a repeat, and neither are two spawns
// of one loadout into different repositories: the second is a genuine first
// spawn there, and a wearier line would be describing a place the unit has
// never been. A command that is not scoped to a place passes an empty one and
// escalates on the loadout alone.
func key(command, subject, place string) string {
	return command + "/" + strings.TrimSpace(subject) + "@" + strings.TrimSpace(place)
}

func (s *Speaker) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Speaker) pick() uint64 {
	if s.Rand != nil {
		return s.Rand()
	}
	return rand.Uint64()
}
