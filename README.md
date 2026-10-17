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
- **Docker, Podman or Kubernetes:** Docker Compose on Docker or Podman, or the Helm chart on a cluster
- **Container engine included:** installs Docker or Podman when neither is on the server, after asking
- **Safe upgrades:** backs up the database first and rolls back if the new version does not respond
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

## Contributing

Contributions are welcome. Start with these files:

- [CONTRIBUTING.md](CONTRIBUTING.md): how to build the installer, propose a change and open a pull request
- [SECURITY.md](SECURITY.md): how to report a security issue

## License

[GPL-3.0](LICENSE).
