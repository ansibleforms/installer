package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/ansibleforms/installer/internal/compose"
	"github.com/ansibleforms/installer/internal/releases"
	"github.com/ansibleforms/installer/internal/state"
	"github.com/ansibleforms/installer/internal/tui"
	"github.com/ansibleforms/installer/internal/ui"
)

// saved saves an installation's state for the test.
//
// Args:
//
//	mode: compose, native or kubernetes.
//	role: controller or runner.
//	version: its version.
//
// Returns:
//
//	The state.
func saved(mode state.Mode, role, version string) *state.State {
	st := &state.State{Mode: mode, Role: role, Version: version, Dir: "/opt/af", Name: "af", Runtime: "docker", Namespace: "forms", Release: "af"}
	_ = st.Save()
	return st
}

// ─── upgrade ──────────────────────────────────────────────────────────────────

// TestUpgradeEachMode checks an upgrade of each mode: the version picked among the newer ones,
// the language asked first, the notes skipped shown, a breaking change confirmed.
func TestUpgradeEachMode(t *testing.T) {
	for _, c := range []struct {
		mode state.Mode
		role string
		call string
	}{
		{state.Compose, "controller", "composeUpgrade"},
		{state.Compose, "runner", "composeUpgrade"},
		{state.Native, "controller", "nativeUpgrade"},
		{state.Kubernetes, "controller", "kubeUpgrade"},
	} {
		s := fakeServer(t)
		s.notes = notes73
		saved(c.mode, c.role, "7.2.0")
		answering(t, map[string][]tui.Answer{
			title("upgrade.which", "version", "7.2.0"): {pick("7.4.0")},
			title("upgrade.confirmBreaking"):           {yes},
		})
		if err := upgrade(context.Background(), Flags{InstallerVersion: "7.4.0"}); err != nil || !s.called(c.call) {
			t.Errorf("%s %s: %v, %v", c.mode, c.role, err, s.calls)
		}
		if st, _ := state.Load(); st.Version != "7.4.0" {
			t.Errorf("%s: version %s", c.mode, st.Version)
		}
	}
}

// TestUpgradeChecks checks an installation up to date, a downgrade, another major, a version
// that is none, no state, a preparation that fails or blocks, and a cancel.
func TestUpgradeChecks(t *testing.T) {
	ctx := context.Background()
	s := fakeServer(t)
	answering(t, map[string][]tui.Answer{title("upgrade.confirm", "from", "7.2.0", "to", "7.3.0"): {no, back}})
	if err := upgrade(ctx, Flags{Lang: "en"}); !errors.Is(err, state.ErrNone) {
		t.Errorf("no state: %v", err)
	}
	saved(state.Compose, "controller", "7.2.0")
	for name, f := range map[string]Flags{
		"downgrade":     {Lang: "en", Version: "7.1.0"},
		"major":         {Lang: "en", Version: "8.0.0"},
		"not a version": {Lang: "en", Version: "x"},
		"cancelled":     {Lang: "en", Version: "7.3.0"},
		"back":          {Lang: "en", Version: "7.3.0"},
	} {
		if err := upgrade(ctx, f); err == nil {
			t.Errorf("%s: upgraded", name)
		}
	}
	if err := upgrade(ctx, Flags{Lang: "en", Version: "7.2.0"}); err != nil {
		t.Errorf("up to date: %v", err)
	}
	s.fail["composePrepare"] = errors.New("cannot fetch")
	if err := upgrade(ctx, Flags{Lang: "en", Version: "7.3.0"}); err == nil {
		t.Error("prepare failed")
	}
	s.fail = map[string]error{}
	composePrepare = func(context.Context, *state.State, string) (*compose.Prep, error) {
		return &compose.Prep{Blocked: []string{"X"}}, nil
	}
	if err := upgrade(ctx, Flags{Lang: "en", Version: "7.3.0"}); err == nil {
		t.Error("prepare blocked")
	}
	(&state.State{Mode: state.Compose, Version: "garbage"}).Save()
	if err := upgrade(ctx, Flags{Lang: "en", Version: "7.3.0"}); err == nil {
		t.Error("a state version that is none")
	}
	answering(t, map[string][]tui.Answer{title("language.title"): {abort}})
	saved(state.Compose, "controller", "7.2.0")
	if err := upgrade(ctx, Flags{}); err == nil {
		t.Error("language aborted")
	}
}

// TestUpgradeNotesBack checks Esc on the notes going back to the language, then on.
func TestUpgradeNotesBack(t *testing.T) {
	s := fakeServer(t)
	s.notes = notes73
	saved(state.Compose, "controller", "7.2.0")
	notes := title("upgrade.notesTitle", "count", 1)
	answering(t, map[string][]tui.Answer{
		notes:                   {back, abort},
		title("language.title"): {pick("en"), pick("en")},
	})
	if err := upgrade(context.Background(), Flags{Version: "7.3.0"}); !errors.Is(err, ui.ErrAborted) {
		t.Errorf("aborted on the notes: %v", err)
	}
	answering(t, map[string][]tui.Answer{notes: {back}, title("language.title"): {pick("en"), abort}})
	if err := upgrade(context.Background(), Flags{Version: "7.3.0"}); !errors.Is(err, ui.ErrAborted) {
		t.Errorf("aborted on the language: %v", err)
	}
	s.fail["releasesFetchChangelog"] = errors.New("offline")
	answering(t, map[string][]tui.Answer{})
	if err := upgrade(context.Background(), Flags{Lang: "en", Version: "7.3.0"}); err != nil {
		t.Errorf("no notes: %v", err)
	}
}

// TestUpgradeRollbacks checks a failed upgrade of each mode rolled back, the rollback declined
// or failing, and a failure with nothing to roll back.
func TestUpgradeRollbacks(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		mode   state.Mode
		up, rb string
	}{
		{state.Compose, "composeUpgrade", "composeRollback"},
		{state.Native, "nativeUpgrade", "nativeRollback"},
		{state.Kubernetes, "kubeUpgrade", "kubeRollback"},
	} {
		s := fakeServer(t)
		s.fail[c.up], s.undo = errors.New("broken"), true
		saved(c.mode, "controller", "7.2.0")
		answering(t, map[string][]tui.Answer{})
		if err := upgrade(ctx, Flags{Lang: "en", Version: "7.3.0"}); err == nil || !s.called(c.rb) {
			t.Errorf("%s: %v, %v", c.mode, err, s.calls)
		}
		s.undo = false
		if c.mode != state.Kubernetes {
			if err := upgrade(ctx, Flags{Lang: "en", Version: "7.3.0"}); err == nil {
				t.Errorf("%s: nothing to roll back", c.mode)
			}
		}
	}
	s := fakeServer(t)
	answering(t, map[string][]tui.Answer{title("upgrade.rollback"): {no}})
	if err := offerRollback(ctx, Flags{}, errors.New("cause"), func() error { return nil }); err == nil || s.called("x") {
		t.Error("declined")
	}
	answering(t, map[string][]tui.Answer{})
	if err := offerRollback(ctx, Flags{}, errors.New("cause"), func() error { return errors.New("rb") }); err == nil {
		t.Error("rollback failed")
	}
}

// TestPickUpgrade checks the newer version offered: none, one, several, unattended, the list
// that cannot be read, and a choice aborted.
func TestPickUpgrade(t *testing.T) {
	ctx := context.Background()
	s := fakeServer(t)
	from := releases.Version{Major: 7, Minor: 3}
	answering(t, map[string][]tui.Answer{title("upgrade.which", "version", "7.3.0"): {abort}})
	if _, err := pickUpgrade(ctx, Flags{}, from); err == nil {
		t.Error("aborted")
	}
	s.versions = []releases.Version{{Major: 7, Minor: 3}, {Major: 6, Minor: 9}}
	if v, _ := pickUpgrade(ctx, Flags{}, from); v != from {
		t.Error("none newer")
	}
	s.versions = []releases.Version{{Major: 7, Minor: 4}}
	if v, _ := pickUpgrade(ctx, Flags{}, from); v.Minor != 4 {
		t.Error("one newer")
	}
	s.fail["releasesListAppVersions"] = errors.New("offline")
	if v, _ := pickUpgrade(ctx, Flags{InstallerVersion: "7.3.1"}, from); v.Patch != 1 {
		t.Error("offline: the installer's")
	}
}

// ─── notes, status, backup, uninstall ─────────────────────────────────────────

// TestReleaseNotes checks the notes since the installed version, of the latest minor's
// first release, and a changelog that cannot be fetched.
func TestReleaseNotes(t *testing.T) {
	ctx := context.Background()
	s := fakeServer(t)
	s.notes = append([]releases.Entry{{Version: releases.Version{Major: 7, Minor: 4}, Date: "2026-11-30",
		Sections: []releases.Section{{Title: "Security", Items: []string{"s"}}, {Title: "Removed", Items: []string{"r"}}, {Title: "Deprecated", Items: []string{"d"}}, {Title: "Empty"}}}}, notes73...)
	answering(t, map[string][]tui.Answer{})
	saved(state.Compose, "controller", "7.2.0")
	if err := releaseNotes(ctx, Flags{Version: "7.4.0"}); err != nil {
		t.Error(err)
	}
	_ = state.Remove()
	if err := releaseNotes(ctx, Flags{Version: "7.4.0"}); err != nil {
		t.Error(err)
	}
	showNotes(nil)
	if err := releaseNotes(ctx, Flags{Version: "x"}); err == nil {
		t.Error("a version that is none")
	}
	s.fail["releasesFetchChangelog"] = errors.New("offline")
	if err := releaseNotes(ctx, Flags{Version: "7.4.0"}); err == nil {
		t.Error("offline")
	}
	if previous(nil, releases.Version{Major: 7}) != (releases.Version{}) {
		t.Error("previous of none")
	}
}

// TestStatusBackupUninstall checks the status, the backup and the uninstall of each mode, and
// their failures.
func TestStatusBackupUninstall(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []state.Mode{state.Compose, state.Native, state.Kubernetes} {
		s := fakeServer(t)
		answering(t, map[string][]tui.Answer{})
		for _, role := range []string{"controller", "runner"} {
			saved(mode, role, "7.3.0")
			if err := status(ctx); err != nil {
				t.Errorf("%s status: %v", mode, err)
			}
		}
		if err := backup(ctx); err != nil {
			t.Errorf("%s backup: %v", mode, err)
		}
		if err := uninstall(ctx, Flags{Yes: true}); err != nil {
			t.Errorf("%s uninstall: %v", mode, err)
		}
		saved(mode, "controller", "7.3.0")
		for k := range map[string]bool{"composeStatus": true, "nativeStatus": true, "kubeStatus": true, "composeBackup": true, "nativeBackup": true, "kubeBackup": true, "composeUninstall": true, "nativeUninstall": true, "kubeUninstall": true} {
			s.fail[k] = errors.New("failed")
		}
		if status(ctx) == nil || backup(ctx) == nil || uninstall(ctx, Flags{Yes: true}) == nil {
			t.Errorf("%s: failures not said", mode)
		}
	}
	fakeServer(t)
	answering(t, map[string][]tui.Answer{})
	if status(ctx) == nil || backup(ctx) == nil || uninstall(ctx, Flags{}) == nil {
		t.Error("nothing installed")
	}
}

// TestUninstallQuestions checks the purge asked and its name typed, a wrong name, a plain
// uninstall confirmed or not, and each question aborted.
func TestUninstallQuestions(t *testing.T) {
	ctx := context.Background()
	fakeServer(t)
	saved(state.Compose, "controller", "7.3.0")
	purge, typeName, confirm := title("uninstall.purge"), title("uninstall.typeName", "name", "af"), title("uninstall.confirm", "name", "af")
	for name, c := range map[string]struct {
		answers map[string][]tui.Answer
		ok      bool
	}{
		"purge aborted":   {map[string][]tui.Answer{purge: {abort}}, false},
		"name aborted":    {map[string][]tui.Answer{purge: {yes}, typeName: {abort}}, false},
		"wrong name":      {map[string][]tui.Answer{purge: {yes}, typeName: {{Value: "other"}}}, false},
		"keep, declined":  {map[string][]tui.Answer{purge: {no}, confirm: {no}}, false},
		"keep, aborted":   {map[string][]tui.Answer{purge: {no}, confirm: {abort}}, false},
		"purge, the name": {map[string][]tui.Answer{purge: {yes}, typeName: {{Value: " af "}}}, true},
	} {
		saved(state.Compose, "controller", "7.3.0")
		answering(t, c.answers)
		if err := uninstall(ctx, Flags{}); (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}
