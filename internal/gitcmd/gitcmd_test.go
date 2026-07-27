package gitcmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobi404/barracks/internal/testutil"
)

func ctx() context.Context { return context.Background() }

func TestRepoRootAndGitDir(t *testing.T) {
	dir := t.TempDir()
	repo := testutil.NewSkillRepo(t, filepath.Join(dir, "repo"),
		testutil.Skill{Path: "skills/react"})

	g := Git{}
	root, err := g.RepoRoot(ctx(), repo.Dir)
	if err != nil {
		t.Fatalf("RepoRoot: %v", err)
	}
	if filepath.Base(root) != "repo" {
		t.Errorf("RepoRoot = %q, want the repository root", root)
	}

	// From a subdirectory the answer is the same root.
	sub := filepath.Join(repo.Dir, "skills", "react")
	rootFromSub, err := g.RepoRoot(ctx(), sub)
	if err != nil {
		t.Fatalf("RepoRoot from a subdirectory: %v", err)
	}
	if rootFromSub != root {
		t.Errorf("RepoRoot from a subdirectory = %q, want %q", rootFromSub, root)
	}

	gitDir, err := g.GitDir(ctx(), repo.Dir)
	if err != nil {
		t.Fatalf("GitDir: %v", err)
	}
	if filepath.Base(gitDir) != ".git" {
		t.Errorf("GitDir = %q, want the .git directory", gitDir)
	}
}

func TestRepoRootOutsideARepository(t *testing.T) {
	// A temp dir under /tmp is not inside any repository.
	plain := t.TempDir()
	g := Git{}
	if _, err := g.RepoRoot(ctx(), plain); !errors.Is(err, ErrNotARepository) {
		t.Errorf("RepoRoot outside a repo = %v, want ErrNotARepository", err)
	}
	if _, err := g.GitDir(ctx(), plain); !errors.Is(err, ErrNotARepository) {
		t.Errorf("GitDir outside a repo = %v, want ErrNotARepository", err)
	}
}

func TestResolveRef(t *testing.T) {
	dir := t.TempDir()
	repo := testutil.NewSkillRepo(t, filepath.Join(dir, "src"),
		testutil.Skill{Path: "skills/react"})
	first := repo.Head(t)
	repo.Tag(t, "v1.0.0")

	repo.AddSkills(t, testutil.Skill{Path: "skills/css"})
	second := repo.Commit(t, "add css")

	g := Git{}
	tests := []struct {
		name string
		ref  string
		want string
	}{
		{"empty ref resolves HEAD", "", second},
		{"branch name", "main", second},
		{"tag", "v1.0.0", first},
		{"full refs path", "refs/heads/main", second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := g.ResolveRef(ctx(), repo.Dir, tt.ref)
			if err != nil {
				t.Fatalf("ResolveRef(%q): %v", tt.ref, err)
			}
			if got != tt.want {
				t.Errorf("ResolveRef(%q) = %q, want %q", tt.ref, got, tt.want)
			}
		})
	}
}

func TestResolveRefOnAnnotatedTagPeelsToTheCommit(t *testing.T) {
	dir := t.TempDir()
	repo := testutil.NewSkillRepo(t, filepath.Join(dir, "src"),
		testutil.Skill{Path: "skills/react"})
	commit := repo.Head(t)

	g := Git{}
	if _, err := g.Run(ctx(), repo.Dir, "tag", "-a", "v2.0.0", "-m", "release"); err != nil {
		t.Fatal(err)
	}
	got, err := g.ResolveRef(ctx(), repo.Dir, "v2.0.0")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if got != commit {
		t.Errorf("ResolveRef on an annotated tag = %q, want the commit %q", got, commit)
	}
}

func TestResolveRefUnknown(t *testing.T) {
	dir := t.TempDir()
	repo := testutil.NewSkillRepo(t, filepath.Join(dir, "src"),
		testutil.Skill{Path: "skills/react"})

	g := Git{}
	_, err := g.ResolveRef(ctx(), repo.Dir, "no-such-branch")
	if err == nil {
		t.Fatal("resolving a missing ref should fail")
	}
	if !strings.Contains(err.Error(), "no-such-branch") {
		t.Errorf("error %v should name the ref that was not found", err)
	}
}

func TestEnsureMirrorAndExportTree(t *testing.T) {
	dir := t.TempDir()
	repo := testutil.NewSkillRepo(t, filepath.Join(dir, "src"),
		testutil.Skill{Path: "skills/react", Body: "# react"},
		testutil.Skill{Path: "skills/css", Body: "# css"},
	)
	commit := repo.Head(t)

	g := Git{}
	mirror := filepath.Join(dir, "mirror.git")
	if err := g.EnsureMirror(ctx(), mirror, repo.Dir); err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}
	if !g.HasCommit(ctx(), mirror, commit) {
		t.Fatal("mirror does not contain the commit it just fetched")
	}
	if g.HasCommit(ctx(), mirror, "0000000000000000000000000000000000000000") {
		t.Error("HasCommit reported a commit that is not there")
	}

	// EnsureMirror is reusable: a second call on an existing mirror works.
	if err := g.EnsureMirror(ctx(), mirror, repo.Dir); err != nil {
		t.Fatalf("second EnsureMirror: %v", err)
	}

	dest := filepath.Join(dir, "export")
	if err := g.ExportTree(ctx(), mirror, commit, dest); err != nil {
		t.Fatalf("ExportTree: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dest, "skills", "react", "SKILL.md"))
	if err != nil {
		t.Fatalf("exported tree missing a file: %v", err)
	}
	if string(body) != "# react" {
		t.Errorf("exported content = %q, want %q", body, "# react")
	}
	// The export is a plain tree, never a live repository.
	if _, err := os.Stat(filepath.Join(dest, ".git")); err == nil {
		t.Error("exported tree contains a .git directory; the store must hold plain files")
	}
}

func TestExportTreePreservesExecutableBits(t *testing.T) {
	dir := t.TempDir()
	repo := testutil.NewSkillRepo(t, filepath.Join(dir, "src"),
		testutil.Skill{Path: "skills/react"})
	script := filepath.Join(repo.Dir, "skills", "react", "run.sh")
	testutil.WriteFile(t, script, "#!/bin/sh\necho hi\n")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	commit := repo.Commit(t, "add script")

	g := Git{}
	mirror := filepath.Join(dir, "mirror.git")
	if err := g.EnsureMirror(ctx(), mirror, repo.Dir); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "export")
	if err := g.ExportTree(ctx(), mirror, commit, dest); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dest, "skills", "react", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("executable bit lost in export: %v", fi.Mode())
	}
}

func TestExportTreeCarriesSymlinks(t *testing.T) {
	dir := t.TempDir()
	repo := testutil.NewSkillRepo(t, filepath.Join(dir, "src"),
		testutil.Skill{Path: "skills/react", Body: "# react"})
	if err := os.Symlink("SKILL.md", filepath.Join(repo.Dir, "skills", "react", "alias.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	commit := repo.Commit(t, "add symlink")

	g := Git{}
	mirror := filepath.Join(dir, "mirror.git")
	if err := g.EnsureMirror(ctx(), mirror, repo.Dir); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "export")
	if err := g.ExportTree(ctx(), mirror, commit, dest); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dest, "skills", "react", "alias.md")
	if !testutil.IsSymlink(t, link) {
		t.Fatal("symlink was not exported as a symlink")
	}
}

func TestExportTreeUnknownCommit(t *testing.T) {
	dir := t.TempDir()
	repo := testutil.NewSkillRepo(t, filepath.Join(dir, "src"),
		testutil.Skill{Path: "skills/react"})
	g := Git{}
	mirror := filepath.Join(dir, "mirror.git")
	if err := g.EnsureMirror(ctx(), mirror, repo.Dir); err != nil {
		t.Fatal(err)
	}
	err := g.ExportTree(ctx(), mirror, "0000000000000000000000000000000000000000", filepath.Join(dir, "export"))
	if err == nil {
		t.Fatal("exporting an unknown commit should fail")
	}
}

func TestSafeJoinRejectsEscapes(t *testing.T) {
	tests := []struct {
		name    string
		entry   string
		wantErr bool
	}{
		{"plain file", "skills/react/SKILL.md", false},
		{"nested", "a/b/c.txt", false},
		{"parent traversal", "../outside", true},
		{"deep traversal", "a/../../outside", true},
		{"absolute", "/etc/passwd", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := safeJoin("/root", tt.entry)
			if tt.wantErr && err == nil {
				t.Fatalf("safeJoin(%q) = nil error, want a refusal", tt.entry)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("safeJoin(%q) = %v, want nil", tt.entry, err)
			}
		})
	}
}

func TestRunReportsGitStderr(t *testing.T) {
	g := Git{}
	_, err := g.Run(ctx(), t.TempDir(), "cat-file", "-e", "nope")
	if err == nil {
		t.Fatal("a failing git command should return an error")
	}
	if !strings.Contains(err.Error(), "cat-file") {
		t.Errorf("error %v should include the git command that failed", err)
	}
}

func TestPickRef(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		want    string
		wantErr bool
	}{
		{
			name: "single ref",
			out:  "abc123\trefs/heads/main",
			want: "abc123",
		},
		{
			name: "annotated tag prefers the peeled commit",
			out:  "tagobj\trefs/tags/v1\npeeled\trefs/tags/v1^{}",
			want: "peeled",
		},
		{
			name: "first match when several",
			out:  "aaa\trefs/heads/main\nbbb\trefs/tags/main",
			want: "aaa",
		},
		{
			name:    "no refs",
			out:     "",
			wantErr: true,
		},
		{
			name:    "malformed lines only",
			out:     "garbage",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pickRef(tt.out, "")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("pickRef = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("pickRef: %v", err)
			}
			if got != tt.want {
				t.Errorf("pickRef = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCustomBinary(t *testing.T) {
	if got := (Git{}).bin(); got != "git" {
		t.Errorf("default binary = %q, want git", got)
	}
	if got := (Git{Bin: "/usr/bin/git"}).bin(); got != "/usr/bin/git" {
		t.Errorf("custom binary = %q, want /usr/bin/git", got)
	}
}
