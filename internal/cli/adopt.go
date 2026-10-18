package cli

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/ansibleforms/installer/internal/compose"
	"github.com/ansibleforms/installer/internal/detect"
	"github.com/ansibleforms/installer/internal/i18n"
	"github.com/ansibleforms/installer/internal/kube"
	"github.com/ansibleforms/installer/internal/state"
	"github.com/ansibleforms/installer/internal/ui"
)

// ─── an AnsibleForms the installer does not manage yet ────────────────────────

// findInstalls looks for an AnsibleForms on this server that has no installer.json: by hand
// from the docker repository, with Podman, by Helm, as systemd services, or answering on a
// port with no engine at all.
//
// Args:
//
//	ctx: the context, to cancel.
//
// Returns:
//
//	What was found.
func findInstalls(ctx context.Context) []detect.Install {
	ui.Step(i18n.T("detect.looking"))
	return detect.Find(ctx)
}

// describe is one line about an installation found: its engine, role, version and place.
//
// Args:
//
//	i: the installation.
//
// Returns:
//
//	The line.
func describe(i detect.Install) string {
	engine := i18n.T("detect.engine_" + i.Engine)
	switch i.Engine {
	case detect.Docker, detect.Podman:
		engine = i18n.T("runtime." + i.Engine)
	}
	role := i18n.T("install.roleControllerShort")
	if i.Role == detect.RoleRunner {
		role = i18n.T("install.roleRunnerShort")
	}
	parts := []string{engine, role, orStr(i.Version, i18n.T("detect.versionUnknown")), i.Where()}
	if !i.Running {
		parts = append(parts, i18n.T("detect.stopped"))
	}
	return strings.Join(parts, " · ")
}

// reason explains why an installation found cannot be taken over, and what to do instead.
//
// Args:
//
//	i: the installation.
//
// Returns:
//
//	The explanation, "" when it can be.
func reason(i detect.Install) string {
	if i.Adoptable() {
		return ""
	}
	key := "detect.why_" + i.Problem
	if i.Problem == detect.Unmanaged && i.Role == detect.RoleRunner {
		// an RTE tells its version only with its token
		key = "detect.why_unmanagedRunner"
	}
	return i18n.T(key, "dir", i.Dir, "port", i.Port, "version", orStr(i.Version, i18n.T("detect.versionUnknown")))
}

// foundList is the text that lists the installations found, each one's reason under it.
//
// Args:
//
//	found: the installations.
//
// Returns:
//
//	The text.
func foundList(found []detect.Install) string {
	var lines []string
	for _, i := range found {
		lines = append(lines, "• "+describe(i))
		if r := reason(i); r != "" {
			for _, l := range strings.Split(r, "\n") {
				lines = append(lines, "  "+l)
			}
		}
	}
	return strings.Join(lines, "\n")
}

// offerFound shows the installations found and asks what to do: take one over and upgrade
// it, install a new one beside them anyway (an install only, confirmed apart), or stop.
//
// Args:
//
//	ctx: the context, to cancel.
//	f: the flags.
//	found: the installations found (at least one).
//	alongside: a new installation beside them may be offered (install, not upgrade).
//
// Returns:
//
//	The state of the installation taken over (saved), nil when the person chose to install
//	beside them.
//
// Errors:
//
//	ErrAborted when the person stops; why it cannot go on unattended, or when none can be
//	taken over for an upgrade.
func offerFound(ctx context.Context, f Flags, found []detect.Install, alongside bool) (*state.State, error) {
	var adoptable []detect.Install
	for _, i := range found {
		if i.Adoptable() {
			adoptable = append(adoptable, i)
		}
	}
	if !alongside && len(adoptable) == 0 {
		ui.Warn(i18n.T("detect.found") + "\n" + foundList(found))
		return nil, errors.New(i18n.T("detect.cannotAdopt"))
	}
	if ui.Unattended {
		ui.Warn(i18n.T("detect.found") + "\n" + foundList(found))
		// only upgrade --yes takes one over unattended, and only when it is the only one
		if alongside || len(adoptable) != 1 {
			return nil, errors.New(i18n.T("detect.unattended"))
		}
		return adopt(ctx, adoptable[0], false)
	}
	for {
		help := i18n.T("detect.found") + "\n\n" + foundList(found) + "\n\n"
		if len(adoptable) > 0 {
			help += i18n.T("detect.foundHelp")
		} else {
			help += i18n.T("detect.foundHelpNone")
		}
		var opts []ui.Option
		def := "stop"
		for n, i := range found {
			if !i.Adoptable() {
				opts = append(opts, ui.Option{Label: i18n.T("detect.cannot", "what", describe(i)), Value: "no:" + strconv.Itoa(n), Disabled: true})
				continue
			}
			if def == "stop" {
				def = "adopt:" + strconv.Itoa(n)
			}
			opts = append(opts, ui.Option{Label: i18n.T("detect.adopt", "what", describe(i)), Value: "adopt:" + strconv.Itoa(n), Hint: i18n.T("detect.adoptHint")})
		}
		if alongside {
			opts = append(opts, ui.Option{Label: i18n.T("detect.alongside"), Value: "alongside", Hint: i18n.T("detect.alongsideHint")})
		}
		opts = append(opts, ui.Option{Label: i18n.T("detect.stop"), Value: "stop", Hint: i18n.T("detect.stopHint")})
		ui.Explain(help)
		v, err := ui.Select(i18n.T("detect.choose"), opts, def)
		if errors.Is(err, ui.ErrBack) {
			continue
		}
		if err != nil {
			return nil, err
		}
		switch {
		case v == "stop":
			return nil, ui.ErrAborted
		case v == "alongside":
			ui.Explain(i18n.T("detect.alongsideHelp"))
			ok, err := ui.Confirm(i18n.T("detect.alongsideConfirm"), i18n.T("detect.alongsideGo"), i18n.T("app.cancel"), false)
			if errors.Is(err, ui.ErrBack) || err == nil && !ok {
				continue
			}
			return nil, err
		case strings.HasPrefix(v, "adopt:"):
			n, _ := strconv.Atoi(strings.TrimPrefix(v, "adopt:"))
			st, err := adopt(ctx, found[n], true)
			if errors.Is(err, ui.ErrBack) {
				continue
			}
			return st, err
		}
	}
}

// adopt takes an installation over: what the installer will remember of it, confirmed, then
// installer.json written (and, on Kubernetes, the release's values file).
//
// Args:
//
//	ctx: the context, to cancel.
//	i: the installation.
//	ask: ask to confirm (false: upgrade --yes).
//
// Returns:
//
//	Its state, saved.
//
// Errors:
//
//	ErrBack when the person declines (back to the list), or when the state cannot be saved.
func adopt(ctx context.Context, i detect.Install, ask bool) (*state.State, error) {
	st := i.State()
	row := func(k, v string) string { return ui.Row(i18n.T("review."+k), v, 15) }
	lines := []string{row("version", st.Version), row("role", strings.Split(describe(i), " · ")[1])}
	switch st.Mode {
	case state.Kubernetes:
		lines = append(lines, row("engine", i18n.T("install.modeKubernetesShort")), row("cluster", st.Namespace+" / "+st.Release))
	case state.Native:
		lines = append(lines, row("engine", i18n.T("install.modeNativeShort")), row("folder", st.Dir), row("name", st.Name), row("port", strconv.Itoa(st.Port)))
	default:
		lines = append(lines, row("engine", i18n.T("runtime."+st.Runtime)), row("folder", st.Dir), row("name", compose.Project(st)), row("port", strconv.Itoa(st.Port)))
	}
	help := i18n.T("detect.adoptReview", "file", state.Path()) + "\n\n" + strings.Join(lines, "\n")
	switch {
	case st.Mode == state.Compose && st.Role == compose.RoleController:
		help += "\n\n" + i18n.T("detect.adoptCompose")
	case st.Mode == state.Compose:
		help += "\n\n" + i18n.T("detect.adoptRunner")
	case st.Mode == state.Kubernetes:
		help += "\n\n" + i18n.T("detect.adoptKube")
	case st.Mode == state.Native:
		help += "\n\n" + i18n.T("detect.adoptNative")
	}
	if ask {
		ui.Explain(help)
		ok, err := ui.Confirm(i18n.T("detect.adoptConfirm"), i18n.T("detect.adoptGo"), i18n.T("app.cancel"), true)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ui.ErrBack
		}
	} else {
		ui.Muted(help)
	}
	if st.Mode == state.Kubernetes {
		if err := kube.Adopt(ctx, st); err != nil {
			return nil, err
		}
	}
	if err := st.Save(); err != nil {
		return nil, err
	}
	ui.Done(i18n.T("detect.adopted", "file", state.Path()))
	return st, nil
}

// statusFound lists, under the status, the installations found that the installer does not
// manage (all of them when it manages none).
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation the installer manages, nil when none.
//
// Returns:
//
//	How many were listed.
func statusFound(ctx context.Context, st *state.State) int {
	var others []detect.Install
	for _, i := range detect.Find(ctx) {
		if !i.Manages(st) {
			others = append(others, i)
		}
	}
	if len(others) == 0 {
		return 0
	}
	if st != nil {
		ui.Blank()
	}
	ui.Heading(i18n.T("detect.unmanagedTitle"))
	takeable := false
	for _, i := range others {
		ui.Line("• " + describe(i))
		if r := reason(i); r != "" {
			for _, l := range strings.Split(r, "\n") {
				ui.Muted("  " + l)
			}
		} else {
			takeable = true
		}
	}
	if takeable && st == nil {
		ui.Blank()
		ui.Muted(i18n.T("detect.statusAdopt"))
	}
	return len(others)
}
