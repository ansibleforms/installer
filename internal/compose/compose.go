package compose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ansibleforms/installer/internal/health"
	"github.com/ansibleforms/installer/internal/i18n"
	"github.com/ansibleforms/installer/internal/state"
	"github.com/ansibleforms/installer/internal/system"
	"github.com/ansibleforms/installer/internal/ui"
)

// ─── roles ────────────────────────────────────────────────────────────────────

const (
	// RoleController is the whole of AnsibleForms: the app, its database and a local RTE.
	RoleController = "controller"
	// RoleRunner is an RTE alone, running the playbooks of a controller elsewhere.
	RoleRunner = "runner"
)

// Options are the answers of a compose installation.
type Options struct {
	// Role is controller or runner.
	Role string
	// Runtime is docker or podman.
	Runtime system.Runtime
	// Dir is the installation folder.
	Dir string
	// Name is the instance's name: its containers' prefix and its compose project. A runner's
	// is also its runner's name under Connections > Runners (RTE_NAME).
	Name string
	// Version is the AnsibleForms version.
	Version string
	// Port is the port on the host: the web interface (controller) or the RTE (runner).
	Port int
	// TLS is how the controller serves: self (a self-signed certificate made here), own (the
	// certificate and key given) or off (HTTP, behind a proxy that does TLS).
	TLS      string
	CertFile string
	KeyFile  string
	// AdminPassword is the controller's first admin password.
	AdminPassword string

	// Runner is what a runner needs: the app, its mode and how it registers.
	Runner RunnerOptions
}

// RunnerOptions are a runner's answers.
type RunnerOptions struct {
	// AppURL is the app's address (https://forms.example.com).
	AppURL string
	// Mode is outbound (the RTE dials the app) or inbound (the app dials the RTE).
	Mode string
	// JoinToken is the join token of Connections > Runners ; empty: registered by hand.
	JoinToken string
	// Address is where the app reaches an inbound runner (http://10.0.0.5:8000).
	Address string
	// TrustAppCert accepts the app's self-signed certificate (APP_TLS_VERIFY=0).
	TrustAppCert bool
	// Token is the runner's own RTE_TOKEN, generated (shown for a registration by hand).
	Token string
}

// Result is what the installation tells at the end.
type Result struct {
	URL           string
	AdminUser     string
	AdminPassword string
	// a runner : its token, to add it by hand when it did not join with a join token
	Token string
}

// ─── compose itself ───────────────────────────────────────────────────────────

// composeCmd is the compose command of a runtime, for one project.
//
// Args:
//
//	r: the runtime.
//	project: the compose project (the instance's name).
//	args: compose's own arguments (up -d, ps...).
//
// Returns:
//
//	The program and all its arguments.
func composeCmd(r system.Runtime, project string, args ...string) (string, []string) {
	return string(r), append([]string{"compose", "-p", project}, args...)
}

// pullArgs are compose's arguments to pull the images, images it cannot pull left to up.
//
// Args:
//
//	st: the installation.
//
// Returns:
//
//	The arguments.
func pullArgs(st *state.State) []string {
	if st.Runtime == string(system.Docker) {
		return []string{"pull", "--ignore-pull-failures"}
	}
	return []string{"pull"}
}

// runCompose runs compose in the installation folder, its output shown.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//	args: compose's arguments.
//
// Errors:
//
//	When compose fails.
func runCompose(ctx context.Context, st *state.State, args ...string) error {
	name, all := composeCmd(system.Runtime(st.Runtime), st.Name, args...)
	return system.Run(ctx, st.Dir, name, all...)
}

// ─── install ──────────────────────────────────────────────────────────────────

// Install installs AnsibleForms with compose: the files, the secrets, the certificate, the
// containers, then waits for it to answer.
//
// Args:
//
//	ctx: the context, to cancel.
//	o: the answers.
//
// Returns:
//
//	The installation's state (saved) and what to tell at the end.
//
// Errors:
//
//	When a step fails; the folder is left for a look.
func Install(ctx context.Context, o Options) (*state.State, *Result, error) {
	if err := CheckFolder(o.Dir); err != nil {
		return nil, nil, err
	}
	if err := checkContainers(ctx, o.Runtime, o.Name, o.Role); err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return nil, nil, err
	}
	st := &state.State{
		Mode: state.Compose, Role: o.Role, Version: o.Version, Runtime: string(o.Runtime),
		Dir: o.Dir, Name: o.Name, Port: o.Port, HTTPS: o.TLS != "off",
		InstalledAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	var res *Result
	var err error
	if o.Role == RoleRunner {
		res, err = writeRunner(ctx, o)
	} else {
		res, err = writeController(ctx, o)
	}
	if err != nil {
		return nil, nil, err
	}
	if err := ownData(ctx, o.Runtime, filepath.Join(o.Dir, "data")); err != nil {
		return nil, nil, err
	}

	ui.Step(i18n.T("compose.pulling"))
	// an image that is only on this server (loaded from a file, a mirror) is not an error : up
	// pulls what is still missing and says so if it cannot
	if err := runCompose(ctx, st, pullArgs(st)...); err != nil {
		return nil, nil, err
	}
	ui.Step(i18n.T("compose.starting"))
	if err := runCompose(ctx, st, "up", "-d"); err != nil {
		return nil, nil, err
	}
	if err := st.Save(); err != nil {
		return nil, nil, err
	}
	if o.Runtime == system.Podman {
		if err := writeSystemd(ctx, st); err != nil {
			ui.Warn(i18n.T("compose.systemdFailed", "error", err))
		}
	}
	if o.Role == RoleController {
		ui.Step(i18n.T("compose.waiting"))
		if _, err := health.Wait(ctx, res.URL, 10*time.Minute); err != nil {
			return st, res, err
		}
	} else {
		ui.Step(i18n.T("compose.waitingRunner"))
		if err := waitHealthy(ctx, st, st.Name+"_rte", 5*time.Minute); err != nil {
			return st, res, err
		}
	}
	return st, res, nil
}

// CheckFolder refuses a folder that already holds something (another installation, files of
// the person's own).
//
// Args:
//
//	dir: the installation folder.
//
// Errors:
//
//	When it exists and is not empty.
func CheckFolder(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		return errors.New(i18n.T("compose.folderNotEmpty", "dir", dir))
	}
	return nil
}

// checkContainers refuses an instance name whose containers exist already (an older
// installation, another instance).
//
// Args:
//
//	ctx: the context, to cancel.
//	r: the runtime.
//	name: the instance's name.
//	role: what is installed (a runner has only its RTE).
//
// Errors:
//
//	When one of its containers exists.
func checkContainers(ctx context.Context, r system.Runtime, name, role string) error {
	wanted := []string{name + "_db", name + "_app", name + "_rte"}
	if role == RoleRunner {
		wanted = []string{name + "_rte"}
	}
	out, err := system.Output(ctx, "", string(r), "ps", "-a", "--format", "{{.Names}}")
	if err != nil {
		return nil // the runtime answers later, or says why itself
	}
	have := map[string]bool{}
	for _, n := range strings.Fields(out) {
		have[n] = true
	}
	for _, w := range wanted {
		if have[w] {
			return errors.New(i18n.T("compose.containerExists", "container", w))
		}
	}
	return nil
}

// writeController writes a controller's files: the docker repository's, the compose file
// pinned to the version, the .env with secrets of its own, and the certificate.
//
// Args:
//
//	ctx: the context, to cancel.
//	o: the answers.
//
// Returns:
//
//	Where it will answer and how to sign in.
//
// Errors:
//
//	When a file cannot be fetched or written.
func writeController(ctx context.Context, o Options) (*Result, error) {
	ui.Step(i18n.T("compose.fetching"))
	ref, _, err := Fetch(ctx, o.Version, o.Dir, nil, false)
	if err != nil {
		return nil, err
	}
	if ref == "main" {
		ui.Warn(i18n.T("compose.fromMain", "version", o.Version))
	}
	if err := pinCompose(o.Dir, o.Version, o.Name); err != nil {
		return nil, err
	}

	ui.Step(i18n.T("compose.secrets"))
	envPath := filepath.Join(o.Dir, ".env")
	env, err := os.ReadFile(envPath)
	if err != nil {
		return nil, err
	}
	scheme, https := "https", "1"
	if o.TLS == "off" {
		scheme, https = "http", "0"
	}
	admin := o.AdminPassword
	if admin == "" {
		admin = Secret(16)
	}
	values := map[string]string{
		"MYSQLDB_ROOT_PASSWORD": Secret(24),
		"MYSQLDB_PASSWORD":      Secret(24),
		// MySQL on this server only : the runners talk to the app, never to its database
		"MYSQLDB_LOCAL_PORT": fmt.Sprintf("127.0.0.1:%d", FreePort("127.0.0.1", 3306)),
		"WEBAPP_LOCAL_PORT":  strconv.Itoa(o.Port),
		"WEBAPP_DOCKER_PORT": portInside(o.TLS),
		"HTTPS":              https,
		"ADMIN_PASSWORD":     admin,
		"RTE_TOKEN":          Secret(32),
		// the key stored credentials are encrypted with : exactly 32 characters (aes-256)
		"ENCRYPTION_SECRET":   Secret(32),
		"ACCESS_TOKEN_SECRET": Secret(48),
	}
	if err := os.WriteFile(envPath, []byte(SetEnv(string(env), values)), 0o600); err != nil {
		return nil, err
	}

	switch o.TLS {
	case "self":
		ui.Step(i18n.T("compose.selfSigned"))
		if err := SelfSigned(filepath.Join(o.Dir, "data/certificates/cert.pem"), filepath.Join(o.Dir, "data/certificates/key.pem")); err != nil {
			return nil, err
		}
	case "own":
		if err := CopyFile(o.CertFile, filepath.Join(o.Dir, "data/certificates/cert.pem"), 0o644); err != nil {
			return nil, err
		}
		if err := CopyFile(o.KeyFile, filepath.Join(o.Dir, "data/certificates/key.pem"), 0o600); err != nil {
			return nil, err
		}
	}
	return &Result{
		URL:           fmt.Sprintf("%s://localhost:%d", scheme, o.Port),
		AdminUser:     orDefault(GetEnv(string(env), "ADMIN_USERNAME"), "admin"),
		AdminPassword: admin,
	}, nil
}

// portInside is the port the app listens on in its container: 443 with TLS, 8000 without,
// as the image's defaults.
//
// Args:
//
//	tls: self, own or off.
//
// Returns:
//
//	The port, as text for the .env.
func portInside(tls string) string {
	if tls == "off" {
		return "8000"
	}
	return "443"
}

// pinCompose rewrites the compose file for the version and the instance (PrepareCompose).
//
// Args:
//
//	dir: the installation folder.
//	version: the AnsibleForms version.
//	name: the instance's name.
//
// Errors:
//
//	When the file cannot be read or written.
func pinCompose(dir, version, name string) error {
	p := filepath.Join(dir, "docker-compose.yml")
	data, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	return os.WriteFile(p, []byte(PrepareCompose(string(data), version, name)), 0o644)
}

// runnerCompose is a runner's compose file: the RTE alone, talking to the app over one
// WebSocket (RTE_MODE in .env), with no database credentials.
const runnerCompose = `# AnsibleForms RTE runner, written by the AnsibleForms installer.
# It runs the playbooks of the app at APP_URL (see .env) over one WebSocket : it dials the app
# (RTE_MODE=outbound) or the app dials it (RTE_MODE=inbound). It holds no database credentials.
# The job's files come from the app with each job ; ./data keeps what is not acked yet
# (rte-spool), the files received (rte-files), and its ansible.cfg, roles and collections.
services:
  rte:
    image: {{IMAGE}}
    container_name: {{NAME}}_rte
    restart: unless-stopped
    ports:
      - ${RTE_LOCAL_PORT}:8000
    env_file:
      - ./.env
    environment:
      - PORT=8000
      - HTTPS=0
      - PYTHONPATH=/app/dist/persistent/python/lib
    healthcheck:
      test: ["CMD", "node", "./healthcheck.js"]
      interval: 10s
      timeout: 5s
      retries: 30
      start_period: 30s
    volumes:
      - ./data:/app/dist/persistent
      - ./data/ssh:/home/node/.ssh
      - ./data/git/.gitconfig:/home/node/.gitconfig
      - ./data/ansible/ansible.cfg:/etc/ansible/ansible.cfg
      - ./data/ansible/roles:/etc/ansible/roles
      - ./data/ansible/collections:/etc/ansible/collections
`

// runnerData are the docker repository's folders a runner keeps: the ansible configuration,
// roles and collections, and the SSH and git settings (its playbooks come from the app).
var runnerData = []string{"data", "data/ansible", "data/ssh", "data/git"}

// writeRunner writes a runner's files: the docker repository's ansible folders, its compose
// file, and its .env with the app's address, its mode and a token of its own.
//
// Args:
//
//	ctx: the context, to cancel.
//	o: the answers.
//
// Returns:
//
//	Its token and, inbound, the address the app reaches it on.
//
// Errors:
//
//	When a file cannot be fetched or written.
func writeRunner(ctx context.Context, o Options) (*Result, error) {
	ui.Step(i18n.T("compose.fetching"))
	only := func(rel string) bool {
		for _, p := range runnerData {
			if rel == p || strings.HasPrefix(rel, p+"/") {
				return true
			}
		}
		return false
	}
	if _, _, err := Fetch(ctx, o.Version, o.Dir, only, false); err != nil {
		return nil, err
	}
	content := strings.NewReplacer("{{IMAGE}}", RteImage(o.Version), "{{NAME}}", o.Name).Replace(runnerCompose)
	if err := os.WriteFile(filepath.Join(o.Dir, "docker-compose.yml"), []byte(content), 0o644); err != nil {
		return nil, err
	}
	r := o.Runner
	token := r.Token
	if token == "" {
		token = Secret(32)
	}
	// outbound : nothing reaches it from outside, its probes on this server only
	bind := fmt.Sprintf("127.0.0.1:%d", o.Port)
	if r.Mode == "inbound" {
		bind = strconv.Itoa(o.Port)
	}
	lines := []string{
		"# AnsibleForms RTE runner, written by the AnsibleForms installer",
		"AF_ROLE=rte",
		"NODE_ENV=production",
		"# outbound : it dials the app ; inbound : the app dials it at RTE_URL",
		"RTE_MODE=" + r.Mode,
		"# the app's address",
		"APP_URL=" + r.AppURL,
		"# its runner's name under Connections > Runners",
		"RTE_NAME=" + o.Name,
		"# the token the app and this RTE share (its runner's RTE credential holds the same)",
		"RTE_TOKEN=" + token,
		"RTE_LOCAL_PORT=" + bind,
	}
	if r.JoinToken != "" {
		lines = append(lines, "# the join token it registers with, once (Connections > Runners > Join tokens)", "RTE_JOIN_TOKEN="+r.JoinToken)
	}
	if r.Mode == "inbound" {
		lines = append(lines, "# where the app reaches it", "RTE_URL="+r.Address)
	}
	if r.TrustAppCert {
		lines = append(lines, "# the app's certificate is self-signed : accepted as it is", "APP_TLS_VERIFY=0")
	}
	if err := os.WriteFile(filepath.Join(o.Dir, ".env"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return nil, err
	}
	return &Result{URL: r.Address, Token: token}, nil
}

// orDefault gives a value, or a default for an empty one.
//
// Args:
//
//	v: the value.
//	def: the default.
//
// Returns:
//
//	v, or def when v is empty.
func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// ownData gives the data folder to the containers' user (uid 1000), as the docker
// repository's README asks: directly as root, through podman unshare rootless, through a
// short container for a docker group member.
//
// Args:
//
//	ctx: the context, to cancel.
//	r: the runtime.
//	dir: the data folder.
//
// Errors:
//
//	When it cannot be changed.
func ownData(ctx context.Context, r system.Runtime, dir string) error {
	switch {
	case os.Geteuid() == 0:
		return system.Run(ctx, "", "chown", "-R", "1000:1000", dir)
	case r == system.Podman:
		return system.Run(ctx, "", "podman", "unshare", "chown", "-R", "1000:1000", dir)
	case os.Geteuid() == 1000:
		return nil
	}
	return system.Run(ctx, "", "docker", "run", "--rm", "-v", dir+":/d", "busybox", "chown", "-R", "1000:1000", "/d")
}

// waitHealthy waits for a container's health check to pass (a runner: nothing answers on
// the host without its token).
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//	container: the container.
//	timeout: how long to wait.
//
// Errors:
//
//	When it is not healthy in time.
func waitHealthy(ctx context.Context, st *state.State, container string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, _ := system.Output(ctx, "", st.Runtime, "inspect", "--format", "{{.State.Health.Status}}", container)
		if out == "healthy" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return errors.New(i18n.T("compose.notHealthy", "container", container))
}
