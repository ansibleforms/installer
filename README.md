# AnsibleForms installer

[![CI](https://img.shields.io/github/actions/workflow/status/ansibleforms/installer/ci.yml?branch=main&label=CI)](https://github.com/ansibleforms/installer/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-GPL--3.0-blue)](LICENSE)
[![Docs](https://img.shields.io/badge/docs-ansibleforms.com-informational)](https://ansibleforms.com)

The installer that sets up [AnsibleForms](https://github.com/ansibleforms/ansibleforms) on a Linux server in one command, written in Go.
It can install a controller (the main application), or a Runner (the component that runs playbooks), or upgrade them.

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
[CHANGELOG.md](CHANGELOG.md) lists what changed in the installer itself with each release, beside the version it installs.

## Commands

The install script keeps the binary as `ansibleforms-installer` in `/usr/local/bin` for root, else in `~/.local/bin`.
Run `ansibleforms-installer --help` for every flag; without a command, the installer installs.
Through sudo, give its full path, `sudo /usr/local/bin/ansibleforms-installer`: sudo's `secure_path` often leaves out `/usr/local/bin`.

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

The installer upgrades any AnsibleForms already running on a server, whether it was installed manually or by the installer, on Docker or Podman, from the Helm chart, or natively. Run `upgrade` (or `install`) on that server: the installer detects it, shows what it found, and takes over one it didn't install only once you confirm. It then upgrades it in place: it adds any new settings to `.env`, backs up the files it changes first, and rolls them back if the upgrade fails. Installations it can't take over are listed with the reason and what to do by hand.

## Contributing

Contributions are welcome. Start with these files:

- [CONTRIBUTING.md](CONTRIBUTING.md): how to build the installer, propose a change, and how it is released
- [CHANGELOG.md](CHANGELOG.md): the changes in every installer release
- [SECURITY.md](SECURITY.md): how to report a security issue

## License

[GPL-3.0](LICENSE).
