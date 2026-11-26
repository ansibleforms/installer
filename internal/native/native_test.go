package native

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ansibleforms/installer/internal/compose"
	"github.com/ansibleforms/installer/internal/fakebin"
	"github.com/ansibleforms/installer/internal/releases"
	"github.com/ansibleforms/installer/internal/state"
	"github.com/ansibleforms/installer/internal/system"
)

// the app's backups run the client the installer found : the new names with the server's
// certificate check off (the local database has no TLS), the old names as they are
func TestClientCommand(t *testing.T) {
	cases := map[string]string{
		"mariadb-dump": "mariadb-dump --ssl-verify-server-cert=OFF",
		"mariadb":      "mariadb --ssl-verify-server-cert=OFF",
		"mysqldump":    "mysqldump",
		"mysql":        "mysql",
	}
	for binary, want := range cases {
		if got := clientCommand(binary); got != want {
			t.Errorf("clientCommand(%s) = %q, want %q", binary, got, want)
		}
	}
}

// an inbound Runner's RTE serves HTTPS with the data folder's certificate ; the others HTTP
func TestRteEnvHTTPS(t *testing.T) {
	l := layout{dir: "/opt/ansibleforms-runner", name: "runner"}
	env := strings.Join(rteEnv(l, true), "\n")
	for _, want := range []string{
		"HTTPS=1",
		"HTTPS_CERT=/opt/ansibleforms-runner/data/certificates/cert.pem",
		"HTTPS_KEY=/opt/ansibleforms-runner/data/certificates/key.pem",
	} {
		if !strings.Contains(env, want+"\n") && !strings.HasSuffix(env, want) {
			t.Errorf("rteEnv(https) lacks %s", want)
		}
	}
	if strings.Contains(env, "HTTPS=0") {
		t.Error("rteEnv(https) still sets HTTPS=0")
	}
	plain := strings.Join(rteEnv(l, false), "\n")
	if !strings.Contains(plain, "HTTPS=0\n") || strings.Contains(plain, "HTTPS_CERT") {
		t.Errorf("rteEnv(http) = %s", plain)
	}
}

// ─── a server, faked ──────────────────────────────────────────────────────────

// the programs native runs ; each one fails when FAIL names a part of its call
var programs = []string{"apt-get", "env", "systemctl", "id", "useradd", "userdel", "chown", "mariadb", "mariadb-dump", "dnf"}

// fakeServer is a root server of Debian on amd64, as native sees it: its programs faked, the
// bundle, Node.js and uv served by a server of the test's own, the docker repository a local
// copy, systemd's units and the installer's state in folders of the test's own.
type fakeServer struct {
	bin  *fakebin.Bin
	dir  string
	web  string
	repo string
}

// fake is the body of a fake program: it fails when FAIL names its call, and runs HOOK when
// HOOKON names it (absolute /bin tools on the test's own files: a folder made where a link must
// go, a file made read-only, between two steps of the installer).
//
// Args:
//
//	name: the program.
//	then: what it does when it does not fail.
//
// Returns:
//
//	The body.
func fake(name, then string) string {
	return `case "` + name + ` $*" in *"${HOOKON:-//none//}"*) eval "$HOOK" ;; esac
case "` + name + ` $*" in *"${FAIL:-//none//}"*) echo "` + name + ` failed" >&2; exit 1 ;; esac
` + then
}

// newServer sets the fake server up.
//
// Args:
//
//	t: the test.
//
// Returns:
//
//	The server ; its dir is where AnsibleForms goes (not made yet).
func newServer(t *testing.T) *fakeServer {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("as root the state is /etc/ansibleforms/installer.json")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("FAIL", "")
	t.Setenv("HOOKON", "")
	t.Setenv("HOOK", "")
	s := &fakeServer{bin: fakebin.New(t), dir: filepath.Join(t.TempDir(), "af")}
	t.Setenv("DIR", s.dir)
	for _, p := range programs {
		s.bin.Add(t, p, fake(p, ""))
	}
	s.bin.Add(t, "id", fake("id", "exit 1"))
	s.bin.Add(t, "mariadb-dump", fake("mariadb-dump", "echo '-- dump'"))

	// root, on Debian, amd64
	euid, osr, arch := system.Geteuid, system.OSReleasePath, system.GOARCH
	system.Geteuid = func() int { return 0 }
	system.OSReleasePath = filepath.Join(t.TempDir(), "os-release")
	_ = os.WriteFile(system.OSReleasePath, []byte("ID=debian\n"), 0o644)
	system.GOARCH = "amd64"

	// the downloads
	node := archive(t, entry{name: "node-v26.2.0-linux-x64/bin/node", kind: tar.TypeReg, body: "#!/bin/sh\n", mode: 0o755})
	uv := archive(t, entry{name: "uv-x86_64-unknown-linux-gnu/uv", kind: tar.TypeReg, body: "#!/bin/sh\n", mode: 0o755})
	bundle := s.bundle(t, "NODE_MAJOR=26\nUV_VERSION=0.5.0\nPYTHON_VERSION=3.13\n")
	s.web = files(t, map[string][]byte{
		"/index.json":                            []byte(`[{"version":"v26.1.0"},{"version":"v26.2.0"},{"version":"v24.9.0"},{"version":"v26.x.0"}]`),
		"/v26.2.0/SHASUMS256.txt":                []byte(sum(node) + "  node-v26.2.0-linux-x64.tar.gz\nffff  other.tar.gz\n"),
		"/v26.2.0/node-v26.2.0-linux-x64.tar.gz": node,
		"/astral-sh/uv/releases/download/0.5.0/uv-x86_64-unknown-linux-gnu.tar.gz":                       uv,
		"/astral-sh/uv/releases/download/0.5.0/uv-x86_64-unknown-linux-gnu.tar.gz.sha256":                []byte(sum(uv)),
		"/" + releases.AppRepo + "/releases/download/7.3.0/ansibleforms-7.3.0-linux-amd64.tar.gz":        bundle,
		"/" + releases.AppRepo + "/releases/download/7.3.0/ansibleforms-7.3.0-linux-amd64.tar.gz.sha256": []byte(sum(bundle)),
		"/" + releases.AppRepo + "/releases/download/7.4.0/ansibleforms-7.4.0-linux-amd64.tar.gz":        bundle,
		"/" + releases.AppRepo + "/releases/download/7.4.0/ansibleforms-7.4.0-linux-amd64.tar.gz.sha256": []byte(sum(bundle)),
	})

	// the docker repository, its seed pointing at the rte container
	s.repo = t.TempDir()
	seed, err := os.ReadFile(filepath.Join("..", "compose", "testdata", "seed-7.3.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"data/seed.yaml":           string(seed),
		"data/ansible/ansible.cfg": "[defaults]\n",
		"data/mysql/init.sql":      "x",
		"docker-compose.yml":       "services: {}\n",
		"README.md":                "repo",
	} {
		p := filepath.Join(s.repo, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(content), 0o644)
	}

	shell := filepath.Join(t.TempDir(), "nologin")
	_ = os.WriteFile(shell, nil, 0o755)
	was := struct {
		unitDir, github, node, source, bundle string
		wait                                  func(context.Context, string, time.Duration) (string, error)
		poll                                  time.Duration
		nologin                               []string
	}{unitDir, githubBase, nodeDist, compose.Source, Bundle, waitApp, pollEvery, nologin}
	unitDir, githubBase, nodeDist, compose.Source, Bundle = t.TempDir(), s.web, s.web+"/", s.repo, ""
	waitApp = func(_ context.Context, url string, _ time.Duration) (string, error) {
		if v := os.Getenv("ANSWERS"); v != "" {
			return v, nil
		}
		return "", errors.New("no answer at " + url)
	}
	t.Setenv("ANSWERS", "7.3.0")
	pollEvery = 10 * time.Millisecond
	nologin = []string{filepath.Join(t.TempDir(), "none"), shell}
	t.Cleanup(func() {
		system.Geteuid, system.OSReleasePath, system.GOARCH = euid, osr, arch
		unitDir, githubBase, nodeDist, compose.Source, Bundle = was.unitDir, was.github, was.node, was.source, was.bundle
		waitApp, pollEvery, nologin = was.wait, was.poll, was.nologin
	})
	return s
}

// bundle makes a release bundle: its versions, the server with its functions folder, and the
// virtual environment's ansible-galaxy (uv, faked, makes none).
//
// Args:
//
//	t: the test.
//	versions: its versions.env.
//
// Returns:
//
//	The archive.
func (s *fakeServer) bundle(t *testing.T, versions string) []byte {
	return archive(t,
		entry{name: "af/versions.env", kind: tar.TypeReg, body: "# versions\n\n" + versions},
		entry{name: "af/server/src/functions/custom.js", kind: tar.TypeReg, body: "//"},
		entry{name: "af/venv/bin/ansible-galaxy", kind: tar.TypeReg, body: "#!/bin/sh\n" + fake("ansible-galaxy", ""), mode: 0o755},
	)
}

// listen opens a port that takes connections, as a started RTE would.
//
// Args:
//
//	t: the test.
//
// Returns:
//
//	The port.
func listen(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l.Addr().(*net.TCPAddr).Port
}

// controller is a controller's answers.
func (s *fakeServer) controller(tls string) compose.Options {
	return compose.Options{Role: compose.RoleController, Dir: s.dir, Name: "af", Version: "7.3.0", Port: 8443, TLS: tls}
}

// ─── install ──────────────────────────────────────────────────────────────────

// TestSupported checks root, a known distribution and architecture, and systemd are needed.
func TestSupported(t *testing.T) {
	s := newServer(t)
	_ = os.Remove(filepath.Join(s.bin.Dir, "systemctl"))
	if Supported() != "unsupported" {
		t.Error("without systemctl")
	}
	s.bin.Add(t, "systemctl", "")
	if Supported() != "" {
		t.Errorf("supported: %q", Supported())
	}
	system.Geteuid = func() int { return 1000 }
	if Supported() != "root" {
		t.Error("a user")
	}
}

// TestInstallController checks a controller installed: the packages, its user, the release
// with Node.js and Python, the data without MySQL's, its settings, its database, its seed
// pointed at the local RTE, its certificate, its units and its state.
func TestInstallController(t *testing.T) {
	s := newServer(t)
	st, res, err := Install(context.Background(), s.controller("self"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode != state.Native || !st.HTTPS || res.URL != "https://localhost:8443" || res.AdminPassword == "" {
		t.Errorf("state %+v, result %+v", st, res)
	}
	l := layout{dir: s.dir, name: "af"}
	for _, p := range []string{
		filepath.Join(l.release("7.3.0"), ".complete"),
		filepath.Join(l.release("7.3.0"), "node", "bin", "node"),
		filepath.Join(l.data(), "certificates", "cert.pem"),
		filepath.Join(l.home(), ".ssh"),
		l.unitPath("app"), l.unitPath("rte"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(l.data(), "mysql")); err == nil {
		t.Error("MySQL's data copied")
	}
	if seed, _ := os.ReadFile(filepath.Join(l.data(), "seed.yaml")); !strings.Contains(string(seed), "url: http://127.0.0.1:") {
		t.Errorf("seed %s", seed)
	}
	if unit, _ := os.ReadFile(l.unitPath("app")); strings.Contains(string(unit), "CAP_NET_BIND_SERVICE") {
		t.Error("port 8443 needs no capability")
	}
	for _, call := range []string{"useradd --system", "systemctl enable --now mariadb", "systemctl daemon-reload", "systemctl enable --now af", "chown root:af"} {
		if !s.bin.Called(call) {
			t.Errorf("no %s in %q", call, s.bin.Calls())
		}
	}
	if _, err := state.Load(); err != nil {
		t.Errorf("state %v", err)
	}
}

// TestInstallOtherTLS checks a controller with its own certificate and one without TLS, on a
// port that needs the capability, its user already there and the bundle a local file.
func TestInstallOtherTLS(t *testing.T) {
	s := newServer(t)
	s.bin.Add(t, "id", "")
	s.bin.Add(t, "sshpass", "")
	certDir := t.TempDir()
	cert, key := filepath.Join(certDir, "c.pem"), filepath.Join(certDir, "k.pem")
	if err := compose.SelfSigned(cert, key); err != nil {
		t.Fatal(err)
	}
	o := s.controller("own")
	o.CertFile, o.KeyFile, o.Port, o.AdminPassword = cert, key, 443, "given"
	st, res, err := Install(context.Background(), o)
	if err != nil || res.AdminPassword != "given" {
		t.Fatalf("own: %+v, %v", res, err)
	}
	if unit, _ := os.ReadFile(layout{dir: s.dir, name: "af"}.unitPath("app")); !strings.Contains(string(unit), "CAP_NET_BIND_SERVICE") {
		t.Error("port 443 needs the capability")
	}
	if s.bin.Called("useradd") {
		t.Error("an existing user made again")
	}
	_ = st

	s2 := newServer(t)
	local := filepath.Join(t.TempDir(), "bundle.tar.gz")
	_ = os.WriteFile(local, s2.bundle(t, "NODE_MAJOR=26\nUV_VERSION=0.5.0\nPYTHON_VERSION=3.13\n"), 0o644)
	Bundle = local
	st2, res2, err := Install(context.Background(), s2.controller("off"))
	if err != nil || st2.HTTPS || res2.URL != "http://localhost:8443" {
		t.Errorf("off: %+v, %+v, %v", st2, res2, err)
	}
}

// TestInstallRunner checks a Runner outbound (its own token kept) and inbound (its
// certificate, the app's trusted), each with its RTE alone.
func TestInstallRunner(t *testing.T) {
	s := newServer(t)
	port := listen(t)
	o := compose.Options{Role: compose.RoleRunner, Dir: s.dir, Name: "dc2", Version: "7.3.0", Port: port,
		Runner: compose.RunnerOptions{AppURL: "https://app", Mode: "outbound", JoinToken: "afj_x", Token: "given-token-0123456789"}}
	st, res, err := Install(context.Background(), o)
	if err != nil || st.Role != compose.RoleRunner || res.Token != "given-token-0123456789" {
		t.Fatalf("outbound: %+v, %+v, %v", st, res, err)
	}
	l := layout{dir: s.dir, name: "dc2"}
	env, _ := os.ReadFile(l.env("rte"))
	for _, want := range []string{"RTE_MODE=outbound", "RTE_JOIN_TOKEN=afj_x", "LISTEN_HOST=127.0.0.1"} {
		if !strings.Contains(string(env), want) {
			t.Errorf("rte.env lacks %s", want)
		}
	}
	if _, err := os.Stat(l.unitPath("app")); err == nil {
		t.Error("a Runner has no app")
	}

	s2 := newServer(t)
	o.Dir, o.Runner = s2.dir, compose.RunnerOptions{AppURL: "https://app", Mode: "inbound", Host: "10.0.0.5", Address: "https://10.0.0.5:8000", TrustAppCert: true}
	_, res, err = Install(context.Background(), o)
	if err != nil || res.CertFingerprint == "" || res.Token == "" {
		t.Fatalf("inbound: %+v, %v", res, err)
	}
	env, _ = os.ReadFile(layout{dir: s2.dir, name: "dc2"}.env("rte"))
	if !strings.Contains(string(env), "RTE_URL=https://10.0.0.5:8000") || !strings.Contains(string(env), "APP_TLS_VERIFY=0") {
		t.Errorf("inbound rte.env %s", env)
	}
}

// hook makes the fake program whose call contains on run a shell line first (with $DIR the
// installation folder).
//
// Args:
//
//	t: the test.
//	on: the part of the call.
//	line: the shell line.
func hook(t *testing.T, on, line string) {
	t.Setenv("HOOKON", on)
	t.Setenv("HOOK", line)
}

// TestInstallFails checks that each step that fails stops the installation with its error.
func TestInstallFails(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, s *fakeServer, o *compose.Options)
	}{
		{"folder not empty", func(t *testing.T, s *fakeServer, o *compose.Options) {
			_ = os.MkdirAll(filepath.Join(s.dir, "x"), 0o755)
		}},
		{"unit exists", func(t *testing.T, s *fakeServer, o *compose.Options) {
			_ = os.WriteFile(layout{name: "af"}.unitPath("app"), nil, 0o644)
		}},
		{"packages", func(t *testing.T, s *fakeServer, o *compose.Options) { t.Setenv("FAIL", "apt-get update") }},
		{"folder", func(t *testing.T, s *fakeServer, o *compose.Options) {
			parent := filepath.Dir(s.dir)
			_ = os.Chmod(parent, 0o500)
			t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
		}},
		{"user", func(t *testing.T, s *fakeServer, o *compose.Options) { t.Setenv("FAIL", "useradd") }},
		{"no bundle", func(t *testing.T, s *fakeServer, o *compose.Options) { o.Version = "9.9.9" }},
		{"switch", func(t *testing.T, s *fakeServer, o *compose.Options) {
			hook(t, "ansible-galaxy", `/bin/mkdir -p "$DIR/current/full"`)
		}},
		{"data", func(t *testing.T, s *fakeServer, o *compose.Options) {
			compose.Source = filepath.Join(t.TempDir(), "none")
		}},
		{"database", func(t *testing.T, s *fakeServer, o *compose.Options) { t.Setenv("FAIL", "enable --now mariadb") }},
		{"own", func(t *testing.T, s *fakeServer, o *compose.Options) { t.Setenv("FAIL", "chown -R") }},
		{"unit", func(t *testing.T, s *fakeServer, o *compose.Options) {
			hook(t, "chown root:af "+s.dir+"/rte.env", `/bin/chmod 0500 "`+unitDir+`"`)
			t.Cleanup(func() { _ = os.Chmod(unitDir, 0o755) })
		}},
		{"daemon-reload", func(t *testing.T, s *fakeServer, o *compose.Options) { t.Setenv("FAIL", "systemctl daemon-reload") }},
		{"enable", func(t *testing.T, s *fakeServer, o *compose.Options) { t.Setenv("FAIL", "enable --now af") }},
		{"state", func(t *testing.T, s *fakeServer, o *compose.Options) {
			file := filepath.Join(t.TempDir(), "file")
			_ = os.WriteFile(file, nil, 0o600)
			t.Setenv("XDG_CONFIG_HOME", file)
		}},
		{"no answer", func(t *testing.T, s *fakeServer, o *compose.Options) { t.Setenv("ANSWERS", "") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newServer(t)
			o := s.controller("off")
			c.setup(t, s, &o)
			if _, _, err := Install(context.Background(), o); err == nil {
				t.Errorf("%s: installed", c.name)
			}
		})
	}
}

// TestInstallRunnerFails checks a Runner whose certificate cannot be made, and one that does
// not take connections in time.
func TestInstallRunnerFails(t *testing.T) {
	s := newServer(t)
	o := compose.Options{Role: compose.RoleRunner, Dir: s.dir, Name: "dc2", Version: "7.3.0", Port: 1,
		Runner: compose.RunnerOptions{Mode: "inbound"}}
	if _, _, err := Install(context.Background(), o); err == nil {
		t.Error("a Runner without its address")
	}
	s = newServer(t)
	o.Dir, o.Runner = s.dir, compose.RunnerOptions{Mode: "outbound"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := Install(ctx, o); err == nil {
		t.Error("a Runner that never listens")
	}
	if err := waitPort(context.Background(), "x", 1, 30*time.Millisecond); err == nil {
		t.Error("waitPort past its time")
	}
}

// TestControllerFails checks the settings, the seed and the certificates that cannot be written.
func TestControllerFails(t *testing.T) {
	cases := map[string]func(t *testing.T, s *fakeServer, o *compose.Options){
		"app.env": func(t *testing.T, s *fakeServer, o *compose.Options) {
			hook(t, "mariadb -uroot", `/bin/mkdir -p "$DIR/app.env/x"`)
		},
		"rte.env": func(t *testing.T, s *fakeServer, o *compose.Options) {
			hook(t, "mariadb -uroot", `/bin/mkdir -p "$DIR/rte.env/x"`)
		},
		"seed": func(t *testing.T, s *fakeServer, o *compose.Options) {
			_ = os.Remove(filepath.Join(s.repo, "data", "seed.yaml"))
		},
		"self-signed": func(t *testing.T, s *fakeServer, o *compose.Options) {
			o.TLS = "self"
			_ = os.WriteFile(filepath.Join(s.repo, "data", "certificates"), nil, 0o644)
		},
		"own folder": func(t *testing.T, s *fakeServer, o *compose.Options) {
			o.TLS = "own"
			_ = os.WriteFile(filepath.Join(s.repo, "data", "certificates"), nil, 0o644)
		},
		"own cert": func(t *testing.T, s *fakeServer, o *compose.Options) {
			o.TLS, o.CertFile = "own", "/none/cert.pem"
		},
		"own key": func(t *testing.T, s *fakeServer, o *compose.Options) {
			cert := filepath.Join(t.TempDir(), "c.pem")
			_ = os.WriteFile(cert, []byte("x"), 0o600)
			o.TLS, o.CertFile, o.KeyFile = "own", cert, "/none/key.pem"
		},
		"runner rte.env": func(t *testing.T, s *fakeServer, o *compose.Options) {
			o.Role, o.Runner = compose.RoleRunner, compose.RunnerOptions{Mode: "outbound"}
			hook(t, "ansible-galaxy", `/bin/mkdir -p "$DIR/rte.env/x"`)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			s := newServer(t)
			o := s.controller("off")
			setup(t, s, &o)
			if _, _, err := Install(context.Background(), o); err == nil {
				t.Errorf("%s: installed", name)
			}
		})
	}
}

// ─── the release ──────────────────────────────────────────────────────────────

// TestSSHPass checks sshpass installed from the distribution, from EPEL on RHEL, and a
// warning only when it cannot be.
func TestSSHPass(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()
	installSSHPass(ctx)
	if !s.bin.Called("env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends sshpass") {
		t.Errorf("apt: %q", s.bin.Calls())
	}
	t.Setenv("FAIL", "apt-get")
	installSSHPass(ctx)
	_ = os.WriteFile(system.OSReleasePath, []byte("ID=rocky\nID_LIKE=rhel\n"), 0o644)
	s.bin.Add(t, "dnf", `case "$*" in "install -y sshpass") test -e "$FAKEBIN_LOG.epel" || exit 1 ;; "install -y epel-release") echo > "$FAKEBIN_LOG.epel" ;; esac`)
	installSSHPass(ctx)
	if !s.bin.Called("dnf install -y epel-release") {
		t.Error("EPEL not enabled")
	}
	s.bin.Add(t, "dnf", "exit 1")
	installSSHPass(ctx)
}

// TestReleaseSteps checks each step of a release that fails: the folder, a bundle that cannot
// be read or unpacked, a download that fails, its versions, Node.js, Python and the links.
func TestReleaseSteps(t *testing.T) {
	ctx := context.Background()
	versions := "NODE_MAJOR=26\nUV_VERSION=0.5.0\nPYTHON_VERSION=3.13\n"
	bundle := func(t *testing.T, s *fakeServer, entries ...entry) {
		p := filepath.Join(t.TempDir(), "b.tar.gz")
		_ = os.WriteFile(p, archive(t, entries...), 0o644)
		Bundle = p
	}
	cases := map[string]func(t *testing.T, s *fakeServer) string{
		"folder": func(t *testing.T, s *fakeServer) string {
			_ = os.MkdirAll(s.dir, 0o755)
			_ = os.WriteFile(filepath.Join(s.dir, "releases"), nil, 0o644)
			return "7.3.0"
		},
		"bundle missing":  func(t *testing.T, s *fakeServer) string { Bundle = "/none.tar.gz"; return "7.3.0" },
		"bundle broken":   func(t *testing.T, s *fakeServer) string { Bundle = system.OSReleasePath; return "7.3.0" },
		"download broken": func(t *testing.T, s *fakeServer) string { return "broken" },
		"no versions": func(t *testing.T, s *fakeServer) string {
			bundle(t, s, entry{name: "af/x", kind: tar.TypeReg})
			return "7.3.0"
		},
		"no node version": func(t *testing.T, s *fakeServer) string {
			bundle(t, s, entry{name: "af/versions.env", kind: tar.TypeReg, body: "UV_VERSION=1\n"})
			return "7.3.0"
		},
		"node link": func(t *testing.T, s *fakeServer) string {
			bundle(t, s, entry{name: "af/versions.env", kind: tar.TypeReg, body: versions}, entry{name: "af/node", kind: tar.TypeReg})
			return "7.3.0"
		},
		"no python version": func(t *testing.T, s *fakeServer) string {
			bundle(t, s, entry{name: "af/versions.env", kind: tar.TypeReg, body: "NODE_MAJOR=26\n"})
			return "7.3.0"
		},
		"persistent link": func(t *testing.T, s *fakeServer) string {
			bundle(t, s, entry{name: "af/versions.env", kind: tar.TypeReg, body: versions},
				entry{name: "af/venv/bin/ansible-galaxy", kind: tar.TypeReg, body: "#!/bin/sh\n", mode: 0o755},
				entry{name: "af/server/persistent", kind: tar.TypeReg})
			return "7.3.0"
		},
		"functions link": func(t *testing.T, s *fakeServer) string {
			bundle(t, s, entry{name: "af/versions.env", kind: tar.TypeReg, body: versions},
				entry{name: "af/server/", kind: tar.TypeDir, mode: 0o755},
				entry{name: "af/venv/bin/ansible-galaxy", kind: tar.TypeReg, body: "#!/bin/sh\n", mode: 0o755})
			return "7.3.0"
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			s := newServer(t)
			v := setup(t, s)
			if err := prepareRelease(ctx, layout{dir: s.dir, name: "af"}, v); err == nil {
				t.Errorf("%s: prepared", name)
			}
		})
	}
	// prepared already : used as it is
	s := newServer(t)
	l := layout{dir: s.dir, name: "af"}
	_ = os.MkdirAll(l.release("7.3.0"), 0o755)
	_ = os.WriteFile(filepath.Join(l.release("7.3.0"), ".complete"), nil, 0o644)
	if err := prepareRelease(ctx, l, "7.3.0"); err != nil || s.bin.Called("env") {
		t.Errorf("complete release: %v", err)
	}
}

// TestRuntimes checks Node.js and uv already there, and each way their download fails.
func TestRuntimes(t *testing.T) {
	ctx := context.Background()
	s := newServer(t)
	l := layout{dir: s.dir, name: "af"}
	node, err := installNode(ctx, l, "26")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := installNode(ctx, l, "26"); err != nil || again != node {
		t.Errorf("again: %s, %v", again, err)
	}
	if _, err := installNode(ctx, l, "99"); err == nil || !strings.Contains(err.Error(), "no Node.js 99") {
		t.Errorf("no release: %v", err)
	}
	nodeDist = s.web + "/none/"
	if _, err := installNode(ctx, l, "26"); err == nil {
		t.Error("no index")
	}
	if _, err := latestNode(ctx, "26"); err == nil {
		t.Error("latestNode without an index")
	}

	// the index names a release whose checksums are missing or lack the archive
	for name, sums := range map[string][]byte{"no sums": nil, "no line": []byte("ffff  other.tar.gz\n")} {
		files := map[string][]byte{"/index.json": []byte(`[{"version":"v26.5.0"}]`)}
		if sums != nil {
			files["/v26.5.0/SHASUMS256.txt"] = sums
		}
		nodeDist = filesServer(t, files) + "/"
		if _, err := installNode(ctx, l, "26"); err == nil {
			t.Errorf("%s: installed", name)
		}
	}
	// the archive itself missing, then a runtimes folder that cannot be made
	nodeDist = filesServer(t, map[string][]byte{"/index.json": []byte(`[{"version":"v26.6.0"}]`), "/v26.6.0/SHASUMS256.txt": []byte("00  node-v26.6.0-linux-x64.tar.gz\n")}) + "/"
	if _, err := installNode(ctx, l, "26"); err == nil {
		t.Error("no archive")
	}
	blocked := layout{dir: system.OSReleasePath, name: "af"}
	if _, err := installNode(ctx, blocked, "26"); err == nil {
		t.Error("runtimes under a file")
	}

	uv, err := installUV(ctx, l, "0.5.0")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := installUV(ctx, l, "0.5.0"); err != nil || again != uv {
		t.Errorf("uv again: %v", err)
	}
	if _, err := installUV(ctx, l, "9.9.9"); err == nil {
		t.Error("no uv release")
	}
	if _, err := installUV(ctx, blocked, "0.5.0"); err == nil {
		t.Error("uv under a file")
	}
	if err := installPython(ctx, l, l.release("x"), "9.9.9", "3.13"); err == nil {
		t.Error("python without uv")
	}
	t.Setenv("FAIL", "python install")
	if err := installPython(ctx, l, l.release("x"), "0.5.0", "3.13"); err == nil {
		t.Error("uv python install failed")
	}
}

// filesServer serves files, as files does, for a test that needs several servers.
func filesServer(t *testing.T, f map[string][]byte) string { return files(t, f) }

// TestGalaxyFails checks the collections that cannot be installed.
func TestGalaxyFails(t *testing.T) {
	s := newServer(t)
	l := layout{dir: s.dir, name: "af"}
	if err := installPython(context.Background(), l, l.release("x"), "0.5.0", "3.13"); err == nil {
		t.Error("ansible-galaxy is missing from the release")
	}
}

// TestSmallSteps checks the user's home that cannot be made, a settings file that cannot be
// given to the user, a link that cannot be replaced, and the MariaDB client's old name.
func TestSmallSteps(t *testing.T) {
	s := newServer(t)
	ctx := context.Background()
	if err := ensureUser(ctx, layout{dir: system.OSReleasePath, name: "af"}); err == nil {
		t.Error("a home under a file")
	}
	l := layout{dir: s.dir, name: "af"}
	_ = os.MkdirAll(s.dir, 0o755)
	_ = os.WriteFile(l.env("app"), nil, 0o640)
	t.Setenv("FAIL", "chown root:")
	if err := own(ctx, l); err == nil {
		t.Error("chown root: failed")
	}
	if err := switchTo(layout{dir: filepath.Join(s.dir, "none"), name: "af"}, "7.3.0"); err == nil {
		t.Error("a link in no folder")
	}
	if currentVersion(l) != "" {
		t.Error("no current release")
	}
	prune(layout{dir: filepath.Join(s.dir, "none")})
	_ = os.Remove(filepath.Join(s.bin.Dir, "mariadb"))
	_ = os.Remove(filepath.Join(s.bin.Dir, "mariadb-dump"))
	if mariadb("client") != "mariadb" {
		t.Error("the new name by default")
	}
	s.bin.Add(t, "mysql", "")
	if mariadb("client") != "mysql" {
		t.Error("the old name found")
	}
	if err := pipe(ctx, "SELECT 1;", "nothing-here"); err == nil {
		t.Error("a client that is not there")
	}
}

// installed installs a controller (or a Runner) on a fake server.
//
// Args:
//
//	t: the test.
//	role: controller or runner.
//
// Returns:
//
//	The server and the installation's state.
func installed(t *testing.T, role string) (*fakeServer, *state.State) {
	t.Helper()
	s := newServer(t)
	o := s.controller("off")
	if role == compose.RoleRunner {
		o.Role, o.Port, o.Runner = role, listen(t), compose.RunnerOptions{Mode: "outbound"}
	}
	st, _, err := Install(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return s, st
}

// TestUpgradeController checks an upgrade: the new release next to the old one, the files the
// new version adds, a backup, the services moved to it, the old releases pruned, the state
// saved ; then a rollback to the version before with its database restored.
func TestUpgradeController(t *testing.T) {
	s, st := installed(t, compose.RoleController)
	l := layout{dir: s.dir, name: "af"}
	_ = os.MkdirAll(l.release("7.1.0"), 0o755)
	_ = os.WriteFile(filepath.Join(s.repo, "data", "new.txt"), []byte("new"), 0o644)
	t.Setenv("ANSWERS", "7.4.0")
	undo, err := Upgrade(context.Background(), st, "7.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != "7.4.0" || currentVersion(l) != "7.4.0" || undo.Version != "7.3.0" || undo.Dump == "" {
		t.Errorf("state %+v, undo %+v", st, undo)
	}
	if _, err := os.Stat(l.release("7.1.0")); err == nil {
		t.Error("7.1.0 not pruned")
	}
	if _, err := os.Stat(filepath.Join(l.data(), "new.txt")); err != nil {
		t.Error("the new file not added")
	}
	if err := Rollback(context.Background(), st, undo); err != nil || st.Version != "7.3.0" || currentVersion(l) != "7.3.0" {
		t.Errorf("rollback %v, %+v", err, st)
	}
	if !s.bin.Called("mariadb -uroot") {
		t.Error("the database not restored")
	}
}

// TestUpgradeRunner checks a Runner moved without a backup, waited for on its port, and rolled
// back without a database.
func TestUpgradeRunner(t *testing.T) {
	s, st := installed(t, compose.RoleRunner)
	undo, err := Upgrade(context.Background(), st, "7.4.0")
	if err != nil || undo.Dump != "" {
		t.Fatalf("undo %+v, %v", undo, err)
	}
	if err := Rollback(context.Background(), st, undo); err != nil {
		t.Error(err)
	}
	if _, err := Backup(context.Background(), st); err == nil {
		t.Error("a Runner backed up")
	}
	_ = s
}

// TestUpgradeFails checks that each step that fails stops the upgrade, with what a rollback
// needs once the backup is made.
func TestUpgradeFails(t *testing.T) {
	cases := map[string]func(t *testing.T, s *fakeServer, st *state.State) string{
		"release": func(t *testing.T, s *fakeServer, st *state.State) string { return "9.9.9" },
		"data": func(t *testing.T, s *fakeServer, st *state.State) string {
			compose.Source = filepath.Join(t.TempDir(), "none")
			return "7.4.0"
		},
		"own": func(t *testing.T, s *fakeServer, st *state.State) string {
			t.Setenv("FAIL", "chown -R")
			return "7.4.0"
		},
		"backup": func(t *testing.T, s *fakeServer, st *state.State) string {
			t.Setenv("FAIL", "mariadb-dump")
			return "7.4.0"
		},
		"stop": func(t *testing.T, s *fakeServer, st *state.State) string {
			t.Setenv("FAIL", "systemctl stop")
			return "7.4.0"
		},
		"switch": func(t *testing.T, s *fakeServer, st *state.State) string {
			hook(t, "systemctl stop", `/bin/mkdir -p "$DIR/current.new/full"`)
			return "7.4.0"
		},
		"start": func(t *testing.T, s *fakeServer, st *state.State) string {
			t.Setenv("FAIL", "systemctl start")
			return "7.4.0"
		},
		"no answer": func(t *testing.T, s *fakeServer, st *state.State) string { t.Setenv("ANSWERS", ""); return "7.4.0" },
		"old answer": func(t *testing.T, s *fakeServer, st *state.State) string {
			t.Setenv("ANSWERS", "7.3.0")
			return "7.4.0"
		},
		"ssh folder": func(t *testing.T, s *fakeServer, st *state.State) string {
			ssh := filepath.Join(layout{dir: s.dir}.data(), "ssh")
			_ = os.RemoveAll(ssh)
			_ = os.WriteFile(ssh, nil, 0o644)
			return "7.4.0"
		},
		"gitconfig": func(t *testing.T, s *fakeServer, st *state.State) string {
			g := filepath.Join(layout{dir: s.dir}.data(), "git", ".gitconfig")
			_ = os.Remove(g)
			_ = os.Symlink("/none/gitconfig", g)
			return "7.4.0"
		},
		"home link": func(t *testing.T, s *fakeServer, st *state.State) string {
			_ = os.RemoveAll(layout{dir: s.dir}.home())
			return "7.4.0"
		},
		"backups": func(t *testing.T, s *fakeServer, st *state.State) string {
			_ = os.WriteFile(layout{dir: s.dir}.backups(), nil, 0o644)
			return "7.4.0"
		},
		"settings copy": func(t *testing.T, s *fakeServer, st *state.State) string {
			_ = os.Remove(layout{dir: s.dir}.env("rte"))
			return "7.4.0"
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			s, st := installed(t, compose.RoleController)
			to := setup(t, s, st)
			if _, err := Upgrade(context.Background(), st, to); err == nil {
				t.Errorf("%s: upgraded", name)
			}
		})
	}
	t.Run("runner port", func(t *testing.T) {
		_, st := installed(t, compose.RoleRunner)
		st.Port = 1
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if _, err := Upgrade(ctx, st, "7.4.0"); err == nil {
			t.Error("a Runner that does not listen")
		}
	})
}

// TestRollbackFails checks a link that cannot be moved back, a restore that fails and services
// that do not start.
func TestRollbackFails(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, s *fakeServer) *Undo{
		"switch":  func(t *testing.T, s *fakeServer) *Undo { return &Undo{Version: "7.3.0"} },
		"restore": func(t *testing.T, s *fakeServer) *Undo { return &Undo{Version: "7.3.0", Dump: "/none.sql"} },
		"start": func(t *testing.T, s *fakeServer) *Undo {
			t.Setenv("FAIL", "systemctl start")
			return &Undo{Version: "7.3.0"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, st := installed(t, compose.RoleController)
			if name == "switch" {
				hook(t, "systemctl stop", `/bin/mkdir -p "$DIR/current.new/full"`)
			}
			if err := Rollback(context.Background(), st, setup(t, s)); err == nil {
				t.Errorf("%s: rolled back", name)
			}
		})
	}
}

// TestStatus checks the services shown, a controller that answers or not, and a Runner's port
// from its settings or its state.
func TestStatus(t *testing.T) {
	ctx := context.Background()
	_, st := installed(t, compose.RoleController)
	if err := Status(ctx, st); err != nil {
		t.Error(err)
	}
	t.Setenv("ANSWERS", "")
	if err := Status(ctx, st); err != nil {
		t.Error(err)
	}
	s, r := installed(t, compose.RoleRunner)
	if err := Status(ctx, r); err != nil {
		t.Error(err)
	}
	_ = os.Remove(layout{dir: s.dir}.env("rte"))
	r.Port = 1
	if err := Status(ctx, r); err != nil {
		t.Error(err)
	}
}

// TestUninstall checks the services removed and, with purge, the database, the folder and
// the user ; and each purge that fails.
func TestUninstall(t *testing.T) {
	ctx := context.Background()
	s, st := installed(t, compose.RoleController)
	if err := Uninstall(ctx, st, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.dir); err == nil || !s.bin.Called("userdel af") || !s.bin.Called("mariadb -uroot") {
		t.Errorf("not purged: %q", s.bin.Calls())
	}
	_, r := installed(t, compose.RoleRunner)
	if err := Uninstall(ctx, r, false); err != nil {
		t.Error(err)
	}
	_, st = installed(t, compose.RoleController)
	t.Setenv("FAIL", "mariadb -uroot")
	if err := Uninstall(ctx, st, true); err == nil {
		t.Error("the database stayed")
	}
	s, r = installed(t, compose.RoleRunner)
	parent := filepath.Dir(s.dir)
	_ = os.Chmod(parent, 0o500)
	defer os.Chmod(parent, 0o755)
	if err := Uninstall(ctx, r, true); err == nil {
		t.Error("a folder that cannot be removed")
	}
}

// TestDataFromMain checks a version whose tag is not in the docker repository yet: its data
// taken from main, with a warning.
func TestDataFromMain(t *testing.T) {
	s := newServer(t)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "docker-main/data/seed.yaml", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("s"))
	_ = tw.Close()
	_ = gz.Close()
	repo := files(t, map[string][]byte{"/" + compose.DockerRepo + "/tar.gz/refs/heads/main": buf.Bytes()})
	was := compose.Codeload
	compose.Codeload, compose.Source = repo+"/", ""
	defer func() { compose.Codeload = was }()
	l := layout{dir: s.dir, name: "af"}
	_ = os.MkdirAll(l.home(), 0o750)
	if err := writeData(context.Background(), l, "7.9.0", compose.RoleController, false); err != nil {
		t.Fatal(err)
	}
}
