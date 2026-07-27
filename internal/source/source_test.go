package source

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		wantHost    string
		wantOwner   string
		wantRepo    string
		wantRef     string
		wantSubpath string
		wantClone   string
	}{
		{
			name: "gh shorthand", raw: "gh:owner/repo",
			wantHost: "github.com", wantOwner: "owner", wantRepo: "repo",
			wantClone: "https://github.com/owner/repo.git",
		},
		{
			name: "bare host path", raw: "github.com/owner/repo",
			wantHost: "github.com", wantOwner: "owner", wantRepo: "repo",
			wantClone: "https://github.com/owner/repo.git",
		},
		{
			name: "https url with .git", raw: "https://github.com/owner/repo.git",
			wantHost: "github.com", wantOwner: "owner", wantRepo: "repo",
			wantClone: "https://github.com/owner/repo.git",
		},
		{
			name: "https url without .git", raw: "https://gitlab.com/group/repo",
			wantHost: "gitlab.com", wantOwner: "group", wantRepo: "repo",
			wantClone: "https://gitlab.com/group/repo",
		},
		{
			name: "scp style ssh", raw: "git@github.com:owner/repo.git",
			wantHost: "github.com", wantOwner: "owner", wantRepo: "repo",
			wantClone: "git@github.com:owner/repo.git",
		},
		{
			name: "pinned tag", raw: "gh:owner/repo#v1.2.0",
			wantHost: "github.com", wantOwner: "owner", wantRepo: "repo", wantRef: "v1.2.0",
			wantClone: "https://github.com/owner/repo.git",
		},
		{
			name: "ref and subpath", raw: "gh:owner/repo#main:skills",
			wantHost: "github.com", wantOwner: "owner", wantRepo: "repo",
			wantRef: "main", wantSubpath: "skills",
			wantClone: "https://github.com/owner/repo.git",
		},
		{
			name: "ssh with ref and subpath", raw: "git@github.com:owner/repo.git#main:packages/skills",
			wantHost: "github.com", wantOwner: "owner", wantRepo: "repo",
			wantRef: "main", wantSubpath: "packages/skills",
			wantClone: "git@github.com:owner/repo.git",
		},
		{
			name: "nested group", raw: "gitlab.com/group/sub/repo",
			wantHost: "gitlab.com", wantOwner: "group/sub", wantRepo: "repo",
			wantClone: "https://gitlab.com/group/sub/repo.git",
		},
		{
			name: "subpath is cleaned", raw: "gh:owner/repo#main:/skills/",
			wantHost: "github.com", wantOwner: "owner", wantRepo: "repo",
			wantRef: "main", wantSubpath: "skills",
			wantClone: "https://github.com/owner/repo.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.raw)
			if err != nil {
				t.Fatalf("Parse(%q) failed: %v", tt.raw, err)
			}
			if got.Host != tt.wantHost {
				t.Errorf("host = %q, want %q", got.Host, tt.wantHost)
			}
			if got.Owner != tt.wantOwner {
				t.Errorf("owner = %q, want %q", got.Owner, tt.wantOwner)
			}
			if got.Repo != tt.wantRepo {
				t.Errorf("repo = %q, want %q", got.Repo, tt.wantRepo)
			}
			if got.Ref != tt.wantRef {
				t.Errorf("ref = %q, want %q", got.Ref, tt.wantRef)
			}
			if got.Subpath != tt.wantSubpath {
				t.Errorf("subpath = %q, want %q", got.Subpath, tt.wantSubpath)
			}
			if got.CloneURL != tt.wantClone {
				t.Errorf("clone url = %q, want %q", got.CloneURL, tt.wantClone)
			}
			if got.Raw != tt.raw {
				t.Errorf("raw = %q, want %q", got.Raw, tt.raw)
			}
		})
	}
}

func TestParseLocalPaths(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "myskills")

	for _, raw := range []string{repo, "file://" + repo} {
		got, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse(%q): %v", raw, err)
		}
		if got.Host != HostLocal {
			t.Errorf("host = %q, want %q", got.Host, HostLocal)
		}
		if got.Repo != "myskills" {
			t.Errorf("repo = %q, want myskills", got.Repo)
		}
		if got.CloneURL != repo {
			t.Errorf("clone url = %q, want %q", got.CloneURL, repo)
		}
		if len(got.Owner) != 12 {
			t.Errorf("owner digest = %q, want 12 hex chars", got.Owner)
		}
	}
}

func TestLocalSourcesInDifferentParentsDoNotCollide(t *testing.T) {
	a, err := Parse(filepath.Join(t.TempDir(), "skills"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse(filepath.Join(t.TempDir(), "skills"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Owner == b.Owner {
		t.Fatalf("same-named fixtures in different parents share store key %q", a.Owner)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"no owner", "gh:repo"},
		{"unrecognised", "just-a-word"},
		{"only a ref", "#main"},
		{"escaping subpath", "gh:owner/repo#main:../../etc"},
		{"url without host", "https:///owner/repo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := Parse(tt.raw); err == nil {
				t.Fatalf("Parse(%q) = %+v, want error", tt.raw, got)
			}
		})
	}
}

func TestStoreAndMirrorKeys(t *testing.T) {
	src, err := Parse("gh:owner/repo#main:skills")
	if err != nil {
		t.Fatal(err)
	}
	commit := "0123456789abcdef0123456789abcdef01234567"

	wantStore := filepath.Join("github.com", "owner", "repo@"+commit)
	if got := src.StoreKey(commit); got != wantStore {
		t.Errorf("StoreKey = %q, want %q", got, wantStore)
	}
	wantMirror := filepath.Join("github.com", "owner", "repo.git")
	if got := src.MirrorKey(); got != wantMirror {
		t.Errorf("MirrorKey = %q, want %q", got, wantMirror)
	}
	// The mirror is shared across commits: one clone serves every pin.
	if src.MirrorKey() != mustParse(t, "gh:owner/repo#v9").MirrorKey() {
		t.Error("mirror key changed with the ref; it must not")
	}
}

func TestIdent(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"gh:owner/repo", "github.com/owner/repo"},
		{"gh:owner/repo#main", "github.com/owner/repo#main"},
		{"gh:owner/repo#main:skills", "github.com/owner/repo#main:skills"},
	}
	for _, tt := range tests {
		if got := mustParse(t, tt.raw).Ident(); got != tt.want {
			t.Errorf("Ident(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestValidateRejectsPathTraversal(t *testing.T) {
	tests := []struct {
		name string
		src  Source
		ok   bool
	}{
		{"clean", Source{Host: "github.com", Owner: "owner", Repo: "repo"}, true},
		{"nested owner", Source{Host: "gitlab.com", Owner: "group/sub", Repo: "repo"}, true},
		{"traversal in owner", Source{Host: "github.com", Owner: "..", Repo: "repo"}, false},
		{"traversal in repo", Source{Host: "github.com", Owner: "owner", Repo: ".."}, false},
		{"separator in repo", Source{Host: "github.com", Owner: "owner", Repo: "a/b"}, false},
		{"empty host", Source{Owner: "owner", Repo: "repo"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.src.Validate()
			if tt.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("Validate() = nil, want error")
			}
		})
	}
}

func TestSHARecognition(t *testing.T) {
	full := "0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		ref       string
		commitish bool
		fullSHA   bool
	}{
		{full, true, true},
		{"0123456", true, false},
		{"main", false, false},
		{"v1.2.0", false, false},
		{"", false, false},
	}
	for _, tt := range tests {
		if got := IsCommitish(tt.ref); got != tt.commitish {
			t.Errorf("IsCommitish(%q) = %v, want %v", tt.ref, got, tt.commitish)
		}
		if got := IsFullSHA(tt.ref); got != tt.fullSHA {
			t.Errorf("IsFullSHA(%q) = %v, want %v", tt.ref, got, tt.fullSHA)
		}
	}
}

func TestParseErrorMentionsTheInput(t *testing.T) {
	_, err := Parse("nonsense")
	if err == nil || !strings.Contains(err.Error(), "nonsense") {
		t.Fatalf("error %v should name the offending source", err)
	}
}

func mustParse(t *testing.T, raw string) Source {
	t.Helper()
	s, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse(%q): %v", raw, err)
	}
	return s
}
