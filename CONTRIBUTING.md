# Contributing to the AnsibleForms installer

Thanks for helping out. This repository is the installer that sets up AnsibleForms on a server.
The application itself lives in [ansibleforms/ansibleforms](https://github.com/ansibleforms/ansibleforms).

## Table of contents

- [What lives where](#what-lives-where)
- [Building and testing](#building-and-testing)
- [Texts and languages](#texts-and-languages)
- [Pull requests](#pull-requests)

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

---

## Building and testing

The installer needs Go 1.27 or newer; the build is a single static binary.

```bash
go build -o ansibleforms-installer .
go vet ./...
go test ./...
```

Try the wizard with `./ansibleforms-installer install --version <a released version>`: nothing changes before its review screen.

---

## Texts and languages

Every text the installer shows is in `internal/i18n/locales/`, with `en.json` as the reference for the others.
A text has one sentence per line, at most 66 characters wide, and the same `{placeholders}` in every language.
A new or changed text goes into all eleven catalogs, with the app's own words for its menus and settings.

---

## Pull requests

Open one pull request per change, against `main`, with a title such as `fix: short summary of the change`.
Describe what changes for the person installing, and how you tried it.
