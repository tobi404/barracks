// Package loadout stores loadout definitions: named bundles of skill sources.
//
// One YAML file per loadout, deliberately plain, because users are expected to
// open and edit them without the CLI.
package loadout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/tobi404/barracks/internal/source"
	"gopkg.in/yaml.v3"
)

// ErrNotFound is returned when no loadout by that name has been trained.
var ErrNotFound = errors.New("loadout not found")

// ErrExists is returned when training over an existing loadout.
var ErrExists = errors.New("loadout already exists")

var nameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// Equipment is one git source attached to a loadout, pinned to a commit so a
// spawn reproduces even after the ref moves.
type Equipment struct {
	source.Source `yaml:",inline"`
	// Commit is the resolved SHA the loadout is pinned to.
	Commit string `yaml:"commit"`
	// Only and Except are glob filters over discovered skill names.
	Only   []string `yaml:"only,omitempty"`
	Except []string `yaml:"except,omitempty"`
	// Skills is the set discovered at equip time, recorded for readability.
	Skills     []string  `yaml:"skills,omitempty"`
	EquippedAt time.Time `yaml:"equipped_at"`
}

// Loadout is a named bundle of skill sources.
type Loadout struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description,omitempty"`
	CreatedAt   time.Time   `yaml:"created_at"`
	Equipment   []Equipment `yaml:"equipment"`
}

// Equip attaches eq, replacing any equipment already attached from the same
// source identity - host, owner, repo, ref, and subpath all matching.
//
// Re-equipping a source is a re-pin, not a second copy. Two entries for one
// source would contribute the same skill names twice and make the loadout
// unspawnable, with a collision message naming the same source on both sides.
// A different ref or subpath is a different source and is kept alongside.
//
// It returns the equipment it replaced, or nil when eq is newly attached.
func (l *Loadout) Equip(eq Equipment) *Equipment {
	for i := range l.Equipment {
		if l.Equipment[i].Ident() != eq.Ident() {
			continue
		}
		previous := l.Equipment[i]
		l.Equipment[i] = eq
		return &previous
	}
	l.Equipment = append(l.Equipment, eq)
	return nil
}

// SkillCount is the number of skills recorded across every source.
func (l *Loadout) SkillCount() int {
	n := 0
	for _, e := range l.Equipment {
		n += len(e.Skills)
	}
	return n
}

// ValidateName rejects names that would not be safe as a filename.
func ValidateName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid loadout name %q: use letters, digits, dot, dash, or underscore (max 64)", name)
	}
	return nil
}

// Store is the loadouts directory.
type Store struct{ Dir string }

// NewStore builds a Store over dir.
func NewStore(dir string) *Store { return &Store{Dir: dir} }

func (s *Store) path(name string) string { return filepath.Join(s.Dir, name+".yaml") }

// Create writes a new, empty loadout.
func (s *Store) Create(name, description string, now time.Time) (*Loadout, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	if _, err := os.Stat(s.path(name)); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrExists, name)
	}
	l := &Loadout{Name: name, Description: description, CreatedAt: now.UTC()}
	if err := s.Save(l); err != nil {
		return nil, err
	}
	return l, nil
}

// Get loads a loadout by name.
func (s *Store) Get(name string) (*Loadout, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(s.path(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return nil, err
	}
	var l Loadout
	if err := yaml.Unmarshal(b, &l); err != nil {
		return nil, fmt.Errorf("parse loadout %s: %w", name, err)
	}
	if l.Name == "" {
		l.Name = name
	}
	return &l, nil
}

// Save writes a loadout, replacing any existing definition.
func (s *Store) Save(l *Loadout) error {
	if err := ValidateName(l.Name); err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(l)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("# barracks loadout %q - edit by hand freely.\n", l.Name)
	return writeAtomic(s.path(l.Name), append([]byte(header), b...))
}

// Delete removes a loadout definition.
func (s *Store) Delete(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if err := os.Remove(s.path(name)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return err
	}
	return nil
}

// List returns every trained loadout, name-sorted. Unreadable files are
// reported rather than aborting the listing.
func (s *Store) List() ([]*Loadout, []error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{err}
	}
	var out []*Loadout
	var problems []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		l, err := s.Get(name)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, problems
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
