package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Digest fingerprints a skill directory's contents.
//
// It exists so an upgrade can tell "this skill changed" from "the repository
// commit moved". Two directories with the same digest are byte-identical -
// same file names, same contents, same executable bits, same symlink targets -
// whatever commit they were exported from.
//
// filepath.WalkDir visits in lexical order, so the digest is stable across
// runs and machines.
func Digest(dir string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		switch {
		case d.IsDir():
			fmt.Fprintf(h, "d\x00%s\x00", rel)
		case d.Type()&fs.ModeSymlink != 0:
			dest, err := os.Readlink(p)
			if err != nil {
				return err
			}
			fmt.Fprintf(h, "l\x00%s\x00%s\x00", rel, filepath.ToSlash(dest))
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return err
			}
			sum, err := fileSum(p)
			if err != nil {
				return err
			}
			// Only the executable bits are recorded: git tracks nothing finer,
			// so a umask difference between two exports must not read as a
			// content change.
			fmt.Fprintf(h, "f\x00%s\x00%d\x00%s\x00", rel, info.Mode().Perm()&0o111, sum)
		default:
			fmt.Fprintf(h, "?\x00%s\x00", rel)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("fingerprint %s: %w", dir, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Index maps skill names to the discovered skills, for set comparison.
func Index(skills []Skill) map[string]Skill {
	out := make(map[string]Skill, len(skills))
	for _, s := range skills {
		out[s.Name] = s
	}
	return out
}
