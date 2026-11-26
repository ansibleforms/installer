package cli

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ansibleforms/installer/internal/detect"
	"github.com/ansibleforms/installer/internal/releases"
	"github.com/ansibleforms/installer/internal/state"
	"github.com/ansibleforms/installer/internal/tui"
	"github.com/ansibleforms/installer/internal/ui"
)

// unattended, with the state kept in a folder of the test's own
func unattended(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("as root the state is /etc/ansibleforms/installer.json")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	was := ui.Unattended
	ui.Unattended = true
	t.Cleanup(func() { ui.Unattended = was })
}

// a compose controller found by hand
var handMade = detect.Install{Engine: detect.Docker, Role: detect.RoleController, Version: "7.2.0", Running: true,
	Dir: "/opt/docker", Project: "docker", Name: "af", Port: 443, HTTPS: true}

// upgrade --yes takes over the only installation found, and remembers it
func TestOfferFoundAdoptsForUpgrade(t *testing.T) {
	unattended(t)
	st, err := offerFound(context.Background(), Flags{Yes: true}, []detect.Install{handMade}, false)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := state.Load()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Mode != state.Compose || saved.Dir != "/opt/docker" || saved.Name != "af" || saved.Project != "docker" || saved.Version != st.Version {
		t.Errorf("saved %+v", saved)
	}
}

// install --yes never takes one over nor installs beside it: it stops, nothing saved
func TestOfferFoundUnattendedInstallStops(t *testing.T) {
	unattended(t)
	if _, err := offerFound(context.Background(), Flags{Yes: true}, []detect.Install{handMade}, true); err == nil {
		t.Fatal("install --yes went on beside an installation found")
	}
	// two found : upgrade --yes does not choose either
	if _, err := offerFound(context.Background(), Flags{Yes: true}, []detect.Install{handMade, handMade}, false); err == nil {
		t.Fatal("upgrade --yes chose one of two")
	}
	if _, err := state.Load(); !errors.Is(err, state.ErrNone) {
		t.Errorf("state saved: %v", err)
	}
}

// nothing that can be taken over: the upgrade says why, nothing saved
func TestOfferFoundNothingAdoptable(t *testing.T) {
	unattended(t)
	dev := detect.Install{Engine: detect.None, Role: detect.RoleController, Version: "7.2.0", Running: true, Port: 3001, Problem: detect.Unmanaged}
	if _, err := offerFound(context.Background(), Flags{Yes: true}, []detect.Install{dev}, false); err == nil {
		t.Fatal("an installation no engine manages was taken over")
	}
	if _, err := state.Load(); !errors.Is(err, state.ErrNone) {
		t.Errorf("state saved: %v", err)
	}
	if r := reason(dev); r == "" {
		t.Errorf("no reason given")
	}
}

// one found at the version the installer installs is taken over without an upgrade, and said
// so; an older one, or a Runner in database mode, is upgraded
func TestCurrent(t *testing.T) {
	latest := releases.Version{Major: 7, Minor: 3}
	at := handMade
	at.Version = "7.3.0"
	if !current(at, latest) {
		t.Errorf("7.3.0 is not current at 7.3.0")
	}
	if current(handMade, latest) {
		t.Errorf("7.2.0 is current at 7.3.0")
	}
	if current(at, releases.Version{}) {
		t.Errorf("current when the latest is unknown")
	}
	at.Database = &detect.DatabaseRunner{}
	if current(at, latest) {
		t.Errorf("a Runner in database mode is current")
	}
}

// the compose file's problems name the file, and root is not told to run as root
func TestReasonNamesTheFile(t *testing.T) {
	i := handMade
	i.Problem, i.ProblemFile = detect.MissingComposeFile, "/opt/docker/.env"
	if r := reason(i); !strings.Contains(r, ".env") || !strings.Contains(r, "/opt/docker") {
		t.Errorf("reason: %s", r)
	}
	i.Problem, i.ProblemFile = detect.NoComposeFile, "/opt/docker/docker-compose.yaml"
	r := reason(i)
	if !strings.Contains(r, "docker-compose.yaml") {
		t.Errorf("reason: %s", r)
	}
	if os.Geteuid() == 0 && strings.Contains(r, "as root or") {
		t.Errorf("root told to run as root: %s", r)
	}
}

// ─── what was found, answered ─────────────────────────────────────────────────

// found are installations of every kind, adoptable or not.
var (
	kubeFound     = detect.Install{Engine: detect.Kubernetes, Role: detect.RoleController, Version: "7.3.0", Running: true, Namespace: "forms", Release: "af"}
	nativeFound   = detect.Install{Engine: detect.Systemd, Role: detect.RoleController, Version: "7.2.0", Dir: "/opt/af", Name: "af", Port: 8443, Units: []string{"af.service"}}
	runnerFound   = detect.Install{Engine: detect.Podman, Role: detect.RoleRunner, Version: "7.2.0", Dir: "/opt/r", Name: "r", Project: "r", Running: false}
	dbRunnerFound = detect.Install{Engine: detect.Docker, Role: detect.RoleRunner, Version: "7.2.0", Dir: "/opt/rte", Name: "af", Project: "rte", Database: &detect.DatabaseRunner{RunnerName: "rte72"}}
	portFound     = detect.Install{Engine: detect.None, Role: detect.RoleRunner, Port: 8000, Problem: detect.Unmanaged}
	noFileFound   = detect.Install{Engine: detect.Docker, Role: detect.RoleController, Dir: "/srv/x", Problem: detect.NoComposeFile, ProblemFile: "/srv/x/compose.yml"}
	argoFound     = detect.Install{Engine: detect.Kubernetes, Role: detect.RoleController, Namespace: "lab", Release: "lab", Problem: detect.ArgoCD, ArgoApp: "lab-forms"}
	noFileFound2  = detect.Install{Engine: detect.Docker, Role: detect.RoleController, Dir: "/srv/y", Problem: detect.NoComposeFile}
)

// TestFoundTexts checks the lines and the advice of each kind of installation found, as root
// or not.
func TestFoundTexts(t *testing.T) {
	fakeServer(t)
	all := []detect.Install{handMade, kubeFound, nativeFound, runnerFound, dbRunnerFound, portFound, noFileFound, argoFound, noFileFound2}
	list := foundList(all)
	for _, want := range []string{"compose.yml", "lab-forms", "rte72"[:0]} {
		if !strings.Contains(list, want) {
			t.Errorf("no %q in %s", want, list)
		}
	}
	if reason(handMade) != "" || reason(portFound) == "" {
		t.Error("reason of one taken over, of a Runner on a port")
	}
	t.Setenv("AS_ROOT", "1")
	if reason(noFileFound) == reason(noFileFound2) && reason(noFileFound) == "" {
		t.Error("reason as root")
	}
	if current(dbRunnerFound, releases.Version{Major: 7, Minor: 2}) || current(handMade, releases.Version{}) || !current(kubeFound, releases.Version{Major: 7, Minor: 3}) {
		t.Error("current")
	}
}

// TestOfferFoundChoices checks the choices offered with what was found: taken over (each kind
// reviewed), alongside confirmed or not, stop, back, abort ; one at the latest version ; and
// unattended with several.
func TestOfferFoundChoices(t *testing.T) {
	ctx := context.Background()
	choose, adoptQ, along := title("detect.choose"), title("detect.adoptConfirm"), title("detect.alongsideConfirm")
	for name, c := range map[string]struct {
		found     []detect.Install
		alongside bool
		answers   map[string][]tui.Answer
		ok        bool
		adopted   bool
	}{
		"compose":        {[]detect.Install{handMade, portFound}, true, map[string][]tui.Answer{choose: {back, pick("adopt:0")}}, true, true},
		"kube":           {[]detect.Install{kubeFound}, false, nil, true, true},
		"native":         {[]detect.Install{nativeFound}, false, nil, true, true},
		"runner":         {[]detect.Install{runnerFound}, false, nil, true, true},
		"database":       {[]detect.Install{dbRunnerFound}, false, nil, true, true},
		"review back":    {[]detect.Install{handMade}, false, map[string][]tui.Answer{adoptQ: {no, abort}}, false, false},
		"alongside":      {[]detect.Install{handMade}, true, map[string][]tui.Answer{choose: {pick("alongside"), pick("alongside")}, along: {no, yes}}, true, false},
		"alongside back": {[]detect.Install{handMade}, true, map[string][]tui.Answer{choose: {pick("alongside"), pick("stop")}, along: {back}}, false, false},
		"stop":           {[]detect.Install{handMade}, true, map[string][]tui.Answer{choose: {pick("stop")}}, false, false},
		"abort":          {[]detect.Install{handMade}, true, map[string][]tui.Answer{choose: {abort}}, false, false},
		"none adoptable": {[]detect.Install{argoFound}, true, map[string][]tui.Answer{choose: {pick("alongside")}, along: {yes}}, true, false},
		"none to take":   {[]detect.Install{argoFound}, false, nil, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			s := fakeServer(t)
			s.versions = []releases.Version{{Major: 7, Minor: 3}}
			answering(t, c.answers)
			st, err := offerFound(ctx, Flags{InstallerVersion: "dev"}, c.found, c.alongside)
			if (err == nil) != c.ok || (st != nil) != c.adopted {
				t.Errorf("%s: %+v, %v", name, st, err)
			}
		})
	}
	s := fakeServer(t)
	s.fail["kubeAdopt"] = errors.New("helm failed")
	answering(t, map[string][]tui.Answer{})
	if _, err := offerFound(ctx, Flags{InstallerVersion: "7.3.0"}, []detect.Install{kubeFound}, false); err == nil {
		t.Error("kube adopt failed")
	}
	s.fail = map[string]error{}
	t.Setenv("XDG_CONFIG_HOME", "/dev/null/x")
	if _, err := offerFound(ctx, Flags{InstallerVersion: "7.3.0"}, []detect.Install{handMade}, false); err == nil {
		t.Error("state not saved")
	}
	unattended(t)
	if _, err := offerFound(ctx, Flags{}, []detect.Install{handMade, nativeFound}, false); err == nil {
		t.Error("unattended with two")
	}
}

// TestStatusFound checks the installations not managed listed with the status, with advice,
// and one that could be taken over when nothing is managed.
func TestStatusFound(t *testing.T) {
	ctx := context.Background()
	s := fakeServer(t)
	answering(t, map[string][]tui.Answer{})
	s.found = []detect.Install{handMade, noFileFound}
	if err := status(ctx); err != nil {
		t.Error(err)
	}
	saved(state.Compose, "controller", "7.3.0")
	if err := status(ctx); err != nil {
		t.Error(err)
	}
	t.Setenv("XDG_CONFIG_HOME", "/dev/null/x")
	if err := status(ctx); err == nil {
		t.Error("a state that cannot be read")
	}
}

// TestInstallFoundFirst checks an installation that finds AnsibleForms already: taken over,
// then upgraded, or the choice aborted.
func TestInstallFoundFirst(t *testing.T) {
	ctx := context.Background()
	s := fakeServer(t)
	s.found = []detect.Install{handMade}
	answering(t, map[string][]tui.Answer{title("detect.choose"): {abort}})
	if err := install(ctx, Flags{InstallerVersion: "7.3.0", Lang: "en"}); err == nil {
		t.Error("aborted")
	}
	answering(t, map[string][]tui.Answer{title("detect.choose"): {pick("adopt:0")}})
	if err := install(ctx, Flags{InstallerVersion: "7.3.0", Lang: "en", Version: "7.3.0"}); err != nil {
		t.Errorf("adopted then upgraded: %v", err)
	}
	_ = state.Remove()
	answering(t, map[string][]tui.Answer{title("detect.choose"): {pick("alongside")}, title("detect.alongsideConfirm"): {yes}})
	if err := install(ctx, Flags{InstallerVersion: "7.3.0", Lang: "en", Port: 443, TLS: "off", AdminPassword: "x"}); err != nil {
		t.Errorf("alongside: %v", err)
	}
	_ = state.Remove()
	s.found = []detect.Install{argoFound}
	answering(t, map[string][]tui.Answer{})
	if err := upgrade(ctx, Flags{Lang: "en"}); err == nil {
		t.Error("upgrade with nothing to take over")
	}
}
