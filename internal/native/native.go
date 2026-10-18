// Package native installs, upgrades and backs up AnsibleForms directly on a server, without
// containers: the release bundle (the app built for Node.js), Node.js itself, Python, Ansible
// and the collections of the RTE images for the Runner, systemd services, and for a controller
// MariaDB from the distribution's packages.
//
// The layout of an installation folder (/opt/ansibleforms):
//
//	releases/<version>/   the bundle (server/, bin/, rte/), its node link, venv and collections
//	current               the link to the release that runs (an upgrade moves it, a rollback too)
//	runtime/              Node.js, uv and Python, shared by the releases
//	data/                 what the containers mount as /app/dist/persistent
//	home/                 the system user's home: .ssh and .gitconfig, linked into data/
//	app.env, rte.env      the services' settings and secrets (the RTE's without the database's)
package native

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ansibleforms/installer/internal/compose"
	"github.com/ansibleforms/installer/internal/health"
	"github.com/ansibleforms/installer/internal/i18n"
	"github.com/ansibleforms/installer/internal/releases"
	"github.com/ansibleforms/installer/internal/state"
	"github.com/ansibleforms/installer/internal/system"
	"github.com/ansibleforms/installer/internal/ui"
)

// ─── what it needs ────────────────────────────────────────────────────────────

// Supported tells why the native mode cannot be used on this server, if it cannot.
//
// Returns:
//
//	"" when it can ; "root" when the installer does not run as root ; "unsupported" for a
//	distribution or an architecture it does not know.
func Supported() string {
	if !system.IsRoot() {
		return "root"
	}
	if system.ReadOSRelease().Family() == "" || system.Arch() == "" || !system.Has("systemctl") {
		return "unsupported"
	}
	return ""
}

// basePackages are what both roles need: git for the repositories, ssh for the playbooks,
// the MariaDB client for the app's backups, what the downloads are unpacked with, and the
// library Node.js links against (libatomic, missing from minimal servers).
var basePackages = system.Packages{
	Apt:    []string{"ca-certificates", "curl", "git", "openssh-client", "mariadb-client", "tar", "gzip", "libatomic1"},
	Dnf:    []string{"ca-certificates", "git", "openssh-clients", "mariadb", "tar", "gzip", "libatomic"},
	Zypper: []string{"ca-certificates", "curl", "git-core", "openssh-clients", "mariadb-client", "tar", "gzip", "libatomic1"},
}

// serverPackages are a controller's database server.
var serverPackages = system.Packages{
	Apt:    []string{"mariadb-server"},
	Dnf:    []string{"mariadb-server"},
	Zypper: []string{"mariadb"},
}

// ─── the folder ───────────────────────────────────────────────────────────────

// layout names the paths of an installation.
type layout struct {
	dir, name string
}

func (l layout) releases() string          { return filepath.Join(l.dir, "releases") }
func (l layout) release(v string) string   { return filepath.Join(l.dir, "releases", v) }
func (l layout) current() string           { return filepath.Join(l.dir, "current") }
func (l layout) runtime() string           { return filepath.Join(l.dir, "runtime") }
func (l layout) data() string              { return filepath.Join(l.dir, "data") }
func (l layout) home() string              { return filepath.Join(l.dir, "home") }
func (l layout) env(service string) string { return filepath.Join(l.dir, service+".env") }
func (l layout) backups() string           { return filepath.Join(l.dir, "installer-backups") }

// unit is the systemd unit of a service: <name> for the app, <name>-rte for the RTE.
func (l layout) unit(service string) string {
	if service == "app" {
		return l.name
	}
	return l.name + "-" + service
}

// unitPath is where a service's unit file goes.
func (l layout) unitPath(service string) string {
	return filepath.Join("/etc/systemd/system", l.unit(service)+".service")
}

// services are the systemd services of a role: the app and its RTE, or a Runner's RTE alone.
func services(role string) []string {
	if role == compose.RoleRunner {
		return []string{"rte"}
	}
	return []string{"app", "rte"}
}

// ─── install ──────────────────────────────────────────────────────────────────

// Install installs AnsibleForms directly on the server: the packages, the system user, the
// release, the data, the settings, the database (a controller) and the services, then waits
// for it to answer.
//
// Args:
//
//	ctx: the context, to cancel.
//	o: the answers (Runtime is not used).
//
// Returns:
//
//	The installation's state (saved) and what to tell at the end.
//
// Errors:
//
//	When a step fails; what was made is left for a look.
func Install(ctx context.Context, o compose.Options) (*state.State, *compose.Result, error) {
	l := layout{dir: o.Dir, name: o.Name}
	if err := compose.CheckFolder(o.Dir); err != nil {
		return nil, nil, err
	}
	for _, s := range services(o.Role) {
		if _, err := os.Stat(l.unitPath(s)); err == nil {
			return nil, nil, errors.New(i18n.T("native.exists", "name", l.unit(s)))
		}
	}

	ui.Step(i18n.T("native.prereqs"))
	pkgs := basePackages
	if o.Role == compose.RoleController {
		pkgs = merge(basePackages, serverPackages)
	}
	if err := system.InstallPackages(ctx, pkgs); err != nil {
		return nil, nil, err
	}
	installSSHPass(ctx)

	ui.Step(i18n.T("native.user", "user", o.Name))
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return nil, nil, err
	}
	if err := ensureUser(ctx, l); err != nil {
		return nil, nil, err
	}

	if err := prepareRelease(ctx, l, o.Version); err != nil {
		return nil, nil, err
	}
	if err := switchTo(l, o.Version); err != nil {
		return nil, nil, err
	}

	ui.Step(i18n.T("native.data"))
	if err := writeData(ctx, l, o.Version, o.Role, false); err != nil {
		return nil, nil, err
	}

	st := &state.State{
		Mode: state.Native, Role: o.Role, Version: o.Version, Dir: o.Dir, Name: o.Name, Port: o.Port,
		HTTPS:       o.Role == compose.RoleController && o.TLS != "off",
		InstalledAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	var res *compose.Result
	var err error
	if o.Role == compose.RoleRunner {
		res, err = writeRunner(l, o)
	} else {
		res, err = writeController(ctx, l, o)
	}
	if err != nil {
		return nil, nil, err
	}
	if err := own(ctx, l); err != nil {
		return nil, nil, err
	}

	ui.Step(i18n.T("native.services"))
	for _, s := range services(o.Role) {
		if err := writeUnit(l, s, o.Port); err != nil {
			return nil, nil, err
		}
	}
	if err := system.Run(ctx, "", "systemctl", "daemon-reload"); err != nil {
		return nil, nil, err
	}
	for _, s := range services(o.Role) {
		if err := system.Run(ctx, "", "systemctl", "enable", "--now", l.unit(s)); err != nil {
			return nil, nil, err
		}
	}
	if err := st.Save(); err != nil {
		return nil, nil, err
	}

	if o.Role == compose.RoleController {
		ui.Step(i18n.T("compose.waiting"))
		if _, err := health.Wait(ctx, res.URL, 10*time.Minute); err != nil {
			return st, res, err
		}
	} else {
		ui.Step(i18n.T("compose.waitingRunner"))
		if err := waitPort(ctx, l.unit("rte"), o.Port, 3*time.Minute); err != nil {
			return st, res, err
		}
	}
	return st, res, nil
}

// merge joins two package sets.
func merge(a, b system.Packages) system.Packages {
	return system.Packages{
		Apt:    append(append([]string{}, a.Apt...), b.Apt...),
		Dnf:    append(append([]string{}, a.Dnf...), b.Dnf...),
		Zypper: append(append([]string{}, a.Zypper...), b.Zypper...),
	}
}

// installSSHPass installs sshpass, which playbooks need to sign in with an SSH password. It
// is in EPEL on RHEL and its rebuilds, and in PackageHub on SUSE Linux Enterprise: when it
// cannot be installed, the installation goes on and says so.
//
// Args:
//
//	ctx: the context, to cancel.
func installSSHPass(ctx context.Context) {
	if system.Has("sshpass") {
		return
	}
	err := system.InstallPackages(ctx, system.Packages{Apt: []string{"sshpass"}, Dnf: []string{"sshpass"}, Zypper: []string{"sshpass"}})
	if err != nil && system.ReadOSRelease().Family() == "dnf" {
		if system.Run(ctx, "", "dnf", "install", "-y", "epel-release") == nil {
			err = system.Run(ctx, "", "dnf", "install", "-y", "sshpass")
		}
	}
	if err != nil {
		ui.Warn(i18n.T("native.sshpassMissing"))
	}
}

// ensureUser creates the system user the services run as, named after the instance, with
// its home in the folder ; an existing one (a reinstall) is kept.
//
// Args:
//
//	ctx: the context, to cancel.
//	l: the installation.
//
// Errors:
//
//	When useradd fails.
func ensureUser(ctx context.Context, l layout) error {
	if err := os.MkdirAll(l.home(), 0o750); err != nil {
		return err
	}
	if _, err := system.Output(ctx, "", "id", "-u", l.name); err == nil {
		return nil
	}
	shell := "/bin/false"
	for _, s := range []string{"/usr/sbin/nologin", "/sbin/nologin"} {
		if _, err := os.Stat(s); err == nil {
			shell = s
			break
		}
	}
	return system.Run(ctx, "", "useradd", "--system", "--user-group", "--home-dir", l.home(), "--no-create-home", "--shell", shell, l.name)
}

// own gives the data and the home to the system user ; the releases and the runtimes stay
// root's, so the services cannot change their own code.
//
// Args:
//
//	ctx: the context, to cancel.
//	l: the installation.
//
// Errors:
//
//	When chown fails.
func own(ctx context.Context, l layout) error {
	for _, d := range []string{l.data(), l.home()} {
		if err := system.Run(ctx, "", "chown", "-R", l.name+":"+l.name, d); err != nil {
			return err
		}
	}
	for _, s := range []string{"app", "rte"} {
		if _, err := os.Stat(l.env(s)); err == nil {
			if err := system.Run(ctx, "", "chown", "root:"+l.name, l.env(s)); err != nil {
				return err
			}
		}
	}
	return nil
}

// ─── the release ──────────────────────────────────────────────────────────────

// Bundle is a local release bundle to install instead of the release's (--bundle) : an
// air-gapped server, or a test ; empty : downloaded.
var Bundle string

// bundleURL is where a release's bundle is published, with its checksum next to it.
func bundleURL(version string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/ansibleforms-%s-linux-%s.tar.gz", releases.AppRepo, version, version, system.Arch())
}

// prepareRelease puts a release in place, next to the one that runs: the bundle, Node.js,
// and Python with Ansible and the collections of the RTE images. A release that is already
// there (a retried upgrade) is used as it is.
//
// Args:
//
//	ctx: the context, to cancel.
//	l: the installation.
//	version: the AnsibleForms version.
//
// Errors:
//
//	When a download, a checksum or an installation fails.
func prepareRelease(ctx context.Context, l layout, version string) error {
	dir := l.release(version)
	if _, err := os.Stat(filepath.Join(dir, ".complete")); err == nil {
		return nil
	}
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	ui.Step(i18n.T("native.bundle", "version", version))
	if Bundle != "" {
		f, err := os.Open(Bundle)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := untar(f, dir, 1); err != nil {
			return err
		}
	} else if err := fetchVerified(ctx, bundleURL(version), dir, 1); err != nil {
		if errors.Is(err, errNotFound) {
			return errors.New(i18n.T("native.noBundle", "version", version))
		}
		return err
	}
	versions, err := readEnvFile(filepath.Join(dir, "versions.env"))
	if err != nil {
		return err
	}

	node, err := installNode(ctx, l, versions["NODE_MAJOR"])
	if err != nil {
		return err
	}
	if err := os.Symlink(node, filepath.Join(dir, "node")); err != nil {
		return err
	}
	if err := installPython(ctx, l, dir, versions["UV_VERSION"], versions["PYTHON_VERSION"]); err != nil {
		return err
	}
	// the app reads its persistent folder and its custom functions from its own folder, as the
	// containers mount them there
	server := filepath.Join(dir, "server")
	if err := os.Symlink(l.data(), filepath.Join(server, "persistent")); err != nil {
		return err
	}
	for _, f := range []string{"custom.js", "jq.custom.definitions"} {
		p := filepath.Join(server, "src", "functions", f)
		_ = os.Remove(p)
		if err := os.Symlink(filepath.Join(l.data(), "functions", f), p); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, ".complete"), []byte(version+"\n"), 0o644)
}

// switchTo makes a release the current one.
//
// Args:
//
//	l: the installation.
//	version: the release.
//
// Errors:
//
//	When the link cannot be replaced.
func switchTo(l layout, version string) error {
	tmp := l.current() + ".new"
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Join("releases", version), tmp); err != nil {
		return err
	}
	return os.Rename(tmp, l.current())
}

// currentVersion is the release the current link points to.
func currentVersion(l layout) string {
	target, err := os.Readlink(l.current())
	if err != nil {
		return ""
	}
	return filepath.Base(target)
}

// prune deletes the releases other than the current one and the one before it.
//
// Args:
//
//	l: the installation.
//	keep: the releases to keep.
func prune(l layout, keep ...string) {
	entries, err := os.ReadDir(l.releases())
	if err != nil {
		return
	}
	for _, e := range entries {
		kept := false
		for _, k := range keep {
			kept = kept || e.Name() == k
		}
		if !kept {
			_ = os.RemoveAll(filepath.Join(l.releases(), e.Name()))
		}
	}
}

// installNode installs the newest Node.js of a major version into the runtimes, from
// nodejs.org, its checksum checked ; one already there is used as it is.
//
// Args:
//
//	ctx: the context, to cancel.
//	l: the installation.
//	major: the major version (26).
//
// Returns:
//
//	Its folder.
//
// Errors:
//
//	When the download or the checksum fails.
func installNode(ctx context.Context, l layout, major string) (string, error) {
	if major == "" {
		return "", errors.New("the bundle names no Node.js version")
	}
	version, err := latestNode(ctx, major)
	if err != nil {
		return "", err
	}
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[system.Arch()]
	name := fmt.Sprintf("node-%s-linux-%s", version, arch)
	dir := filepath.Join(l.runtime(), name)
	if _, err := os.Stat(filepath.Join(dir, "bin", "node")); err == nil {
		return dir, nil
	}
	ui.Step(i18n.T("native.node", "version", strings.TrimPrefix(version, "v")))
	base := "https://nodejs.org/dist/" + version + "/"
	sums, err := getText(ctx, base+"SHASUMS256.txt")
	if err != nil {
		return "", err
	}
	sum := ""
	for _, line := range strings.Split(sums, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == name+".tar.gz" {
			sum = f[0]
		}
	}
	if sum == "" {
		return "", fmt.Errorf("no checksum for %s.tar.gz", name)
	}
	tmp := dir + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", err
	}
	if err := fetchSum(ctx, base+name+".tar.gz", sum, tmp, 1); err != nil {
		return "", err
	}
	return dir, os.Rename(tmp, dir)
}

// installPython installs uv, then the Python of the RTE images with it, and makes the
// release's virtual environment: Ansible and the libraries of the full RTE image, then its
// collections.
//
// Args:
//
//	ctx: the context, to cancel.
//	l: the installation.
//	release: the release's folder.
//	uvVersion: the uv version (from the bundle).
//	pyVersion: the Python version (from the bundle).
//
// Errors:
//
//	When a download or an installation fails.
func installPython(ctx context.Context, l layout, release, uvVersion, pyVersion string) error {
	if uvVersion == "" || pyVersion == "" {
		return errors.New("the bundle names no Python or uv version")
	}
	uv, err := installUV(ctx, l, uvVersion)
	if err != nil {
		return err
	}
	ui.Step(i18n.T("native.python", "version", pyVersion))
	env := []string{
		"UV_PYTHON_INSTALL_DIR=" + filepath.Join(l.runtime(), "python"),
		"UV_CACHE_DIR=" + filepath.Join(l.runtime(), "uv-cache"),
		"UV_NO_PROGRESS=1",
	}
	venv := filepath.Join(release, "venv")
	steps := [][]string{
		{uv, "python", "install", pyVersion},
		{uv, "venv", "--seed", "--python", pyVersion, venv},
		{uv, "pip", "install", "--python", filepath.Join(venv, "bin", "python"),
			"-r", filepath.Join(release, "rte", "requirements-core.txt"),
			"-r", filepath.Join(release, "rte", "requirements-full.txt")},
	}
	for _, s := range steps {
		if err := runEnv(ctx, env, s[0], s[1:]...); err != nil {
			return err
		}
	}
	ui.Step(i18n.T("native.collections"))
	if err := runEnv(ctx, nil, filepath.Join(venv, "bin", "ansible-galaxy"), "collection", "install",
		"-r", filepath.Join(release, "rte", "collections.yml"), "-p", filepath.Join(release, "collections")); err != nil {
		return err
	}
	_ = os.RemoveAll(filepath.Join(l.runtime(), "uv-cache"))
	return nil
}

// installUV installs uv into the runtimes, from its GitHub release, its checksum checked.
//
// Args:
//
//	ctx: the context, to cancel.
//	l: the installation.
//	version: the uv version.
//
// Returns:
//
//	The uv program.
//
// Errors:
//
//	When the download or the checksum fails.
func installUV(ctx context.Context, l layout, version string) (string, error) {
	dir := filepath.Join(l.runtime(), "uv-"+version)
	uv := filepath.Join(dir, "uv")
	if _, err := os.Stat(uv); err == nil {
		return uv, nil
	}
	arch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[system.Arch()]
	name := "uv-" + arch + "-unknown-linux-gnu.tar.gz"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := fetchVerified(ctx, "https://github.com/astral-sh/uv/releases/download/"+version+"/"+name, dir, 1); err != nil {
		return "", err
	}
	return uv, nil
}

// runEnv runs a command with a few more environment variables, its output shown.
func runEnv(ctx context.Context, env []string, name string, args ...string) error {
	if len(env) == 0 {
		return system.Run(ctx, "", name, args...)
	}
	return system.Run(ctx, "", "env", append(env, append([]string{name}, args...)...)...)
}

// ─── the data and the settings ────────────────────────────────────────────────

// runnerData are the docker repository's folders a Runner keeps (its playbooks come from the
// app): the ansible configuration, roles and collections, and the SSH and git settings.
var runnerData = []string{"data/ansible", "data/ssh", "data/git"}

// writeData writes the docker repository's data folder (without MySQL's, which the containers
// alone use), and links the system user's home to its SSH and git settings.
//
// Args:
//
//	ctx: the context, to cancel.
//	l: the installation.
//	version: the AnsibleForms version, whose files are taken.
//	role: controller (all of data/) or runner (runnerData).
//	keep: files that exist are left as they are (an upgrade).
//
// Errors:
//
//	When the files cannot be fetched or written.
func writeData(ctx context.Context, l layout, version, role string, keep bool) error {
	only := func(rel string) bool {
		if rel == "data" || strings.HasPrefix(rel, "data/mysql") {
			return rel == "data"
		}
		if role == compose.RoleRunner {
			for _, p := range runnerData {
				if rel == p || strings.HasPrefix(rel, p+"/") {
					return true
				}
			}
			return false
		}
		return strings.HasPrefix(rel, "data/")
	}
	ref, added, err := compose.Fetch(ctx, version, l.dir, only, keep)
	if err != nil {
		return err
	}
	if ref == "main" && !keep {
		ui.Warn(i18n.T("compose.fromMain", "version", version))
	}
	if keep && len(added) > 0 {
		ui.Muted(i18n.T("compose.addedFiles", "files", strings.Join(added, ", ")))
	}
	for _, d := range []string{"ssh", "git", "ansible/roles", "ansible/collections", "python/lib"} {
		if err := os.MkdirAll(filepath.Join(l.data(), d), 0o755); err != nil {
			return err
		}
	}
	if err := os.Chmod(filepath.Join(l.data(), "ssh"), 0o700); err != nil {
		return err
	}
	gitconfig := filepath.Join(l.data(), "git", ".gitconfig")
	if _, err := os.Stat(gitconfig); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(gitconfig, nil, 0o644); err != nil {
			return err
		}
	}
	for link, target := range map[string]string{".ssh": filepath.Join(l.data(), "ssh"), ".gitconfig": gitconfig} {
		p := filepath.Join(l.home(), link)
		if _, err := os.Lstat(p); err == nil {
			continue
		}
		if err := os.Symlink(target, p); err != nil {
			return err
		}
	}
	return nil
}

// rteEnv is what both roles' RTE runs with: its role, Node.js, Ansible and the collections of
// the release, and the data's ansible.cfg, roles and collections as the containers mount them.
//
// Args:
//
//	l: the installation.
//	https: it serves HTTPS (an inbound Runner) with the certificate in data/certificates.
//
// Returns:
//
//	Its settings, one KEY=value per line.
func rteEnv(l layout, https bool) []string {
	tls := "HTTPS=0"
	if https {
		tls = "HTTPS=1"
	}
	env := []string{
		"AF_ROLE=rte",
		"NODE_ENV=production",
		tls,
		"LOG_LEVEL=info",
		"LOG_CONSOLE_LEVEL=info",
		"HOME=" + l.home(),
		"PATH=" + strings.Join([]string{filepath.Join(l.current(), "venv", "bin"), filepath.Join(l.current(), "node", "bin"), filepath.Join(l.current(), "bin"), "/usr/local/bin", "/usr/bin", "/bin"}, ":"),
		"ANSIBLE_CONFIG=" + filepath.Join(l.data(), "ansible", "ansible.cfg"),
		"ANSIBLE_ROLES_PATH=" + filepath.Join(l.data(), "ansible", "roles"),
		"ANSIBLE_COLLECTIONS_PATH=" + filepath.Join(l.data(), "ansible", "collections") + ":" + filepath.Join(l.current(), "collections"),
		// the libraries the ansibleforms_pip_install playbook installs into the data folder
		"PYTHONPATH=" + filepath.Join(l.data(), "python", "lib"),
	}
	if https {
		env = append(env,
			"# its socket over TLS, with the self-signed certificate that the app pins",
			"HTTPS_CERT="+filepath.Join(l.data(), "certificates", "cert.pem"),
			"HTTPS_KEY="+filepath.Join(l.data(), "certificates", "key.pem"))
	}
	return env
}

// writeEnv writes a service's settings, readable by root and the system user only.
func writeEnv(l layout, service, header string, lines []string) error {
	content := "# " + header + "\n" + strings.Join(lines, "\n") + "\n"
	return os.WriteFile(l.env(service), []byte(content), 0o640)
}

// writeController writes a controller's settings, makes its database, its certificate and
// its config seed (its local RTE as the default Runner).
//
// Args:
//
//	ctx: the context, to cancel.
//	l: the installation.
//	o: the answers.
//
// Returns:
//
//	Where it will answer and how to sign in.
//
// Errors:
//
//	When the database or a file cannot be made.
func writeController(ctx context.Context, l layout, o compose.Options) (*compose.Result, error) {
	ui.Step(i18n.T("compose.secrets"))
	admin := o.AdminPassword
	if admin == "" {
		admin = compose.Secret(16)
	}
	dbPassword := compose.Secret(24)
	rteToken := compose.Secret(32)
	rtePort := compose.FreePort("127.0.0.1", 8001)
	scheme, https := "https", "1"
	if o.TLS == "off" {
		scheme, https = "http", "0"
	}

	ui.Step(i18n.T("native.database"))
	if err := setupDatabase(ctx, o.Name, dbPassword); err != nil {
		return nil, err
	}

	app := []string{
		"NODE_ENV=production",
		"LOG_LEVEL=info",
		"LOG_CONSOLE_LEVEL=info",
		"HOME=" + l.home(),
		"PATH=" + strings.Join([]string{filepath.Join(l.current(), "node", "bin"), filepath.Join(l.current(), "bin"), "/usr/local/bin", "/usr/bin", "/bin"}, ":"),
		"PORT=" + strconv.Itoa(o.Port),
		"HTTPS=" + https,
		"DB_HOST=127.0.0.1",
		"DB_PORT=3306",
		"DB_USER=" + o.Name,
		"DB_PASSWORD=" + dbPassword,
		"ADMIN_USERNAME=admin",
		"ADMIN_PASSWORD=" + admin,
		"# the key stored credentials are encrypted with : exactly 32 characters (aes-256)",
		"ENCRYPTION_SECRET=" + compose.Secret(32),
		"ACCESS_TOKEN_SECRET=" + compose.Secret(48),
		"# the token of the local RTE, which the config seed hands to its Runner's credential",
		"RTE_TOKEN=" + rteToken,
		"CONFIG_SEED_PATH=" + filepath.Join(l.data(), "seed.yaml"),
	}
	if err := writeEnv(l, "app", "AnsibleForms, written by the AnsibleForms installer", app); err != nil {
		return nil, err
	}
	// the local RTE : the app dials it on this server, it holds no database credentials
	rte := append(rteEnv(l, false), "RTE_MODE=inbound", "RTE_TOKEN="+rteToken, "RTE_FILES=local", "PORT="+strconv.Itoa(rtePort))
	if err := writeEnv(l, "rte", "the AnsibleForms RTE of this server, written by the AnsibleForms installer", rte); err != nil {
		return nil, err
	}
	if err := writeSeed(l, rtePort); err != nil {
		return nil, err
	}

	cert, key := filepath.Join(l.data(), "certificates", "cert.pem"), filepath.Join(l.data(), "certificates", "key.pem")
	switch o.TLS {
	case "self":
		ui.Step(i18n.T("compose.selfSigned"))
		if err := compose.SelfSigned(cert, key); err != nil {
			return nil, err
		}
	case "own":
		if err := os.MkdirAll(filepath.Dir(cert), 0o755); err != nil {
			return nil, err
		}
		if err := compose.CopyFile(o.CertFile, cert, 0o644); err != nil {
			return nil, err
		}
		if err := compose.CopyFile(o.KeyFile, key, 0o600); err != nil {
			return nil, err
		}
	}
	return &compose.Result{URL: fmt.Sprintf("%s://localhost:%d", scheme, o.Port), AdminUser: "admin", AdminPassword: admin}, nil
}

// seedURL is the RTE's address in the docker repository's config seed.
var seedURL = regexp.MustCompile(`(?m)^(\s*url:\s*)http://rte:8000\s*$`)

// writeSeed points the docker repository's config seed at the RTE of this server.
//
// Args:
//
//	l: the installation.
//	port: the RTE's port on this server.
//
// Errors:
//
//	When the seed cannot be read or written.
func writeSeed(l layout, port int) error {
	p := filepath.Join(l.data(), "seed.yaml")
	data, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	s := seedURL.ReplaceAllString(string(data), "${1}http://127.0.0.1:"+strconv.Itoa(port))
	s = strings.ReplaceAll(s, "the rte container of docker-compose.yml", "the RTE service of this server")
	s = strings.ReplaceAll(s, "The rte container of docker-compose.yml", "The RTE service of this server")
	return os.WriteFile(p, []byte(s), 0o644)
}

// writeRunner writes a Runner's settings: the app's address, its mode and a token of its own.
//
// Args:
//
//	l: the installation.
//	o: the answers.
//
// Returns:
//
//	Its token and, inbound, the address the app reaches it on.
//
// Errors:
//
//	When the file cannot be written.
func writeRunner(l layout, o compose.Options) (*compose.Result, error) {
	r := o.Runner
	token := r.Token
	if token == "" {
		token = compose.Secret(32)
	}
	inbound := r.Mode == "inbound"
	lines := append(rteEnv(l, inbound),
		"PORT="+strconv.Itoa(o.Port),
		"# outbound : it dials the app ; inbound : the app dials it at RTE_URL",
		"RTE_MODE="+r.Mode,
		"APP_URL="+r.AppURL,
		"# its Runner's name under Connections > Runners",
		"RTE_NAME="+o.Name,
		"# the token the app and this RTE share (its Runner's RTE credential holds the same)",
		"RTE_TOKEN="+token,
	)
	if r.JoinToken != "" {
		lines = append(lines, "# the join token that approves it at once, presented once", "RTE_JOIN_TOKEN="+r.JoinToken)
	}
	res := &compose.Result{URL: r.Address, Token: token}
	if inbound {
		// its socket over TLS : a certificate of its own address, which the app pins when it
		// asks to join (own gives it to the system user afterwards)
		ui.Step(i18n.T("compose.selfSigned"))
		fp, err := compose.RunnerCertificate(filepath.Join(l.data(), "certificates", "cert.pem"), filepath.Join(l.data(), "certificates", "key.pem"), r.Host)
		if err != nil {
			return nil, err
		}
		res.CertFingerprint = fp
		lines = append(lines, "# where the app reaches it", "RTE_URL="+r.Address)
	}
	if r.TrustAppCert {
		lines = append(lines, "# the app's certificate is self-signed : accepted as it is", "APP_TLS_VERIFY=0")
	}
	if err := writeEnv(l, "rte", "AnsibleForms Runner, written by the AnsibleForms installer", lines); err != nil {
		return nil, err
	}
	return res, nil
}

// ─── the database ─────────────────────────────────────────────────────────────

// mariadb is the MariaDB client, under its new name or its old one.
func mariadb(tool string) string {
	names := map[string][]string{"client": {"mariadb", "mysql"}, "dump": {"mariadb-dump", "mysqldump"}}[tool]
	for _, n := range names {
		if system.Has(n) {
			return n
		}
	}
	return names[0]
}

// sqlString quotes a value for SQL (the passwords are letters and digits, this is a guard).
func sqlString(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// setupDatabase starts MariaDB and makes the AnsibleForms schema and its user: all rights on
// that schema and none elsewhere, signing in from this server only. Root signs in through
// its socket, as the installer runs as root.
//
// Args:
//
//	ctx: the context, to cancel.
//	user: the database user (the instance's name).
//	password: its password.
//
// Errors:
//
//	When MariaDB does not start or the statements fail.
func setupDatabase(ctx context.Context, user, password string) error {
	if err := system.Run(ctx, "", "systemctl", "enable", "--now", "mariadb"); err != nil {
		return err
	}
	var sql strings.Builder
	sql.WriteString("CREATE DATABASE IF NOT EXISTS `AnsibleForms`;\n")
	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		account := sqlString(user) + "@" + sqlString(host)
		fmt.Fprintf(&sql, "CREATE USER IF NOT EXISTS %s IDENTIFIED BY %s;\n", account, sqlString(password))
		fmt.Fprintf(&sql, "ALTER USER %s IDENTIFIED BY %s;\n", account, sqlString(password))
		fmt.Fprintf(&sql, "GRANT ALL PRIVILEGES ON `AnsibleForms`.* TO %s;\n", account)
	}
	sql.WriteString("FLUSH PRIVILEGES;\n")
	return pipe(ctx, sql.String(), mariadb("client"), "-uroot")
}

// dropDatabase removes the schema and its user (an uninstall that deletes the data).
func dropDatabase(ctx context.Context, user string) error {
	var sql strings.Builder
	sql.WriteString("DROP DATABASE IF EXISTS `AnsibleForms`;\n")
	for _, host := range []string{"localhost", "127.0.0.1", "::1"} {
		fmt.Fprintf(&sql, "DROP USER IF EXISTS %s@%s;\n", sqlString(user), sqlString(host))
	}
	return pipe(ctx, sql.String(), mariadb("client"), "-uroot")
}

// pipe runs a command with a text as its input, without showing the text (it holds passwords).
func pipe(ctx context.Context, input, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", name, strings.TrimSpace(string(out)+" "+err.Error()))
	}
	return nil
}

// ─── the services ─────────────────────────────────────────────────────────────

// unitTemplate is a service's systemd unit. The app may bind a port under 1024 (443) without
// root ; the RTE keeps the right to sudo, which playbooks with become need on this server.
// Node.js keeps the links' own paths (--preserve-symlinks): the custom functions are linked
// from the data folder into the app's, and import its modules from there, as the containers'
// mounts let them.
const unitTemplate = `# {{DESCRIPTION}}, written by the AnsibleForms installer
[Unit]
Description={{DESCRIPTION}}
After=network-online.target{{AFTER}}
Wants=network-online.target

[Service]
Type=simple
User={{USER}}
Group={{USER}}
WorkingDirectory={{DIR}}/current/server
EnvironmentFile={{DIR}}/{{SERVICE}}.env
ExecStart={{DIR}}/current/node/bin/node --preserve-symlinks index.js
Restart=on-failure
RestartSec=5
PrivateTmp=true
ProtectSystem=full
{{EXTRA}}
[Install]
WantedBy=multi-user.target
`

// writeUnit writes a service's unit file.
//
// Args:
//
//	l: the installation.
//	service: app or rte.
//	port: the app's port (a port under 1024 needs the capability to bind it).
//
// Errors:
//
//	When the file cannot be written.
func writeUnit(l layout, service string, port int) error {
	desc, after, extra := "AnsibleForms ("+l.name+")", " mariadb.service", "NoNewPrivileges=true\n"
	if port < 1024 {
		extra += "AmbientCapabilities=CAP_NET_BIND_SERVICE\n"
	}
	if service == "rte" {
		desc, after, extra = "AnsibleForms RTE ("+l.name+")", "", ""
	}
	content := strings.NewReplacer("{{DESCRIPTION}}", desc, "{{AFTER}}", after, "{{USER}}", l.name,
		"{{DIR}}", l.dir, "{{SERVICE}}", service, "{{EXTRA}}", extra).Replace(unitTemplate)
	return os.WriteFile(l.unitPath(service), []byte(content), 0o644)
}

// waitPort waits for a service to take connections on its port (an RTE: its API needs the
// token for anything else).
//
// Args:
//
//	ctx: the context, to cancel.
//	unit: the service, named in the error.
//	port: its port.
//	timeout: how long to wait.
//
// Errors:
//
//	When it does not answer in time.
func waitPort(ctx context.Context, unit string, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
		if err == nil {
			c.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return errors.New(i18n.T("native.notReady", "unit", unit))
}

// rtePort reads the RTE's port from its settings.
func rtePort(l layout) int {
	env, _ := readEnvFile(l.env("rte"))
	p, _ := strconv.Atoi(env["PORT"])
	return p
}

// systemctl runs systemctl on the services of an installation.
func systemctl(ctx context.Context, l layout, role, verb string) error {
	args := []string{verb}
	for _, s := range services(role) {
		args = append(args, l.unit(s))
	}
	return system.Run(ctx, "", "systemctl", args...)
}

// ─── backups ──────────────────────────────────────────────────────────────────

// Backup dumps a controller's database and keeps a copy of its settings.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//
// Returns:
//
//	The dump's path.
//
// Errors:
//
//	For a Runner (its controller holds the data), or when the dump fails.
func Backup(ctx context.Context, st *state.State) (string, error) {
	if st.Role == compose.RoleRunner {
		return "", errors.New(i18n.T("compose.runnerNoBackup"))
	}
	l := layout{dir: st.Dir, name: st.Name}
	if err := os.MkdirAll(l.backups(), 0o700); err != nil {
		return "", err
	}
	stamp := time.Now().Format("20060102-150405")
	dump := filepath.Join(l.backups(), fmt.Sprintf("ansibleforms-%s-%s.sql", st.Version, stamp))
	ui.Step(i18n.T("compose.dumping"))
	if err := system.ToFile(ctx, "", dump, mariadb("dump"), "-uroot", "--single-transaction", "--routines", "--triggers",
		"--add-drop-database", "--databases", "AnsibleForms"); err != nil {
		return "", err
	}
	for _, s := range []string{"app", "rte"} {
		if err := compose.CopyFile(l.env(s), filepath.Join(l.backups(), fmt.Sprintf("%s.env.%s.%s", s, st.Version, stamp)), 0o600); err != nil {
			return "", err
		}
	}
	return dump, nil
}

// ─── upgrade ──────────────────────────────────────────────────────────────────

// Undo is what a failed upgrade needs to go back: the version and the dump before it.
type Undo struct {
	Version string
	Dump    string
}

// Upgrade moves an installation to another version: the new release is put in place while
// the old one runs, then a backup (a controller), the services stopped, the current link
// moved, the services started, and the app waited for at that version.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation (saved with the new version once it answers).
//	to: the version to upgrade to.
//
// Returns:
//
//	What Rollback needs, set as soon as the backup is made (also when an error follows).
//
// Errors:
//
//	When a step fails.
func Upgrade(ctx context.Context, st *state.State, to string) (*Undo, error) {
	l := layout{dir: st.Dir, name: st.Name}
	undo := &Undo{Version: st.Version}
	if err := prepareRelease(ctx, l, to); err != nil {
		return nil, err
	}
	ui.Step(i18n.T("native.data"))
	if err := writeData(ctx, l, to, st.Role, true); err != nil {
		return nil, err
	}
	if err := own(ctx, l); err != nil {
		return nil, err
	}
	if st.Role != compose.RoleRunner {
		dump, err := Backup(ctx, st)
		if err != nil {
			return nil, err
		}
		undo.Dump = dump
		ui.Done(i18n.T("compose.backedUp", "file", dump))
	}
	ui.Step(i18n.T("native.services"))
	if err := systemctl(ctx, l, st.Role, "stop"); err != nil {
		return undo, err
	}
	if err := switchTo(l, to); err != nil {
		return undo, err
	}
	if err := systemctl(ctx, l, st.Role, "start"); err != nil {
		return undo, err
	}
	if st.Role == compose.RoleRunner {
		if err := waitPort(ctx, l.unit("rte"), st.Port, 3*time.Minute); err != nil {
			return undo, err
		}
	} else {
		ui.Step(i18n.T("compose.waiting"))
		got, err := health.Wait(ctx, URL(st), 15*time.Minute)
		if err != nil {
			return undo, err
		}
		if got != "" && got != to {
			return undo, errors.New(i18n.T("compose.wrongVersion", "got", got, "want", to))
		}
	}
	prune(l, to, undo.Version)
	st.Version = to
	st.UpdatedAt = time.Now().UTC()
	return undo, st.Save()
}

// Rollback goes back to the version before a failed upgrade: its release, and for a
// controller its database as the backup holds it (a new version may have changed the schema).
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//	undo: what Upgrade kept.
//
// Errors:
//
//	When a step fails; the backup stays where it is for a restore by hand.
func Rollback(ctx context.Context, st *state.State, undo *Undo) error {
	l := layout{dir: st.Dir, name: st.Name}
	ui.Step(i18n.T("compose.rollingBack", "version", undo.Version))
	_ = systemctl(ctx, l, st.Role, "stop")
	if err := switchTo(l, undo.Version); err != nil {
		return err
	}
	if st.Role != compose.RoleRunner && undo.Dump != "" {
		ui.Step(i18n.T("compose.restoring", "file", undo.Dump))
		if err := system.FromFile(ctx, "", undo.Dump, mariadb("client"), "-uroot"); err != nil {
			return err
		}
	}
	if err := systemctl(ctx, l, st.Role, "start"); err != nil {
		return err
	}
	st.Version = undo.Version
	return st.Save()
}

// ─── status and uninstall ─────────────────────────────────────────────────────

// URL is where a controller answers on this server.
//
// Args:
//
//	st: the installation.
//
// Returns:
//
//	Its address.
func URL(st *state.State) string {
	return compose.URL(st)
}

// Status shows the services and, for a controller, the version it answers with.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//
// Errors:
//
//	When systemctl cannot show them.
func Status(ctx context.Context, st *state.State) error {
	l := layout{dir: st.Dir, name: st.Name}
	args := []string{"status", "--no-pager", "--lines=0"}
	for _, s := range services(st.Role) {
		args = append(args, l.unit(s))
	}
	// systemctl status exits 3 for a stopped service : what it shows is the answer
	_ = system.Run(ctx, "", "systemctl", args...)
	if v := currentVersion(l); v != "" {
		ui.Muted(filepath.Join(l.dir, "releases", v))
	}
	if st.Role != compose.RoleRunner {
		if v, err := health.Wait(ctx, URL(st), 5*time.Second); err == nil {
			ui.Done(i18n.T("status.answers", "url", URL(st), "version", v))
		} else {
			ui.Warn(i18n.T("status.noAnswer", "url", URL(st)))
		}
	} else if err := waitPort(ctx, l.unit("rte"), rtePortOr(l, st.Port), 3*time.Second); err != nil {
		ui.Warn(err.Error())
	}
	return nil
}

// rtePortOr is the RTE's port from its settings, or a default.
func rtePortOr(l layout, def int) int {
	if p := rtePort(l); p != 0 {
		return p
	}
	return def
}

// Uninstall stops and removes the services and, with purge, the folder, the system user and
// the database with its user. The packages stay: other software may use them.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//	purge: also delete the data (the database, forms, playbooks...).
//
// Errors:
//
//	When a step fails.
func Uninstall(ctx context.Context, st *state.State, purge bool) error {
	l := layout{dir: st.Dir, name: st.Name}
	for _, s := range services(st.Role) {
		_ = system.Run(ctx, "", "systemctl", "disable", "--now", l.unit(s))
		_ = os.Remove(l.unitPath(s))
	}
	_ = system.Run(ctx, "", "systemctl", "daemon-reload")
	if purge {
		if st.Role != compose.RoleRunner {
			if err := dropDatabase(ctx, st.Name); err != nil {
				return err
			}
		}
		ui.Step(i18n.T("uninstall.deleting", "dir", st.Dir))
		if err := os.RemoveAll(st.Dir); err != nil {
			return err
		}
		_ = system.Run(ctx, "", "userdel", st.Name)
	}
	return state.Remove()
}

// ─── small helpers ────────────────────────────────────────────────────────────

// readEnvFile reads KEY=value lines (comments and blank lines skipped).
func readEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[k] = v
		}
	}
	return out, nil
}

// latestNode is the newest release of a Node.js major version, from nodejs.org's index.
func latestNode(ctx context.Context, major string) (string, error) {
	index, err := getText(ctx, "https://nodejs.org/dist/index.json")
	if err != nil {
		return "", err
	}
	re := regexp.MustCompile(`"version":"(v` + regexp.QuoteMeta(major) + `\.[0-9]+\.[0-9]+)"`)
	var found []releases.Version
	for _, m := range re.FindAllStringSubmatch(index, -1) {
		if v, err := releases.ParseVersion(m[1]); err == nil {
			found = append(found, v)
		}
	}
	if len(found) == 0 {
		return "", fmt.Errorf("no Node.js %s release on nodejs.org", major)
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Compare(found[j]) > 0 })
	return "v" + found[0].String(), nil
}
