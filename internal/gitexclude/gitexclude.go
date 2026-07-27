// Package gitexclude registers spawned paths in .git/info/exclude.
//
// Never .gitignore: that file is committed, and a spawn must not show up in the
// user's diff. Registration is a fenced block keyed by lease ID, so removing it
// restores the file byte-for-byte.
package gitexclude

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Record is what a lease stores so its block can be removed exactly.
type Record struct {
	File string `yaml:"file"`
	// Existed reports whether the exclude file was there before barracks wrote
	// to it. If it was not, removal deletes it again.
	Existed bool `yaml:"existed"`
	// AddedNewline reports whether barracks had to terminate the previous last
	// line. If it did, removal strips that newline back off.
	AddedNewline bool     `yaml:"added_newline"`
	Patterns     []string `yaml:"patterns"`
}

func begin(id string) string { return "# barracks:" + id + " begin" }
func end(id string) string   { return "# barracks:" + id + " end" }

// Add appends a fenced block of repo-root-relative patterns to
// <gitDir>/info/exclude and returns the record needed to undo it.
func Add(gitDir, leaseID string, patterns []string) (*Record, error) {
	if len(patterns) == 0 {
		return nil, nil
	}
	infoDir := filepath.Join(gitDir, "info")
	file := filepath.Join(infoDir, "exclude")

	original, existed, err := read(file)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", infoDir, err)
	}

	var b strings.Builder
	b.WriteString(original)
	addedNewline := false
	if original != "" && !strings.HasSuffix(original, "\n") {
		b.WriteString("\n")
		addedNewline = true
	}
	b.WriteString(begin(leaseID))
	b.WriteString("\n")
	for _, p := range patterns {
		b.WriteString(p)
		b.WriteString("\n")
	}
	b.WriteString(end(leaseID))
	b.WriteString("\n")

	if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", file, err)
	}
	return &Record{File: file, Existed: existed, AddedNewline: addedNewline, Patterns: patterns}, nil
}

// Remove deletes the block for leaseID and restores the file to exactly what it
// was before Add ran.
func Remove(rec *Record, leaseID string) error {
	if rec == nil || rec.File == "" {
		return nil
	}
	content, existed, err := read(rec.File)
	if err != nil {
		return err
	}
	if !existed {
		return nil
	}

	stripped, found := stripBlock(content, leaseID)
	if !found {
		// Somebody edited the block away already; leave the file alone rather
		// than guessing at what to remove.
		return nil
	}
	if rec.AddedNewline {
		stripped = strings.TrimSuffix(stripped, "\n")
	}
	if !rec.Existed && strings.TrimSpace(stripped) == "" {
		if err := os.Remove(rec.File); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return os.WriteFile(rec.File, []byte(stripped), 0o644)
}

// stripBlock removes the fenced region, inclusive of both fences.
func stripBlock(content, leaseID string) (string, bool) {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	inBlock, found := false, false
	for _, line := range lines {
		switch strings.TrimSpace(line) {
		case begin(leaseID):
			inBlock, found = true, true
			continue
		case end(leaseID):
			if inBlock {
				inBlock = false
				continue
			}
		}
		if inBlock {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n"), found
}

func read(file string) (content string, existed bool, err error) {
	b, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read %s: %w", file, err)
	}
	return string(b), true, nil
}

// Pattern converts an absolute path inside root into an exclude pattern
// anchored at the repository root.
func Pattern(root, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("path %s is outside the repository", abs)
	}
	return "/" + rel, nil
}
