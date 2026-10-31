# Changelog

## Unreleased

## 7.4.2

AnsibleForms 7.4.2.

### Changed

- **Defaults to AnsibleForms 7.4.2**: `install` and `upgrade` go to it unless `--version` says
  otherwise. See the [7.4.2 release notes](https://github.com/ansibleforms/ansibleforms/releases/tag/7.4.2).

## 7.4.1

AnsibleForms 7.4.1.

### Changed

- **Defaults to AnsibleForms 7.4.1**: `install` and `upgrade` go to it unless `--version` says
  otherwise. See the [7.4.1 release notes](https://github.com/ansibleforms/ansibleforms/releases/tag/7.4.1).

## 7.4.0

AnsibleForms 7.4.0.

### Changed

- **Defaults to AnsibleForms 7.4.0**: `install` and `upgrade` go to it unless `--version` says
  otherwise. See the [7.4.0 release notes](https://github.com/ansibleforms/ansibleforms/releases/tag/7.4.0).

## 7.3.0

AnsibleForms 7.3.0, the installer's first release.

### Added

- **A full-screen wizard** that installs a controller (the whole application) or an RTE Runner
  that joins one with a join token, one question at a time, with a review before anything changes.
- **Docker, Podman, Kubernetes or native:** Docker Compose, the Helm chart (a Runner with the
  chart's runner mode too), or systemd services without containers. Docker or Podman is
  installed when neither is on the server, after asking.
- **Upgrades** that show the release notes first, back up the database and roll back when the
  new version does not respond. On Docker or Podman, `.env` gets the keys the new version needs
  and a shipped `data/seed.yaml` is replaced, both saved beside them first.
- **Existing installations are found** (Docker, Podman, Helm, native, or only answering on a
  port) and taken over, including one started from `docker-compose.yaml`. A 7.2 Runner in
  database mode moves to a socket mode on its upgrade. Workloads managed by Argo CD are left alone.
- **Inbound Runners over https**, with a pinned self-signed certificate of their own address.
- **Eleven languages**, the app's, and an unattended mode: every question has a flag, and
  `--yes` answers the rest with their defaults.
- **Defaults to AnsibleForms 7.3.0**. See the
  [7.3.0 release notes](https://github.com/ansibleforms/ansibleforms/releases/tag/7.3.0).
