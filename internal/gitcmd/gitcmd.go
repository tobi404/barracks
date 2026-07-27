// Package gitcmd is a thin, explicit wrapper over the git binary. barracks
// shells out rather than embedding a git implementation so that whatever
// credentials, proxies, and transports the user's git already has keep working.
package gitcmd

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNotARepository is returned by RepoRoot when dir is outside any work tree.
var ErrNotARepository = errors.New("not a git repository")

// Git runs git commands. The zero value uses "git" from PATH.
type Git struct {
	Bin string
}

func (g Git) bin() string {
	if g.Bin != "" {
		return g.Bin
	}
	return "git"
}

// Run executes git in dir and returns trimmed stdout.
func (g Git) Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, g.bin(), args...)
	cmd.Dir = dir
	// Keep git non-interactive: a hung credential prompt in a CLI that runs on
	// every command would be worse than a clean failure.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ADVICE=0")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

// RepoRoot returns the work-tree root containing dir.
func (g Git) RepoRoot(ctx context.Context, dir string) (string, error) {
	out, err := g.Run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil || out == "" {
		return "", ErrNotARepository
	}
	return out, nil
}

// GitDir returns the .git directory for the repository containing dir.
func (g Git) GitDir(ctx context.Context, dir string) (string, error) {
	out, err := g.Run(ctx, dir, "rev-parse", "--absolute-git-dir")
	if err != nil || out == "" {
		return "", ErrNotARepository
	}
	return out, nil
}

// ResolveRef turns a ref (branch, tag, or commit) into a full commit SHA
// without cloning. An empty ref resolves the remote's default branch.
func (g Git) ResolveRef(ctx context.Context, cloneURL, ref string) (string, error) {
	args := []string{"ls-remote", "--exit-code", cloneURL}
	if ref == "" {
		args = append(args, "HEAD")
	} else {
		// Ask for the exact ref plus the tag/head spellings so "v1.2.0" and
		// "main" both resolve without the caller knowing which they gave. The
		// "^{}" spellings are what an annotated tag peels to; ls-remote only
		// emits them when a pattern matches them explicitly.
		args = append(args, ref, ref+"^{}", "refs/heads/"+ref, "refs/tags/"+ref, "refs/tags/"+ref+"^{}")
	}
	out, err := g.Run(ctx, "", args...)
	if err != nil {
		return "", fmt.Errorf("resolve %q in %s: %w", refOrHead(ref), cloneURL, err)
	}
	sha, err := pickRef(out, ref)
	if err != nil {
		return "", fmt.Errorf("resolve %q in %s: %w", refOrHead(ref), cloneURL, err)
	}
	return sha, nil
}

func refOrHead(ref string) string {
	if ref == "" {
		return "HEAD"
	}
	return ref
}

// pickRef reads ls-remote output, preferring an annotated tag's peeled commit
// over the tag object itself so a pinned tag materialises a tree, not a tag.
func pickRef(out, ref string) (string, error) {
	var first, peeled string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		sha, name := fields[0], fields[1]
		if strings.HasSuffix(name, "^{}") {
			peeled = sha
			continue
		}
		if first == "" {
			first = sha
		}
	}
	if peeled != "" {
		return peeled, nil
	}
	if first != "" {
		return first, nil
	}
	return "", fmt.Errorf("no such ref")
}

// EnsureMirror creates (or reuses) a bare mirror and fetches cloneURL into it.
// The mirror is shared by every loadout using the same repository.
func (g Git) EnsureMirror(ctx context.Context, mirrorDir, cloneURL string) error {
	if _, err := os.Stat(filepath.Join(mirrorDir, "HEAD")); err != nil {
		if err := os.MkdirAll(mirrorDir, 0o755); err != nil {
			return fmt.Errorf("create mirror dir: %w", err)
		}
		if _, err := g.Run(ctx, "", "init", "--bare", "--quiet", mirrorDir); err != nil {
			return err
		}
	}
	_, err := g.Run(ctx, mirrorDir, "fetch", "--quiet", "--tags", "--force", cloneURL,
		"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
	return err
}

// HasCommit reports whether the mirror already contains commit.
func (g Git) HasCommit(ctx context.Context, mirrorDir, commit string) bool {
	_, err := g.Run(ctx, mirrorDir, "cat-file", "-e", commit+"^{commit}")
	return err == nil
}

// ExportTree extracts the tree at commit from a mirror into destDir.
//
// It streams `git archive` through a tar reader rather than checking out a work
// tree, so nothing in the store is ever a live git repository.
func (g Git) ExportTree(ctx context.Context, mirrorDir, commit, destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create export dir: %w", err)
	}
	cmd := exec.CommandContext(ctx, g.bin(), "archive", "--format=tar", commit)
	cmd.Dir = mirrorDir
	var errb bytes.Buffer
	cmd.Stderr = &errb
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	extractErr := extractTar(stdout, destDir)
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	if extractErr != nil {
		return extractErr
	}
	if waitErr != nil {
		return fmt.Errorf("git archive %s: %w: %s", commit, waitErr, strings.TrimSpace(errb.String()))
	}
	return nil
}

func extractTar(r io.Reader, destDir string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		target, err := safeJoin(destDir, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeFile(target, tr, os.FileMode(hdr.Mode).Perm()); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Reject links escaping the export root; the store must stay
			// self-contained so link-safety checks on recall stay meaningful.
			if !linkStaysInside(destDir, hdr.Name, hdr.Linkname) {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		}
	}
}

func writeFile(path string, r io.Reader, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o644
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return err
	}
	return f.Close()
}

// linkStaysInside reports whether a symlink entry resolves back inside the
// export root.
//
// An absolute link target has to be rejected before the join: filepath.Join
// swallows a leading separator, so joining "skills/react" with "/etc/passwd"
// yields "skills/react/etc/passwd" and an escape would look safe.
func linkStaysInside(root, name, linkname string) bool {
	if linkname == "" || strings.HasPrefix(linkname, "/") {
		return false
	}
	link := filepath.FromSlash(linkname)
	if filepath.IsAbs(link) {
		return false
	}
	_, err := safeJoin(root, filepath.Join(filepath.Dir(name), link))
	return err == nil
}

func safeJoin(root, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q escapes the export root", name)
	}
	return filepath.Join(root, clean), nil
}
