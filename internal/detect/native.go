package detect

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ─── systemd ──────────────────────────────────────────────────────────────────

// unit is what systemctl show says of a service.
type unit struct {
	ID, Description, User, WorkingDirectory, EnvironmentFiles, ExecStart, ActiveState string
}

// the properties asked of each service
const unitProperties = "Id,Description,User,WorkingDirectory,EnvironmentFiles,ExecStart,ActiveState"

// services finds AnsibleForms run as systemd services: the units whose description, folder,
// settings or command name AnsibleForms and that run Node.js themselves (a unit that brings a
// compose project up is the containers', found with them).
//
// Args:
//
//	ctx: the context, to cancel.
//	env: the commands.
//
// Returns:
//
//	One installation per folder.
func services(ctx context.Context, env Env) []Install {
	if !env.Has("systemctl") {
		return nil
	}
	out, err := env.run(ctx, "systemctl", "list-unit-files", "--type=service", "--no-legend", "--no-pager")
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		// a template (getty@.service) cannot be shown without an instance
		if len(f) > 0 && strings.HasSuffix(f[0], ".service") && !strings.Contains(f[0], "@") {
			names = append(names, f[0])
		}
	}
	if len(names) == 0 {
		return nil
	}
	out, err = env.run(ctx, "systemctl", append([]string{"show", "--no-pager", "--property=" + unitProperties}, names...)...)
	if err != nil {
		return nil
	}
	// the units of one folder together : the app's and its RTE's
	byDir := map[string][]unit{}
	var dirs []string
	for _, u := range parseShow(out) {
		if !isAnsibleForms(u) {
			continue
		}
		d := baseDir(u)
		if _, ok := byDir[d]; !ok {
			dirs = append(dirs, d)
		}
		byDir[d] = append(byDir[d], u)
	}
	var found []Install
	for _, d := range dirs {
		found = append(found, nativeInstall(ctx, env, d, byDir[d]))
	}
	return found
}

// parseShow reads systemctl show's blocks, one per unit, separated by a blank line.
//
// Args:
//
//	out: what systemctl show printed.
//
// Returns:
//
//	The units.
func parseShow(out string) []unit {
	var list []unit
	var u unit
	flush := func() {
		if u.ID != "" {
			list = append(list, u)
		}
		u = unit{}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		k, v, _ := strings.Cut(line, "=")
		switch k {
		case "Id":
			u.ID = v
		case "Description":
			u.Description = v
		case "User":
			u.User = v
		case "WorkingDirectory":
			// "-/path" in the unit (a folder that may be missing) is shown "!/path"
			u.WorkingDirectory = strings.TrimLeft(v, "-!")
		case "EnvironmentFiles":
			u.EnvironmentFiles = v
		case "ExecStart":
			u.ExecStart = v
		case "ActiveState":
			u.ActiveState = v
		}
	}
	flush()
	return list
}

// isAnsibleForms tells whether a unit runs AnsibleForms itself.
//
// Args:
//
//	u: the unit.
//
// Returns:
//
//	True when it names AnsibleForms and runs Node.js, not a container engine.
func isAnsibleForms(u unit) bool {
	all := strings.ToLower(u.ID + " " + u.Description + " " + u.WorkingDirectory + " " + u.EnvironmentFiles + " " + u.ExecStart)
	if !strings.Contains(all, "ansibleforms") && !strings.Contains(all, "ansible forms") {
		return false
	}
	cmd := strings.ToLower(u.ExecStart)
	if strings.Contains(cmd, "docker") || strings.Contains(cmd, "podman") || strings.Contains(cmd, " compose ") {
		return false
	}
	return strings.Contains(cmd, "node") || strings.Contains(cmd, "npm")
}

// envFile is the first EnvironmentFile of a unit (systemctl shows "path (ignore_errors=no)").
func envFile(u unit) string {
	f := strings.Fields(u.EnvironmentFiles)
	if len(f) == 0 {
		return ""
	}
	return strings.TrimPrefix(f[0], "-")
}

// baseDir is a unit's installation folder: its WorkingDirectory without current/server (the
// installer's layout) or server, else its EnvironmentFile's folder.
//
// Args:
//
//	u: the unit.
//
// Returns:
//
//	The folder.
func baseDir(u unit) string {
	wd := filepath.Clean(u.WorkingDirectory)
	switch {
	case u.WorkingDirectory == "":
		if e := envFile(u); e != "" {
			return filepath.Dir(e)
		}
		return "/"
	case strings.HasSuffix(wd, "/current/server"):
		return strings.TrimSuffix(wd, "/current/server")
	case strings.HasSuffix(wd, "/server"):
		return strings.TrimSuffix(wd, "/server")
	}
	return wd
}

// isRTE tells whether a unit is an RTE's: its settings are rte.env, or its name ends in -rte.
func isRTE(u unit) bool {
	return filepath.Base(envFile(u)) == "rte.env" || strings.HasSuffix(strings.TrimSuffix(u.ID, ".service"), "-rte")
}

// nativeInstall makes an installation of a folder's units.
//
// Args:
//
//	ctx: the context, to cancel.
//	env: the probe.
//	dir: the folder.
//	units: its units.
//
// Returns:
//
//	The installation.
func nativeInstall(ctx context.Context, env Env, dir string, units []unit) Install {
	i := Install{Engine: Systemd, Role: RoleRunner, Dir: dir, key: "systemd:" + dir}
	var app, rte *unit
	for n := range units {
		u := &units[n]
		i.Units = append(i.Units, u.ID)
		if u.ActiveState == "active" {
			i.Running = true
		}
		if isRTE(*u) {
			rte = u
		} else {
			app = u
		}
	}
	sort.Strings(i.Units)
	main := rte
	if app != nil {
		i.Role, main = RoleController, app
		i.Name = strings.TrimSuffix(app.ID, ".service")
	} else {
		i.Name = strings.TrimSuffix(strings.TrimSuffix(rte.ID, ".service"), "-rte")
	}
	settings := readEnv(envFile(*main))
	i.Port, i.HTTPS = atoi(settings["PORT"]), settings["HTTPS"] == "1"

	// the version : the release current points to, else the app's package.json, else what
	// it answers
	if target, err := os.Readlink(filepath.Join(dir, "current")); err == nil {
		i.Version = fullVersion(filepath.Base(target))
	}
	var candidates []string
	if main.WorkingDirectory != "" {
		candidates = append(candidates, filepath.Join(main.WorkingDirectory, "package.json"))
	}
	candidates = append(candidates, filepath.Join(dir, "server", "package.json"), filepath.Join(dir, "package.json"))
	for _, p := range candidates {
		if i.Version != "" {
			break
		}
		var pkg struct {
			Version string `json:"version"`
		}
		if data, err := os.ReadFile(p); err == nil && json.Unmarshal(data, &pkg) == nil {
			i.Version = fullVersion(pkg.Version)
		}
	}
	if i.Version == "" && i.Role == RoleController && i.Running {
		if a := env.probe(ctx, i.Port); a.App {
			i.Version = fullVersion(a.Version)
		}
	}
	i.Problem = nativeProblem(i, app, rte)
	return i
}

// nativeProblem tells why native services cannot be taken over, if they cannot: the upgrade
// puts a release in releases/, moves the current link and restarts <name> and <name>-rte,
// run by the system user <name> with app.env and rte.env, as the installer writes them.
//
// Args:
//
//	i: the installation found.
//	app: the app's unit (nil for a Runner).
//	rte: the RTE's unit (nil when there is none).
//
// Returns:
//
//	The problem's code, "" when there is none.
func nativeProblem(i Install, app, rte *unit) string {
	layout := func(u *unit, service string) bool {
		want := i.Name
		if service == "rte" {
			want += "-rte"
		}
		return u != nil && u.ID == want+".service" && u.User == i.Name &&
			filepath.Clean(u.WorkingDirectory) == filepath.Join(i.Dir, "current", "server") &&
			envFile(*u) == filepath.Join(i.Dir, service+".env")
	}
	if !layout(rte, "rte") || (i.Role == RoleController && !layout(app, "app")) {
		return NativeLayout
	}
	target, err := os.Readlink(filepath.Join(i.Dir, "current"))
	if err != nil || filepath.Dir(filepath.Clean(target)) != "releases" {
		return NativeLayout
	}
	if i.Version == "" {
		return UnknownVersion
	}
	return ""
}
