# AnsibleForms installer

[![License](https://img.shields.io/badge/license-GPL--3.0-blue)](LICENSE)
[![Docs](https://img.shields.io/badge/docs-ansibleforms.com-informational)](https://ansibleforms.com)

The installer that sets up [AnsibleForms](https://github.com/ansibleforms/ansibleforms) on a Linux server in one command, written in Go.
It installs a controller (the whole application) or an RTE Runner for a controller elsewhere, then upgrades and maintains it.

```bash
curl -fsSL https://ansibleforms.com/install | sh
```

## Features

A full-screen wizard asks one question at a time, with Esc to go back and a review before anything changes.

- **Eleven languages:** the app's languages, taken from the server's locale or chosen first
- **Release notes first:** breaking changes and deprecations are shown before anything changes
- **Controller or Runner:** the whole application, or a Runner that joins it with a join token
- **Docker, Podman, Kubernetes or native:** Compose, the Helm chart, or systemd services, no containers
- **Container engine included:** installs Docker or Podman when neither is on the server, after asking
- **Safe upgrades:** backs up the database first and rolls back if the new version does not respond
- **Finds what is there:** takes over an AnsibleForms installed by hand, by Helm or natively
- **Unattended mode:** every question has a flag, and `--yes` answers the rest with their defaults

## Versions

The installer's version follows the app's: each AnsibleForms release comes with an installer of the same version, which installs it.
Every release on the [Releases](https://github.com/ansibleforms/installer/releases) page has binaries for Linux on amd64 and arm64.

## Commands

Run `ansibleforms-installer --help` for every flag; without a command, the installer installs.

| Command | What it does |
|---|---|
| `install` | install AnsibleForms, as a controller or a Runner (the default) |
| `upgrade` | upgrade to a newer version, showing its release notes first |
| `status` | show the installation and its containers or pods |
| `backup` | back up the database now |
| `release-notes` | show the release notes of a version |
| `uninstall` | remove the installation, keeping its data unless asked otherwise |
| `version` | print the installer's version |

## Existing installations

The installer remembers what it installed in `installer.json` (`/etc/ansibleforms` for root, else `~/.config/ansibleforms`).
Without that file, `install`, `upgrade` and `status` look for an AnsibleForms already on the server, whatever installed it.

| Found | How it is found | Taken over when |
|---|---|---|
| Docker or Podman | containers of the app or RTE image, grouped by compose project | the folder holds the docker repository's compose file and `.env` |
| Kubernetes | Helm releases of the `ansibleforms` chart, Deployments and StatefulSets of its images | it is a release of the chart; its current values become its values file |
| Native | systemd services that run AnsibleForms with Node.js | they follow the installer's layout: `releases/`, `current`, `app.env`, `rte.env` |
| No engine | the app or an RTE answering on a usual port (443, 3000, 3001, 8000, 8001, 8010, 8443) | never: it runs from source or by hand |

The installer shows what it found and offers to take it over and upgrade it, or to stop; it saves `installer.json` only once you confirm.
An installation it cannot take over is listed with the reason and what to do by hand, and a second installation beside it needs your explicit confirmation.
Only the app's and the RTE's images count: a MySQL container named after AnsibleForms is not an installation.

On Docker or Podman, an upgrade adds to `.env` the keys the new version needs, with secrets made as a fresh install makes them.
It replaces `data/seed.yaml` when it is one the docker repository shipped, saves both files beside them first, and a rollback restores them.

## Contributing

Contributions are welcome. Start with these files:

- [CONTRIBUTING.md](CONTRIBUTING.md): how to build the installer, propose a change and open a pull request
- [SECURITY.md](SECURITY.md): how to report a security issue

## License

[GPL-3.0](LICENSE).
