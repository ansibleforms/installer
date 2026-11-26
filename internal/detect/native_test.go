package detect

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ansibleforms/installer/internal/state"
)

// systemd services of the installer's layout are taken over; a clone run by hand is found
// with its package.json's version, not taken over; the podman unit and sshd are neither
func TestFindNative(t *testing.T) {
	f := &fake{t: t, has: map[string]bool{}, out: map[string]string{}}
	dir, hand := nativeServices(t, f)
	got := find(context.Background(), f.env())
	if len(got) != 2 {
		t.Fatalf("found %+v, want 2", got)
	}
	var mine, theirs Install
	for _, i := range got {
		if i.Dir == dir {
			mine = i
		} else {
			theirs = i
		}
	}
	if mine.Engine != Systemd || mine.Role != RoleController || mine.Version != "7.3.0" || mine.Name != "ansibleforms" || mine.Port != 8443 || !mine.HTTPS || !mine.Running {
		t.Errorf("installer's layout: %+v", mine)
	}
	if !mine.Adoptable() || len(mine.Units) != 2 {
		t.Fatalf("installer's layout not adoptable: %+v", mine)
	}
	if st := mine.State(); st.Mode != state.Native || st.Dir != dir || st.Name != "ansibleforms" || st.Port != 8443 {
		t.Errorf("state: %+v", st)
	}
	if theirs.Dir != hand || theirs.Version != "7.2.0" || theirs.Problem != NativeLayout || theirs.Running {
		t.Errorf("by hand: %+v", theirs)
	}
}

// TestParseShow checks that systemctl show's blocks become units, a folder that may be
// missing (-/path, shown !/path) read as its path, and carriage returns dropped.
func TestParseShow(t *testing.T) {
	out := "Id=a.service\r\nWorkingDirectory=!/opt/a/server\r\nActiveState=active\r\n\r\nId=b.service\nUser=b\nEnvironmentFiles=-/opt/b/rte.env (ignore_errors=yes)\n\n\n"
	got := parseShow(out)
	if len(got) != 2 || got[0].ID != "a.service" || got[0].WorkingDirectory != "/opt/a/server" || got[0].ActiveState != "active" {
		t.Fatalf("units %+v", got)
	}
	if got[1].User != "b" || envFile(got[1]) != "/opt/b/rte.env" || !isRTE(got[1]) {
		t.Errorf("second unit %+v", got[1])
	}
	if len(parseShow("")) != 0 {
		t.Error("nothing shown, units found")
	}
}

// TestUnitHelpers checks which units are AnsibleForms run by Node.js, which are an RTE's, and
// the installation folder of each layout.
func TestUnitHelpers(t *testing.T) {
	for u, want := range map[unit]bool{
		{ID: "ansibleforms.service", ExecStart: "/opt/af/current/node/bin/node index.js"}: true,
		{Description: "Ansible Forms by hand", ExecStart: "npm start"}:                    true,
		{ID: "ansibleforms-af.service", ExecStart: "/usr/bin/podman compose -p af up -d"}: false,
		{ID: "ansibleforms-docker.service", ExecStart: "/usr/bin/docker start af_app"}:    false,
		{ID: "sshd.service", ExecStart: "/usr/sbin/sshd -D"}:                              false,
		{ID: "ansibleforms.service", ExecStart: "/usr/bin/python3 app.py"}:                false,
	} {
		if got := isAnsibleForms(u); got != want {
			t.Errorf("isAnsibleForms(%+v) = %v", u, got)
		}
	}
	if !isRTE(unit{ID: "forms-rte.service"}) || isRTE(unit{ID: "forms.service", EnvironmentFiles: "/opt/f/app.env"}) {
		t.Error("isRTE")
	}
	for u, want := range map[unit]string{
		{WorkingDirectory: "/opt/af/current/server"}:         "/opt/af",
		{WorkingDirectory: "/srv/forms/server/"}:             "/srv/forms",
		{WorkingDirectory: "/srv/other"}:                     "/srv/other",
		{EnvironmentFiles: "/etc/forms/app.env (ignore=no)"}: "/etc/forms",
		{}: "/",
	} {
		if got := baseDir(u); got != want {
			t.Errorf("baseDir(%+v) = %s, want %s", u, got, want)
		}
	}
}

// TestNativeProblem checks the installer's own layout taken over, and each way a layout is
// not its own.
func TestNativeProblem(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "releases", "7.3.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("releases", "7.3.0"), filepath.Join(dir, "current")); err != nil {
		t.Fatal(err)
	}
	mk := func(id, env string) *unit {
		return &unit{ID: id, User: "af", WorkingDirectory: filepath.Join(dir, "current", "server"), EnvironmentFiles: filepath.Join(dir, env)}
	}
	i := Install{Role: RoleController, Name: "af", Dir: dir, Version: "7.3.0"}
	app, rte := mk("af.service", "app.env"), mk("af-rte.service", "rte.env")
	if p := nativeProblem(i, app, rte); p != "" {
		t.Errorf("the installer's layout: %q", p)
	}
	other := *app
	other.User = "root"
	if p := nativeProblem(i, &other, rte); p != NativeLayout {
		t.Errorf("another user: %q", p)
	}
	if p := nativeProblem(i, app, nil); p != NativeLayout {
		t.Errorf("no RTE: %q", p)
	}
	runner := i
	runner.Role = RoleRunner
	if p := nativeProblem(runner, nil, rte); p != "" {
		t.Errorf("a Runner: %q", p)
	}
	unknown := i
	unknown.Version = ""
	if p := nativeProblem(unknown, app, rte); p != UnknownVersion {
		t.Errorf("no version: %q", p)
	}
	if err := os.Remove(filepath.Join(dir, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/opt/elsewhere", filepath.Join(dir, "current")); err != nil {
		t.Fatal(err)
	}
	if p := nativeProblem(i, app, rte); p != NativeLayout {
		t.Errorf("current outside releases: %q", p)
	}
}

// TestServicesNone checks no systemctl, no service unit and a show that fails find nothing.
func TestServicesNone(t *testing.T) {
	f := &fake{t: t, has: map[string]bool{}, out: map[string]string{}}
	if services(context.Background(), f.env()) != nil {
		t.Error("no systemctl")
	}
	f = &fake{t: t, has: map[string]bool{"systemctl": true}, out: map[string]string{"systemctl list-unit-files": "error"}}
	if services(context.Background(), f.env()) != nil {
		t.Error("list-unit-files failed")
	}
	f.out["systemctl list-unit-files"] = "getty@.service enabled\nfoo.socket enabled\n"
	if services(context.Background(), f.env()) != nil {
		t.Error("no service")
	}
	f.out["systemctl list-unit-files"] = "af.service enabled\n"
	f.out["systemctl show"] = "error"
	if services(context.Background(), f.env()) != nil {
		t.Error("show failed")
	}
}

// TestNativeInstallRunnerAndProbe checks a Runner's units named after its RTE, and a running
// controller's version asked on its port when no file says it.
func TestNativeInstallRunnerAndProbe(t *testing.T) {
	dir := t.TempDir()
	f := &fake{t: t, has: map[string]bool{}, out: map[string]string{}, answers: map[int]Answer{8443: {App: true, Version: "7.3.2"}}}
	r := nativeInstall(context.Background(), f.env(), dir, []unit{{ID: "forms-rte.service", EnvironmentFiles: filepath.Join(dir, "rte.env")}})
	if r.Role != RoleRunner || r.Name != "forms" {
		t.Errorf("runner %+v", r)
	}
	write(t, dir, map[string]string{"app.env": "PORT=8443\n"})
	c := nativeInstall(context.Background(), f.env(), dir, []unit{{ID: "forms.service", ActiveState: "active", EnvironmentFiles: filepath.Join(dir, "app.env")}})
	if c.Role != RoleController || c.Version != "7.3.2" {
		t.Errorf("controller %+v", c)
	}
}
