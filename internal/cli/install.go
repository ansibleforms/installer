package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ansibleforms/installer/internal/compose"
	"github.com/ansibleforms/installer/internal/health"
	"github.com/ansibleforms/installer/internal/i18n"
	"github.com/ansibleforms/installer/internal/kube"
	"github.com/ansibleforms/installer/internal/native"
	"github.com/ansibleforms/installer/internal/releases"
	"github.com/ansibleforms/installer/internal/state"
	"github.com/ansibleforms/installer/internal/system"
	"github.com/ansibleforms/installer/internal/ui"
)

// ─── the wizard ───────────────────────────────────────────────────────────────

// wstep is one question (or a few) of the wizard, under a step of the sidebar.
type wstep struct {
	// label is the key of its step at the left (step.<label>) ; several wsteps may share one
	label string
	// skip : not asked (a flag answered it, or the role or the mode has no such step)
	skip func() bool
	// ask asks and keeps the answer ; ui.ErrBack goes to the step before
	ask func() error
}

// runWizard asks the steps in order. Esc goes back to the one before (skipped ones skipped
// again) ; a step asked again shows its answer of before as its default.
//
// Args:
//
//	list: the steps.
//
// Errors:
//
//	ErrAborted when stopped, or a step's own error.
func runWizard(list []wstep) error {
	for i := 0; i < len(list); {
		ui.Steps(visibleLabels(list)...)
		s := list[i]
		if s.skip != nil && s.skip() {
			i++
			continue
		}
		ui.Section(stepName(s.label))
		err := s.ask()
		if errors.Is(err, ui.ErrBack) {
			j := i - 1
			for j > 0 && list[j].skip != nil && list[j].skip() {
				j--
			}
			i = max(0, j)
			continue
		}
		if err != nil {
			return err
		}
		i++
	}
	ui.Steps(visibleLabels(list)...)
	return nil
}

// visibleLabels are the steps at the left : those not skipped, each once, in order.
//
// Args:
//
//	list: the steps.
//
// Returns:
//
//	Their labels.
func visibleLabels(list []wstep) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range list {
		if (s.skip != nil && s.skip()) || seen[s.label] {
			continue
		}
		seen[s.label] = true
		out = append(out, stepName(s.label))
	}
	return out
}

// ─── the answers ──────────────────────────────────────────────────────────────

// plan is every answer of an install, kept across Esc.
type plan struct {
	version releases.Version
	tools   system.Tools

	role, mode string
	runtime    system.Runtime

	dir, name                      string
	port                           int
	tls, cert, key                 string
	admin                          string
	join, appURL, runnerMode, host string
	trust                          bool
	appVersion                     string

	namespace, release, hostname, ingressClass, storageClass string
	kubeTLS                                                  bool
}

// step names, translated once
func stepName(key string) string { return i18n.T("step." + key) }

// ─── install ──────────────────────────────────────────────────────────────────

// install asks how to install - a step at a time, back with Esc - then installs.
//
// Args:
//
//	ctx: the context, to cancel.
//	f: the flags.
//
// Errors:
//
//	When a step fails or the person stops.
func install(ctx context.Context, f Flags) error {
	if st, err := state.Load(); err == nil {
		ui.Warn(i18n.T("install.already", "version", st.Version, "mode", string(st.Mode)))
		goOn, err := ui.Confirm(i18n.T("install.upgradeInstead"), i18n.T("app.yes"), i18n.T("app.no"), true)
		if err != nil || !goOn {
			return err
		}
		return upgrade(ctx, f)
	}
	v, err := targetVersion(ctx, f)
	if err != nil {
		return err
	}
	p := &plan{version: v, role: f.Role, mode: f.Mode, runtime: system.Runtime(f.Runtime), dir: f.Dir, name: f.Name,
		port: f.Port, tls: f.TLS, cert: f.Cert, key: f.Key, admin: f.AdminPassword, join: f.JoinToken, appURL: f.AppURL,
		runnerMode: f.RunnerMode, host: f.Host, namespace: f.Namespace, release: f.Release, hostname: f.Hostname,
		ingressClass: f.IngressClass, storageClass: f.StorageClass, kubeTLS: true}
	// the server's tools looked at in the background from the start, while the release notes
	// and the role are read, so the engine step has them without a screen of its own
	toolsReady := make(chan system.Tools, 1)
	go func() { toolsReady <- system.Detect(ctx) }()
	detected := false

	controller := func() bool { return p.role == compose.RoleController }
	runner := func() bool { return p.role == compose.RoleRunner }
	kubernetes := func() bool { return controller() && p.mode == string(state.Kubernetes) }
	composeCtl := func() bool { return controller() && !kubernetes() }
	nativeMode := func() bool { return p.mode == string(state.Native) }

	var list []wstep
	list = []wstep{
		// first the language, the server's locale chosen ; its steps renamed once changed
		{label: "language", skip: func() bool { return ui.Unattended || f.Lang != "" }, ask: func() error {
			return chooseLanguage(func() { ui.Relabel(visibleLabels(list)...) })
		}},
		// then what this version brings : its breaking changes, removals and deprecations on top
		{label: "notes", skip: func() bool { return ui.Unattended }, ask: func() error {
			return versionNotes(ctx, v)
		}},
		{label: "role", ask: func() error {
			ui.Explain(i18n.T("install.roleHelp", "version", v.String()))
			r, err := pickAgain(f.Role, i18n.T("install.role"), []ui.Option{
				{Label: i18n.T("install.roleController"), Value: compose.RoleController, Hint: i18n.T("install.roleControllerHint")},
				{Label: i18n.T("install.roleRunner"), Value: compose.RoleRunner, Hint: i18n.T("install.roleRunnerHint")},
			}, orStr(p.role, compose.RoleController))
			p.role = r
			if err == nil {
				ui.Answered(map[bool]string{true: i18n.T("install.roleRunnerShort"), false: i18n.T("install.roleControllerShort")}[r == compose.RoleRunner])
			}
			return err
		}},
		{label: "engine", ask: func() error {
			if !detected {
				p.tools = <-toolsReady
				detected = true
			}
			if f.Mode == string(state.Kubernetes) {
				p.mode = f.Mode
				ui.Answered(i18n.T("install.modeKubernetesShort"))
				return nil
			}
			if f.Mode == string(state.Native) {
				if why := native.Supported(); why != "" {
					return errors.New(i18n.T("native.cannot_" + why))
				}
				p.mode = f.Mode
				ui.Answered(i18n.T("install.modeNativeShort"))
				return nil
			}
			// Docker, Podman, Kubernetes and directly on this server, in one list
			prev := string(p.runtime)
			if kubernetes() {
				prev = string(state.Kubernetes)
			}
			if nativeMode() {
				prev = string(state.Native)
			}
			v, err := chooseEngine(ctx, f, p.tools, prev, f.Mode == "", runner())
			if err != nil {
				return err
			}
			if v == string(state.Kubernetes) {
				p.mode = v
				ui.Answered(i18n.T("install.modeKubernetesShort"))
				return nil
			}
			if v == string(state.Native) {
				p.mode = v
				ui.Answered(i18n.T("install.modeNativeShort"))
				return nil
			}
			p.mode = string(state.Compose)
			p.runtime = system.Runtime(v)
			ui.Answered(i18n.T("runtime." + v))
			return nil
		}},
		// compose : the folder and the instance
		{label: "folder", skip: kubernetes, ask: func() error {
			def := "/opt/ansibleforms"
			if runner() {
				def = "/opt/ansibleforms-runner"
			}
			if !system.IsRoot() {
				home, _ := os.UserHomeDir()
				def = filepath.Join(home, filepath.Base(def))
			}
			if nativeMode() {
				ui.Explain(i18n.T("install.dirHelpNative"))
			} else {
				ui.Explain(i18n.T("install.dirHelp"))
			}
			d, err := textAgain(f.Dir, i18n.T("install.dir"), orStr(p.dir, def), notEmpty)
			if err != nil {
				return err
			}
			p.dir, _ = filepath.Abs(d)
			return nil
		}},
		{label: "folder", skip: kubernetes, ask: func() error {
			def := map[bool]string{true: "runner", false: "af"}[runner()]
			help, title := i18n.T("install.nameHelp"), i18n.T("install.name")
			if nativeMode() {
				// the system user and the services are named after it
				def = map[bool]string{true: "ansibleforms-runner", false: "ansibleforms"}[runner()]
				help, title = i18n.T("install.nameHelpNative"), i18n.T("install.nameNative")
			}
			ui.Explain(help)
			n, err := textAgain(f.Name, title, orStr(p.name, def), validName)
			p.name = n
			if err == nil {
				ui.Answered(p.dir + " · " + n)
			}
			return err
		}},
		// a runner : its app (it asks to join, an admin approves it there), its mode
		{label: "app", skip: func() bool { return !runner() }, ask: func() error {
			return askApp(ctx, f, p)
		}},
		{label: "mode", skip: func() bool { return !runner() || f.RunnerMode != "" }, ask: func() error {
			ui.Explain(i18n.T("runner.modeHelp"))
			m, err := ui.Select(i18n.T("runner.mode"), []ui.Option{
				{Label: i18n.T("runner.modeOutbound"), Value: "outbound", Hint: i18n.T("runner.modeOutboundHint")},
				{Label: i18n.T("runner.modeInbound"), Value: "inbound", Hint: i18n.T("runner.modeInboundHint")},
			}, orStr(p.runnerMode, "outbound"))
			p.runnerMode = m
			if err == nil {
				ui.Answered(i18n.T("runner.mode_" + m))
			}
			return err
		}},
		// the network : the controller's web port, a runner's port (and address, inbound)
		{label: "network", skip: func() bool { return kubernetes() || f.Port != 0 }, ask: func() error {
			title, def := i18n.T("install.port"), compose.FreePort("0.0.0.0", 443)
			ui.Explain(i18n.T("install.portHelp"))
			if runner() {
				title, def = i18n.T("runner.port"), compose.FreePort("0.0.0.0", 8000)
				ui.Explain(i18n.T("runner.portHelp"))
			}
			if p.port != 0 {
				def = p.port
			}
			s, err := ui.Input(title, strconv.Itoa(def), false, validPort)
			if err != nil {
				return err
			}
			p.port, _ = strconv.Atoi(s)
			return nil
		}},
		{label: "network", skip: func() bool { return !runner() || p.runnerMode != "inbound" }, ask: func() error {
			def := orStr(p.host, orStr(f.Host, compose.OutboundIP()))
			ui.Explain(i18n.T("runner.addressHelp"))
			h, err := textAgain(f.Host, i18n.T("runner.address"), def, notEmpty)
			p.host = h
			if err == nil {
				ui.Answered(fmt.Sprintf("%s:%d", h, p.port))
			}
			return err
		}},
		// a controller on compose : HTTPS and the first admin
		{label: "https", skip: func() bool { return !composeCtl() || f.TLS != "" }, ask: func() error {
			ui.Explain(i18n.T("install.tlsHelp"))
			t, err := ui.Select(i18n.T("install.tls"), []ui.Option{
				{Label: i18n.T("install.tlsSelf"), Value: "self", Hint: i18n.T("install.tlsSelfHint")},
				{Label: i18n.T("install.tlsOwn"), Value: "own", Hint: i18n.T("install.tlsOwnHint")},
				{Label: i18n.T("install.tlsOff"), Value: "off", Hint: i18n.T("install.tlsOffHint")},
			}, orStr(p.tls, "self"))
			p.tls = t
			if err == nil {
				ui.Answered(i18n.T("install.tls_" + t))
			}
			return err
		}},
		{label: "https", skip: func() bool { return !composeCtl() || p.tls != "own" || f.Cert != "" }, ask: func() error {
			c, err := ui.Input(i18n.T("install.certFile"), p.cert, false, fileExists)
			p.cert = c
			return err
		}},
		{label: "https", skip: func() bool { return !composeCtl() || p.tls != "own" || f.Key != "" }, ask: func() error {
			k, err := ui.Input(i18n.T("install.keyFile"), p.key, false, fileExists)
			p.key = k
			if err == nil {
				ui.Answered(i18n.T("install.tlsOwnShort"))
			}
			return err
		}},
		{label: "admin", skip: func() bool { return !composeCtl() || f.AdminPassword != "" || ui.Unattended }, ask: func() error {
			// typed twice, so a typo does not lock the admin out
			ui.Explain(i18n.T("install.adminPasswordHelp"))
			a, err := ui.NewPassword(i18n.T("install.adminPassword"), i18n.T("install.adminPasswordAgain"), i18n.T("check.passwordMismatch"), p.admin)
			if err != nil {
				return err
			}
			p.admin = a
			if a == "" {
				ui.Answered(i18n.T("install.adminGenerated"))
			}
			return nil
		}},
		// Kubernetes : the release, its ingress, its volumes
		{label: "cluster", skip: func() bool { return !kubernetes() }, ask: func() error {
			ns, err := textAgain(f.Namespace, i18n.T("kube.namespace"), orStr(p.namespace, "ansibleforms"), notEmpty)
			p.namespace = ns
			return err
		}},
		{label: "cluster", skip: func() bool { return !kubernetes() }, ask: func() error {
			r, err := textAgain(f.Release, i18n.T("kube.release"), orStr(p.release, "ansibleforms"), notEmpty)
			p.release = r
			if err == nil {
				ui.Answered(p.namespace + " / " + r)
			}
			return err
		}},
		{label: "ingress", skip: func() bool { return !kubernetes() }, ask: func() error {
			h, err := textAgain(f.Hostname, i18n.T("kube.hostname"), p.hostname, notEmpty)
			p.hostname = h
			return err
		}},
		{label: "ingress", skip: func() bool { return !kubernetes() }, ask: func() error {
			c, err := choice(f.IngressClass, i18n.T("kube.ingressClass"), kube.IngressClasses(ctx), orStr(p.ingressClass, "nginx"))
			p.ingressClass = c
			return err
		}},
		{label: "ingress", skip: func() bool { return !kubernetes() || f.Yes }, ask: func() error {
			t, err := ui.Confirm(i18n.T("kube.tls"), i18n.T("app.yes"), i18n.T("app.no"), p.kubeTLS)
			p.kubeTLS = t
			if err == nil {
				ui.Answered(p.hostname + map[bool]string{true: " · HTTPS", false: " · HTTP"}[t])
			}
			return err
		}},
		{label: "storage", skip: func() bool { return !kubernetes() }, ask: func() error {
			s, err := choice(f.StorageClass, i18n.T("kube.storageClass"), append([]string{""}, kube.StorageClasses(ctx)...), p.storageClass)
			p.storageClass = s
			return err
		}},
		// the review : every answer, then Install or back
		{label: "review", skip: func() bool { return ui.Unattended }, ask: func() error {
			ui.Explain(strings.Join(review(p), "\n"))
			ok, err := ui.Confirm(i18n.T("install.ready"), i18n.T("install.go"), i18n.T("install.changeAnswers"), true)
			if err != nil {
				return err
			}
			if !ok {
				return ui.ErrBack
			}
			ui.Answered(i18n.T("install.reviewed"))
			return nil
		}},
		{label: "install", ask: func() error { return nil }},
	}
	// unattended : no page to read, where the notes are said once
	if ui.Unattended {
		ui.Muted(i18n.T("notes.more", "url", "https://github.com/"+releases.AppRepo+"/releases/tag/"+v.String()))
	}
	if err := runWizard(list); err != nil {
		return err
	}
	ui.Section(stepName("install"))
	switch {
	case kubernetes():
		return doKube(ctx, f, p)
	case runner():
		return doRunner(ctx, p)
	}
	return doController(ctx, p)
}

// the release notes read once per run : going back to them does not fetch them again
var notesCache = map[string][]releases.Entry{}

// versionNotes shows the release notes of the version installed : what needs attention
// first (breaking changes, removals, deprecations), then the rest. Not fetched (offline, a
// version not released yet) : said, and the wizard goes on.
//
// Args:
//
//	ctx: the context, to cancel.
//	v: the version installed.
//
// Errors:
//
//	ErrAborted when stopped.
func versionNotes(ctx context.Context, v releases.Version) error {
	entries, ok := notesCache[v.String()]
	if !ok {
		all, err := releases.FetchChangelog(ctx, v)
		if err != nil {
			ui.Warn(i18n.T("install.notesUnavailable", "version", v.String()))
			notesCache[v.String()] = nil
			return nil
		}
		entries = releases.Between(all, previous(all, v), v)
		notesCache[v.String()] = entries
	}
	if len(entries) == 0 {
		return nil
	}
	ui.Explain(i18n.T("install.notesHelp", "version", v.String()))
	err := ui.Document(i18n.T("install.notesTitle", "version", v.String()), func() { showNotes(entries) })
	if err == nil {
		ui.Answered(i18n.T("install.notesRead"))
	}
	return err
}

// review lists the answers, as the review step shows them.
//
// Args:
//
//	p: the answers.
//
// Returns:
//
//	One line per answer.
func review(p *plan) []string {
	row := func(k, v string) string { return ui.Row(i18n.T("review."+k), v, 15) }
	engine := i18n.T("runtime." + string(p.runtime))
	if p.mode == string(state.Native) {
		engine = i18n.T("install.modeNativeShort")
	}
	out := []string{row("version", p.version.String())}
	switch {
	case p.role == compose.RoleRunner:
		out = append(out, row("role", i18n.T("install.roleRunnerShort")), row("engine", engine),
			row("folder", p.dir), row("name", p.name), row("app", p.appURL), row("mode", i18n.T("runner.mode_"+p.runnerMode)))
		if p.runnerMode == "inbound" {
			out = append(out, row("address", fmt.Sprintf("http://%s:%d", p.host, p.port)))
		} else {
			out = append(out, row("port", strconv.Itoa(p.port)))
		}
		if p.join != "" {
			out = append(out, row("register", i18n.T("runner.registerJoinShort")))
		} else {
			out = append(out, row("register", i18n.T("runner.registerApproveShort")))
		}
	case p.mode == string(state.Kubernetes):
		out = append(out, row("role", i18n.T("install.roleControllerShort")), row("engine", i18n.T("install.modeKubernetesShort")),
			row("cluster", p.namespace+" / "+p.release), row("ingress", p.hostname), row("storage", orStr(p.storageClass, i18n.T("kube.default"))))
	default:
		out = append(out, row("role", i18n.T("install.roleControllerShort")), row("engine", engine),
			row("folder", p.dir), row("name", p.name), row("port", strconv.Itoa(p.port)), row("https", i18n.T("install.tls_"+p.tls)))
	}
	return out
}

// askApp reads the join string (or asks the app's address), then reaches the app : its
// certificate (trusted when the person says so) and its version, which the runner runs.
//
// Args:
//
//	ctx: the context, to cancel.
//	f: the flags.
//	p: the answers.
//
// Errors:
//
//	When the app cannot be reached, is too old, or the person stops.
func askApp(ctx context.Context, f Flags, p *plan) error {
	if p.join != "" {
		j, err := compose.ParseJoin(p.join)
		if err != nil {
			return err
		}
		if j.AppURL != "" {
			p.appURL = j.AppURL
		}
	}
	if p.appURL == "" || (f.AppURL == "" && (p.join == "" || !strings.HasPrefix(p.join, "afjoin2."))) {
		if ui.Unattended && p.appURL == "" {
			return errors.New(i18n.T("runner.needApp"))
		}
		if !ui.Unattended {
			ui.Explain(i18n.T("runner.appUrlHelp"))
			u, err := ui.Input(i18n.T("runner.appUrl"), p.appURL, false, validURL)
			if err != nil {
				return err
			}
			p.appURL = u
		}
	}
	p.appURL = strings.TrimRight(p.appURL, "/")
	ui.Step(i18n.T("runner.checkingApp", "url", p.appURL))
	v, trusted, err := health.AppVersion(ctx, p.appURL)
	if err != nil && !trusted {
		// why it asks : under the question in the full screen, a line before it otherwise
		if ui.Fullscreen {
			ui.Explain(i18n.T("runner.selfSigned", "url", p.appURL))
		} else {
			ui.Warn(i18n.T("runner.selfSigned", "url", p.appURL))
		}
		ok, err2 := ui.Confirm(i18n.T("runner.trustCert"), i18n.T("app.yes"), i18n.T("app.no"), f.Yes || p.trust)
		if err2 != nil {
			return err2
		}
		if !ok {
			return errors.New(i18n.T("runner.certRefused"))
		}
		p.trust = true
		v, _, err = health.AppVersionInsecure(ctx, p.appURL)
	}
	if err != nil {
		return errors.New(i18n.T("runner.appUnreachable", "url", p.appURL, "error", err))
	}
	if pv, perr := releases.ParseVersion(v); perr == nil {
		if pv.Compare(releases.Version{Major: 7, Minor: 3}) < 0 && os.Getenv("AF_SKIP_VERSION_CHECK") != "1" {
			return errors.New(i18n.T("runner.appTooOld", "version", pv.String()))
		}
		if pv.String() != p.version.String() {
			ui.Warn(i18n.T("runner.versionFromApp", "version", pv.String()))
			p.version = pv
		}
	}
	ui.Done(i18n.T("runner.appReached", "version", v))
	ui.Answered(p.appURL)
	return nil
}

// ─── doing it ─────────────────────────────────────────────────────────────────

// doController installs a controller on compose.
//
// Args:
//
//	ctx: the context, to cancel.
//	p: the answers.
//
// Errors:
//
//	When a step fails.
func doController(ctx context.Context, p *plan) error {
	o := compose.Options{Role: compose.RoleController, Runtime: p.runtime, Version: p.version.String(), Dir: p.dir, Name: p.name,
		Port: p.port, TLS: p.tls, CertFile: p.cert, KeyFile: p.key, AdminPassword: p.admin}
	install := compose.Install
	if p.mode == string(state.Native) {
		install = native.Install
	}
	st, res, err := install(ctx, o)
	if err != nil {
		return err
	}
	lines := []string{
		i18n.T("done.url", "url", res.URL),
		i18n.T("done.user", "user", res.AdminUser),
		i18n.T("done.password", "password", res.AdminPassword),
		"",
		i18n.T("done.folder", "dir", st.Dir),
	}
	if p.tls == "self" {
		lines = append(lines, i18n.T("done.selfSigned"))
	}
	lines = append(lines, "", i18n.T("done.joinHint"))
	ui.Summary(i18n.T("done.title", "version", st.Version), lines)
	return nil
}

// doRunner installs a runner on compose.
//
// Args:
//
//	ctx: the context, to cancel.
//	p: the answers.
//
// Errors:
//
//	When a step fails.
func doRunner(ctx context.Context, p *plan) error {
	r := compose.RunnerOptions{AppURL: p.appURL, Mode: orStr(p.runnerMode, "outbound"), TrustAppCert: p.trust, Token: compose.Secret(32)}
	if p.join != "" {
		j, err := compose.ParseJoin(p.join)
		if err != nil {
			return err
		}
		r.JoinToken = j.Token
	}
	if r.Mode == "inbound" {
		r.Address = fmt.Sprintf("http://%s:%d", p.host, p.port)
	}
	o := compose.Options{Role: compose.RoleRunner, Runtime: p.runtime, Version: p.version.String(), Dir: p.dir, Name: p.name, Port: p.port, Runner: r}
	install := compose.Install
	if p.mode == string(state.Native) {
		install = native.Install
	}
	st, res, err := install(ctx, o)
	if err != nil {
		return err
	}
	lines := []string{
		i18n.T("runner.doneName", "name", st.Name),
		i18n.T("runner.doneMode", "mode", i18n.T("runner.mode_"+r.Mode)),
		i18n.T("runner.doneApp", "url", r.AppURL),
	}
	if r.Mode == "inbound" {
		lines = append(lines, i18n.T("runner.doneAddress", "url", res.URL))
	}
	if r.JoinToken != "" {
		lines = append(lines, "", i18n.T("runner.doneRegistered"))
	} else {
		// it asked to join : an admin approves it under Connections > Runners, after checking
		// the fingerprint of its token there is this one
		lines = append(lines, "", i18n.T("runner.doneApprove"),
			"", i18n.T("runner.doneFingerprint", "fingerprint", compose.Fingerprint(res.Token)))
	}
	lines = append(lines, "", i18n.T("done.folder", "dir", st.Dir))
	ui.Summary(i18n.T("runner.done", "version", st.Version), lines)
	return nil
}

// doKube installs the chart.
//
// Args:
//
//	ctx: the context, to cancel.
//	f: the flags.
//	p: the answers.
//
// Errors:
//
//	When helm fails.
func doKube(ctx context.Context, f Flags, p *plan) error {
	st, err := kube.Install(ctx, kube.Options{Version: p.version.String(), Namespace: p.namespace, Release: p.release,
		Hostname: p.hostname, IngressClass: p.ingressClass, TLS: p.kubeTLS, StorageClass: p.storageClass, ExtraValues: f.Values})
	if err != nil {
		return err
	}
	ui.Summary(i18n.T("done.title", "version", st.Version), []string{
		i18n.T("done.url", "url", st.URL),
		i18n.T("kube.doneSecret", "release", st.Release, "namespace", st.Namespace),
		"",
		i18n.T("kube.doneValues", "file", st.ValuesFile),
	})
	return nil
}

// ─── container engines ────────────────────────────────────────────────────────

// chooseEngine picks Docker or Podman, or Kubernetes for a controller, in one list : the
// engines the server has as they are, Docker or Podman to install when missing, Kubernetes
// greyed out when no cluster is reached.
//
// Args:
//
//	ctx: the context, to cancel.
//	f: the flags.
//	tools: what the server has.
//	prev: the answer of before (Esc), the default.
//	withKube: Kubernetes is listed (no --mode).
//	runner: a runner, for which Kubernetes is always greyed out (the Helm chart installs it).
//
// Returns:
//
//	"docker", "podman" or "kubernetes".
//
// Errors:
//
//	When the engine chosen is missing and cannot be installed.
func chooseEngine(ctx context.Context, f Flags, tools system.Tools, prev string, withKube, runner bool) (string, error) {
	if f.Runtime != "" {
		r := system.Runtime(f.Runtime)
		if !tools.ComposeReady(r) {
			if err := installRuntime(ctx, f, r); err != nil {
				return "", err
			}
		}
		return string(r), nil
	}
	var opts []ui.Option
	var ready []string
	for _, r := range []system.Runtime{system.Docker, system.Podman} {
		name := i18n.T("runtime." + string(r))
		if tools.ComposeReady(r) {
			ready = append(ready, string(r))
			opts = append(opts, ui.Option{Label: name, Value: string(r), Hint: i18n.T("runtime." + string(r) + "Hint")})
			continue
		}
		opts = append(opts, ui.Option{Label: i18n.T("runtime.toInstall", "runtime", name), Value: string(r), Hint: i18n.T("runtime." + string(r) + "InstallHint")})
	}
	kube := withKube && !runner && tools.Kubectl && tools.Helm && tools.Cluster
	nativeOption := func() ui.Option {
		hint := "install.modeNativeHint"
		if runner {
			hint = "install.modeNativeRunnerHint"
		}
		if why := native.Supported(); why != "" {
			return ui.Option{Label: i18n.T("install.modeNative_" + why), Value: string(state.Native), Disabled: true}
		}
		return ui.Option{Label: i18n.T("install.modeNative"), Value: string(state.Native), Hint: i18n.T(hint)}
	}
	if withKube {
		switch {
		case runner:
			opts = append(opts, ui.Option{Label: i18n.T("install.modeKubernetesRunner"), Value: string(state.Kubernetes), Disabled: true})
		case kube:
			opts = append(opts, ui.Option{Label: i18n.T("install.modeKubernetes"), Value: string(state.Kubernetes), Hint: i18n.T("install.modeKubernetesHint")})
		default:
			opts = append(opts, ui.Option{Label: i18n.T("install.modeKubernetesMissing"), Value: string(state.Kubernetes), Disabled: true})
		}
	}
	def := string(system.Docker)
	var help string
	switch len(ready) {
	case 0:
		help = i18n.T("install.noRuntime")
		if kube {
			def = string(state.Kubernetes)
		}
	case 1:
		def = ready[0]
		other := string(system.Podman)
		if ready[0] == other {
			other = string(system.Docker)
		}
		help = i18n.T("install.runtimeHelpOne", "runtime", i18n.T("runtime."+ready[0]), "other", i18n.T("runtime."+other))
	default:
		help = i18n.T("install.runtimeHelp")
	}
	if kube {
		help += "\n" + i18n.T("install.runtimeHelpKube")
	}
	if withKube {
		opts = append(opts, nativeOption())
	}
	ui.Explain(help)
	v, err := ui.Select(i18n.T("install.runtime"), opts, orStr(prev, def))
	if err != nil || v == string(state.Kubernetes) || v == string(state.Native) {
		return v, err
	}
	if tools.ComposeReady(system.Runtime(v)) {
		return v, nil
	}
	return v, installRuntime(ctx, f, system.Runtime(v))
}

// installRuntime installs Docker or Podman, once the person agrees (or --install-runtime).
//
// Args:
//
//	ctx: the context, to cancel.
//	f: the flags.
//	r: the runtime.
//
// Errors:
//
//	When the person declines or the installation fails.
func installRuntime(ctx context.Context, f Flags, r system.Runtime) error {
	if !f.InstallRuntime {
		ok, err := ui.Confirm(i18n.T("install.confirmRuntime", "runtime", i18n.T("runtime."+string(r))), i18n.T("app.yes"), i18n.T("app.no"), f.Yes)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New(i18n.T("install.runtimeNeeded"))
		}
	}
	ui.Step(i18n.T("install.installingRuntime", "runtime", i18n.T("runtime."+string(r))))
	if r == system.Podman {
		return system.InstallPodman(ctx)
	}
	return system.InstallDocker(ctx)
}

// ─── small helpers ────────────────────────────────────────────────────────────

// orStr gives a value, or a default for an empty one.
func orStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// pickAgain takes a flag's value, or asks (the answer of before as the default).
func pickAgain(flagValue, title string, options []ui.Option, def string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	return ui.Select(title, options, def)
}

// textAgain takes a flag's value (checked), or asks (the answer of before as the default).
func textAgain(flagValue, title, def string, validate func(string) error) (string, error) {
	return text(flagValue, title, def, validate)
}
