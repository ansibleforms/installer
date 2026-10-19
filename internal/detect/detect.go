// Package detect finds an AnsibleForms already on this server that the installer does not
// manage yet (no installer.json): installed by hand from the docker repository's compose
// files, with Podman, by Helm, or as systemd services. Each one found says whether the
// installer can take it over (a state an upgrade works from) or why not.
//
// It is best effort and quick: every command has a short timeout, an engine that is not
// there is skipped, and the engines are looked at side by side. Only the app's and the
// RTE's images count: a MySQL container named ansibleforms-mysql is not AnsibleForms.
package detect

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ansibleforms/installer/internal/state"
	"github.com/ansibleforms/installer/internal/system"
)

// ─── what is found ────────────────────────────────────────────────────────────

// The engines an installation is found on.
const (
	Docker     = "docker"
	Podman     = "podman"
	Kubernetes = "kubernetes"
	Systemd    = "systemd"
	// None: it answers on a port of this server, and no engine runs it (from source, by hand)
	None = "none"
)

// The roles, as the state names them.
const (
	RoleController = "controller"
	RoleRunner     = "runner"
)

// Why an installation cannot be taken over; "" when it can. The installer explains each one
// in the person's language (detect.why_<code>).
const (
	// its containers were not started by compose: no project, no folder
	NotCompose = "notCompose"
	// its compose folder has no docker-compose.yml or .env the installer can read
	NoComposeFile = "noComposeFile"
	// its compose file is not the docker repository's (services, containers' names)
	NotRepoLayout = "notRepoLayout"
	// a controller without the mysqldb service: its database is elsewhere
	ExternalDB = "externalDb"
	// a Runner on compose that the installer did not set up
	RunnerLayout = "runnerLayout"
	// systemd services that are not the installer's layout (releases/, current, app.env)
	NativeLayout = "nativeLayout"
	// on Kubernetes, not a release of the ansibleforms chart (or helm is missing here)
	NoHelm = "noHelm"
	// on Kubernetes, managed by an Argo CD Application : upgraded there, never from here
	ArgoCD = "argoCD"
	// its version could not be read
	UnknownVersion = "unknownVersion"
	// it answers on a port, and no engine manages it
	Unmanaged = "unmanaged"
)

// Install is one AnsibleForms found on this server.
type Install struct {
	// Engine is docker, podman, kubernetes, systemd or none.
	Engine string
	// Role is controller (the app, its database and an RTE) or runner (an RTE alone).
	Role string
	// Version is the AnsibleForms version (7.2.0), "" when it could not be read.
	Version string
	// Running: a container, a service or a release of it is running (deployed).
	Running bool

	// Compose: the project's folder (com.docker.compose.project.working_dir), the project,
	// the instance name (its containers' prefix: af for af_app) and the containers.
	Dir        string
	Project    string
	Name       string
	Containers []string

	// Native: the systemd units (ansibleforms.service, ansibleforms-rte.service). Dir and
	// Name are the folder and the instance name as above.
	Units []string

	// Compose, native and none: the port on this server (the web interface of a controller,
	// a Runner's RTE) and whether it serves HTTPS.
	Port  int
	HTTPS bool

	// Kubernetes: the namespace, the Helm release (or the workload's name), the chart and the
	// Deployments and StatefulSets that run the images.
	Namespace string
	Release   string
	Chart     string
	// ArgoApp is the Argo CD Application that manages it (its Problem ArgoCD), when known
	ArgoApp   string
	Workloads []string

	// Database: a Runner of 7.2 in the deprecated database mode (an RTE alone in its compose
	// project, with the app's DB_* and no RTE_MODE), what its upgrade to a socket mode needs;
	// nil for anything else.
	Database *DatabaseRunner

	// Problem is why the installer cannot take it over, one of the codes above; "" : it can.
	Problem string

	// key makes the same installation found twice (docker as podman's alias) one
	key string
}

// Adoptable tells whether the installer can take it over and upgrade it.
//
// Returns:
//
//	True when nothing stands in the way.
func (i Install) Adoptable() bool {
	return i.Problem == ""
}

// Where says where it is: its folder, its namespace and release, or its port.
//
// Returns:
//
//	A short text, for a line of the list.
func (i Install) Where() string {
	switch i.Engine {
	case Kubernetes:
		return i.Namespace + " / " + i.Release
	case Systemd:
		return i.Dir + " (" + strings.Join(i.Units, ", ") + ")"
	case None:
		return "localhost:" + itoa(i.Port)
	}
	if i.Dir == "" {
		return strings.Join(i.Containers, ", ")
	}
	if i.Project != "" && i.Project != i.Name {
		return i.Dir + " (" + i.Project + ")"
	}
	return i.Dir
}

// State is what the installer remembers of it once taken over: enough for an upgrade, a
// backup, the status and an uninstall. A Kubernetes one still needs its values file
// (kube.Adopt writes it).
//
// Returns:
//
//	The state, not saved; nil when it cannot be taken over.
func (i Install) State() *state.State {
	if !i.Adoptable() {
		return nil
	}
	now := time.Now().UTC()
	st := &state.State{Role: i.Role, Version: i.Version, InstalledAt: now, UpdatedAt: now}
	switch i.Engine {
	case Kubernetes:
		st.Mode = state.Kubernetes
		st.Namespace, st.Release = i.Namespace, i.Release
	case Systemd:
		st.Mode = state.Native
		st.Dir, st.Name, st.Port, st.HTTPS = i.Dir, i.Name, i.Port, i.HTTPS
	default:
		st.Mode = state.Compose
		st.Runtime = i.Engine
		st.Dir, st.Name, st.Port, st.HTTPS = i.Dir, i.Name, i.Port, i.HTTPS
		if i.Project != i.Name {
			st.Project = i.Project
		}
		// its next upgrade moves it to a socket mode, even when it is interrupted after this
		st.DatabaseMode = i.Database != nil
	}
	return st
}

// Manages tells whether a state is this installation (the one the installer manages already).
//
// Args:
//
//	st: the installer's state.
//
// Returns:
//
//	True when both name the same folder, release or port.
func (i Install) Manages(st *state.State) bool {
	if st == nil {
		return false
	}
	switch i.Engine {
	case Kubernetes:
		return st.Mode == state.Kubernetes && st.Namespace == i.Namespace && st.Release == i.Release
	case None:
		return st.Port != 0 && st.Port == i.Port
	}
	return i.Dir != "" && filepath.Clean(st.Dir) == filepath.Clean(i.Dir)
}

// ─── how it looks ─────────────────────────────────────────────────────────────

// Env is what the detection runs commands and probes with; tests give fakes.
type Env struct {
	// Run runs a command and returns its standard output (system.Output by default).
	Run func(ctx context.Context, name string, args ...string) (string, error)
	// Has tells whether a program is on the PATH.
	Has func(name string) bool
	// Probe asks what answers on a port of this server.
	Probe func(ctx context.Context, port int) Answer
	// Timeout bounds each command and each probe.
	Timeout time.Duration
	// Ports are the ports probed when no engine runs AnsibleForms.
	Ports []int
}

// DefaultPorts are the ports looked at when no engine runs AnsibleForms: the installer's
// (443 for the web interface, 8000 for a Runner, 8001 for a native controller's RTE), the
// app's development ports (3000, 3001), its plain port (8000) and the usual others.
var DefaultPorts = []int{443, 3000, 3001, 8000, 8001, 8010, 8443}

// defaultEnv is the real server: its commands, with a timeout each, and its ports.
//
// Returns:
//
//	The environment.
func defaultEnv() Env {
	return Env{
		Run: func(ctx context.Context, name string, args ...string) (string, error) {
			return system.Output(ctx, "", name, args...)
		},
		Has:     system.Has,
		Probe:   ProbePort,
		Timeout: 5 * time.Second,
		Ports:   DefaultPorts,
	}
}

// run runs a command with the environment's timeout.
//
// Args:
//
//	ctx: the context, to cancel.
//	name: the program.
//	args: its arguments.
//
// Returns:
//
//	Its output.
//
// Errors:
//
//	When it fails or takes too long.
func (e Env) run(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, e.Timeout)
	defer cancel()
	return e.Run(ctx, name, args...)
}

// probe asks a port with the environment's timeout.
//
// Args:
//
//	ctx: the context, to cancel.
//	port: the port.
//
// Returns:
//
//	What answers there.
func (e Env) probe(ctx context.Context, port int) Answer {
	if port <= 0 || e.Probe == nil {
		return Answer{}
	}
	ctx, cancel := context.WithTimeout(ctx, e.Timeout)
	defer cancel()
	return e.Probe(ctx, port)
}

// ─── finding them ─────────────────────────────────────────────────────────────

// Find looks for AnsibleForms on this server: containers of Docker and Podman, Helm releases
// and workloads on the cluster kubectl reaches, systemd services, and, when none of them
// runs it, the usual ports.
//
// Args:
//
//	ctx: the context, to cancel.
//
// Returns:
//
//	What was found, in a stable order; none when nothing was.
func Find(ctx context.Context) []Install {
	return find(ctx, defaultEnv())
}

// find is Find with its environment given.
//
// Args:
//
//	ctx: the context, to cancel.
//	env: the commands and the probe.
//
// Returns:
//
//	What was found.
func find(ctx context.Context, env Env) []Install {
	lookers := []func(context.Context, Env) []Install{
		func(ctx context.Context, env Env) []Install { return containers(ctx, env, Docker) },
		func(ctx context.Context, env Env) []Install { return containers(ctx, env, Podman) },
		kubernetes,
		services,
	}
	results := make([][]Install, len(lookers))
	var wg sync.WaitGroup
	for n, look := range lookers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[n] = look(ctx, env)
		}()
	}
	wg.Wait()
	var out []Install
	seen := map[string]bool{}
	for _, list := range results {
		for _, i := range list {
			if i.key != "" && seen[i.key] {
				continue
			}
			seen[i.key] = true
			out = append(out, i)
		}
	}
	if len(out) == 0 {
		out = ports(ctx, env)
	}
	order := map[string]int{Docker: 0, Podman: 1, Kubernetes: 2, Systemd: 3, None: 4}
	sort.SliceStable(out, func(a, b int) bool {
		if order[out[a].Engine] != order[out[b].Engine] {
			return order[out[a].Engine] < order[out[b].Engine]
		}
		return out[a].Where() < out[b].Where()
	})
	return out
}

// ports probes the usual ports for an AnsibleForms that no engine runs.
//
// Args:
//
//	ctx: the context, to cancel.
//	env: the probe.
//
// Returns:
//
//	One per port that answers as the app or an RTE.
func ports(ctx context.Context, env Env) []Install {
	answers := make([]Answer, len(env.Ports))
	var wg sync.WaitGroup
	for n, p := range env.Ports {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answers[n] = env.probe(ctx, p)
		}()
	}
	wg.Wait()
	var out []Install
	for n, a := range answers {
		if !a.App && !a.RTE {
			continue
		}
		role := RoleController
		if !a.App {
			role = RoleRunner
		}
		out = append(out, Install{Engine: None, Role: role, Version: a.Version, Running: true,
			Port: env.Ports[n], HTTPS: a.HTTPS, Problem: Unmanaged, key: "port:" + itoa(env.Ports[n])})
	}
	return out
}

// ─── images and versions ──────────────────────────────────────────────────────

// semverRe is a full version (7.2.0, v7.3.0-rc.1), not a floating tag (7, latest)
var semverRe = regexp.MustCompile(`^v?(\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?)$`)

// fullVersion gives a full version without its v, or "" for anything else.
//
// Args:
//
//	s: a tag, a label or a file's version.
//
// Returns:
//
//	The version, or "".
func fullVersion(s string) string {
	m := semverRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return ""
	}
	return m[1]
}

// imageKind tells whether an image is the app's or an RTE's: the last part of its repository
// is ansibleforms (ghcr.io/ansibleforms/ansibleforms, ansibleguy/ansibleforms of before) or
// starts with ansibleforms-rte. Another image whose name only contains the word (a MySQL
// image of its own, ansibleforms-mysql) is neither.
//
// Args:
//
//	image: the image (ghcr.io/ansibleforms/ansibleforms:7.2.0, with a digest or not).
//
// Returns:
//
//	"app", "rte" or "".
func imageKind(image string) string {
	repo, _ := splitImage(image)
	last := strings.ToLower(repo[strings.LastIndex(repo, "/")+1:])
	switch {
	case last == "ansibleforms":
		return "app"
	case strings.HasPrefix(last, "ansibleforms-rte"):
		return "rte"
	}
	return ""
}

// splitImage cuts an image into its repository and its tag.
//
// Args:
//
//	image: the image.
//
// Returns:
//
//	The repository and the tag ("" when it has none).
func splitImage(image string) (string, string) {
	image, _, _ = strings.Cut(image, "@")
	slash := strings.LastIndex(image, "/")
	if colon := strings.LastIndex(image, ":"); colon > slash {
		return image[:colon], image[colon+1:]
	}
	return image, ""
}

// ─── small helpers ────────────────────────────────────────────────────────────

// readEnv reads KEY=value lines of a .env or an EnvironmentFile (comments skipped, quotes
// around a value taken off).
//
// Args:
//
//	path: the file.
//
// Returns:
//
//	Its variables; nil when it cannot be read.
func readEnv(path string) map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return out
}

// hostPort reads a port of a .env (443, 127.0.0.1:8000, 0.0.0.0:443).
//
// Args:
//
//	v: the value.
//
// Returns:
//
//	The port on the host, 0 when there is none.
func hostPort(v string) int {
	v = strings.TrimSpace(v)
	if i := strings.LastIndex(v, ":"); i >= 0 {
		v = v[i+1:]
	}
	return atoi(v)
}

// atoi reads a port, 0 for anything else.
func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 || n > 65535 {
		return 0
	}
	return n
}

// itoa writes a number.
func itoa(n int) string { return strconv.Itoa(n) }
