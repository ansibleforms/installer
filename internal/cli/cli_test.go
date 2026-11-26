package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ansibleforms/installer/internal/compose"
	"github.com/ansibleforms/installer/internal/detect"
	"github.com/ansibleforms/installer/internal/i18n"
	"github.com/ansibleforms/installer/internal/kube"
	"github.com/ansibleforms/installer/internal/native"
	"github.com/ansibleforms/installer/internal/releases"
	"github.com/ansibleforms/installer/internal/state"
	"github.com/ansibleforms/installer/internal/system"
	"github.com/ansibleforms/installer/internal/tui"
	"github.com/ansibleforms/installer/internal/ui"
)

// ─── a person answering ───────────────────────────────────────────────────────

// person answers the wizard's questions: by the question's title (a part of it), in turn, else
// with the question's default ; and remembers what was asked.
type person struct {
	t       *testing.T
	answers map[string][]tui.Answer
	asked   []string
}

// answering puts a person in front of the full-screen wizard for the test.
//
// Args:
//
//	t: the test.
//	answers: the answers by a part of the question's title, in the order they are given.
//
// Returns:
//
//	The person.
func answering(t *testing.T, answers map[string][]tui.Answer) *person {
	t.Helper()
	p := &person{t: t, answers: answers}
	was, wasFull, wasUnattended := ui.Asker, ui.Fullscreen, ui.Unattended
	ui.Asker, ui.Fullscreen, ui.Unattended = p.ask, true, false
	t.Cleanup(func() {
		ui.Asker, ui.Fullscreen, ui.Unattended = was, wasFull, wasUnattended
		ui.Close(errors.New("end of test"))
	})
	return p
}

// ask answers a question.
//
// Args:
//
//	q: the question.
//
// Returns:
//
//	The answer.
func (p *person) ask(q tui.Question) tui.Answer {
	p.asked = append(p.asked, q.Title)
	for part, list := range p.answers {
		if part != "" && strings.Contains(q.Title, part) && len(list) > 0 {
			p.answers[part] = list[1:]
			return list[0]
		}
	}
	switch q.Kind {
	case tui.Confirm:
		return tui.Answer{Bool: q.DefaultBool}
	case tui.Document:
		return tui.Answer{Bool: true}
	case tui.Select:
		for _, o := range q.Options {
			if o.Value == q.Default && !o.Disabled {
				return tui.Answer{Value: o.Value}
			}
		}
		for _, o := range q.Options {
			if !o.Disabled {
				return tui.Answer{Value: o.Value}
			}
		}
	}
	return tui.Answer{Value: q.Default}
}

// wasAsked tells whether a question whose title holds a text was asked.
func (p *person) wasAsked(part string) bool {
	for _, a := range p.asked {
		if strings.Contains(a, part) {
			return true
		}
	}
	return false
}

// title is a question's title in English, its placeholders filled.
func title(key string, args ...any) string { return i18n.T(key, args...) }

// pick answers a select with a value.
func pick(v string) tui.Answer { return tui.Answer{Value: v} }

// yes and no answer a confirmation ; back and abort leave a question.
var (
	yes   = tui.Answer{Bool: true}
	no    = tui.Answer{Bool: false}
	back  = tui.Answer{Back: true}
	abort = tui.Answer{Abort: true}
)

// ─── the server, faked ────────────────────────────────────────────────────────

// server is what the commands did on the fake server, and how it answers.
type server struct {
	calls []string
	// fail names the action that fails (composeInstall...), with err.
	fail map[string]error
	// tools are the container tools found ; found the installations found.
	tools system.Tools
	found []detect.Install
	// versions are the releases on GitHub ; notes the changelog's entries.
	versions []releases.Version
	notes    []releases.Entry
	// app is the version the app answers, trusted its certificate.
	app     string
	trusted bool
	// undo is what a failed upgrade hands back (nil : nothing to roll back).
	undo bool
	// db is the database-mode Runner FindDatabaseRunner finds.
	db *detect.Install
}

// did records an action and fails it when told to.
func (s *server) did(name string) error {
	s.calls = append(s.calls, name)
	return s.fail[name]
}

// called tells whether an action was done.
func (s *server) called(name string) bool {
	for _, c := range s.calls {
		if c == name {
			return true
		}
	}
	return false
}

// installedState saves the state of an installation done by the fake actions.
func installedState(o compose.Options, mode state.Mode) *state.State {
	st := &state.State{Mode: mode, Role: o.Role, Version: o.Version, Dir: o.Dir, Name: o.Name, Port: o.Port, Runtime: string(o.Runtime)}
	_ = st.Save()
	return st
}

// fakeServer puts the fake actions in place of the real ones for the test, with the state in a
// folder of the test's own.
//
// Args:
//
//	t: the test.
//
// Returns:
//
//	The server, to tell how it answers and read what was done.
func fakeServer(t *testing.T) *server {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("as root the state is /etc/ansibleforms/installer.json")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	for _, k := range []string{"LANG", "LC_ALL", "LC_MESSAGES", "LANGUAGE", "AF_SKIP_VERSION_CHECK"} {
		t.Setenv(k, "")
	}
	i18n.Set("en")
	notesCache = map[string][]releases.Entry{}
	s := &server{fail: map[string]error{}, app: "7.3.0", trusted: true,
		tools:    system.Tools{Docker: true, DockerCompose: true},
		versions: []releases.Version{{Major: 7, Minor: 4}, {Major: 7, Minor: 3, Patch: 1}, {Major: 7, Minor: 3}}}
	restore := snapshot()
	t.Cleanup(restore)

	composeInstall = func(_ context.Context, o compose.Options) (*state.State, *compose.Result, error) {
		if err := s.did("composeInstall"); err != nil {
			return nil, nil, err
		}
		return installedState(o, state.Compose), &compose.Result{URL: "https://localhost", AdminUser: "admin", AdminPassword: "pw", Token: "tok", CertFingerprint: "ab-cd"}, nil
	}
	nativeInstall = func(_ context.Context, o compose.Options) (*state.State, *compose.Result, error) {
		if err := s.did("nativeInstall"); err != nil {
			return nil, nil, err
		}
		return installedState(o, state.Native), &compose.Result{URL: "https://localhost", AdminUser: "admin", AdminPassword: "pw", Token: "tok"}, nil
	}
	kubeInstall = func(_ context.Context, o kube.Options) (*state.State, error) {
		if err := s.did("kubeInstall"); err != nil {
			return nil, err
		}
		st := &state.State{Mode: state.Kubernetes, Role: "controller", Version: o.Version, Namespace: o.Namespace, Release: o.Release, URL: "https://" + o.Hostname, ValuesFile: "/v.yaml"}
		_ = st.Save()
		return st, nil
	}
	composeUpgrade = func(_ context.Context, st *state.State, to string, _ *compose.Prep) (*compose.Undo, error) {
		if err := s.did("composeUpgrade"); err != nil {
			if s.undo {
				return &compose.Undo{}, err
			}
			return nil, err
		}
		st.Version = to
		return &compose.Undo{}, st.Save()
	}
	nativeUpgrade = func(_ context.Context, st *state.State, to string) (*native.Undo, error) {
		if err := s.did("nativeUpgrade"); err != nil {
			if s.undo {
				return &native.Undo{}, err
			}
			return nil, err
		}
		st.Version = to
		return &native.Undo{}, st.Save()
	}
	kubeUpgrade = func(_ context.Context, st *state.State, to string) (string, error) {
		if err := s.did("kubeUpgrade"); err != nil {
			return "d.sql", err
		}
		st.Version = to
		return "d.sql", st.Save()
	}
	composeRollback = func(context.Context, *state.State, *compose.Undo) error { return s.did("composeRollback") }
	nativeRollback = func(context.Context, *state.State, *native.Undo) error { return s.did("nativeRollback") }
	kubeRollback = func(context.Context, *state.State, string, string) error { return s.did("kubeRollback") }
	composeBackup = func(context.Context, *state.State) (string, error) { return "/b/d.sql", s.did("composeBackup") }
	nativeBackup = func(context.Context, *state.State) (string, error) { return "/b/d.sql", s.did("nativeBackup") }
	kubeBackup = func(context.Context, *state.State) (string, error) { return "/b/d.sql", s.did("kubeBackup") }
	composeStatus = func(context.Context, *state.State) error { return s.did("composeStatus") }
	nativeStatus = func(context.Context, *state.State) error { return s.did("nativeStatus") }
	kubeStatus = func(context.Context, *state.State) error { return s.did("kubeStatus") }
	composeUninstall = func(context.Context, *state.State, bool) error { _ = state.Remove(); return s.did("composeUninstall") }
	nativeUninstall = func(context.Context, *state.State, bool) error { _ = state.Remove(); return s.did("nativeUninstall") }
	kubeUninstall = func(context.Context, *state.State, bool) error { _ = state.Remove(); return s.did("kubeUninstall") }
	composePrepare = func(context.Context, *state.State, string) (*compose.Prep, error) {
		return &compose.Prep{}, s.did("composePrepare")
	}
	composeMigrateRunner = func(_ context.Context, st *state.State, to string, m compose.Migration) (*compose.MigrationUndo, *compose.Result, error) {
		if err := s.did("composeMigrateRunner"); err != nil {
			if s.undo {
				return &compose.MigrationUndo{}, nil, err
			}
			return nil, nil, err
		}
		st.Version, st.DatabaseMode = to, false
		return &compose.MigrationUndo{}, &compose.Result{Token: "tok", CertFingerprint: "ab-cd"}, st.Save()
	}
	composeRollbackMigration = func(context.Context, *state.State, *compose.MigrationUndo) error {
		return s.did("composeRollbackMigration")
	}
	composeOutboundIP = func() string { return "10.0.0.5" }
	nativeSupported = func() string { s.calls = append(s.calls, "nativeSupported"); return os.Getenv("NATIVE_WHY") }
	kubeAdopt = func(context.Context, *state.State) error { return s.did("kubeAdopt") }
	kubeIngressClasses = func(context.Context) []string { return []string{"nginx", "traefik"} }
	kubeStorageClasses = func(context.Context) []string { return []string{"fast"} }
	systemDetect = func(context.Context) system.Tools { return s.tools }
	systemInstallDocker = func(context.Context) error { return s.did("systemInstallDocker") }
	systemInstallPodman = func(context.Context) error { return s.did("systemInstallPodman") }
	systemIsRoot = func() bool { return os.Getenv("AS_ROOT") == "1" }
	detectFind = func(context.Context) []detect.Install { return s.found }
	detectFindDatabaseRunner = func(context.Context, *state.State) *detect.Install { return s.db }
	releasesListAppVersions = func(context.Context) ([]releases.Version, error) {
		return s.versions, s.did("releasesListAppVersions")
	}
	releasesFetchChangelog = func(context.Context, releases.Version) ([]releases.Entry, error) {
		return s.notes, s.did("releasesFetchChangelog")
	}
	healthAppVersion = func(context.Context, string) (string, bool, error) {
		if err := s.did("healthAppVersion"); err != nil {
			return "", s.trusted, err
		}
		if !s.trusted {
			return "", false, errors.New("x509: certificate signed by unknown authority")
		}
		return s.app, true, nil
	}
	healthAppVersionInsecure = func(context.Context, string) (string, bool, error) {
		return s.app, true, s.did("healthAppVersionInsecure")
	}
	return s
}

// snapshot keeps every action as it is, to put back after the test.
func snapshot() func() {
	a1, a2, a3, a4, a5, a6, a7, a8, a9, a10 := composeInstall, composeUpgrade, composeRollback, composeBackup, composeStatus, composeUninstall, composePrepare, composeMigrateRunner, composeRollbackMigration, composeOutboundIP
	b1, b2, b3, b4, b5, b6, b7 := nativeInstall, nativeUpgrade, nativeRollback, nativeBackup, nativeStatus, nativeUninstall, nativeSupported
	c1, c2, c3, c4, c5, c6, c7, c8, c9 := kubeInstall, kubeAdopt, kubeUpgrade, kubeRollback, kubeBackup, kubeStatus, kubeUninstall, kubeIngressClasses, kubeStorageClasses
	d1, d2, d3, d4 := systemDetect, systemInstallDocker, systemInstallPodman, systemIsRoot
	e1, e2, e3, e4, e5, e6 := detectFind, detectFindDatabaseRunner, releasesListAppVersions, releasesFetchChangelog, healthAppVersion, healthAppVersionInsecure
	return func() {
		composeInstall, composeUpgrade, composeRollback, composeBackup, composeStatus, composeUninstall, composePrepare, composeMigrateRunner, composeRollbackMigration, composeOutboundIP = a1, a2, a3, a4, a5, a6, a7, a8, a9, a10
		nativeInstall, nativeUpgrade, nativeRollback, nativeBackup, nativeStatus, nativeUninstall, nativeSupported = b1, b2, b3, b4, b5, b6, b7
		kubeInstall, kubeAdopt, kubeUpgrade, kubeRollback, kubeBackup, kubeStatus, kubeUninstall, kubeIngressClasses, kubeStorageClasses = c1, c2, c3, c4, c5, c6, c7, c8, c9
		systemDetect, systemInstallDocker, systemInstallPodman, systemIsRoot = d1, d2, d3, d4
		detectFind, detectFindDatabaseRunner, releasesListAppVersions, releasesFetchChangelog, healthAppVersion, healthAppVersionInsecure = e1, e2, e3, e4, e5, e6
	}
}

// ─── the commands ─────────────────────────────────────────────────────────────

// TestRunCommands checks the version printed, an unknown command, the language asked for the
// commands that are not the wizard, and each command dispatched.
func TestRunCommands(t *testing.T) {
	s := fakeServer(t)
	ctx := context.Background()
	if err := Run(ctx, "version", Flags{InstallerVersion: "7.3.0"}); err != nil {
		t.Error(err)
	}
	langTitle := title("language.title")
	p := answering(t, map[string][]tui.Answer{langTitle: {pick("de")}})
	if err := Run(ctx, "nonsense", Flags{InstallerVersion: "7.3.0"}); err == nil {
		t.Error("an unknown command")
	}
	if i18n.Current() != "de" || !p.wasAsked(langTitle) {
		t.Errorf("the language not asked: %s, %q", i18n.Current(), p.asked)
	}
	i18n.Set("en")
	answering(t, map[string][]tui.Answer{title("language.title"): {abort}})
	if err := Run(ctx, "status", Flags{InstallerVersion: "7.3.0"}); !errors.Is(err, ui.ErrAborted) {
		t.Errorf("aborted language: %v", err)
	}
	for _, cmd := range []string{"status", "backup", "release-notes", "uninstall", "upgrade", "install"} {
		_ = Run(ctx, cmd, Flags{InstallerVersion: "7.3.0", Lang: "en", Version: "7.3.0", Yes: true})
	}
	_ = s
}

// TestTargetVersion checks the version asked for, the installer's own, the latest release, and
// none to be found.
func TestTargetVersion(t *testing.T) {
	s := fakeServer(t)
	ctx := context.Background()
	if v, _ := targetVersion(ctx, Flags{Version: "7.2.1"}); v.String() != "7.2.1" {
		t.Error("asked for")
	}
	if v, _ := targetVersion(ctx, Flags{InstallerVersion: "7.3.0"}); v.String() != "7.3.0" {
		t.Error("the installer's")
	}
	if v, _ := targetVersion(ctx, Flags{InstallerVersion: "dev"}); v.String() != "7.4.0" {
		t.Error("the latest")
	}
	s.versions = nil
	if _, err := targetVersion(ctx, Flags{InstallerVersion: "dev"}); err == nil {
		t.Error("no release")
	}
	s.fail["releasesListAppVersions"] = errors.New("offline")
	if _, err := targetVersion(ctx, Flags{InstallerVersion: "dev"}); err == nil {
		t.Error("offline")
	}
}

// TestHelpers checks the small questions and checks: a choice and a text from a flag or
// asked, a number, and each check.
func TestHelpers(t *testing.T) {
	fakeServer(t)
	answering(t, map[string][]tui.Answer{"Class": {pick("")}, "Text": {{Value: "bad name"}, {Value: "ok"}}, "Port": {{Value: "x"}}})
	if v, _ := choice("flag", "Class", nil, ""); v != "flag" {
		t.Error("choice flag")
	}
	if v, _ := choice("", "Class", []string{"", "fast"}, ""); v != "" {
		t.Error("choice default class")
	}
	if v, _ := choice("", "Free", nil, "typed"); v != "typed" {
		t.Error("choice without values")
	}
	if _, err := text("bad name", "Text", "", validName); err == nil {
		t.Error("a flag that fails its check")
	}
	if _, err := text("", "Text", "", validName); err == nil {
		t.Error("an answer that fails its check")
	}
	if n, _ := number(8080, "Port", 1); n != 8080 {
		t.Error("number flag")
	}
	if _, err := number(0, "Port", 1); err == nil {
		t.Error("a port that is none")
	}
	if n, _ := number(0, "Other", 8443); n != 8443 {
		t.Error("number default")
	}
	for _, c := range []struct {
		check func(string) error
		in    string
		ok    bool
	}{
		{notEmpty, " ", false}, {notEmpty, "x", true},
		{validName, "Bad", false}, {validName, "good-1", true},
		{validURL, "ftp://x", false}, {validURL, "https://x", true},
		{validPort, "0", false}, {validPort, "443", true},
		{fileExists, "/none/file", false}, {fileExists, os.TempDir(), true},
	} {
		if err := c.check(c.in); (err == nil) != c.ok {
			t.Errorf("%q: %v", c.in, err)
		}
	}
}

// keep imports used by the other test files of the package.
var _ = time.Second
