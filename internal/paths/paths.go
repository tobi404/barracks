// Package paths resolves the on-disk locations barracks uses for loadout
// definitions, the content-addressed source store, and lease records.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvHome overrides every other resolution rule. Primarily used by tests, but
// also a legitimate way for a user to keep an isolated barracks.
const EnvHome = "BARRACKS_HOME"

// Layout is the set of root directories barracks reads and writes.
//
// Config holds hand-editable loadout definitions. Data holds machine-managed
// state: the source store, git mirrors, lease records, and the voice's
// escalation state.
type Layout struct {
	Config string
	Data   string
}

// Resolve determines the layout from the environment.
//
// Resolution order:
//
//	BARRACKS_HOME            -> both Config and Data
//	XDG_CONFIG_HOME/barracks -> Config   (falls back to ~/.barracks)
//	XDG_DATA_HOME/barracks   -> Data     (falls back to ~/.barracks)
func Resolve(env func(string) string, home func() (string, error)) (Layout, error) {
	if env == nil {
		env = os.Getenv
	}
	if home == nil {
		home = os.UserHomeDir
	}

	if h := strings.TrimSpace(env(EnvHome)); h != "" {
		abs, err := filepath.Abs(h)
		if err != nil {
			return Layout{}, fmt.Errorf("resolve %s: %w", EnvHome, err)
		}
		return Layout{Config: abs, Data: abs}, nil
	}

	hd, err := home()
	if err != nil {
		return Layout{}, fmt.Errorf("locate home directory: %w", err)
	}
	fallback := filepath.Join(hd, ".barracks")

	return Layout{
		Config: xdg(env("XDG_CONFIG_HOME"), fallback),
		Data:   xdg(env("XDG_DATA_HOME"), fallback),
	}, nil
}

func xdg(base, fallback string) string {
	base = strings.TrimSpace(base)
	if base == "" || !filepath.IsAbs(base) {
		return fallback
	}
	return filepath.Join(base, "barracks")
}

// LoadoutsDir holds one hand-editable YAML file per loadout.
func (l Layout) LoadoutsDir() string { return filepath.Join(l.Config, "loadouts") }

// StoreDir is the content-addressed store: <host>/<owner>/<repo>@<commit>.
func (l Layout) StoreDir() string { return filepath.Join(l.Data, "store") }

// MirrorsDir holds bare git mirrors so a repo is cloned at most once.
func (l Layout) MirrorsDir() string { return filepath.Join(l.Data, "mirrors") }

// LeasesDir holds one YAML record per live spawn.
func (l Layout) LeasesDir() string { return filepath.Join(l.Data, "leases") }

// EnsureDirs creates every directory the layout needs.
func (l Layout) EnsureDirs() error {
	for _, d := range []string{l.LoadoutsDir(), l.StoreDir(), l.MirrorsDir(), l.LeasesDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	return nil
}
