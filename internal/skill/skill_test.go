package skill

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tobi404/barracks/internal/testutil"
)

// tree builds a directory layout: each entry is a file path with contents.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, body := range files {
		testutil.WriteFile(t, filepath.Join(root, filepath.FromSlash(p)), body)
	}
	return root
}

func TestDiscover(t *testing.T) {
	tests := []struct {
		name    string
		files   map[string]string
		subpath string
		want    []string
	}{
		{
			name: "skills at the top level",
			files: map[string]string{
				"react/SKILL.md": "x",
				"css/SKILL.md":   "x",
			},
			want: []string{"css", "react"},
		},
		{
			name: "skills nested under a directory",
			files: map[string]string{
				"skills/react/SKILL.md": "x",
				"skills/css/SKILL.md":   "x",
				"README.md":             "not a skill",
			},
			want: []string{"skills/css", "skills/react"},
		},
		{
			name: "subpath restricts the scan",
			files: map[string]string{
				"skills/react/SKILL.md":  "x",
				"other/ignored/SKILL.md": "x",
			},
			subpath: "skills",
			want:    []string{"skills/react"},
		},
		{
			name: "the subpath itself can be a skill",
			files: map[string]string{
				"skills/react/SKILL.md": "x",
			},
			subpath: "skills/react",
			want:    []string{"skills/react"},
		},
		{
			name: "scanning does not descend into a skill",
			files: map[string]string{
				"react/SKILL.md":               "x",
				"react/examples/SKILL.md":      "x",
				"react/examples/deep/SKILL.md": "x",
			},
			want: []string{"react"},
		},
		{
			name: "hidden directories are skipped",
			files: map[string]string{
				"react/SKILL.md":        "x",
				".hidden/SKILL.md":      "x",
				".git/objects/SKILL.md": "x",
			},
			want: []string{"react"},
		},
		{
			name: "a directory without SKILL.md is not a skill",
			files: map[string]string{
				"docs/README.md": "x",
				"react/SKILL.md": "x",
			},
			want: []string{"react"},
		},
		{
			name:  "no skills at all",
			files: map[string]string{"README.md": "x"},
			want:  nil,
		},
		{
			name: "deeply nested skills are found",
			files: map[string]string{
				"a/b/c/deep-skill/SKILL.md": "x",
			},
			want: []string{"a/b/c/deep-skill"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := tree(t, tt.files)
			got, err := Discover(root, tt.subpath)
			if err != nil {
				t.Fatalf("Discover: %v", err)
			}
			var paths []string
			for _, s := range got {
				paths = append(paths, s.RelPath)
			}
			if !reflect.DeepEqual(paths, tt.want) {
				t.Fatalf("Discover = %v, want %v", paths, tt.want)
			}
			for _, s := range got {
				if s.Name != filepath.Base(s.RelPath) {
					t.Errorf("skill %+v: name should be the directory name", s)
				}
				if !filepath.IsAbs(s.AbsPath) {
					t.Errorf("skill %+v: AbsPath should be absolute", s)
				}
			}
		})
	}
}

func TestDiscoverErrors(t *testing.T) {
	root := tree(t, map[string]string{"skills/react/SKILL.md": "x", "notes.txt": "x"})

	if _, err := Discover(root, "does-not-exist"); err == nil {
		t.Error("Discover with a missing subpath should fail")
	}
	if _, err := Discover(root, "notes.txt"); err == nil {
		t.Error("Discover with a file as subpath should fail")
	}
}

func TestDiscoverIgnoresSKILLmdAsADirectory(t *testing.T) {
	root := t.TempDir()
	// A directory literally named SKILL.md must not make its parent a skill.
	testutil.WriteFile(t, filepath.Join(root, "weird", "SKILL.md", "inner.txt"), "x")
	got, err := Discover(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Discover = %+v, want nothing: SKILL.md must be a regular file", got)
	}
}

func TestFilter(t *testing.T) {
	all := []Skill{
		{Name: "react-hooks", RelPath: "skills/react-hooks"},
		{Name: "react-forms", RelPath: "skills/react-forms"},
		{Name: "css-grid", RelPath: "skills/css-grid"},
		{Name: "deprecated", RelPath: "legacy/deprecated"},
	}

	tests := []struct {
		name   string
		only   []string
		except []string
		want   []string
	}{
		{"no filters keeps everything", nil, nil, []string{"react-hooks", "react-forms", "css-grid", "deprecated"}},
		{"only by prefix glob", []string{"react-*"}, nil, []string{"react-hooks", "react-forms"}},
		{"only with several patterns", []string{"react-*", "css-*"}, nil, []string{"react-hooks", "react-forms", "css-grid"}},
		{"except by name", nil, []string{"deprecated"}, []string{"react-hooks", "react-forms", "css-grid"}},
		{"except by glob", nil, []string{"react-*"}, []string{"css-grid", "deprecated"}},
		{"only and except combined", []string{"react-*"}, []string{"react-forms"}, []string{"react-hooks"}},
		{"except wins over only", []string{"react-*"}, []string{"react-*"}, nil},
		{"patterns match the relative path too", []string{"legacy/*"}, nil, []string{"deprecated"}},
		{"exact name", []string{"css-grid"}, nil, []string{"css-grid"}},
		{"nothing matches", []string{"nope-*"}, nil, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Filter(all, tt.only, tt.except)
			if err != nil {
				t.Fatalf("Filter: %v", err)
			}
			if !reflect.DeepEqual(Names(got), tt.want) {
				t.Fatalf("Filter = %v, want %v", Names(got), tt.want)
			}
		})
	}
}

func TestFilterRejectsBadPatterns(t *testing.T) {
	if _, err := Filter(nil, []string{"["}, nil); err == nil {
		t.Error("a malformed --only pattern should be reported")
	}
	if _, err := Filter(nil, nil, []string{"["}); err == nil {
		t.Error("a malformed --except pattern should be reported")
	}
}

func TestNamesOnEmpty(t *testing.T) {
	if got := Names(nil); len(got) != 0 {
		t.Fatalf("Names(nil) = %v, want empty", got)
	}
}
