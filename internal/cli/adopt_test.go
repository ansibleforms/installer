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
