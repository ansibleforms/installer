package detect

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ansibleforms/installer/internal/state"
)

// ─── a fake server ────────────────────────────────────────────────────────────

// fake is a server whose commands answer from fixtures: each command line (the program and
// its arguments, joined by spaces) is matched by its start.
type fake struct {
	t *testing.T
	// programs on the PATH
	has map[string]bool
	// command line prefix -> output; an error when the output is "error"
	out map[string]string
	// command line prefixes that hang until their context is cancelled (a timeout)
	hang []string
	// ports that answer, and how
	answers map[int]Answer

	mu  sync.Mutex
	ran []string
}

// env is the detection's environment on the fake server.
func (f *fake) env() Env {
	return Env{
		Has: func(name string) bool { return f.has[name] },
		Run: func(ctx context.Context, name string, args ...string) (string, error) {
			line := strings.Join(append([]string{name}, args...), " ")
			f.mu.Lock()
			f.ran = append(f.ran, line)
			f.mu.Unlock()
			if !f.has[name] {
				f.t.Errorf("%s ran, but %s is not on the PATH", line, name)
			}
			for _, h := range f.hang {
				if strings.HasPrefix(line, h) {
					<-ctx.Done()
					return "", ctx.Err()
				}
			}
			for prefix, out := range f.out {
				if strings.HasPrefix(line, prefix) {
					if out == "error" {
						return "", errors.New(line + ": failed")
					}
					return out, nil
				}
			}
			return "", errors.New(line + ": no fixture")
		},
		Probe: func(ctx context.Context, port int) Answer {
			return f.answers[port]
		},
		Timeout: 2 * time.Second,
		Ports:   DefaultPorts,
	}
}

// fixture reads a file of testdata, its {{DIR}} and {{HAND}} replaced.
func fixture(t *testing.T, name string, repl ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.NewReplacer(repl...).Replace(string(data))
}

// write writes files under a folder.
func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// keep leaves only some containers in an inspect fixture.
func keep(t *testing.T, inspect string, ids ...string) string {
	t.Helper()
	var all []map[string]any
	if err := json.Unmarshal([]byte(inspect), &all); err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, c := range all {
		for _, id := range ids {
			if c["Id"] == id {
				out = append(out, c)
			}
		}
	}
	data, _ := json.Marshal(out)
	return string(data)
}

// the docker repository's compose file, as far as the detection reads it
const repoCompose = `services:
  mysqldb:
    image: mysql:8.4.11
    container_name: af_db
  app:
    image: ghcr.io/ansibleforms/ansibleforms:7
    container_name: af_app
  rte:
    image: ghcr.io/ansibleforms/ansibleforms-rte-full:7
    container_name: af_rte
`

// composeController is a controller installed by hand from the docker repository (7.2.0, its
// images at the floating tag 7), and a MySQL container of its own named ansibleforms-mysql.
func composeController(t *testing.T) (*fake, string) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"docker-compose.yml": repoCompose,
		".env":               "MYSQLDB_LOCAL_PORT=3306\nWEBAPP_LOCAL_PORT=443\nWEBAPP_DOCKER_PORT=443\nHTTPS=1\n",
	})
	f := &fake{t: t, has: map[string]bool{"docker": true}, out: map[string]string{
		"docker ps -a -q --no-trunc": "aaa111\nbbb222\nccc333\nddd444",
		"docker inspect":             fixture(t, "docker-inspect-controller.json", "{{DIR}}", dir),
	}}
	return f, dir
}

// podmanRunner is a Runner the installer set up on Podman, stopped.
func podmanRunner(t *testing.T, f *fake) string {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"docker-compose.yml": "# AnsibleForms RTE runner, written by the AnsibleForms installer.\nservices:\n  rte:\n    container_name: runner_rte\n",
		".env":               "AF_ROLE=rte\nRTE_MODE=outbound\nAPP_URL=https://forms.example.com\nRTE_NAME=runner\nRTE_LOCAL_PORT=127.0.0.1:8000\n",
	})
	f.has["podman"] = true
	f.out["podman ps -a -q --no-trunc"] = "eee555"
	f.out["podman inspect"] = fixture(t, "podman-inspect-runner.json", "{{DIR}}", dir)
	return dir
}

// helmRelease is a release of the chart in forms, and a StatefulSet in lab made by hand.
func helmRelease(t *testing.T, f *fake) {
	f.has["helm"], f.has["kubectl"] = true, true
	f.out["helm list"] = fixture(t, "helm-list.json")
	f.out["kubectl get deployments,statefulsets"] = fixture(t, "kubectl-workloads.json")
}

// nativeServices is a controller the installer set up as systemd services (7.3.0), and one
// installed by hand from a clone (7.2.0), a podman unit and sshd beside them.
func nativeServices(t *testing.T, f *fake) (string, string) {
	dir, hand := t.TempDir(), t.TempDir()
	write(t, dir, map[string]string{
		"releases/7.3.0/server/package.json": `{"name":"ansibleforms-server","version":"7.3.0"}`,
		"app.env":                            "PORT=8443\nHTTPS=1\nDB_HOST=127.0.0.1\n",
		"rte.env":                            "AF_ROLE=rte\nRTE_MODE=inbound\nPORT=8001\n",
	})
	if err := os.Symlink(filepath.Join("releases", "7.3.0"), filepath.Join(dir, "current")); err != nil {
		t.Fatal(err)
	}
	write(t, hand, map[string]string{"server/package.json": `{"name":"ansibleforms-server","version":"7.2.0"}`})
	f.has["systemctl"] = true
	f.out["systemctl list-unit-files"] = fixture(t, "systemctl-list.txt")
	f.out["systemctl show"] = fixture(t, "systemctl-show.txt", "{{DIR}}", dir, "{{HAND}}", hand)
	return dir, hand
}

// ─── the cases ────────────────────────────────────────────────────────────────

// nothing on the server: the engines are asked, nothing answers on the ports
func TestFindNone(t *testing.T) {
	f := &fake{t: t, has: map[string]bool{"docker": true, "systemctl": true}, out: map[string]string{
		"docker ps -a -q --no-trunc": "",
		"systemctl list-unit-files":  "sshd.service enabled enabled\n",
		"systemctl show":             "Id=sshd.service\nDescription=OpenSSH\nExecStart={ path=/usr/sbin/sshd ; argv[]=/usr/sbin/sshd -D }\n",
	}}
	if got := find(context.Background(), f.env()); len(got) != 0 {
		t.Fatalf("found %+v, want none", got)
	}
}

// a controller from the docker repository's compose files: taken over with its folder's
// project (docker) and its containers' prefix (af); the MySQL container alone is not it
func TestFindComposeController(t *testing.T) {
	f, dir := composeController(t)
	got := find(context.Background(), f.env())
	if len(got) != 1 {
		t.Fatalf("found %d installations, want 1: %+v", len(got), got)
	}
	i := got[0]
	if i.Engine != Docker || i.Role != RoleController || i.Version != "7.2.0" || !i.Running {
		t.Errorf("found %+v", i)
	}
	if i.Dir != dir || i.Project != "docker" || i.Name != "af" || i.Port != 443 || !i.HTTPS {
		t.Errorf("place: %+v", i)
	}
	if !i.Adoptable() {
		t.Fatalf("not adoptable: %s", i.Problem)
	}
	for _, c := range i.Containers {
		if c == "ansibleforms-mysql" {
			t.Errorf("the MySQL container alone was taken for AnsibleForms")
		}
	}
	st := i.State()
	if st.Mode != state.Compose || st.Runtime != "docker" || st.Dir != dir || st.Name != "af" || st.Project != "docker" || st.Version != "7.2.0" || st.Port != 443 || !st.HTTPS {
		t.Errorf("state: %+v", st)
	}
	if st.InstalledAt.IsZero() {
		t.Errorf("state without its date")
	}
}

// a bare MySQL container named ansibleforms-mysql is not AnsibleForms
func TestFindIgnoresMySQL(t *testing.T) {
	f := &fake{t: t, has: map[string]bool{"docker": true}, out: map[string]string{
		"docker ps -a -q --no-trunc": "ddd444",
		"docker inspect": `[{"Id":"ddd444","Name":"/ansibleforms-mysql","Config":{"Image":"mysql:8.4.11","Labels":{}},"State":{"Status":"running"}},
			{"Id":"fff666","Name":"/af_db","Config":{"Image":"mysql:8.4.11","Labels":{"com.docker.compose.project":"local","com.docker.compose.service":"mysqldb"}},"State":{"Status":"running"}}]`,
	}}
	if got := find(context.Background(), f.env()); len(got) != 0 {
		t.Fatalf("found %+v, want none", got)
	}
}

// no version in the tag nor the image's label: the app's version endpoint on its port tells
func TestFindComposeVersionFromProbe(t *testing.T) {
	f, _ := composeController(t)
	f.out["docker inspect"] = strings.Replace(f.out["docker inspect"], `"org.opencontainers.image.version": "7.2.0"`, `"org.opencontainers.image.revision": "abc"`, 1)
	f.answers = map[int]Answer{443: {App: true, Version: "7.2.1", HTTPS: true}}
	got := find(context.Background(), f.env())
	if len(got) != 1 || got[0].Version != "7.2.1" || !got[0].Adoptable() {
		t.Fatalf("found %+v", got)
	}
	// and nothing answers: found, but not taken over without a version
	f.answers = nil
	got = find(context.Background(), f.env())
	if len(got) != 1 || got[0].Problem != UnknownVersion {
		t.Fatalf("found %+v, want %s", got, UnknownVersion)
	}
}

// a compose project without the mysqldb service keeps its database elsewhere; a compose file
// of its own (other names) is not the docker repository's
func TestFindComposeRefused(t *testing.T) {
	f, _ := composeController(t)
	f.out["docker inspect"] = keep(t, f.out["docker inspect"], "aaa111", "ccc333")
	if got := find(context.Background(), f.env()); len(got) != 1 || got[0].Problem != ExternalDB || got[0].State() != nil {
		t.Fatalf("found %+v, want %s", got, ExternalDB)
	}
	f, _ = composeController(t)
	f.out["docker inspect"] = strings.ReplaceAll(f.out["docker inspect"], `"/af_app"`, `"/forms-web"`)
	if got := find(context.Background(), f.env()); len(got) != 1 || got[0].Problem != NotRepoLayout {
		t.Fatalf("found %+v, want %s", got, NotRepoLayout)
	}
	f, dir := composeController(t)
	if err := os.Remove(filepath.Join(dir, "docker-compose.yml")); err != nil {
		t.Fatal(err)
	}
	if got := find(context.Background(), f.env()); len(got) != 1 || got[0].Problem != MissingComposeFile || got[0].ProblemFile != filepath.Join(dir, "docker-compose.yml") {
		t.Fatalf("found %+v, want %s", got, MissingComposeFile)
	}
	f, dir = composeController(t)
	if err := os.Remove(filepath.Join(dir, ".env")); err != nil {
		t.Fatal(err)
	}
	if got := find(context.Background(), f.env()); len(got) != 1 || got[0].Problem != MissingComposeFile || got[0].ProblemFile != filepath.Join(dir, ".env") {
		t.Fatalf("found %+v, want %s for .env", got, MissingComposeFile)
	}
	// started with -f from a file of another name: not the docker repository's
	f, dir = composeController(t)
	f.out["docker inspect"] = strings.ReplaceAll(f.out["docker inspect"], dir+"/docker-compose.yml", dir+"/forms.yml")
	if got := find(context.Background(), f.env()); len(got) != 1 || got[0].Problem != NotRepoLayout {
		t.Fatalf("found %+v, want %s for forms.yml", got, NotRepoLayout)
	}
	// a container run without compose
	f, _ = composeController(t)
	f.out["docker ps -a -q --no-trunc"] = "aaa111"
	f.out["docker inspect"] = `[{"Id":"aaa111","Name":"/forms","Config":{"Image":"ansibleguy/ansibleforms:5.0.10","Labels":{}},"State":{"Status":"running"}}]`
	if got := find(context.Background(), f.env()); len(got) != 1 || got[0].Problem != NotCompose || got[0].Version != "5.0.10" {
		t.Fatalf("found %+v, want %s", got, NotCompose)
	}
}

// a controller started from docker-compose.yaml (or compose.yaml): read from that file and
// taken over, as compose reads it
func TestFindComposeYaml(t *testing.T) {
	for _, name := range []string{"docker-compose.yaml", "compose.yaml", "compose.yml"} {
		f, dir := composeController(t)
		if err := os.Rename(filepath.Join(dir, "docker-compose.yml"), filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
		f.out["docker inspect"] = strings.ReplaceAll(f.out["docker inspect"], dir+"/docker-compose.yml", dir+"/"+name)
		if got := find(context.Background(), f.env()); len(got) != 1 || !got[0].Adoptable() {
			t.Fatalf("%s: found %+v, want it adoptable", name, got)
		}
	}
}

// the compose file of a project: its label's when it is one of the folder's own, else the
// first compose reads by itself
func TestProjectComposeFile(t *testing.T) {
	dir := t.TempDir()
	if got := ProjectComposeFile(dir, dir+"/docker-compose.yaml"); got != filepath.Join(dir, "docker-compose.yaml") {
		t.Errorf("label: %s", got)
	}
	if got := ProjectComposeFile(dir, "/elsewhere/docker-compose.yml"); got != "" {
		t.Errorf("another folder: %s", got)
	}
	if got := ProjectComposeFile(dir, ""); got != filepath.Join(dir, "docker-compose.yml") {
		t.Errorf("none: %s", got)
	}
	write(t, dir, map[string]string{"compose.yml": "services: {}\n", "docker-compose.yml": "services: {}\n"})
	if got := ProjectComposeFile(dir, ""); got != filepath.Join(dir, "compose.yml") {
		t.Errorf("no label: %s, want compose.yml (read first)", got)
	}
}

// a Runner alone (the RTE's image, no app): on Podman, stopped, taken over
func TestFindRunnerOnly(t *testing.T) {
	f := &fake{t: t, has: map[string]bool{}, out: map[string]string{}}
	dir := podmanRunner(t, f)
	got := find(context.Background(), f.env())
	if len(got) != 1 {
		t.Fatalf("found %+v, want 1", got)
	}
	i := got[0]
	if i.Engine != Podman || i.Role != RoleRunner || i.Version != "7.3.0" || i.Running || i.Name != "runner" || i.Port != 8000 || !i.Adoptable() {
		t.Fatalf("found %+v", i)
	}
	st := i.State()
	if st.Mode != state.Compose || st.Runtime != "podman" || st.Role != RoleRunner || st.Dir != dir || st.Project != "" {
		t.Errorf("state: %+v", st)
	}
	// a Runner whose .env lacks what the installer's Runner file needs
	write(t, dir, map[string]string{".env": "APP_URL=https://forms.example.com\n"})
	if got := find(context.Background(), f.env()); len(got) != 1 || got[0].Problem != RunnerLayout {
		t.Fatalf("found %+v, want %s", got, RunnerLayout)
	}
}

// a release of the chart is taken over; a StatefulSet by hand is found, not taken over; the
// chart's MySQL and another chart are not AnsibleForms
func TestFindHelmRelease(t *testing.T) {
	f := &fake{t: t, has: map[string]bool{}, out: map[string]string{}}
	helmRelease(t, f)
	got := find(context.Background(), f.env())
	if len(got) != 2 {
		t.Fatalf("found %+v, want 2", got)
	}
	rel, lab := got[1], got[0]
	if rel.Namespace != "forms" {
		rel, lab = lab, rel
	}
	if rel.Engine != Kubernetes || rel.Release != "ansibleforms" || rel.Version != "7.2.0" || !rel.Running || !rel.Adoptable() {
		t.Errorf("release: %+v", rel)
	}
	if len(rel.Workloads) != 1 || rel.Workloads[0] != "deploy/ansibleforms-server" {
		t.Errorf("release's workloads: %v", rel.Workloads)
	}
	st := rel.State()
	if st.Mode != state.Kubernetes || st.Namespace != "forms" || st.Release != "ansibleforms" || st.Role != RoleController {
		t.Errorf("state: %+v", st)
	}
	if lab.Problem != NoHelm || lab.Version != "7.1.4" || lab.Workloads[0] != "sts/forms-by-hand" || lab.Running {
		t.Errorf("by hand: %+v", lab)
	}
}

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

// several at once, in a stable order: the containers, the cluster, the services
func TestFindSeveral(t *testing.T) {
	f, _ := composeController(t)
	podmanRunner(t, f)
	helmRelease(t, f)
	nativeServices(t, f)
	got := find(context.Background(), f.env())
	var engines []string
	for _, i := range got {
		engines = append(engines, i.Engine)
	}
	want := "docker podman kubernetes kubernetes systemd systemd"
	if strings.Join(engines, " ") != want {
		t.Fatalf("engines %v, want %s", engines, want)
	}
}

// no engine on the server: nothing is run, and the ports are asked
func TestFindEngineMissing(t *testing.T) {
	f := &fake{t: t, has: map[string]bool{}, out: map[string]string{}}
	if got := find(context.Background(), f.env()); len(got) != 0 {
		t.Fatalf("found %+v, want none", got)
	}
	if len(f.ran) != 0 {
		t.Errorf("ran %v without the programs", f.ran)
	}
	// a daemon that does not answer is skipped the same way
	f = &fake{t: t, has: map[string]bool{"docker": true, "helm": true}, out: map[string]string{
		"docker ps": "error",
		"helm list": "error",
	}}
	if got := find(context.Background(), f.env()); len(got) != 0 {
		t.Fatalf("found %+v, want none", got)
	}
}

// an engine that hangs is given up after the timeout, and the others are still found
func TestFindTimeout(t *testing.T) {
	f := &fake{t: t, has: map[string]bool{"docker": true, "kubectl": true}, out: map[string]string{}, hang: []string{"docker ps", "kubectl get"}}
	podmanRunner(t, f)
	env := f.env()
	env.Timeout = 100 * time.Millisecond
	start := time.Now()
	got := find(context.Background(), env)
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("took %s", took)
	}
	if len(got) != 1 || got[0].Engine != Podman {
		t.Fatalf("found %+v, want the Podman Runner", got)
	}
}

// no engine runs it, yet it answers on a port (run from source): found, never taken over
func TestFindUnmanagedPorts(t *testing.T) {
	f := &fake{t: t, has: map[string]bool{}, out: map[string]string{}, answers: map[int]Answer{
		3001: {App: true, Version: "7.2.0"},
		8010: {RTE: true},
	}}
	got := find(context.Background(), f.env())
	if len(got) != 2 {
		t.Fatalf("found %+v, want 2", got)
	}
	if got[0].Port != 3001 || got[0].Role != RoleController || got[0].Version != "7.2.0" || got[0].Problem != Unmanaged || got[0].State() != nil {
		t.Errorf("app: %+v", got[0])
	}
	if got[1].Port != 8010 || got[1].Role != RoleRunner || got[1].Problem != Unmanaged {
		t.Errorf("rte: %+v", got[1])
	}
	// an engine's installation found: the ports are not asked
	c, _ := composeController(t)
	c.answers = f.answers
	if got := find(context.Background(), c.env()); len(got) != 1 || got[0].Engine != Docker {
		t.Fatalf("found %+v, want the compose one only", got)
	}
}

// the same containers seen by docker and podman (podman-docker) are one installation
func TestFindSameContainersOnce(t *testing.T) {
	f, _ := composeController(t)
	f.has["podman"] = true
	f.out["podman ps -a -q --no-trunc"] = f.out["docker ps -a -q --no-trunc"]
	f.out["podman inspect"] = f.out["docker inspect"]
	if got := find(context.Background(), f.env()); len(got) != 1 {
		t.Fatalf("found %d, want 1: %+v", len(got), got)
	}
}

// the installation the installer manages is told apart from the others
func TestManages(t *testing.T) {
	c := Install{Engine: Docker, Dir: "/opt/ansibleforms/"}
	if !c.Manages(&state.State{Mode: state.Compose, Dir: "/opt/ansibleforms"}) || c.Manages(&state.State{Dir: "/opt/other"}) || c.Manages(nil) {
		t.Errorf("compose")
	}
	k := Install{Engine: Kubernetes, Namespace: "forms", Release: "af"}
	if !k.Manages(&state.State{Mode: state.Kubernetes, Namespace: "forms", Release: "af"}) || k.Manages(&state.State{Mode: state.Kubernetes, Namespace: "x", Release: "af"}) {
		t.Errorf("kubernetes")
	}
}

// only the app's and the RTE's images count
func TestImageKind(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/ansibleforms/ansibleforms:7.2.0":          "app",
		"ghcr.io/ansibleforms/ansibleforms@sha256:0123":    "app",
		"registry.local:5000/mirror/ansibleforms:7":        "app",
		"ansibleguy/ansibleforms:5.0.10":                   "app",
		"ghcr.io/ansibleforms/ansibleforms-rte-full:7.3.0": "rte",
		"ghcr.io/ansibleforms/ansibleforms-rte-core":       "rte",
		"mysql:8.4.11":                                          "",
		"myorg/ansibleforms-mysql:8":                            "",
		"registry.local:5000/ansibleforms-backup:latest":        "",
		"docker.io/library/mariadb:11":                          "",
		"ghcr.io/ansibleforms/ansibleforms-docs:7.3.0":          "",
		"localhost/ansibleforms-rte-custom:dev":                 "rte",
		"ghcr.io/ansibleforms/ansibleforms:7.3.0@sha256:abcdef": "app",
	}
	for image, want := range cases {
		if got := imageKind(image); got != want {
			t.Errorf("imageKind(%s) = %q, want %q", image, got, want)
		}
	}
	if fullVersion("7") != "" || fullVersion("latest") != "" || fullVersion("v7.3.0") != "7.3.0" || fullVersion("7.3.0-rc.1") != "7.3.0-rc.1" {
		t.Errorf("fullVersion")
	}
}

// ─── the probe ────────────────────────────────────────────────────────────────

// serverPort is the port of a test server on 127.0.0.1.
func serverPort(t *testing.T, s *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := strconv.Atoi(u.Port())
	return p
}

// the app answers its version endpoint; an RTE its liveness probe and asks its token for
// the rest; another service's /live is not an RTE; nothing listening is nothing
func TestProbePort(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("%s %s: only GET may be sent", r.Method, r.URL.Path)
		}
		if r.URL.Path == "/api/v2/version" {
			_, _ = w.Write([]byte(`{"version":"7.2.0","server":{"gitSha":"dev"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer app.Close()
	rte := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live":
			_, _ = w.Write([]byte(`{"status":"live"}`))
		case "/rte/v1/health":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer rte.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/live" {
			_, _ = w.Write([]byte(`{"status":"live"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer other.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if a := ProbePort(ctx, serverPort(t, app)); !a.App || a.Version != "7.2.0" || a.HTTPS {
		t.Errorf("app: %+v", a)
	}
	if a := ProbePort(ctx, serverPort(t, rte)); !a.RTE || a.App || !a.HTTPS {
		t.Errorf("rte: %+v", a)
	}
	if a := ProbePort(ctx, serverPort(t, other)); a.App || a.RTE {
		t.Errorf("another service: %+v", a)
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	port := serverPort(t, closed)
	closed.Close()
	if a := ProbePort(ctx, port); a.App || a.RTE {
		t.Errorf("nothing listening: %+v", a)
	}
}

// workloads Argo CD manages are found, named after their Application, and never taken over:
// by its tracking annotation, and by the chart's labels without a Helm release (label tracking)
func TestFindArgoCD(t *testing.T) {
	f := &fake{t: t, has: map[string]bool{"kubectl": true}, out: map[string]string{
		"kubectl get deployments,statefulsets": `{"items":[
 {"kind":"Deployment","metadata":{"name":"forms-server","namespace":"forms","labels":{"app.kubernetes.io/instance":"forms"},
  "annotations":{"argocd.argoproj.io/tracking-id":"ansibleforms-prod:apps/Deployment:forms/forms-server"}},
  "spec":{"template":{"spec":{"containers":[{"image":"ghcr.io/ansibleforms/ansibleforms:7.3.0"}]}}},"status":{"readyReplicas":1}},
 {"kind":"Deployment","metadata":{"name":"lab-server","namespace":"lab","labels":{"app.kubernetes.io/instance":"lab-forms","app.kubernetes.io/managed-by":"Helm"}},
  "spec":{"template":{"spec":{"containers":[{"image":"ghcr.io/ansibleforms/ansibleforms:7.2.0"}]}}},"status":{"readyReplicas":1}}
]}`,
	}}
	got := find(context.Background(), f.env())
	if len(got) != 2 {
		t.Fatalf("found %+v, want 2", got)
	}
	apps := map[string]string{}
	for _, i := range got {
		if i.Problem != ArgoCD || i.Adoptable() {
			t.Errorf("%s: problem %q, want argoCD and not taken over", i.Namespace, i.Problem)
		}
		apps[i.Namespace] = i.ArgoApp
	}
	if apps["forms"] != "ansibleforms-prod" || apps["lab"] != "lab-forms" {
		t.Errorf("applications %v", apps)
	}
}
