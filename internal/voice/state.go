package voice

import (
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Window is how long a repeat still counts as pestering. Repeat a command on
// the same loadout inside it and the next line is wearier; leave it alone for
// longer and the unit greets you fresh, having forgotten yesterday entirely.
const Window = 10 * time.Minute

// StateFile is the state's name inside the data directory.
const StateFile = "voice.yaml"

// StatePath is where a layout's data directory keeps the escalation state.
func StatePath(dataDir string) string { return filepath.Join(dataDir, StateFile) }

// record is one key's place in the escalation.
type record struct {
	Key  string    `yaml:"key"`
	Step int       `yaml:"step"`
	Seen time.Time `yaml:"seen"`
}

// state is the whole file. It only ever holds keys seen inside the window -
// every read drops the rest - so it cannot grow without bound however many
// loadouts pass through it.
type state struct {
	Records []record `yaml:"records"`
}

// bump records one invocation of key and returns the escalation step it earns.
//
// It is deliberately total: an unreadable, corrupt, or unwritable state file
// costs nothing but the escalation, so a flavor line can never be the reason a
// command misbehaves.
func bump(path, key string, now time.Time) int {
	if path == "" {
		return 0
	}
	st := load(path)

	kept := make([]record, 0, len(st.Records)+1)
	step := 0
	for _, r := range st.Records {
		if now.Sub(r.Seen) >= Window {
			continue // gone quiet; forget it entirely
		}
		if r.Key == key {
			step = r.Step + 1
			if step >= steps {
				step = steps - 1
			}
			continue // rewritten below with the new step and time
		}
		kept = append(kept, r)
	}
	kept = append(kept, record{Key: key, Step: step, Seen: now})

	save(path, state{Records: kept})
	return step
}

func load(path string) state {
	data, err := os.ReadFile(path)
	if err != nil {
		return state{}
	}
	var st state
	if err := yaml.Unmarshal(data, &st); err != nil {
		return state{}
	}
	return st
}

func save(path string, st state) {
	data, err := yaml.Marshal(st)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0o644)
}
