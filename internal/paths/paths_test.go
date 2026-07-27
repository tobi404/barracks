package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	home := "/home/tester"
	homeFn := func() (string, error) { return home, nil }

	tests := []struct {
		name       string
		env        map[string]string
		wantConfig string
		wantData   string
	}{
		{
			name:       "defaults to ~/.barracks",
			env:        nil,
			wantConfig: filepath.Join(home, ".barracks"),
			wantData:   filepath.Join(home, ".barracks"),
		},
		{
			name:       "BARRACKS_HOME wins over everything",
			env:        map[string]string{EnvHome: "/opt/brk", "XDG_CONFIG_HOME": "/c", "XDG_DATA_HOME": "/d"},
			wantConfig: "/opt/brk",
			wantData:   "/opt/brk",
		},
		{
			name:       "XDG config only",
			env:        map[string]string{"XDG_CONFIG_HOME": "/c"},
			wantConfig: filepath.Join("/c", "barracks"),
			wantData:   filepath.Join(home, ".barracks"),
		},
		{
			name:       "XDG data only",
			env:        map[string]string{"XDG_DATA_HOME": "/d"},
			wantConfig: filepath.Join(home, ".barracks"),
			wantData:   filepath.Join("/d", "barracks"),
		},
		{
			name:       "both XDG vars",
			env:        map[string]string{"XDG_CONFIG_HOME": "/c", "XDG_DATA_HOME": "/d"},
			wantConfig: filepath.Join("/c", "barracks"),
			wantData:   filepath.Join("/d", "barracks"),
		},
		{
			name:       "relative XDG values are ignored",
			env:        map[string]string{"XDG_CONFIG_HOME": "relative/path"},
			wantConfig: filepath.Join(home, ".barracks"),
			wantData:   filepath.Join(home, ".barracks"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(func(k string) string { return tt.env[k] }, homeFn)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got.Config != tt.wantConfig {
				t.Errorf("Config = %q, want %q", got.Config, tt.wantConfig)
			}
			if got.Data != tt.wantData {
				t.Errorf("Data = %q, want %q", got.Data, tt.wantData)
			}
		})
	}
}

func TestResolveHomeFailure(t *testing.T) {
	_, err := Resolve(func(string) string { return "" }, func() (string, error) {
		return "", errFake
	})
	if err == nil {
		t.Fatal("Resolve should fail when the home directory is unknown")
	}
}

func TestSubdirectories(t *testing.T) {
	l := Layout{Config: "/cfg", Data: "/data"}
	tests := []struct {
		got  string
		want string
	}{
		{l.LoadoutsDir(), filepath.Join("/cfg", "loadouts")},
		{l.StoreDir(), filepath.Join("/data", "store")},
		{l.MirrorsDir(), filepath.Join("/data", "mirrors")},
		{l.LeasesDir(), filepath.Join("/data", "leases")},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("got %q, want %q", tt.got, tt.want)
		}
	}
}

func TestEnsureDirs(t *testing.T) {
	dir := t.TempDir()
	l := Layout{Config: filepath.Join(dir, "cfg"), Data: filepath.Join(dir, "data")}
	if err := l.EnsureDirs(); err != nil {
		t.Fatalf("EnsureDirs: %v", err)
	}
	for _, d := range []string{l.LoadoutsDir(), l.StoreDir(), l.MirrorsDir(), l.LeasesDir()} {
		if !isDir(d) {
			t.Errorf("%s was not created", d)
		}
	}
	// Idempotent.
	if err := l.EnsureDirs(); err != nil {
		t.Fatalf("second EnsureDirs: %v", err)
	}
}

func TestResolveUsesRealEnvWhenNil(t *testing.T) {
	t.Setenv(EnvHome, t.TempDir())
	got, err := Resolve(nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Config != got.Data {
		t.Fatalf("BARRACKS_HOME should point Config and Data at the same place, got %q and %q", got.Config, got.Data)
	}
}

var errFake = fakeErr{}

type fakeErr struct{}

func (fakeErr) Error() string { return "no home" }

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
