// Package buildinfo carries the identity of a build: the release it was cut
// from, the commit it was built at, and when it was built.
//
// The three variables are stamped at link time - by LDFLAGS in the Makefile for
// local builds, and by .goreleaser.yaml for released ones. Nothing here is ever
// edited by hand. When they are unstamped (a plain `go build`, or
// `go install ...@v1.2.3`) the values are recovered from the module build info
// the toolchain embeds, so a binary that barracks itself did not link still
// reports something true rather than a placeholder.
package buildinfo

import (
	"fmt"
	"runtime/debug"
)

// Stamped at link time with
// -X github.com/tobi404/barracks/internal/buildinfo.Version=... (and Commit, Date).
var (
	Version string
	Commit  string
	Date    string
)

// unknown is what every field falls back to when neither the linker nor the
// toolchain could say. It is deliberately not a plausible-looking placeholder:
// a reader must be able to tell "nobody knows" from "v0.0.0".
const unknown = "unknown"

// devVersion marks a binary built outside a release.
const devVersion = "dev"

// String renders the single line `barracks --version` prints.
func String() string {
	v, c, d := resolve(debug.ReadBuildInfo)
	return fmt.Sprintf("%s (commit %s, built %s)", v, c, d)
}

// resolve fills in whatever the linker did not stamp from the module build
// info. read is a parameter so tests can supply build info of their own.
func resolve(read func() (*debug.BuildInfo, bool)) (version, commit, date string) {
	version, commit, date = Version, Commit, Date

	if version == "" || commit == "" || date == "" {
		if bi, ok := read(); ok && bi != nil {
			if version == "" {
				version = moduleVersion(bi.Main.Version)
			}
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					if commit == "" {
						commit = s.Value
					}
				case "vcs.time":
					if date == "" {
						date = s.Value
					}
				}
			}
		}
	}

	return orUnknown(version, devVersion), orUnknown(commit, unknown), orUnknown(date, unknown)
}

// moduleVersion translates the toolchain's module version into a release tag.
// A binary built from a working tree rather than a published module carries
// "(devel)" or nothing at all; neither is a version anyone can install.
func moduleVersion(v string) string {
	if v == "(devel)" {
		return ""
	}
	return v
}

func orUnknown(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
