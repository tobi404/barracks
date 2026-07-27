package gitexclude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAddThenRemoveRestoresByteForByte is the guarantee that makes spawn/recall
// leave a repository exactly as it was found.
func TestAddThenRemoveRestoresByteForByte(t *testing.T) {
	tests := []struct {
		name     string
		original string
		// exists says whether the exclude file is there before Add runs.
		exists bool
	}{
		{"typical git-created file", "# comments\n*.log\n", true},
		{"file with no trailing newline", "*.log", true},
		{"empty file", "", true},
		{"file does not exist at all", "", false},
		{"file with blank lines", "\n\n*.tmp\n\n", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitDir := filepath.Join(t.TempDir(), ".git")
			file := filepath.Join(gitDir, "info", "exclude")
			if tt.exists {
				if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(tt.original), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			rec, err := Add(gitDir, "lease1", []string{"/.claude/skills/react", "/.claude/skills/css"})
			if err != nil {
				t.Fatalf("Add: %v", err)
			}

			after, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("read after Add: %v", err)
			}
			if !strings.Contains(string(after), "/.claude/skills/react") {
				t.Error("Add did not register the pattern")
			}
			if !strings.Contains(string(after), "# barracks:lease1 begin") {
				t.Error("Add did not fence the block")
			}

			if err := Remove(rec, "lease1"); err != nil {
				t.Fatalf("Remove: %v", err)
			}

			restored, err := os.ReadFile(file)
			switch {
			case tt.exists:
				if err != nil {
					t.Fatalf("read after Remove: %v", err)
				}
				if string(restored) != tt.original {
					t.Errorf("not restored byte for byte\n got: %q\nwant: %q", restored, tt.original)
				}
			default:
				if err == nil {
					t.Errorf("exclude file should have been deleted again, got %q", restored)
				}
			}
		})
	}
}

func TestAddIsANoOpWithoutPatterns(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	rec, err := Add(gitDir, "lease1", nil)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if rec != nil {
		t.Fatalf("Add with no patterns returned %+v, want nil", rec)
	}
	if _, err := os.Stat(filepath.Join(gitDir, "info", "exclude")); err == nil {
		t.Error("Add with no patterns should not create the exclude file")
	}
}

func TestTwoLeasesCoexistAndUnwindIndependently(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	file := filepath.Join(gitDir, "info", "exclude")
	original := "# git default\n"
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	recA, err := Add(gitDir, "leaseA", []string{"/.claude/skills/a"})
	if err != nil {
		t.Fatal(err)
	}
	recB, err := Add(gitDir, "leaseB", []string{"/.claude/skills/b"})
	if err != nil {
		t.Fatal(err)
	}

	if err := Remove(recA, "leaseA"); err != nil {
		t.Fatal(err)
	}
	mid, _ := os.ReadFile(file)
	if strings.Contains(string(mid), "/.claude/skills/a") {
		t.Error("lease A's pattern survived its own removal")
	}
	if !strings.Contains(string(mid), "/.claude/skills/b") {
		t.Error("removing lease A took lease B's block with it")
	}

	if err := Remove(recB, "leaseB"); err != nil {
		t.Fatal(err)
	}
	final, _ := os.ReadFile(file)
	if string(final) != original {
		t.Errorf("after both removals: %q, want %q", final, original)
	}
}

// TestTwoLeasesRestoreAnAbsentFileInEitherOrder is the ordering case the
// per-record Existed flag could not answer: the second lease to register always
// finds the file present, so revoking it last must still leave the repository
// with no exclude file at all.
func TestTwoLeasesRestoreAnAbsentFileInEitherOrder(t *testing.T) {
	tests := []struct {
		name  string
		first string
	}{
		{"first lease revoked first", "leaseA"},
		{"second lease revoked first", "leaseB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitDir := filepath.Join(t.TempDir(), ".git")
			file := filepath.Join(gitDir, "info", "exclude")

			recs := map[string]*Record{}
			for _, id := range []string{"leaseA", "leaseB"} {
				rec, err := Add(gitDir, id, []string{"/.claude/skills/" + id})
				if err != nil {
					t.Fatalf("Add %s: %v", id, err)
				}
				recs[id] = rec
			}

			second := "leaseB"
			if tt.first == "leaseB" {
				second = "leaseA"
			}
			for _, id := range []string{tt.first, second} {
				if err := Remove(recs[id], id); err != nil {
					t.Fatalf("Remove %s: %v", id, err)
				}
			}

			if got, err := os.ReadFile(file); err == nil {
				t.Errorf("exclude file survived both removals with %q; it was not there to begin with", got)
			}
		})
	}
}

// TestRemoveKeepsAFileTheUserAlreadyHad guards the other direction: barracks
// never deletes a file it did not bring into being.
func TestRemoveKeepsAFileTheUserAlreadyHad(t *testing.T) {
	tests := []struct {
		name     string
		original string
	}{
		{"empty file", ""},
		{"whitespace only", "\n\n"},
		{"real content", "# mine\n*.log\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitDir := filepath.Join(t.TempDir(), ".git")
			file := filepath.Join(gitDir, "info", "exclude")
			if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte(tt.original), 0o644); err != nil {
				t.Fatal(err)
			}

			rec, err := Add(gitDir, "lease1", []string{"/.claude/skills/react"})
			if err != nil {
				t.Fatal(err)
			}
			if err := Remove(rec, "lease1"); err != nil {
				t.Fatal(err)
			}

			got, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("barracks deleted a file the user already had: %v", err)
			}
			if string(got) != tt.original {
				t.Errorf("not restored byte for byte\n got: %q\nwant: %q", got, tt.original)
			}
		})
	}
}

func TestOnlyBarracksBlocks(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"empty", "", true},
		{"one block", "# barracks:a begin\n/p\n# barracks:a end\n", true},
		{"two blocks", "# barracks:a begin\n/p\n# barracks:a end\n# barracks:b begin\n/q\n# barracks:b end\n", true},
		{"block plus user content", "# barracks:a begin\n/p\n# barracks:a end\n*.log\n", false},
		{"user content only", "*.log\n", false},
		{"unterminated block", "# barracks:a begin\n/p\n", false},
		{"stray end fence", "# barracks:a end\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := onlyBarracksBlocks(tt.content); got != tt.want {
				t.Errorf("onlyBarracksBlocks(%q) = %v, want %v", tt.content, got, tt.want)
			}
		})
	}
}

func TestRemoveToleratesAHandEditedFile(t *testing.T) {
	gitDir := filepath.Join(t.TempDir(), ".git")
	file := filepath.Join(gitDir, "info", "exclude")

	rec, err := Add(gitDir, "lease1", []string{"/.claude/skills/react"})
	if err != nil {
		t.Fatal(err)
	}
	// The user removed the block themselves.
	handEdited := "# I cleaned this up\n"
	if err := os.WriteFile(file, []byte(handEdited), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Remove(rec, "lease1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got, _ := os.ReadFile(file)
	if string(got) != handEdited {
		t.Errorf("Remove clobbered a hand-edited file: %q", got)
	}
}

func TestRemoveHandlesMissingInputs(t *testing.T) {
	if err := Remove(nil, "lease1"); err != nil {
		t.Errorf("Remove(nil) = %v, want nil", err)
	}
	if err := Remove(&Record{}, "lease1"); err != nil {
		t.Errorf("Remove with an empty record = %v, want nil", err)
	}
	if err := Remove(&Record{File: filepath.Join(t.TempDir(), "gone")}, "lease1"); err != nil {
		t.Errorf("Remove of a vanished file = %v, want nil", err)
	}
}

func TestPattern(t *testing.T) {
	tests := []struct {
		name    string
		root    string
		abs     string
		want    string
		wantErr bool
	}{
		{"nested path", "/repo", "/repo/.claude/skills/react", "/.claude/skills/react", false},
		{"direct child", "/repo", "/repo/thing", "/thing", false},
		{"outside the repository", "/repo", "/elsewhere/thing", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Pattern(tt.root, tt.abs)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Pattern = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Pattern: %v", err)
			}
			if got != tt.want {
				t.Errorf("Pattern = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripBlock(t *testing.T) {
	content := "a\n# barracks:x begin\n/p1\n/p2\n# barracks:x end\nb\n"
	got, found := stripBlock(content, "x")
	if !found {
		t.Fatal("block not found")
	}
	if got != "a\nb\n" {
		t.Errorf("stripBlock = %q, want %q", got, "a\nb\n")
	}

	if _, found := stripBlock("a\nb\n", "x"); found {
		t.Error("stripBlock reported a block that is not there")
	}
}
