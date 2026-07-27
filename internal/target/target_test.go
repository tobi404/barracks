package target

import (
	"os"
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
		for _, alias := range tgt.Aliases {
			if seen[alias] {
				t.Errorf("alias %q of target %q collides with another target or alias", alias, tgt.ID)
			}
			seen[alias] = true
		}
		if filepath.IsAbs(tgt.RepoDir) {
			t.Errorf("target %q: RepoDir must be relative to the repository root, got %q", tgt.ID, tgt.RepoDir)
		}
		// Every path in this map was read from the agent's own documentation.
		// Requiring the source keeps a future entry from being a guess.
		if tgt.Docs == "" {
			t.Errorf("target %q declares no Docs; every path here must be traceable to a primary source", tgt.ID)
		}
		// Detection is data too, and a target nothing can detect silently never
		// gets chosen for a loadout that declares no targets.
		if len(tgt.Markers) == 0 {
			t.Errorf("target %q declares no Markers, so it can never be detected", tgt.ID)
		}
		for _, m := range tgt.Markers {
			if filepath.IsAbs(m) {
				t.Errorf("target %q: marker %q must be relative to the repository root", tgt.ID, m)
			}
		}
	}
	if !seen[DefaultID] {
		t.Errorf("default target %q is not in the registry", DefaultID)
	}
}

// TestEveryTargetConsumesASkillDirectory records the finding the registry rests
// on: every agent barracks deploys to reads a directory containing a SKILL.md,
// so barracks never has to translate a skill into another format. A future
// entry that does not is a product decision, not a map edit.
func TestEveryTargetConsumesASkillDirectory(t *testing.T) {
	for _, tgt := range Registry {
		if tgt.Unit != "skill" {
			t.Errorf("target %q consumes %q, not a skill directory; barracks has no way to produce that", tgt.ID, tgt.Unit)
		}
	}
}

func TestLookupResolvesAliases(t *testing.T) {
	got, err := Lookup("codex")
	if err != nil {
		t.Fatalf("Lookup(codex): %v", err)
	}
	if got.ID != "agents" {
		t.Errorf("Lookup(codex).ID = %q, want the agents target it is an alias of", got.ID)
	}
}

func TestLookupAll(t *testing.T) {
	tests := []struct {
		name    string
		ids     []string
		want    []string
		wantErr bool
	}{
		{"order is preserved", []string{"cursor", "claude"}, []string{"cursor", "claude"}, false},
		{"repeats collapse", []string{"claude", "claude"}, []string{"claude"}, false},
		{"an alias collapses into its target", []string{"agents", "codex"}, []string{"agents"}, false},
		{"blanks are ignored", []string{"", "  ", "claude"}, []string{"claude"}, false},
		{"nothing yields nothing", nil, nil, false},
		{"an unknown id fails", []string{"claude", "emacs"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LookupAll(tt.ids)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("LookupAll(%v) = %v, want error", tt.ids, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("LookupAll(%v): %v", tt.ids, err)
			}
			var ids []string
			for _, g := range got {
				ids = append(ids, g.ID)
			}
			if strings.Join(ids, ",") != strings.Join(tt.want, ",") {
				t.Errorf("LookupAll(%v) = %v, want %v", tt.ids, ids, tt.want)
			}
		})
	}
}

func TestDetect(t *testing.T) {
	root := t.TempDir()
	if got := Detect(root); len(got) != 0 {
		t.Errorf("Detect on an empty repo = %v, want nothing", got)
	}

	for _, marker := range []string{".cursor", ".claude"} {
		if err := os.MkdirAll(filepath.Join(root, marker), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var ids []string
	for _, tgt := range Detect(root) {
		ids = append(ids, tgt.ID)
	}
	// Registry order, not marker-creation order.
	want := []string{"claude", "cursor"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Errorf("Detect = %v, want %v", ids, want)
	}
}

func TestDetectGlobal(t *testing.T) {
	home := t.TempDir()
	homeFn := func() (string, error) { return home, nil }
	env := func(string) string { return "" }

	if got := DetectGlobal(env, homeFn); len(got) != 0 {
		t.Errorf("DetectGlobal on a bare home = %v, want nothing", got)
	}

	// The parent of a target's global skills directory is the directory that
	// agent creates when it is installed.
	if err := os.MkdirAll(filepath.Join(home, ".codeium", "windsurf"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := DetectGlobal(env, homeFn)
	if len(got) != 1 || got[0].ID != "windsurf" {
		t.Errorf("DetectGlobal = %v, want just windsurf", got)
	}
}

func TestSelectPrecedence(t *testing.T) {
	detected := []Target{mustLookup(t, "windsurf")}

	tests := []struct {
		name       string
		override   []string
		declared   []string
		detected   []Target
		want       []string
		wantOrigin Origin
	}{
		{"the flag wins over everything", []string{"cursor"}, []string{"claude"}, detected, []string{"cursor"}, OriginFlag},
		{"the declaration wins over detection", nil, []string{"claude", "cursor"}, detected, []string{"claude", "cursor"}, OriginLoadout},
		{"detection wins over the default", nil, nil, detected, []string{"windsurf"}, OriginDetected},
		{"the default is the last resort", nil, nil, nil, []string{DefaultID}, OriginDefault},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel, err := Select(tt.override, tt.declared, tt.detected)
			if err != nil {
				t.Fatalf("Select: %v", err)
			}
			if strings.Join(sel.IDs(), ",") != strings.Join(tt.want, ",") {
				t.Errorf("Select = %v, want %v", sel.IDs(), tt.want)
			}
			if sel.Origin != tt.wantOrigin {
				t.Errorf("Origin = %q, want %q", sel.Origin, tt.wantOrigin)
			}
			if sel.Reason() == "" {
				t.Error("every origin should render a reason for output")
			}
		})
	}
}

func TestSelectRejectsUnknownIDs(t *testing.T) {
	if _, err := Select([]string{"emacs"}, nil, nil); err == nil {
		t.Error("an unknown --target should be refused")
	}
	// A hand-edited loadout naming a target barracks does not know must say so
	// clearly rather than silently falling through to the default.
	_, err := Select(nil, []string{"emacs"}, nil)
	if err == nil || !strings.Contains(err.Error(), "loadout declares") {
		t.Errorf("err = %v, want it to name the loadout as the source of the bad target", err)
	}
}

func mustLookup(t *testing.T, id string) Target {
	t.Helper()
	tgt, err := Lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	return tgt
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
