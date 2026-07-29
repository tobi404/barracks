# Development

Working on barracks itself: the make targets, what CI runs, the two install paths that are
not Homebrew or `go install`, and how a release is cut. Back to the [README](../README.md).

---

## Build and test

```bash
make build            # build both binaries into ./bin
make test             # go test -race ./...
make cover            # coverage report
make cover-check      # coverage report, failing under 80%
make lint             # gofmt check and go vet
make fmt-check        # gofmt check on its own, exactly as CI runs it
make fmt              # rewrite the tree with gofmt, what fmt-check tells you to run
make vet              # go vet on its own, exactly as CI runs it
make golangci         # the full linter suite, at the version CI pins
make release-check    # validate .goreleaser.yaml
make release-snapshot # build every platform into ./dist and publish nothing
```

Tests never touch the network: they build local git repository fixtures on disk and point
sources at those paths.

Every push to `main` and every pull request runs the same checks in GitHub Actions
(`.github/workflows/ci.yml`): build, `go vet`, `gofmt`, `go test -race` with an 80%
coverage floor, and `golangci-lint`. The test job runs on both Linux and macOS, because
`internal/proc` decides whether a lease's owner is still alive differently per operating
system. No secrets are needed to run it.

## Installing from source or from a release archive

The two install paths the [README](../README.md#install) does not cover.

### From a clone

```bash
make install
```

### Direct download

Prebuilt binaries for macOS and Linux, `arm64` and `amd64`, are attached to every
[release](https://github.com/tobi404/barracks/releases), with a `checksums.txt` beside
them. Each archive contains both binaries:

```bash
tar xzf barracks_<version>_<os>_<arch>.tar.gz
install barracks brk /usr/local/bin/
```

Verify what you downloaded before installing it:

```bash
sha256sum --check --ignore-missing checksums.txt   # shasum -a 256 -c on macOS
```

Homebrew on Linux is not an install path: barracks ships as a Homebrew *cask*, and casks
are macOS-only. On Linux use `go install` or the tarball.

---

## Releasing

A release is one push of one tag. Everything else - building four platforms, attaching
archives and checksums to a GitHub release, and publishing the Homebrew cask - happens in
`.github/workflows/release.yml`, which runs GoReleaser at the version pinned in
`.goreleaser-version`.

### Tag format

`vMAJOR.MINOR.PATCH`, semver, always with the leading `v`:

```text
v1.0.0        a release
v1.2.0-rc.1   a prerelease - any suffix marks the GitHub release as a prerelease
```

A prerelease still publishes its GitHub release with archives and checksums, but the
Homebrew cask is not pushed, so `brew install tobi404/tap/barracks` keeps serving the
latest stable version.

The workflow triggers on `v*` and nothing else. The tag is the version: `barracks
--version` reports it, and the Homebrew cask points at that tag's archives.

```bash
git tag -a v1.0.0 -m "v1.0.0"
git push origin v1.0.0
```

Never move or re-push a tag. Homebrew and the checksums file both pin to it; publish a
new patch version instead.

### One-time setup

The tap repository [`tobi404/homebrew-tap`](https://github.com/tobi404/homebrew-tap)
exists - public, default branch `main`. GoReleaser commits `Casks/barracks.rb` into it on
each release, which is what makes `brew install tobi404/tap/barracks` resolve. Nothing
needs to be done to it by hand.

One thing is still outstanding, and it cannot be created from this repository:

**The repository secret `HOMEBREW_TAP_GITHUB_TOKEN`.** The workflow's built-in
`GITHUB_TOKEN` is scoped to this repository alone and cannot push to the tap, so the
cask needs a token of its own:

- Create a **fine-grained personal access token** (GitHub → Settings → Developer settings
  → Personal access tokens → Fine-grained tokens).
- Resource owner `tobi404`, repository access **only** `tobi404/homebrew-tap`.
- Repository permission **Contents: Read and write**. Nothing else - no access to
  `tobi404/barracks`, no organisation or account permissions.
- Add it to this repository as a secret named exactly `HOMEBREW_TAP_GITHUB_TOKEN`
  (Settings → Secrets and variables → Actions → New repository secret).

The release workflow checks it before it builds anything and fails immediately if the
secret is missing, or if the token no longer reaches the tap with write access - an
expired or revoked token is caught here, not halfway through publishing. A tag pushed
without a working token produces no release at all, rather than a release nobody can
`brew install`. The token expires, so re-issuing it is part of releasing.

### Verifying without releasing

```bash
make release-check      # is .goreleaser.yaml valid and free of deprecated keys?
make release-snapshot   # build all four platforms, archive, checksum, render the cask
```

`make release-snapshot` writes to `./dist` and publishes nothing: the archives, the
`checksums.txt`, and `dist/homebrew/Casks/barracks.rb` are exactly what a real tag would
produce, minus the upload.

Both targets reach GoReleaser through `go run`, which builds the pinned version from
source. GoReleaser's own Go requirement is much newer than the one barracks compiles
against, so that only works because `GOTOOLCHAIN` defaults to `auto` on a developer
machine and quietly fetches a newer toolchain. The release workflow cannot: `setup-go`
pins `GOTOOLCHAIN=local`, so it installs the pinned GoReleaser *binary* and passes it in
as `make release-check GORELEASER=goreleaser`. Do the same when you want a local run to
match what a tag will do:

```bash
GOTOOLCHAIN=local make release-check GORELEASER=/path/to/goreleaser
```
