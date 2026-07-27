package target

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestRegistryIsExercised guards the design rule that target resolution is a
// declarative map, not code. More than one entry must exist, or the
// abstraction is a pretence.
func TestRegistryIsExercised(t *testing.T) {
	if len(Registry) < 2 {
		t.Fatalf("registry has %d entries; at least two are needed for the target map to be genuinely exercised", len(Registry))
	}
	seen := map[string]bool{}
	for _, tgt := range Registry {
		if tgt.ID == "" || tgt.Display == "" || tgt.RepoDir == "" || tgt.GlobalDir == "" || tgt.Unit == "" {
			t.Errorf("target %+v has an empty required field", tgt)
		}
		if seen[tgt.ID] {
			t.Errorf("duplicate target id %q", tgt.ID)
		}
		seen[tgt.ID] = true
		if filepath.IsAbs(tgt.RepoDir) {
			t.Errorf("target %q: RepoDir must be relative to the repository root, got %q", tgt.ID, tgt.RepoDir)
		}
	}
	if !seen[DefaultID] {
		t.Errorf("default target %q is not in the registry", DefaultID)
	}
}

func TestLookup(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		want    string
		wantErr bool
	}{
		{"claude by id", "claude", "claude", false},
		{"opencode by id", "opencode", "opencode", false},
		{"empty falls back to the default", "", DefaultID, false},
		{"whitespace falls back to the default", "  ", DefaultID, false},
		{"unknown id", "emacs-agent", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Lookup(tt.id)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Lookup(%q) = %+v, want error", tt.id, got)
				}
				// The error must help: it lists what is available.
				for _, id := range IDs() {
					if !strings.Contains(err.Error(), id) {
						t.Errorf("error %q should list the known target %q", err, id)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Lookup(%q): %v", tt.id, err)
			}
			if got.ID != tt.want {
				t.Errorf("Lookup(%q).ID = %q, want %q", tt.id, got.ID, tt.want)
			}
		})
	}
}

func TestDefault(t *testing.T) {
	if Default().ID != DefaultID {
		t.Fatalf("Default().ID = %q, want %q", Default().ID, DefaultID)
	}
	if Default().Display == "" {
		t.Fatal("the default target should have a display name")
	}
}

func TestIDsIsSorted(t *testing.T) {
	ids := IDs()
	for i := 1; i < len(ids); i++ {
		if ids[i-1] > ids[i] {
			t.Fatalf("IDs() = %v, want sorted", ids)
		}
	}
}

func TestRepoPath(t *testing.T) {
	claude, err := Lookup("claude")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/repo", ".claude", "skills")
	if got := claude.RepoPath("/repo"); got != want {
		t.Errorf("RepoPath = %q, want %q", got, want)
	}
}

func TestGlobalPath(t *testing.T) {
	home := "/home/tester"
	homeFn := func() (string, error) { return home, nil }

	tests := []struct {
		name   string
		target Target
		env    map[string]string
		want   string
	}{
		{
			name:   "tilde expands to the home directory",
			target: Target{ID: "claude", GlobalDir: filepath.Join("~", ".claude", "skills")},
			want:   filepath.Join(home, ".claude", "skills"),
		},
		{
			name: "XDG_CONFIG_HOME is honoured when set",
			target: Target{
				ID:             "opencode",
				GlobalDir:      filepath.Join("${XDG_CONFIG_HOME}", "opencode", "skill"),
				GlobalFallback: filepath.Join("~", ".config", "opencode", "skill"),
			},
			env:  map[string]string{"XDG_CONFIG_HOME": "/xdg"},
			want: filepath.Join("/xdg", "opencode", "skill"),
		},
		{
			name: "unset XDG_CONFIG_HOME uses the declared fallback",
			target: Target{
				ID:             "opencode",
				GlobalDir:      filepath.Join("${XDG_CONFIG_HOME}", "opencode", "skill"),
				GlobalFallback: filepath.Join("~", ".config", "opencode", "skill"),
			},
			want: filepath.Join(home, ".config", "opencode", "skill"),
		},
		{
			name: "relative XDG_CONFIG_HOME is ignored",
			target: Target{
				ID:             "opencode",
				GlobalDir:      filepath.Join("${XDG_CONFIG_HOME}", "opencode", "skill"),
				GlobalFallback: filepath.Join("~", ".config", "opencode", "skill"),
			},
			env:  map[string]string{"XDG_CONFIG_HOME": "not/absolute"},
			want: filepath.Join(home, ".config", "opencode", "skill"),
		},
		{
			name:   "an already absolute path is left alone",
			target: Target{ID: "x", GlobalDir: "/opt/agent/skills"},
			want:   "/opt/agent/skills",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.target.GlobalPath(func(k string) string { return tt.env[k] }, homeFn)
			if err != nil {
				t.Fatalf("GlobalPath: %v", err)
			}
			if got != tt.want {
				t.Errorf("GlobalPath = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGlobalPathErrors(t *testing.T) {
	noHome := func() (string, error) { return "", errNoHome{} }
	env := func(string) string { return "" }

	if _, err := (Target{ID: "x", GlobalDir: "~/skills"}).GlobalPath(env, noHome); err == nil {
		t.Error("expanding ~ without a home directory should fail")
	}
	if _, err := (Target{ID: "x", GlobalDir: "relative/skills"}).GlobalPath(env, nil); err == nil {
		t.Error("a non-absolute global directory should be rejected")
	}
}

// TestEveryRegisteredTargetResolves makes sure a new map entry cannot be added
// with a global path that cannot be expanded.
func TestEveryRegisteredTargetResolves(t *testing.T) {
	homeFn := func() (string, error) { return "/home/tester", nil }
	for _, variant := range []map[string]string{{}, {"XDG_CONFIG_HOME": "/xdg"}} {
		for _, tgt := range Registry {
			got, err := tgt.GlobalPath(func(k string) string { return variant[k] }, homeFn)
			if err != nil {
				t.Errorf("target %q global path with env %v: %v", tgt.ID, variant, err)
				continue
			}
			if !filepath.IsAbs(got) {
				t.Errorf("target %q resolved to a relative path %q", tgt.ID, got)
			}
		}
	}
}

type errNoHome struct{}

func (errNoHome) Error() string { return "no home directory" }
