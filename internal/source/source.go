// Package source parses the skill-source syntax barracks accepts and turns it
// into a stable, content-addressable identity.
package source

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// HostLocal is the pseudo-host used for sources that point at a filesystem
// path rather than a remote. It keeps local fixtures in the same store shape as
// anything fetched over the network.
const HostLocal = "local"

// Source is a parsed, not-yet-resolved skill source.
//
// Host/Owner/Repo form the store key. Ref is whatever the user asked to track
// (branch, tag, or commit) and may be empty, meaning "the default branch".
// Subpath restricts skill discovery to a directory inside the repo.
type Source struct {
	Raw      string `yaml:"raw"`
	Host     string `yaml:"host"`
	Owner    string `yaml:"owner"`
	Repo     string `yaml:"repo"`
	Ref      string `yaml:"ref,omitempty"`
	Subpath  string `yaml:"subpath,omitempty"`
	CloneURL string `yaml:"clone_url"`
}

var (
	scpLike     = regexp.MustCompile(`^(?:([^@/]+)@)?([^:/@]+):(.+)$`)
	fullSHA     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	shortSHA    = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	safeSegment = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// IsCommitish reports whether ref already looks like a concrete commit SHA.
func IsCommitish(ref string) bool { return shortSHA.MatchString(ref) }

// IsFullSHA reports whether ref is a full 40-character object name.
func IsFullSHA(ref string) bool { return fullSHA.MatchString(ref) }

// Parse accepts every documented source form:
//
//	gh:owner/repo
//	github.com/owner/repo
//	https://github.com/owner/repo.git
//	git@github.com:owner/repo.git
//	./path/to/local/repo              (filesystem, mainly for fixtures)
//
// Any form may carry a "#ref" suffix, optionally "#ref:subpath".
func Parse(raw string) (Source, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return Source{}, fmt.Errorf("empty source")
	}

	base, ref, subpath, err := splitRefSpec(trimmed)
	if err != nil {
		return Source{}, err
	}

	src, err := parseBase(base)
	if err != nil {
		return Source{}, fmt.Errorf("parse source %q: %w", raw, err)
	}
	src.Raw = trimmed
	src.Ref = ref
	src.Subpath = subpath
	return src, nil
}

// splitRefSpec peels "#ref" and "#ref:subpath" off the end of a source.
//
// The split is on the first '#', so scp-style URLs (which contain ':' before
// any '#') are unambiguous.
func splitRefSpec(raw string) (base, ref, subpath string, err error) {
	base = raw
	if i := strings.Index(raw, "#"); i >= 0 {
		base, ref = raw[:i], raw[i+1:]
	}
	if base == "" {
		return "", "", "", fmt.Errorf("source %q has no repository part", raw)
	}
	if i := strings.Index(ref, ":"); i >= 0 {
		ref, subpath = ref[:i], ref[i+1:]
	}
	ref = strings.TrimSpace(ref)
	subpath = strings.Trim(strings.TrimSpace(subpath), "/")
	if subpath != "" {
		clean := path.Clean(subpath)
		if clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
			return "", "", "", fmt.Errorf("source %q has an escaping subpath %q", raw, subpath)
		}
		subpath = clean
	}
	return base, ref, subpath, nil
}

func parseBase(base string) (Source, error) {
	switch {
	case strings.HasPrefix(base, "gh:"):
		return fromOwnerRepo("github.com", strings.TrimPrefix(base, "gh:"))

	case strings.HasPrefix(base, "file://"):
		return fromLocalPath(strings.TrimPrefix(base, "file://"))

	case isLocalPath(base):
		return fromLocalPath(base)

	case strings.Contains(base, "://"):
		return fromURL(base)

	case scpLike.MatchString(base) && !strings.Contains(base, "://"):
		m := scpLike.FindStringSubmatch(base)
		return fromOwnerRepoWithURL(m[2], m[3], base)

	default:
		// host/owner/repo, e.g. github.com/owner/repo
		parts := strings.SplitN(base, "/", 2)
		if len(parts) != 2 || !strings.Contains(parts[0], ".") {
			return Source{}, fmt.Errorf("unrecognised source form (expected gh:owner/repo, host/owner/repo, a URL, or a local path)")
		}
		return fromOwnerRepo(parts[0], parts[1])
	}
}

func isLocalPath(base string) bool {
	return strings.HasPrefix(base, "/") ||
		strings.HasPrefix(base, "./") ||
		strings.HasPrefix(base, "../") ||
		base == "." ||
		base == ".."
}

func fromURL(base string) (Source, error) {
	u, err := url.Parse(base)
	if err != nil {
		return Source{}, err
	}
	if u.Scheme == "file" {
		return fromLocalPath(u.Path)
	}
	if u.Host == "" {
		return Source{}, fmt.Errorf("URL %q has no host", base)
	}
	return fromOwnerRepoWithURL(u.Host, strings.TrimPrefix(u.Path, "/"), base)
}

func fromOwnerRepo(host, ownerRepo string) (Source, error) {
	src, err := splitOwnerRepo(host, ownerRepo)
	if err != nil {
		return Source{}, err
	}
	src.CloneURL = fmt.Sprintf("https://%s/%s/%s.git", src.Host, src.Owner, src.Repo)
	return src, nil
}

func fromOwnerRepoWithURL(host, ownerRepo, cloneURL string) (Source, error) {
	src, err := splitOwnerRepo(host, ownerRepo)
	if err != nil {
		return Source{}, err
	}
	src.CloneURL = cloneURL
	return src, nil
}

func splitOwnerRepo(host, ownerRepo string) (Source, error) {
	ownerRepo = strings.Trim(ownerRepo, "/")
	ownerRepo = strings.TrimSuffix(ownerRepo, ".git")
	parts := strings.Split(ownerRepo, "/")
	if len(parts) < 2 {
		return Source{}, fmt.Errorf("expected owner/repo, got %q", ownerRepo)
	}
	repo := parts[len(parts)-1]
	owner := strings.Join(parts[:len(parts)-1], "/")
	if owner == "" || repo == "" {
		return Source{}, fmt.Errorf("expected owner/repo, got %q", ownerRepo)
	}
	return Source{Host: strings.ToLower(host), Owner: owner, Repo: repo}, nil
}

// fromLocalPath makes a filesystem repository addressable in the same store
// shape as a remote. The owner slot becomes a short digest of the absolute
// parent path, so two same-named fixtures never collide.
func fromLocalPath(p string) (Source, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return Source{}, err
	}
	abs = filepath.Clean(abs)
	repo := filepath.Base(abs)
	if repo == "" || repo == string(filepath.Separator) || repo == "." {
		return Source{}, fmt.Errorf("local source %q has no directory name", p)
	}
	sum := sha256.Sum256([]byte(filepath.Dir(abs)))
	return Source{
		Host:     HostLocal,
		Owner:    hex.EncodeToString(sum[:])[:12],
		Repo:     repo,
		CloneURL: abs,
	}, nil
}

// StoreKey is the store-relative directory for this source at a given commit.
func (s Source) StoreKey(commit string) string {
	return filepath.Join(s.Host, filepath.FromSlash(s.Owner), s.Repo+"@"+commit)
}

// MirrorKey is the mirrors-relative directory of the bare clone for this repo.
// It deliberately omits the commit: one mirror serves every commit.
func (s Source) MirrorKey() string {
	return filepath.Join(s.Host, filepath.FromSlash(s.Owner), s.Repo+".git")
}

// Ident is a short human label, e.g. "github.com/tobi404/skills#main:skills".
func (s Source) Ident() string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "%s/%s/%s", s.Host, s.Owner, s.Repo)
	if s.Ref != "" {
		fmt.Fprintf(b, "#%s", s.Ref)
	}
	if s.Subpath != "" {
		fmt.Fprintf(b, ":%s", s.Subpath)
	}
	return b.String()
}

// Validate guards the fields that end up as filesystem path segments.
//
// Host, owner, and repo are concatenated into a store path, so a segment that
// is "." or ".." - both of which are otherwise made of legal characters - would
// let a crafted source write outside the store.
func (s Source) Validate() error {
	if !safePathSegment(s.Host) {
		return fmt.Errorf("unsafe host %q", s.Host)
	}
	for _, seg := range strings.Split(s.Owner, "/") {
		if !safePathSegment(seg) {
			return fmt.Errorf("unsafe owner %q", s.Owner)
		}
	}
	if !safePathSegment(s.Repo) {
		return fmt.Errorf("unsafe repository name %q", s.Repo)
	}
	return nil
}

func safePathSegment(seg string) bool {
	if seg == "." || seg == ".." {
		return false
	}
	return safeSegment.MatchString(seg)
}
