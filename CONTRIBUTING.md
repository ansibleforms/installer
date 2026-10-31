# Contributing to the AnsibleForms installer

Thanks for helping out. This repository is the installer that sets up AnsibleForms on a server.
The application itself lives in [ansibleforms/ansibleforms](https://github.com/ansibleforms/ansibleforms).

## Table of contents

- [What lives where](#what-lives-where)
- [Building and testing](#building-and-testing)
- [Texts and languages](#texts-and-languages)
- [Pull requests](#pull-requests)
- [Releasing](#releasing)
- [Following app releases](#following-app-releases)

---

## What lives where

The installer is one Go program; each package under `internal/` holds one part of it.

| Path | Holds |
|---|---|
| `main.go` | the flags, the help and the start of a command |
| `internal/cli` | the commands: the install wizard, upgrade, status, backup, uninstall |
| `internal/compose` | the Docker Compose setups of a controller and of a Runner |
| `internal/kube` | the Helm chart on Kubernetes |
| `internal/releases` | the versions and release notes, read from GitHub |
| `internal/system` | running commands, finding and installing Docker or Podman |
| `internal/tui`, `internal/ui` | the full-screen wizard, and the plain output when it does not fit |
| `internal/i18n` | the texts, one JSON catalog per language |
| `internal/version` | `version.App`, the AnsibleForms version installed by default and the installer's own |

---

## Building and testing

The installer needs Go 1.27 or newer; the build is a single static binary.

```bash
go build -o ansibleforms-installer .
go vet ./...
go test ./...
```

Try the wizard with `./ansibleforms-installer install --version <a released version>`: nothing changes before its review screen.
The binary installs `version.App` from `internal/version/version.go` by default, and `./ansibleforms-installer version` prints it.

---

## Texts and languages

Every text the installer shows is in `internal/i18n/locales/`, with `en.json` as the reference for the others.
A text has one sentence per line, at most 66 characters wide, and the same `{placeholders}` in every language.
A new or changed text goes into all eleven catalogs, with the app's own words for its menus and settings.

---

## Pull requests

Open one pull request per change, against `main`, with a title such as `fix: short summary of the change`.
Describe what changes for the person installing, and how you tried it.

---

## Releasing

The installer is versioned with AnsibleForms: installer v7.4.2 installs AnsibleForms 7.4.2 by default.
That version is `version.App` in `internal/version/version.go`, one line `const App = "x.y.z"` that a test checks.
A release is the *Follow app release* pull request moving it to a new AnsibleForms version.
Once that is merged, the release workflow tests and builds the installer for Linux on amd64 and arm64,
and creates the release `v<version>` with the binaries and their checksums, as ansibleforms[bot].

It runs after CI, not alongside it, and does nothing unless CI finished green on a push to `main` or `release/<major>.x`.
Merging when `version.App` already has a release is a no-op, which is what makes it safe to leave running on every push.
Only the highest version is GitHub's Latest release, so a patch of an older line does not take it from the newer one.
To release by hand what `version.App` says, after a failed run: Actions, *Release*, Run workflow, on that branch.

A change to the installer keeps the version and goes out with the next AnsibleForms release.
Say what it changes for the person installing under `## Unreleased` at the top of `CHANGELOG.md`.
Changes that do not reach the binary, to CI, to tests or to the documentation, need no entry.

---

## Following app releases

An AnsibleForms release sends an `app-release` dispatch here, and the *Follow app release* workflow takes it from there.
It opens a pull request on the line of that major (`main`, or `release/<major>.x`), titled `build: default to AnsibleForms x.y.z`.
The pull request moves `version.App` to the release and turns `## Unreleased` into that version's changelog entry.
It merges itself once **CI passed** is green, and the release workflow releases the installer.
A new major version opens an issue instead, because moving the installer across a major takes real work.
To catch up on a release by hand: Actions, *Follow app release*, Run workflow, with the version.

Both workflows use a token of the `ansibleforms-release` GitHub App: its client ID in the `RELEASE_APP_CLIENT_ID` variable
and its private key in the `RELEASE_APP_PRIVATE_KEY` secret. A pull request opened with `GITHUB_TOKEN` would start no checks.
