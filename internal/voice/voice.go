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
// subject is what makes two invocations "the same": the loadout the command
// acted on, or "" for a command that named none.
func (s *Speaker) Line(command, subject string) string {
	pool, ok := pools[command]
	if !ok {
		return ""
	}
	lines := pool[bump(s.Path, key(command, subject), s.now())]
	if len(lines) == 0 {
		return ""
	}
	return lines[int(s.pick()%uint64(len(lines)))]
}

// key is what the escalation counts repeats of. Two spawns of different
// loadouts are not a repeat; asking for the same one twice is.
func key(command, subject string) string {
	return command + "/" + strings.TrimSpace(subject)
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
