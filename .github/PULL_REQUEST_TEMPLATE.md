<!--
The TITLE must be a Conventional Commit - `fix: ...`, `feat: ...` - because it becomes the
squash commit. See CONTRIBUTING.md.
-->

## What this changes

<!-- One or two sentences. If it follows an AnsibleForms change, link that pull request. -->

## Checklist

- [ ] Branch is named `<type>/<description>` and the title is a Conventional Commit
- [ ] `gofmt -l .` prints nothing, and `go vet ./...` and `go test ./...` pass
- [ ] Tried the changed flow with the built binary on a test machine, or the change does not affect it
- [ ] New or changed texts are in every language under `internal/i18n/locales`
