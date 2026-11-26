package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ansibleforms/installer/internal/fakebin"
	"github.com/ansibleforms/installer/internal/state"
	"github.com/ansibleforms/installer/internal/system"
)

// ─── a container host, faked ──────────────────────────────────────────────────

// host is a server with Docker and Podman, as compose sees it: its programs faked (each one
// fails when FAIL names a part of its call, and runs HOOK when HOOKON does), the docker
// repository a local copy of the testdata, and the state and the user's units in folders of
// the test's own.
type host struct {
	bin  *fakebin.Bin
	repo string
	dir  string
}

// fakeProgram is the body of a fake program.
//
// Args:
//
//	name: the program.
//	then: what it does when it does not fail.
//
// Returns:
//
//	The body.
func fakeProgram(name, then string) string {
	return `case "` + name + ` $*" in *"${HOOKON:-//none//}"*) eval "$HOOK" ;; esac
case "` + name + ` $*" in *"${FAIL:-//none//}"*) echo "` + name + ` failed" >&2; exit 1 ;; esac
` + then
}

// engine is what docker and podman answer: no container yet, healthy containers, a command
// for sh -c.
const engine = `case "$*" in
  "ps -a --format {{.Names}}") echo "${CONTAINERS:-}" ;;
  *"{{.State.Health.Status}}"*) echo "${HEALTH:-healthy}" ;;
  *"{{.State.Status}}"*) echo "${STATUS:-running}" ;;
  *logs*) echo "${LOGS:-}" ;;
esac`

// newHost sets the fake host up, as a user (not root, uid 1001).
//
// Args:
//
//	t: the test.
//
// Returns:
//
//	The host ; its dir is where AnsibleForms goes (not made yet).
func newHost(t *testing.T) *host {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("as root the state is /etc/ansibleforms/installer.json")
	}
	for _, k := range []string{"FAIL", "HOOKON", "HOOK", "CONTAINERS", "HEALTH", "STATUS", "LOGS", "AF_APP_IMAGE", "AF_RTE_IMAGE"} {
		t.Setenv(k, "")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USER", "tester")
	h := &host{bin: fakebin.New(t), dir: filepath.Join(t.TempDir(), "af")}
	t.Setenv("DIR", h.dir)
	for _, p := range []string{"docker", "podman"} {
		h.bin.Add(t, p, fakeProgram(p, engine))
	}
	for _, p := range []string{"chown", "systemctl", "loginctl"} {
		h.bin.Add(t, p, fakeProgram(p, ""))
	}
	h.bin.Add(t, "sh", fakeProgram("sh", `echo /usr/bin/podman`))

	h.repo = t.TempDir()
	put(t, h.repo, map[string]string{
		".env":                     read(t, "env-7.3.template"),
		"docker-compose.yml":       read(t, "compose-7.3.yml"),
		"data/seed.yaml":           read(t, "seed-7.3.yaml"),
		"data/mysql/conf.d/my.cnf": "[mysqld]\n",
	})
	wasSource, wasEuid, wasWait, wasPoll, wasSystemd := Source, geteuid, waitApp, pollEvery, systemdDir
	Source, pollEvery, systemdDir = h.repo, 10*time.Millisecond, t.TempDir()+"/"
	geteuid = func() int { return 1001 }
	t.Setenv("ANSWERS", "7.3.0")
	waitApp = func(_ context.Context, url string, _ time.Duration) (string, error) {
		if v := os.Getenv("ANSWERS"); v != "" {
			return v, nil
		}
		return "", errors.New("no answer at " + url)
	}
	t.Cleanup(func() {
		Source, geteuid, waitApp, pollEvery, systemdDir = wasSource, wasEuid, wasWait, wasPoll, wasSystemd
	})
	return h
}

// options are a controller's answers on the fake host.
func (h *host) options(r system.Runtime, tls string) Options {
	return Options{Role: RoleController, Runtime: r, Dir: h.dir, Name: "af", Version: "7.3.0", Port: 8443, TLS: tls}
}

// hookOn makes the fake program whose call contains on run a shell line first.
func hookOn(t *testing.T, on, line string) {
	t.Setenv("HOOKON", on)
	t.Setenv("HOOK", line)
}

// ─── install ──────────────────────────────────────────────────────────────────

// TestInstallController checks a controller on Docker: the repository's files, the images
// pinned, its secrets, its certificate, the data given to the containers' user, the images
// pulled, started, its state, and the app waited for.
func TestInstallController(t *testing.T) {
	h := newHost(t)
	st, res, err := Install(context.Background(), h.options(system.Docker, "self"))
	if err != nil {
		t.Fatal(err)
	}
	if res.URL != "https://localhost:8443" || res.AdminUser != "admin" || res.AdminPassword == "" || !st.HTTPS {
		t.Errorf("result %+v, state %+v", res, st)
	}
	if _, err := os.Stat(filepath.Join(h.dir, "data", "certificates", "cert.pem")); err != nil {
		t.Error("no certificate")
	}
	compose, _ := os.ReadFile(filepath.Join(h.dir, "docker-compose.yml"))
	if !strings.Contains(string(compose), "ansibleforms:7.3.0") {
		t.Errorf("not pinned: %s", compose)
	}
	for _, call := range []string{"docker run --rm -v " + h.dir + "/data:/d busybox chown", "docker compose -p af pull --ignore-pull-failures", "docker compose -p af up -d"} {
		if !h.bin.Called(call) {
			t.Errorf("no %s", call)
		}
	}
	if _, err := state.Load(); err != nil {
		t.Errorf("state: %v", err)
	}
}

// TestInstallPodmanRunner checks a Runner on Podman, inbound: its own compose file, its
// certificate, its settings, the data given through podman unshare, its systemd unit, and
// its container waited for until healthy.
func TestInstallPodmanRunner(t *testing.T) {
	h := newHost(t)
	o := Options{Role: RoleRunner, Runtime: system.Podman, Dir: h.dir, Name: "dc2", Version: "7.3.0", Port: 8000,
		Runner: RunnerOptions{AppURL: "https://app", Mode: "inbound", Host: "10.0.0.5", Address: "https://10.0.0.5:8000", JoinToken: "afj_x", TrustAppCert: true}}
	st, res, err := Install(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if res.CertFingerprint == "" || res.Token == "" || st.Role != RoleRunner {
		t.Errorf("result %+v", res)
	}
	env, _ := os.ReadFile(filepath.Join(h.dir, ".env"))
	for _, want := range []string{"RTE_MODE=inbound", "RTE_LOCAL_PORT=8000", "RTE_JOIN_TOKEN=afj_x", "APP_TLS_VERIFY=0", "HTTPS=1", "RTE_NAME=dc2"} {
		if !strings.Contains(string(env), want) {
			t.Errorf(".env lacks %s", want)
		}
	}
	if !h.bin.Called("podman unshare chown") || !h.bin.Called("podman compose -p dc2 pull") || !h.bin.Called("systemctl --user enable ansibleforms-dc2.service") || !h.bin.Called("loginctl enable-linger tester") {
		t.Errorf("calls %q", h.bin.Calls())
	}
}

// TestInstallOthers checks a controller with its own certificate and its own admin password
// as root, one without TLS on Podman whose systemd unit cannot be made (a warning), and an
// outbound Runner as uid 1000 with a token given.
func TestInstallOthers(t *testing.T) {
	h := newHost(t)
	geteuid = func() int { return 0 }
	certDir := t.TempDir()
	cert, key := filepath.Join(certDir, "c.pem"), filepath.Join(certDir, "k.pem")
	if err := SelfSigned(cert, key); err != nil {
		t.Fatal(err)
	}
	o := h.options(system.Docker, "own")
	o.CertFile, o.KeyFile, o.AdminPassword = cert, key, "given"
	if _, res, err := Install(context.Background(), o); err != nil || res.AdminPassword != "given" || !h.bin.Called("chown -R 1000:1000") {
		t.Fatalf("own: %+v, %v", res, err)
	}

	h = newHost(t)
	_ = os.Remove(filepath.Join(h.bin.Dir, "systemctl"))
	if st, res, err := Install(context.Background(), h.options(system.Podman, "off")); err != nil || st.HTTPS || res.URL != "http://localhost:8443" {
		t.Errorf("off: %+v, %v", res, err)
	}

	h = newHost(t)
	geteuid = func() int { return 1000 }
	o = Options{Role: RoleRunner, Runtime: system.Docker, Dir: h.dir, Name: "r", Version: "7.3.0", Port: 8000,
		Runner: RunnerOptions{Mode: "outbound", Token: "given-token-0123456789", RteName: "lab-runner"}}
	_, res, err := Install(context.Background(), o)
	if err != nil || res.Token != "given-token-0123456789" || h.bin.Called("docker run") {
		t.Errorf("outbound: %+v, %v", res, err)
	}
	if env, _ := os.ReadFile(filepath.Join(h.dir, ".env")); !strings.Contains(string(env), "RTE_LOCAL_PORT=127.0.0.1:8000") || !strings.Contains(string(env), "RTE_NAME=lab-runner") {
		t.Errorf(".env %s", env)
	}
}

// TestInstallFails checks that each step that fails stops the installation.
func TestInstallFails(t *testing.T) {
	cases := map[string]func(t *testing.T, h *host, o *Options){
		"folder not empty": func(t *testing.T, h *host, o *Options) { put(t, h.dir, map[string]string{"x": "x"}) },
		"folder unreadable": func(t *testing.T, h *host, o *Options) {
			put(t, filepath.Dir(h.dir), map[string]string{"af": "a file"})
		},
		"container exists": func(t *testing.T, h *host, o *Options) { t.Setenv("CONTAINERS", "other\naf_app") },
		"folder": func(t *testing.T, h *host, o *Options) {
			parent := filepath.Dir(h.dir)
			_ = os.Chmod(parent, 0o500)
			t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })
		},
		"fetch":      func(t *testing.T, h *host, o *Options) { Source = filepath.Join(t.TempDir(), "none") },
		"no compose": func(t *testing.T, h *host, o *Options) { _ = os.Remove(filepath.Join(h.repo, "docker-compose.yml")) },
		"no .env":    func(t *testing.T, h *host, o *Options) { _ = os.Remove(filepath.Join(h.repo, ".env")) },
		"secrets":    func(t *testing.T, h *host, o *Options) { _ = os.Chmod(filepath.Join(h.repo, ".env"), 0o444) },
		"self-signed": func(t *testing.T, h *host, o *Options) {
			o.TLS = "self"
			put(t, h.repo, map[string]string{"data/certificates": "x"})
		},
		"own cert": func(t *testing.T, h *host, o *Options) { o.TLS, o.CertFile = "own", "/none/c.pem" },
		"own key": func(t *testing.T, h *host, o *Options) {
			cert := filepath.Join(t.TempDir(), "c.pem")
			_ = os.WriteFile(cert, []byte("x"), 0o600)
			o.TLS, o.CertFile, o.KeyFile = "own", cert, "/none/k.pem"
		},
		"own data":  func(t *testing.T, h *host, o *Options) { t.Setenv("FAIL", "docker run") },
		"pull":      func(t *testing.T, h *host, o *Options) { t.Setenv("FAIL", "compose -p af pull") },
		"up":        func(t *testing.T, h *host, o *Options) { t.Setenv("FAIL", "compose -p af up") },
		"state":     func(t *testing.T, h *host, o *Options) { t.Setenv("XDG_CONFIG_HOME", filepath.Join(h.repo, ".env")) },
		"no answer": func(t *testing.T, h *host, o *Options) { t.Setenv("ANSWERS", "") },
		"runner fetch": func(t *testing.T, h *host, o *Options) {
			o.Role, o.Runner = RoleRunner, RunnerOptions{Mode: "outbound"}
			Source = filepath.Join(t.TempDir(), "none")
		},
		"runner files": func(t *testing.T, h *host, o *Options) {
			// an empty folder it may not write in, and nothing of the repository to copy
			o.Role, o.Runner = RoleRunner, RunnerOptions{Mode: "outbound"}
			_ = os.RemoveAll(filepath.Join(h.repo, "data"))
			_ = os.MkdirAll(h.dir, 0o500)
			t.Cleanup(func() { _ = os.Chmod(h.dir, 0o755) })
		},
		"runner certificate": func(t *testing.T, h *host, o *Options) {
			o.Role, o.Runner = RoleRunner, RunnerOptions{Mode: "inbound"}
		},
		"runner unhealthy": func(t *testing.T, h *host, o *Options) {
			o.Role, o.Runner = RoleRunner, RunnerOptions{Mode: "outbound"}
			t.Setenv("HEALTH", "starting")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHost(t)
			o := h.options(system.Docker, "off")
			setup(t, h, &o)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if _, _, err := Install(ctx, o); err == nil {
				t.Errorf("%s: installed", name)
			}
		})
	}
}

// TestInstallFromMain checks a version whose tag is not in the docker repository yet: its
// files taken from main, with a warning, into a folder that exists empty.
func TestInstallFromMain(t *testing.T) {
	h := newHost(t)
	files := []tarEntry{{name: "docker-main/docker-compose.yml", body: read(t, "compose-7.3.yml")}, {name: "docker-main/.env", body: read(t, "env-7.3.template")}}
	serveRepo(t, map[string][]byte{"refs/heads/main": repoArchive(t, files...)})
	_ = os.MkdirAll(h.dir, 0o755)
	if _, _, err := Install(context.Background(), h.options(system.Docker, "off")); err != nil {
		t.Fatal(err)
	}
}

// TestSmallHelpers checks the port the app listens on in its container, a value or its
// default, and the containers' user on each case.
func TestSmallHelpers(t *testing.T) {
	if portInside("off") != "8000" || portInside("self") != "443" {
		t.Error("portInside")
	}
	if orDefault("", "d") != "d" || orDefault("v", "d") != "v" {
		t.Error("orDefault")
	}
	h := newHost(t)
	ctx := context.Background()
	geteuid = func() int { return 1000 }
	if err := ownData(ctx, system.Docker, t.TempDir()); err != nil || h.bin.Called("docker") {
		t.Errorf("uid 1000: %v", err)
	}
	if err := pinCompose(filepath.Join(t.TempDir(), "none"), "7.3.0", "af"); err == nil {
		t.Error("no compose file to pin")
	}
	if err := checkContainers(ctx, system.Docker, "af", RoleRunner); err != nil {
		t.Error(err)
	}
	t.Setenv("FAIL", "docker ps")
	if err := checkContainers(ctx, system.Docker, "af", RoleController); err != nil {
		t.Errorf("a runtime that does not answer: %v", err)
	}
}

// TestWaitHealthy checks a container that becomes healthy, one that never does, and a
// context that ends.
func TestWaitHealthy(t *testing.T) {
	newHost(t)
	st := &state.State{Runtime: "docker"}
	if err := waitHealthy(context.Background(), st, "af_rte", time.Second); err != nil {
		t.Error(err)
	}
	t.Setenv("HEALTH", "starting")
	if err := waitHealthy(context.Background(), st, "af_rte", 30*time.Millisecond); err == nil {
		t.Error("never healthy")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitHealthy(ctx, st, "af_rte", time.Second); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled: %v", err)
	}
}
