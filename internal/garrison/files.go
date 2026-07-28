package garrison

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// State is what barracks found at a path it has a record for.
type State string

const (
	// StateMissing means nothing is there. The record says there should be.
	StateMissing State = "missing"
	// StateMatches means the file is byte-for-byte what barracks wrote.
	StateMatches State = "matches"
	// StateModified means a regular file is there with different content. This
	// is the case the committed tier has to get right: it is somebody's edit,
	// and neither overwriting nor deleting it silently is acceptable.
	StateModified State = "modified"
	// StateNotRegular means the path is a directory, a symlink, or a device.
	// barracks wrote a plain file there, so whatever this is, it is not ours.
	StateNotRegular State = "not-a-regular-file"
	// StateUnreadable means the path could not be inspected.
	StateUnreadable State = "unreadable"
)

// Modes are the only two file modes barracks writes. git tracks the executable
// bit and nothing else, so vendoring anything more precise would be a promise
// the round trip through a clone cannot keep.
const (
	modeFile = 0o644
	modeExec = 0o755
)

func (f File) mode() os.FileMode {
	if f.Exec {
		return modeExec
	}
	return modeFile
}

// digest is the sha256 of a file's contents, hex-encoded and prefixed with the
// algorithm so a future format change is legible rather than ambiguous.
func digest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// inspect classifies what is on disk against a recorded file.
func inspect(abs string, rec File) (State, string) {
	fi, err := os.Lstat(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return StateMissing, ""
		}
		return StateUnreadable, err.Error()
	}
	if !fi.Mode().IsRegular() {
		return StateNotRegular, describeMode(fi.Mode())
	}
	got, err := digest(abs)
	if err != nil {
		return StateUnreadable, err.Error()
	}
	if got != rec.Digest {
		return StateModified, ""
	}
	return StateMatches, ""
}

func describeMode(m os.FileMode) string {
	switch {
	case m&os.ModeSymlink != 0:
		return "a symlink"
	case m.IsDir():
		return "a directory"
	default:
		return "not a regular file"
	}
}

// scan reads a skill directory in the store and returns the files a garrison
// would vendor from it.
//
// Only regular files are vendored. A symlink inside a source skill is skipped
// and reported: the committed tier exists so that a clone works with no barracks
// and no store, and a link is the one thing that cannot survive that trip.
func scan(storeDir string) (files []File, skipped []string, err error) {
	err = filepath.WalkDir(storeDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(storeDir, p)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			return nil
		}
		slashRel := filepath.ToSlash(rel)
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		if !info.Mode().IsRegular() {
			skipped = append(skipped, slashRel+" ("+describeMode(info.Mode())+")")
			return nil
		}
		sum, derr := digest(p)
		if derr != nil {
			return derr
		}
		files = append(files, File{
			Path:   slashRel,
			Digest: sum,
			Exec:   info.Mode().Perm()&0o111 != 0,
		})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, skipped, nil
}

// relDir renders an absolute path inside root as the slash-separated relative
// form the lockfile stores.
func relDir(root, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("path %s is outside the repository", abs)
	}
	return rel, nil
}

// missingDirs lists the directories that would have to be created to reach dir,
// shallowest first, without creating anything.
//
// Only directories barracks actually had to make are recorded, so removal can
// never prune one that was already there.
func missingDirs(dir string) ([]string, error) {
	var missing []string
	for p := filepath.Clean(dir); ; p = filepath.Dir(p) {
		if _, err := os.Stat(p); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect %s: %w", p, err)
		}
		missing = append(missing, p)
		if parent := filepath.Dir(p); parent == p {
			break
		}
	}
	for i, j := 0, len(missing)-1; i < j; i, j = i+1, j-1 {
		missing[i], missing[j] = missing[j], missing[i]
	}
	return missing, nil
}

// deepestFirst orders directories so children are pruned before parents.
func deepestFirst(dirs []string) []string {
	out := append([]string(nil), dirs...)
	sort.Slice(out, func(i, j int) bool {
		di := strings.Count(out[i], "/")
		dj := strings.Count(out[j], "/")
		if di == dj {
			return out[i] > out[j]
		}
		return di > dj
	})
	return out
}

// copyFile writes src to dst atomically, with the mode the record asks for.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".barracks-tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}
