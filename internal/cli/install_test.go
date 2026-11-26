package cli

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ansibleforms/installer/internal/releases"
	"github.com/ansibleforms/installer/internal/state"
	"github.com/ansibleforms/installer/internal/system"
	"github.com/ansibleforms/installer/internal/tui"
	"github.com/ansibleforms/installer/internal/ui"
)

// TestVisibleLabelsKeepsAnsweredLanguage checks that the language, asked before the wizard
// began (so its own step is skipped), stays at the left, while a step skipped for another
// reason does not show.
func TestVisibleLabelsKeepsAnsweredLanguage(t *testing.T) {
	asked := true
	skipped := func() bool { return true }
	list := []wstep{
		{label: "language", skip: skipped, shown: func() bool { return asked }},
		{label: "notes"},
		{label: "folder", skip: skipped},
		{label: "role"},
	}
	want := []string{stepName("language"), stepName("notes"), stepName("role")}
	if got := visibleLabels(list); !reflect.DeepEqual(got, want) {
		t.Fatalf("visibleLabels = %v, want %v", got, want)
	}
	// given with --lang : never asked, so not at the left
	asked = false
	want = []string{stepName("notes"), stepName("role")}
	if got := visibleLabels(list); !reflect.DeepEqual(got, want) {
		t.Fatalf("visibleLabels with --lang = %v, want %v", got, want)
	}
}

// TestRunWizardBackToAnsweredLanguage checks that Esc on the step after the language, asked
// before the wizard began, asks the language again, then goes on.
func TestRunWizardBackToAnsweredLanguage(t *testing.T) {
	var asked []string
	notesSeen := 0
	list := []wstep{
		{label: "language", skip: func() bool { return true }, shown: func() bool { return true }, ask: func() error {
			asked = append(asked, "language")
			return nil
		}},
		{label: "notes", ask: func() error {
			asked = append(asked, "notes")
			notesSeen++
			if notesSeen == 1 {
				return ui.ErrBack
			}
			return nil
		}},
		{label: "role", ask: func() error {
			asked = append(asked, "role")
			return nil
		}},
	}
	if err := runWizard(list); err != nil {
		t.Fatal(err)
	}
	want := []string{"notes", "language", "notes", "role"}
	if !reflect.DeepEqual(asked, want) {
		t.Fatalf("asked %v, want %v", asked, want)
	}
}

// ─── the wizard, answered ─────────────────────────────────────────────────────

// notes73 are release notes of 7.3.0 and of the release before it, a breaking change among
// them.
var notes73 = []releases.Entry{
	{Version: releases.Version{Major: 7, Minor: 4}, Sections: []releases.Section{{Title: "⚠ BREAKING CHANGES", Items: []string{"a change"}}}},
	{Version: releases.Version{Major: 7, Minor: 3}, Sections: []releases.Section{{Title: "Added", Items: []string{"a feature"}}, {Title: "Fixed", Items: []string{"a fix"}}}},
	{Version: releases.Version{Major: 7, Minor: 2}},
}

// TestInstallControllerWizard checks a controller installed through every question with
// their defaults: the language, the notes, the engine, the folder, the port, HTTPS, the admin
// password generated, the review ; and the summary.
func TestInstallControllerWizard(t *testing.T) {
	s := fakeServer(t)
	s.notes = notes73
	p := answering(t, map[string][]tui.Answer{title("install.ready"): {no, yes}})
	if err := install(context.Background(), Flags{InstallerVersion: "7.3.0"}); err != nil {
		t.Fatal(err)
	}
	if !s.called("composeInstall") || !p.wasAsked(title("install.notesTitle", "version", "7.3.0")) || !p.wasAsked(title("install.adminPassword")) {
		t.Errorf("calls %v, asked %q", s.calls, p.asked)
	}
}

// TestInstallControllerOwnTLS checks a controller on Podman with its own certificate, a port
// and a password given, as root (the folder under /opt).
func TestInstallControllerOwnTLS(t *testing.T) {
	s := fakeServer(t)
	s.tools = system.Tools{Podman: true, PodmanCompose: true}
	t.Setenv("AS_ROOT", "1")
	cert := t.TempDir()
	answering(t, map[string][]tui.Answer{
		title("install.tls"):           {pick("own")},
		title("install.certFile"):      {{Value: cert}},
		title("install.keyFile"):       {{Value: cert}},
		title("install.adminPassword"): {{Value: "secret"}},
		title("install.port"):          {{Value: "8443"}},
	})
	if err := install(context.Background(), Flags{InstallerVersion: "7.3.0", Lang: "en"}); err != nil {
		t.Fatal(err)
	}
	st, _ := state.Load()
	if st.Runtime != "podman" || st.Dir != "/opt/ansibleforms" || st.Port != 8443 {
		t.Errorf("state %+v", st)
	}
}

// TestInstallNativeAndKube checks a native controller (its engine chosen), one refused where
// native cannot run, and a Kubernetes controller and Runner with their cluster's classes.
func TestInstallNativeAndKube(t *testing.T) {
	s := fakeServer(t)
	answering(t, map[string][]tui.Answer{title("install.runtime"): {pick("native")}})
	if err := install(context.Background(), Flags{InstallerVersion: "7.3.0", Lang: "en"}); err != nil || !s.called("nativeInstall") {
		t.Fatalf("native: %v, %v", err, s.calls)
	}
	s = fakeServer(t)
	t.Setenv("NATIVE_WHY", "root")
	if err := install(context.Background(), Flags{InstallerVersion: "7.3.0", Lang: "en", Mode: "native"}); err == nil {
		t.Error("native where it cannot run")
	}
	t.Setenv("NATIVE_WHY", "")
	if err := install(context.Background(), Flags{InstallerVersion: "7.3.0", Lang: "en", Mode: "native", Role: "controller"}); err != nil {
		t.Errorf("native by flag: %v", err)
	}

	s = fakeServer(t)
	s.tools = system.Tools{Kubectl: true, Helm: true, Cluster: true}
	answering(t, map[string][]tui.Answer{
		title("kube.hostname"):     {{Value: "forms.example.com"}},
		title("kube.ingressClass"): {pick("traefik")},
		title("kube.tls"):          {no},
	})
	if err := install(context.Background(), Flags{InstallerVersion: "7.3.0", Lang: "en"}); err != nil || !s.called("kubeInstall") {
		t.Fatalf("kube: %v, %v", err, s.calls)
	}
	s = fakeServer(t)
	answering(t, map[string][]tui.Answer{title("runner.appUrl"): {{Value: "https://app"}}})
	if err := install(context.Background(), Flags{InstallerVersion: "7.3.0", Lang: "en", Role: "runner", Mode: "kubernetes", JoinToken: "afj_x"}); err != nil || !s.called("kubeInstall") {
		t.Fatalf("kube runner: %v, %v", err, s.calls)
	}
	s = fakeServer(t)
	answering(t, map[string][]tui.Answer{title("runner.appUrl"): {{Value: "https://app"}}})
	if err := install(context.Background(), Flags{InstallerVersion: "7.3.0", Lang: "en", Role: "runner", Mode: "kubernetes"}); err != nil {
		t.Errorf("kube runner to approve: %v", err)
	}
	_ = state.Remove()
	s.fail["kubeInstall"] = errors.New("helm failed")
	if err := install(context.Background(), Flags{InstallerVersion: "7.3.0", Lang: "en", Mode: "kubernetes", Hostname: "h", Yes: true}); err == nil {
		t.Error("kube failed")
	}
	if err := install(context.Background(), Flags{InstallerVersion: "7.3.0", Lang: "en", Role: "runner", Mode: "kubernetes", AppURL: "https://app", Yes: true}); err == nil {
		t.Error("kube runner failed")
	}
}

// TestInstallRunnerWizard checks Runners: inbound with its address asked, the app's
// self-signed certificate trusted, its version taken from the app ; outbound with a join
// token ; and a failed installation.
func TestInstallRunnerWizard(t *testing.T) {
	s := fakeServer(t)
	s.trusted, s.app = false, "7.3.1"
	answering(t, map[string][]tui.Answer{
		title("install.role"):     {pick("runner")},
		title("runner.appUrl"):    {{Value: "https://app/"}},
		title("runner.mode"):      {pick("inbound")},
		title("runner.trustCert"): {yes},
	})
	if err := install(context.Background(), Flags{InstallerVersion: "7.3.0", Lang: "en"}); err != nil {
		t.Fatal(err)
	}
	st, _ := state.Load()
	if st.Role != "runner" || st.Version != "7.3.1" {
		t.Errorf("state %+v", st)
	}
	for _, mode := range []string{"", "native"} {
		s = fakeServer(t)
		answering(t, map[string][]tui.Answer{})
		f := Flags{InstallerVersion: "7.3.0", Lang: "en", Role: "runner", AppURL: "https://app", JoinToken: "afj_x", RunnerMode: "outbound", Port: 8000, Mode: mode}
		if err := install(context.Background(), f); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
	}
	s = fakeServer(t)
	s.fail["composeInstall"] = errors.New("docker failed")
	answering(t, map[string][]tui.Answer{})
	if err := install(context.Background(), Flags{InstallerVersion: "7.3.0", Lang: "en", Role: "runner", AppURL: "https://app", Port: 8000}); err == nil {
		t.Error("runner failed")
	}
	if err := install(context.Background(), Flags{InstallerVersion: "7.3.0", Lang: "en", Port: 443, TLS: "off", AdminPassword: "x"}); err == nil {
		t.Error("controller failed")
	}
}

// TestInstallAppChecks checks the app's address asked again after a failure: unreachable,
// too old, its certificate refused ; a join token that is none ; and no address unattended.
func TestInstallAppChecks(t *testing.T) {
	ctx := context.Background()
	s := fakeServer(t)
	s.fail["healthAppVersion"] = errors.New("refused")
	p := &plan{appURL: "https://app", version: releases.Version{Major: 7, Minor: 3}}
	answering(t, map[string][]tui.Answer{title("runner.appUrl"): {{Value: "https://app"}, back}})
	if err := askApp(ctx, Flags{}, p); !errors.Is(err, ui.ErrBack) {
		t.Errorf("unreachable, then back: %v", err)
	}
	s.fail = map[string]error{}
	s.app = "7.2.0"
	if err := reachApp(ctx, Flags{}, p); err == nil {
		t.Error("too old")
	}
	t.Setenv("AF_SKIP_VERSION_CHECK", "1")
	if err := reachApp(ctx, Flags{}, p); err != nil {
		t.Errorf("old, the check skipped: %v", err)
	}
	s.trusted = false
	answering(t, map[string][]tui.Answer{title("runner.trustCert"): {no, abort}})
	if err := reachApp(ctx, Flags{}, p); err == nil {
		t.Error("certificate refused")
	}
	if err := reachApp(ctx, Flags{}, p); !errors.Is(err, ui.ErrAborted) {
		t.Errorf("aborted: %v", err)
	}
	ui.Fullscreen = false
	ui.Unattended = true
	defer func() { ui.Unattended = false }()
	if err := reachApp(ctx, Flags{Yes: true}, &plan{appURL: "https://app"}); err != nil {
		t.Errorf("trusted unattended: %v", err)
	}
	if err := askApp(ctx, Flags{}, &plan{}); err == nil {
		t.Error("no address unattended")
	}
	if err := askApp(ctx, Flags{}, &plan{join: "nope"}); err == nil {
		t.Error("a join token that is none")
	}
	if err := doRunner(ctx, &plan{join: "nope"}); err == nil {
		t.Error("doRunner: a join token that is none")
	}
	if err := doKubeRunner(ctx, Flags{}, &plan{join: "nope"}); err == nil {
		t.Error("doKubeRunner: a join token that is none")
	}
}

// TestChooseEngine checks the engine offered by what is there: a runtime asked for and
// installed, none ready, one, both, Kubernetes ready or not, native disabled, and the runtime
// installation declined or failed.
func TestChooseEngine(t *testing.T) {
	ctx := context.Background()
	s := fakeServer(t)
	answering(t, map[string][]tui.Answer{title("install.confirmRuntime", "runtime", title("runtime.podman")): {yes, no}})
	if v, err := chooseEngine(ctx, Flags{Runtime: "podman"}, system.Tools{}, "", true, false); err != nil || v != "podman" || !s.called("systemInstallPodman") {
		t.Errorf("podman installed: %q, %v", v, err)
	}
	if _, err := chooseEngine(ctx, Flags{Runtime: "podman"}, system.Tools{}, "", true, false); err == nil {
		t.Error("podman declined")
	}
	kube := system.Tools{Kubectl: true, Helm: true, Cluster: true}
	t.Setenv("NATIVE_WHY", "unsupported")
	for name, c := range map[string]struct {
		tools  system.Tools
		runner bool
		want   string
	}{
		"none, kube":   {kube, false, "kubernetes"},
		"none, runner": {kube, true, "kubernetes"},
		"podman only":  {system.Tools{Podman: true, PodmanCompose: true}, false, "podman"},
		"both":         {system.Tools{Docker: true, DockerCompose: true, Podman: true, PodmanCompose: true}, false, "docker"},
	} {
		v, err := chooseEngine(ctx, Flags{}, c.tools, "", true, c.runner)
		if err != nil || v != c.want {
			t.Errorf("%s: %q, %v", name, v, err)
		}
	}
	answering(t, map[string][]tui.Answer{title("install.runtime"): {pick("docker")}})
	if v, err := chooseEngine(ctx, Flags{InstallRuntime: true}, system.Tools{}, "", false, false); err != nil || v != "docker" || !s.called("systemInstallDocker") {
		t.Errorf("docker installed: %q, %v", v, err)
	}
	answering(t, map[string][]tui.Answer{title("install.runtime"): {abort}})
	if _, err := chooseEngine(ctx, Flags{}, system.Tools{}, "", false, false); err == nil {
		t.Error("aborted")
	}
	answering(t, map[string][]tui.Answer{})
	if err := installRuntime(ctx, Flags{}, system.Podman); err == nil {
		t.Error("declined")
	}
	answering(t, map[string][]tui.Answer{title("install.confirmRuntime", "runtime", title("runtime.docker")): {abort}})
	if err := installRuntime(ctx, Flags{}, system.Docker); err == nil {
		t.Error("aborted")
	}
}

// TestInstallStateAndFound checks an installation that is there already: the upgrade offered,
// declined or taken ; and the notes that cannot be fetched, or are empty.
func TestInstallStateAndFound(t *testing.T) {
	ctx := context.Background()
	s := fakeServer(t)
	(&state.State{Mode: state.Compose, Role: "controller", Version: "7.3.0", Dir: t.TempDir(), Name: "af"}).Save()
	answering(t, map[string][]tui.Answer{title("install.upgradeInstead"): {no, yes}})
	if err := install(ctx, Flags{InstallerVersion: "7.3.0", Lang: "en"}); err != nil {
		t.Errorf("declined: %v", err)
	}
	if err := install(ctx, Flags{InstallerVersion: "7.3.0", Lang: "en", Version: "7.3.0"}); err != nil {
		t.Errorf("upgrade instead: %v", err)
	}
	_ = state.Remove()
	s.fail["releasesFetchChangelog"] = errors.New("offline")
	if err := versionNotes(ctx, releases.Version{Major: 7, Minor: 3}); err != nil {
		t.Error(err)
	}
	if err := versionNotes(ctx, releases.Version{Major: 7, Minor: 3}); err != nil {
		t.Error("cached")
	}
	s.fail = map[string]error{}
	s.notes = nil
	if err := versionNotes(ctx, releases.Version{Major: 7, Minor: 9}); err != nil {
		t.Error(err)
	}
	answering(t, map[string][]tui.Answer{title("language.title"): {abort}})
	if err := install(ctx, Flags{InstallerVersion: "7.3.0"}); err == nil {
		t.Error("language aborted")
	}
	answering(t, map[string][]tui.Answer{})
	if err := install(ctx, Flags{InstallerVersion: "dev", Version: "x", Lang: "en"}); err == nil {
		t.Error("a version that is none")
	}
}

// TestReview checks the review of each kind of installation.
func TestReview(t *testing.T) {
	v := releases.Version{Major: 7, Minor: 3}
	for _, p := range []*plan{
		{version: v, role: "runner", mode: "kubernetes", join: "afj_x"},
		{version: v, role: "runner", mode: "kubernetes"},
		{version: v, role: "runner", mode: "native", runnerMode: "inbound", host: "10.0.0.5", port: 8000, join: "afj_x"},
		{version: v, role: "runner", mode: "compose", runtime: "docker", runnerMode: "outbound", port: 8000},
		{version: v, role: "controller", mode: "kubernetes", hostname: "h"},
		{version: v, role: "controller", mode: "compose", runtime: "docker", tls: "self"},
	} {
		if len(review(p)) < 4 {
			t.Errorf("review of %+v", p)
		}
	}
}

// TestInstallBackAndAbort checks going back through the wizard: from the notes to the
// language asked in this run, from the cluster to the engine (Kubernetes), from the folder to
// the engine (native), from HTTPS to the port ; and each question aborted.
func TestInstallBackAndAbort(t *testing.T) {
	ctx := context.Background()
	s := fakeServer(t)
	s.notes = notes73
	notes := title("install.notesTitle", "version", "7.3.0")
	answering(t, map[string][]tui.Answer{notes: {back}})
	if err := install(ctx, Flags{InstallerVersion: "7.3.0"}); err != nil {
		t.Errorf("notes back: %v", err)
	}

	s = fakeServer(t)
	s.tools = system.Tools{Kubectl: true, Helm: true, Cluster: true}
	answering(t, map[string][]tui.Answer{title("kube.namespace"): {back}, title("kube.hostname"): {{Value: "h"}}})
	if err := install(ctx, Flags{InstallerVersion: "7.3.0", Lang: "en"}); err != nil {
		t.Errorf("kube back: %v", err)
	}
	s = fakeServer(t)
	answering(t, map[string][]tui.Answer{title("install.runtime"): {pick("native")}, title("install.dir"): {back}})
	if err := install(ctx, Flags{InstallerVersion: "7.3.0", Lang: "en"}); err != nil {
		t.Errorf("native back: %v", err)
	}
	s = fakeServer(t)
	answering(t, map[string][]tui.Answer{title("install.tls"): {back}})
	if err := install(ctx, Flags{InstallerVersion: "7.3.0", Lang: "en"}); err != nil {
		t.Errorf("https back: %v", err)
	}

	for _, q := range []string{title("install.runtime"), title("install.dir"), title("install.port"), title("install.ready"), title("install.notesTitle", "version", "7.3.0"), title("install.adminPassword")} {
		s = fakeServer(t)
		s.notes = notes73
		answering(t, map[string][]tui.Answer{q: {abort}})
		if err := install(ctx, Flags{InstallerVersion: "7.3.0", Lang: "en"}); !errors.Is(err, ui.ErrAborted) {
			t.Errorf("%s aborted: %v", q, err)
		}
	}
}

// TestInstallUnattended checks an installation with every answer a flag, nothing asked.
func TestInstallUnattended(t *testing.T) {
	s := fakeServer(t)
	unattended(t)
	f := Flags{InstallerVersion: "7.3.0", Role: "controller", Mode: "compose", Runtime: "docker", Dir: t.TempDir(), Name: "af", Port: 443, TLS: "off"}
	if err := install(context.Background(), f); err != nil || !s.called("composeInstall") {
		t.Errorf("%v, %v", err, s.calls)
	}
}
