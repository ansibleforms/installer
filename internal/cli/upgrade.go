package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ansibleforms/installer/internal/compose"
	"github.com/ansibleforms/installer/internal/detect"
	"github.com/ansibleforms/installer/internal/i18n"
	"github.com/ansibleforms/installer/internal/kube"
	"github.com/ansibleforms/installer/internal/native"
	"github.com/ansibleforms/installer/internal/releases"
	"github.com/ansibleforms/installer/internal/state"
	"github.com/ansibleforms/installer/internal/ui"
)

// ─── upgrade ──────────────────────────────────────────────────────────────────

// upgrade shows what the versions skipped bring, asks to go on (and to confirm breaking
// changes), backs up, upgrades, and offers a rollback when the new version does not answer.
// Without installer.json, an AnsibleForms installed another way is found and taken over first.
//
// Args:
//
//	ctx: the context, to cancel.
//	f: the flags.
//
// Errors:
//
//	When a step fails, the person stops, or the upgrade crosses a major version.
func upgrade(ctx context.Context, f Flags) error {
	st, err := state.Load()
	var found []detect.Install
	if errors.Is(err, state.ErrNone) {
		// no installer.json : an AnsibleForms installed another way, taken over once the
		// language is chosen
		if found = findInstalls(ctx); len(found) > 0 {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	// the language first, as the install's wizard asks it
	askLang := ui.Fullscreen && f.Lang == ""
	if askLang {
		langLabels := func() []string {
			return []string{stepName("language"), stepName("version"), stepName("notes"), stepName("upgrade")}
		}
		ui.Steps(langLabels()...)
		ui.Section(stepName("language"))
		if err := chooseLanguage(func() { ui.Relabel(langLabels()...) }); err != nil {
			return err
		}
	}
	if st == nil {
		if st, err = offerFound(ctx, f, found, false); err != nil {
			return err
		}
	}
	from, err := releases.ParseVersion(st.Version)
	if err != nil {
		return err
	}
	to, err := pickUpgrade(ctx, f, from)
	if err != nil {
		return err
	}
	if to.Compare(from) == 0 {
		ui.Done(i18n.T("upgrade.upToDate", "version", from.String()))
		return nil
	}
	if to.Compare(from) < 0 {
		return errors.New(i18n.T("upgrade.noDowngrade", "from", from.String(), "to", to.String()))
	}
	if to.Major != from.Major {
		return errors.New(i18n.T("upgrade.major", "from", from.String(), "to", to.String()))
	}
	if st.Role == compose.RoleRunner {
		ui.Warn(i18n.T("upgrade.runnerOrder"))
	}

	labels := func() []string {
		l := []string{stepName("version"), stepName("notes"), stepName("upgrade")}
		if askLang {
			l = append([]string{stepName("language")}, l...)
		}
		return l
	}
	ui.Steps(labels()...)
	ui.Section(stepName("version"))
	ui.Answered(from.String() + " → " + to.String())
	ui.Section(stepName("notes"))
	entries, err := releases.FetchChangelog(ctx, to)
	if err != nil {
		ui.Warn(err.Error())
	}
	skipped := releases.Between(entries, from, to)
	if len(skipped) > 0 {
		ui.Explain(i18n.T("upgrade.notesHelp", "from", from.String(), "to", to.String()))
		if err := ui.Document(i18n.T("upgrade.notesTitle", "count", len(skipped)), func() { showNotes(skipped) }); err != nil && !errors.Is(err, ui.ErrBack) {
			return err
		}
	}
	question := i18n.T("upgrade.confirm", "from", from.String(), "to", to.String())
	def := true
	if releases.HasBreaking(skipped) {
		question, def = i18n.T("upgrade.confirmBreaking"), f.Yes
	}
	ui.Explain(i18n.T("upgrade.backupHelp"))
	ok, err := ui.Confirm(question, i18n.T("upgrade.goOn"), i18n.T("app.cancel"), def)
	if err != nil || !ok {
		if err == nil || errors.Is(err, ui.ErrBack) {
			err = ui.ErrAborted
		}
		return err
	}
	ui.Section(stepName("upgrade"))

	switch st.Mode {
	case state.Kubernetes:
		dump, err := kube.Upgrade(ctx, st, to.String())
		if err != nil {
			return offerRollback(ctx, f, err, func() error { return kube.Rollback(ctx, st, from.String(), dump) })
		}
	case state.Native:
		undo, err := native.Upgrade(ctx, st, to.String())
		if err != nil {
			if undo == nil {
				return err
			}
			return offerRollback(ctx, f, err, func() error { return native.Rollback(ctx, st, undo) })
		}
	default:
		undo, err := compose.Upgrade(ctx, st, to.String())
		if err != nil {
			if undo == nil {
				return err
			}
			return offerRollback(ctx, f, err, func() error { return compose.Rollback(ctx, st, undo) })
		}
	}
	ui.Summary(i18n.T("upgrade.done", "version", to.String()), []string{i18n.T("upgrade.doneFrom", "from", from.String())})
	return nil
}

// pickUpgrade picks the version to upgrade to: --version, else the newest in the same major
// among this installer's version and the releases, asked when several are newer.
//
// Args:
//
//	ctx: the context, to cancel.
//	f: the flags.
//	from: the installed version.
//
// Returns:
//
//	The version.
//
// Errors:
//
//	When the releases cannot be listed and no version was given.
func pickUpgrade(ctx context.Context, f Flags, from releases.Version) (releases.Version, error) {
	if f.Version != "" {
		return releases.ParseVersion(f.Version)
	}
	list, err := releases.ListAppVersions(ctx)
	if err != nil {
		return targetVersion(ctx, f)
	}
	var newer []releases.Version
	for _, v := range list {
		if v.Major == from.Major && v.Compare(from) > 0 {
			newer = append(newer, v)
		}
	}
	if len(newer) == 0 {
		return from, nil
	}
	if len(newer) == 1 || ui.Unattended {
		return newer[0], nil
	}
	opts := make([]ui.Option, len(newer))
	for i, v := range newer {
		label := v.String()
		if i == 0 {
			label += "  " + i18n.T("upgrade.latest")
		}
		opts[i] = ui.Option{Label: label, Value: v.String()}
	}
	s, err := ui.Select(i18n.T("upgrade.which", "version", from.String()), opts, newer[0].String())
	if err != nil {
		return releases.Version{}, err
	}
	return releases.ParseVersion(s)
}

// offerRollback says the upgrade failed and offers to go back.
//
// Args:
//
//	ctx: the context, to cancel.
//	f: the flags.
//	cause: why the upgrade failed.
//	rollback: going back.
//
// Errors:
//
//	The cause, or the rollback's own error.
func offerRollback(ctx context.Context, f Flags, cause error, rollback func() error) error {
	ui.Fail(i18n.T("upgrade.failed", "error", cause))
	ok, err := ui.Confirm(i18n.T("upgrade.rollback"), i18n.T("app.yes"), i18n.T("app.no"), true)
	if err != nil || !ok {
		return cause
	}
	if err := rollback(); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("upgrade.rollbackFailed"), err)
	}
	ui.Done(i18n.T("upgrade.rolledBack"))
	return cause
}

// ─── release notes ────────────────────────────────────────────────────────────

// showNotes writes the release notes of the versions an upgrade brings: first what needs
// attention across them all (breaking changes, removals, deprecations), then each version's
// other changes.
//
// Args:
//
//	entries: the versions, newest first.
func showNotes(entries []releases.Entry) {
	if len(entries) == 0 {
		return
	}
	attention := []struct {
		kind  releases.Kind
		key   string
		color string
	}{
		{releases.KindBreaking, "notes.breaking", "bad"},
		{releases.KindRemoved, "notes.removed", "bad"},
		{releases.KindDeprecated, "notes.deprecated", "warn"},
		// plain : red is the accent and the breaking changes' colour, amber the deprecations'
		{releases.KindSecurity, "notes.security", ""},
	}
	for _, a := range attention {
		var lines []string
		for _, e := range entries {
			for _, s := range e.Sections {
				if releases.KindOf(s.Title) != a.kind {
					continue
				}
				for _, item := range s.Items {
					// the version before each item only when the page covers several
					if len(entries) > 1 {
						item = fmt.Sprintf("%s  %s", ui.Colour("accent", e.Version.String()), item)
					}
					lines = append(lines, item)
				}
			}
		}
		if len(lines) > 0 {
			ui.Heading(ui.Colour(a.color, i18n.T(a.key)))
			for _, l := range lines {
				ui.Line("• " + l)
			}
		}
	}
	ui.Heading(i18n.T("notes.changes"))
	for _, e := range entries {
		head := e.Version.String()
		if e.Date != "" {
			head += "  (" + e.Date + ")"
		}
		ui.Line(ui.Colour("accent", head))
		ui.Blank()
		for _, s := range e.Sections {
			if releases.KindOf(s.Title) != releases.KindOther || len(s.Items) == 0 {
				continue
			}
			ui.Muted(s.Title)
			ui.Blank()
			for _, item := range s.Items {
				ui.Line("  • " + item)
			}
			ui.Blank()
		}
	}
	ui.Muted(i18n.T("notes.more", "url", "https://github.com/"+releases.AppRepo+"/blob/main/CHANGELOG.md"))
}

// releaseNotes shows the notes between two versions (--version, and the installed or the
// previous one) without upgrading.
//
// Args:
//
//	ctx: the context, to cancel.
//	f: the flags.
//
// Errors:
//
//	When the notes cannot be fetched.
func releaseNotes(ctx context.Context, f Flags) error {
	to, err := targetVersion(ctx, f)
	if err != nil {
		return err
	}
	from := releases.Version{Major: to.Major}
	if st, err := state.Load(); err == nil {
		if v, err := releases.ParseVersion(st.Version); err == nil && v.Compare(to) < 0 {
			from = v
		}
	}
	entries, err := releases.FetchChangelog(ctx, to)
	if err != nil {
		return err
	}
	picked := releases.Between(entries, from, to)
	if from.IsZero() || from.Minor == 0 && from.Patch == 0 {
		// nothing installed : only the version asked for
		picked = releases.Between(entries, previous(entries, to), to)
	}
	showNotes(picked)
	return nil
}

// previous finds the version released just before another.
//
// Args:
//
//	entries: every entry, newest first.
//	v: the version.
//
// Returns:
//
//	The one before it, 0.0.0 when none.
func previous(entries []releases.Entry, v releases.Version) releases.Version {
	for _, e := range entries {
		if e.Version.Compare(v) < 0 {
			return e.Version
		}
	}
	return releases.Version{}
}

// ─── status, backup, join token, uninstall ────────────────────────────────────

// status shows the installation and its containers or pods, then any other AnsibleForms on
// this server that the installer does not manage.
//
// Args:
//
//	ctx: the context, to cancel.
//
// Errors:
//
//	When there is none or it cannot be read.
func status(ctx context.Context) error {
	st, err := state.Load()
	if errors.Is(err, state.ErrNone) {
		// nothing managed : what is on this server all the same
		if statusFound(ctx, nil) == 0 {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	if err := statusManaged(ctx, st); err != nil {
		return err
	}
	statusFound(ctx, st)
	return nil
}

// statusManaged shows the installation the installer manages and its containers, pods or
// services.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//
// Errors:
//
//	When it cannot be read.
func statusManaged(ctx context.Context, st *state.State) error {
	role := i18n.T("install.roleControllerShort")
	if st.Role == compose.RoleRunner {
		role = i18n.T("install.roleRunnerShort")
	}
	mode := map[state.Mode]string{state.Compose: "Docker Compose", state.Kubernetes: i18n.T("install.modeKubernetesShort"), state.Native: i18n.T("install.modeNativeShort")}[st.Mode]
	ui.Line(i18n.T("status.summary", "role", role, "mode", mode, "version", st.Version))
	if st.Mode == state.Kubernetes {
		ui.Line(i18n.T("status.release", "release", st.Release, "namespace", st.Namespace))
		return kube.Status(ctx, st)
	}
	if st.Mode == state.Native {
		ui.Line(i18n.T("status.folder", "dir", st.Dir, "runtime", i18n.T("install.modeNativeShort")))
		return native.Status(ctx, st)
	}
	ui.Line(i18n.T("status.folder", "dir", st.Dir, "runtime", st.Runtime))
	return compose.Status(ctx, st)
}

// backup dumps the database now.
//
// Args:
//
//	ctx: the context, to cancel.
//
// Errors:
//
//	When there is none or the dump fails.
func backup(ctx context.Context) error {
	st, err := state.Load()
	if err != nil {
		return err
	}
	var dump string
	switch st.Mode {
	case state.Kubernetes:
		dump, err = kube.Backup(ctx, st)
	case state.Native:
		dump, err = native.Backup(ctx, st)
	default:
		dump, err = compose.Backup(ctx, st)
	}
	if err != nil {
		return err
	}
	ui.Done(i18n.T("compose.backedUp", "file", dump))
	return nil
}

// uninstall removes the installation, after a confirmation (typing the instance's name when
// the data goes too).
//
// Args:
//
//	ctx: the context, to cancel.
//	f: the flags.
//
// Errors:
//
//	When there is none, the person declines, or a step fails.
func uninstall(ctx context.Context, f Flags) error {
	st, err := state.Load()
	if err != nil {
		return err
	}
	purge := f.Purge
	if !purge && !f.Yes {
		if purge, err = ui.Confirm(i18n.T("uninstall.purge"), i18n.T("uninstall.purgeYes"), i18n.T("uninstall.purgeNo"), false); err != nil {
			return err
		}
	}
	name := st.Name
	if st.Mode == state.Kubernetes {
		name = st.Release
	}
	if purge && !ui.Unattended {
		typed, err := ui.Input(i18n.T("uninstall.typeName", "name", name), "", false, nil)
		if err != nil {
			return err
		}
		if strings.TrimSpace(typed) != name {
			return errors.New(i18n.T("uninstall.nameMismatch"))
		}
	} else if !f.Yes {
		ok, err := ui.Confirm(i18n.T("uninstall.confirm", "name", name), i18n.T("app.yes"), i18n.T("app.no"), false)
		if err != nil || !ok {
			if err == nil {
				err = ui.ErrAborted
			}
			return err
		}
	}
	switch st.Mode {
	case state.Kubernetes:
		err = kube.Uninstall(ctx, st, purge)
	case state.Native:
		err = native.Uninstall(ctx, st, purge)
	default:
		err = compose.Uninstall(ctx, st, purge)
	}
	if err != nil {
		return err
	}
	ui.Done(i18n.T("uninstall.done"))
	return nil
}
