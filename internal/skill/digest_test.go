package skill

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tobi404/barracks/internal/testutil"
)

// TestDigestSeesContentNotLocation is the property an upgrade depends on: the
// same skill exported from two different commits fingerprints the same, so
// "modified" can mean the skill changed rather than the repository moved.
func TestDigestSeesContentNotLocation(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	for _, dir := range []string{a, b} {
		testutil.WriteFile(t, filepath.Join(dir, Manifest), "body\n")
		testutil.WriteFile(t, filepath.Join(dir, "nested", "extra.md"), "more\n")
	}

	da, err := Digest(a)
	if err != nil {
		t.Fatal(err)
	}
	db, err := Digest(b)
	if err != nil {
		t.Fatal(err)
	}
	if da != db {
		t.Errorf("identical trees fingerprinted differently:\n%s\n%s", da, db)
	}
	if da == "" {
		t.Error("digest is empty")
	}
}

func TestDigestChanges(t *testing.T) {
	base := func(t *testing.T) string {
		dir := filepath.Join(t.TempDir(), "skill")
		testutil.WriteFile(t, filepath.Join(dir, Manifest), "body\n")
		return dir
	}

	tests := []struct {
		name   string
		mutate func(t *testing.T, dir string)
	}{
		{"content", func(t *testing.T, dir string) {
			testutil.WriteFile(t, filepath.Join(dir, Manifest), "different\n")
		}},
		{"a new file", func(t *testing.T, dir string) {
			testutil.WriteFile(t, filepath.Join(dir, "extra.md"), "body\n")
		}},
		{"a renamed file", func(t *testing.T, dir string) {
			if err := os.Rename(filepath.Join(dir, Manifest), filepath.Join(dir, "OTHER.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{"the executable bit", func(t *testing.T, dir string) {
			if err := os.Chmod(filepath.Join(dir, Manifest), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"a symlink target", func(t *testing.T, dir string) {
			if err := os.Symlink("elsewhere", filepath.Join(dir, "link")); err != nil {
				t.Fatal(err)
			}
		}},
		{"an empty directory", func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, "empty"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := base(t)
			before, err := Digest(dir)
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(t, dir)
			after, err := Digest(dir)
			if err != nil {
				t.Fatal(err)
			}
			if before == after {
				t.Errorf("changing %s did not change the digest", tt.name)
			}
		})
	}
}

// TestDigestIgnoresTheNonExecutablePermissionBits keeps a umask difference
// between two exports from reading as a content change.
func TestDigestIgnoresTheNonExecutablePermissionBits(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skill")
	testutil.WriteFile(t, filepath.Join(dir, Manifest), "body\n")
	before, err := Digest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, Manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := Digest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Error("a umask difference was reported as a content change")
	}
}

func TestDigestOfAMissingDirectory(t *testing.T) {
	if _, err := Digest(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("expected an error for a directory that is not there")
	}
}

func TestIndex(t *testing.T) {
	idx := Index([]Skill{{Name: "react"}, {Name: "css"}})
	if len(idx) != 2 || idx["react"].Name != "react" {
		t.Errorf("Index() = %v", idx)
	}
	if len(Index(nil)) != 0 {
		t.Error("Index(nil) should be empty")
	}
}
