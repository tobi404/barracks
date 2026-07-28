// Package testutil builds local git fixtures for tests.
//
// Every barracks test works against repositories created on disk here. Nothing
// in the test suite touches the network.
package testutil

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"os"
)

// Skill is a skill directory to create inside a fixture repository.
type Skill struct {
	// Path is the repo-relative directory, e.g. "skills/react".
	Path string
	// Body is the SKILL.md contents. A default is used when empty.
	Body string
}

// GitRepo is a fixture repository on disk.
type GitRepo struct {
	Dir    string
	Branch string
}

// NewGitRepo initialises an empty git repository at dir.
func NewGitRepo(t *testing.T, dir string) *GitRepo {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.email", "fixture@barracks.test")
	run(t, dir, "config", "user.name", "barracks fixture")
	return &GitRepo{Dir: dir, Branch: "main"}
}

// NewSkillRepo initialises a repository containing the given skills and
// commits them.
func NewSkillRepo(t *testing.T, dir string, skills ...Skill) *GitRepo {
	t.Helper()
	r := NewGitRepo(t, dir)
	r.AddSkills(t, skills...)
	r.Commit(t, "add skills")
	return r
}

// AddSkills writes skill directories into the work tree without committing.
func (r *GitRepo) AddSkills(t *testing.T, skills ...Skill) {
	t.Helper()
	for _, s := range skills {
		body := s.Body
		if body == "" {
			body = "---\nname: " + filepath.Base(s.Path) + "\n---\n\nfixture skill\n"
		}
		WriteFile(t, filepath.Join(r.Dir, filepath.FromSlash(s.Path), "SKILL.md"), body)
	}
}

// Commit stages everything and commits.
func (r *GitRepo) Commit(t *testing.T, message string) string {
	t.Helper()
	run(t, r.Dir, "add", "-A")
	run(t, r.Dir, "commit", "-q", "-m", message)
	return r.Head(t)
}

// Head returns the current commit SHA.
func (r *GitRepo) Head(t *testing.T) string {
	t.Helper()
	return run(t, r.Dir, "rev-parse", "HEAD")
}

// Tag creates a lightweight tag at HEAD.
func (r *GitRepo) Tag(t *testing.T, name string) {
	t.Helper()
	run(t, r.Dir, "tag", name)
}

// Status returns porcelain status output, empty when the work tree is clean.
func (r *GitRepo) Status(t *testing.T) string {
	t.Helper()
	return run(t, r.Dir, "status", "--porcelain")
}

// ExcludeFile is the path of .git/info/exclude in this repository.
func (r *GitRepo) ExcludeFile() string {
	return filepath.Join(r.Dir, ".git", "info", "exclude")
}

// ReadExclude returns the current .git/info/exclude contents, or "" if absent.
func (r *GitRepo) ReadExclude(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(r.ExcludeFile())
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read exclude: %v", err)
	}
	return string(b)
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// WriteFile creates parent directories and writes a file.
func WriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// WriteScript writes an executable shell script and returns its path. Tests use
// it to stand in for an agent's own CLI, so `barracks run -- <agent>` can be
// exercised without that agent being installed.
func WriteScript(t *testing.T, path, body string) string {
	t.Helper()
	WriteFile(t, path, "#!/bin/sh\n"+body+"\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	return path
}

// MkDir creates a directory and its parents. Tests use it to plant the
// configuration directories barracks detects targets by.
func MkDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

// IsSymlink reports whether path is a symbolic link.
func IsSymlink(t *testing.T, path string) bool {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeSymlink != 0
}

// Exists reports whether anything exists at path, following no links.
func Exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// Entries lists the names directly inside dir, or nil when dir is absent.
func Entries(t *testing.T, dir string) []string {
	t.Helper()
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range des {
		out = append(out, d.Name())
	}
	return out
}
