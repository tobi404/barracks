// Package buildcheck holds tests for the repository's own check targets. The
// Makefile is the single implementation of every check CI runs, so a hole in a
// target is a hole in CI, and nothing else in the tree would catch it.
package buildcheck

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	formattedSrc   = "package probe\n\nfunc F() int {\n\treturn 1\n}\n"
	unformattedSrc = "package probe\n\nfunc  F( ) int {\nreturn 1\n}\n"
	unparseableSrc = "package probe\n\nfunc F() int {\n\treturn 1\n"
)

// sandbox writes the real Makefile into a scratch tree containing probe.go, so
// the target under test is the one CI runs rather than a copy of its logic.
func sandbox(t *testing.T, src string) string {
	t.Helper()
	makefile, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	dir := t.TempDir()
	files := map[string]string{
		"Makefile":               string(makefile),
		".golangci-lint-version": "v0.0.0\n",
		"probe.go":               src,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// fmtCheck runs `make fmt-check` in dir. A nil env inherits this process's.
func fmtCheck(t *testing.T, dir string, env []string) (string, int) {
	t.Helper()
	makeBin, err := exec.LookPath("make")
	if err != nil {
		t.Skipf("make is not on PATH: %v", err)
	}
	cmd := exec.Command(makeBin, "fmt-check")
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("run make fmt-check: %v", err)
	}
	return string(out), cmd.ProcessState.ExitCode()
}

func requireGofmt(t *testing.T) string {
	t.Helper()
	gofmt, err := exec.LookPath("gofmt")
	if err != nil {
		t.Skipf("gofmt is not on PATH: %v", err)
	}
	return gofmt
}

func TestFmtCheckAcceptsFormattedCode(t *testing.T) {
	requireGofmt(t)
	out, code := fmtCheck(t, sandbox(t, formattedSrc), nil)
	if code != 0 {
		t.Fatalf("fmt-check failed on gofmt-clean code: exit %d\n%s", code, out)
	}
}

func TestFmtCheckRejectsUnformattedCode(t *testing.T) {
	requireGofmt(t)
	out, code := fmtCheck(t, sandbox(t, unformattedSrc), nil)
	if code == 0 {
		t.Fatalf("fmt-check passed on unformatted code\n%s", out)
	}
	if !strings.Contains(out, "::error::") {
		t.Errorf("output carries no ::error:: annotation, so CI would not annotate the failure\n%s", out)
	}
	if !strings.Contains(out, "probe.go") {
		t.Errorf("output does not name the offending file\n%s", out)
	}
}

// TestFmtCheckRejectsUnparseableCode covers the case that reads as success: a
// file gofmt cannot parse produces a non-zero status and an empty stdout list,
// so a target that only inspects the list reports a clean tree it never checked.
func TestFmtCheckRejectsUnparseableCode(t *testing.T) {
	gofmt := requireGofmt(t)
	dir := sandbox(t, unparseableSrc)

	listed, err := exec.Command(gofmt, "-l", dir).Output()
	if err == nil {
		t.Fatalf("premise broken: gofmt succeeded on unparseable source")
	}
	if len(listed) != 0 {
		t.Fatalf("premise broken: gofmt listed %q on stdout for unparseable source", listed)
	}

	out, code := fmtCheck(t, dir, nil)
	if code == 0 {
		t.Fatalf("fmt-check passed while gofmt could not parse the tree\n%s", out)
	}
}

// TestFmtCheckRejectsMissingGofmt covers the other silent-success path: with no
// gofmt to run, the command substitution yields an empty list and exit 127.
func TestFmtCheckRejectsMissingGofmt(t *testing.T) {
	dir := sandbox(t, formattedSrc)
	out, code := fmtCheck(t, dir, []string{"PATH=" + t.TempDir()})
	if code == 0 {
		t.Fatalf("fmt-check passed with no gofmt on PATH\n%s", out)
	}
}
