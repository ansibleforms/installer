package compose

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
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

// ─── backups ──────────────────────────────────────────────────────────────────

// backupDir is where the installer keeps its own backups, next to the data (not in it: the
// data folder belongs to the containers' user).
//
// Args:
//
//	st: the installation.
//
// Returns:
//
//	The folder.
func backupDir(st *state.State) string {
	return filepath.Join(st.Dir, "installer-backups")
}

// the database's dump inside its container : every object of the AnsibleForms schema, the
// schema dropped and made again on a restore, read with the root password MySQL itself holds
const dumpScript = `MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysqldump -uroot --single-transaction --routines --triggers --add-drop-database --databases AnsibleForms`
const restoreScript = `MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot`

// Backup dumps a controller's database and keeps a copy of its compose file and .env.
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
//	For a runner (its controller holds the data), or when the dump fails.
func Backup(ctx context.Context, st *state.State) (string, error) {
	if st.Role == RoleRunner {
		return "", errors.New(i18n.T("compose.runnerNoBackup"))
	}
	dir := backupDir(st)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	stamp := time.Now().Format("20060102-150405")
	dump := filepath.Join(dir, fmt.Sprintf("ansibleforms-%s-%s.sql", st.Version, stamp))
	name, args := composeCmd(system.Runtime(st.Runtime), st.Name, "exec", "-T", "mysqldb", "sh", "-c", dumpScript)
	ui.Step(i18n.T("compose.dumping"))
	if err := system.ToFile(ctx, st.Dir, dump, name, args...); err != nil {
		return "", err
	}
	for _, f := range []string{"docker-compose.yml", ".env"} {
		if err := CopyFile(filepath.Join(st.Dir, f), filepath.Join(dir, fmt.Sprintf("%s.%s.%s", f, st.Version, stamp)), 0o600); err != nil {
			return "", err
		}
	}
	return dump, nil
}

// restore loads a dump into a controller's database.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//	dump: the dump's path.
//
// Errors:
//
//	When the restore fails.
func restore(ctx context.Context, st *state.State, dump string) error {
	name, args := composeCmd(system.Runtime(st.Runtime), st.Name, "exec", "-T", "mysqldb", "sh", "-c", restoreScript)
	return system.FromFile(ctx, st.Dir, dump, name, args...)
}

// ─── upgrade ──────────────────────────────────────────────────────────────────

// Undo is what a failed upgrade needs to go back: the version, compose file and dump before.
type Undo struct {
	Version string
	Compose string
	Dump    string
}

// Upgrade moves an installation to another version: a backup first (a controller), the new
// compose file, the new images, then waits for it to answer at that version.
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
	undo := &Undo{Version: st.Version}
	composeFile := filepath.Join(st.Dir, "docker-compose.yml")
	old, err := os.ReadFile(composeFile)
	if err != nil {
		return nil, err
	}
	undo.Compose = string(old)

	if st.Role == RoleRunner {
		content := strings.NewReplacer("{{IMAGE}}", RteImage(to), "{{NAME}}", st.Name).Replace(runnerCompose)
		if err := os.WriteFile(composeFile, []byte(content), 0o644); err != nil {
			return undo, err
		}
	} else {
		dump, err := Backup(ctx, st)
		if err != nil {
			return nil, err
		}
		undo.Dump = dump
		ui.Done(i18n.T("compose.backedUp", "file", dump))
		ui.Step(i18n.T("compose.fetching"))
		// the new compose file, and the data files the new version brings (a new playbook);
		// the files the installation has are kept as they are
		if _, _, err := Fetch(ctx, to, st.Dir, func(rel string) bool { return rel == "docker-compose.yml" }, false); err != nil {
			return undo, err
		}
		if _, added, err := Fetch(ctx, to, st.Dir, func(rel string) bool { return strings.HasPrefix(rel, "data/") || rel == "data" }, true); err != nil {
			return undo, err
		} else if len(added) > 0 {
			ui.Muted(i18n.T("compose.addedFiles", "files", strings.Join(added, ", ")))
			if err := ownData(ctx, system.Runtime(st.Runtime), filepath.Join(st.Dir, "data")); err != nil {
				return undo, err
			}
		}
		if err := pinCompose(st.Dir, to, st.Name); err != nil {
			return undo, err
		}
	}

	ui.Step(i18n.T("compose.pulling"))
	// an image that is only on this server (loaded from a file, a mirror) is not an error : up
	// pulls what is still missing and says so if it cannot
	if err := runCompose(ctx, st, pullArgs(st)...); err != nil {
		return undo, err
	}
	ui.Step(i18n.T("compose.starting"))
	if err := runCompose(ctx, st, "up", "-d"); err != nil {
		return undo, err
	}
	if st.Role == RoleRunner {
		if err := waitHealthy(ctx, st, st.Name+"_rte", 5*time.Minute); err != nil {
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
	st.Version = to
	st.UpdatedAt = time.Now().UTC()
	return undo, st.Save()
}

// Rollback goes back to the version before a failed upgrade: its compose file, and for a
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
	ui.Step(i18n.T("compose.rollingBack", "version", undo.Version))
	if st.Role != RoleRunner {
		_ = runCompose(ctx, st, "stop", "app", "rte")
	}
	if err := os.WriteFile(filepath.Join(st.Dir, "docker-compose.yml"), []byte(undo.Compose), 0o644); err != nil {
		return err
	}
	if st.Role != RoleRunner && undo.Dump != "" {
		if err := runCompose(ctx, st, "up", "-d", "mysqldb"); err != nil {
			return err
		}
		if err := waitHealthy(ctx, st, st.Name+"_db", 5*time.Minute); err != nil {
			return err
		}
		ui.Step(i18n.T("compose.restoring", "file", undo.Dump))
		if err := restore(ctx, st, undo.Dump); err != nil {
			return err
		}
	}
	if err := runCompose(ctx, st, "up", "-d"); err != nil {
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
	scheme := "https"
	if !st.HTTPS {
		scheme = "http"
	}
	return fmt.Sprintf("%s://localhost:%d", scheme, st.Port)
}

// Status shows the containers and, for a controller, the version it answers with.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//
// Errors:
//
//	When compose cannot list them.
func Status(ctx context.Context, st *state.State) error {
	if err := runCompose(ctx, st, "ps"); err != nil {
		return err
	}
	if st.Role != RoleRunner {
		if v, err := health.Wait(ctx, URL(st), 5*time.Second); err == nil {
			ui.Done(i18n.T("status.answers", "url", URL(st), "version", v))
		} else {
			ui.Warn(i18n.T("status.noAnswer", "url", URL(st)))
		}
	}
	return nil
}

// Uninstall stops and removes the containers, the systemd unit, and with purge the folder
// and its data.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//	purge: also delete the folder (the database, forms, playbooks...).
//
// Errors:
//
//	When a step fails.
func Uninstall(ctx context.Context, st *state.State, purge bool) error {
	removeSystemd(ctx, st)
	if err := runCompose(ctx, st, "down"); err != nil {
		return err
	}
	if purge {
		ui.Step(i18n.T("uninstall.deleting", "dir", st.Dir))
		if err := removeAll(ctx, system.Runtime(st.Runtime), st.Dir); err != nil {
			return err
		}
	}
	return state.Remove()
}

// removeAll deletes the installation folder, whose data belongs to the containers' user.
//
// Args:
//
//	ctx: the context, to cancel.
//	r: the runtime.
//	dir: the folder.
//
// Errors:
//
//	When it cannot be deleted.
func removeAll(ctx context.Context, r system.Runtime, dir string) error {
	switch {
	case os.Geteuid() == 0:
	case r == system.Podman:
		if err := system.Run(ctx, "", "podman", "unshare", "rm", "-rf", filepath.Join(dir, "data")); err != nil {
			return err
		}
	default:
		// the data belongs to the containers' users (the database's to mysql, uid 999), not to
		// whoever runs the installer : a short container deletes it, as root inside
		if err := system.Run(ctx, "", "docker", "run", "--rm", "-v", dir+":/d", "busybox", "rm", "-rf", "/d/data"); err != nil {
			return err
		}
	}
	return os.RemoveAll(dir)
}

// ─── Podman at boot ───────────────────────────────────────────────────────────

// unitName is the systemd unit of an instance.
//
// Args:
//
//	st: the installation.
//
// Returns:
//
//	Its name.
func unitName(st *state.State) string {
	return "ansibleforms-" + st.Name + ".service"
}

// unitPath is where the unit goes: the system's for root, the user's own otherwise.
//
// Args:
//
//	st: the installation.
//
// Returns:
//
//	Its path.
func unitPath(st *state.State) string {
	if os.Geteuid() == 0 {
		return "/etc/systemd/system/" + unitName(st)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config/systemd/user", unitName(st))
}

// writeSystemd makes Podman start the instance at boot (Podman has no daemon to restart its
// containers): a unit that brings the compose project up, enabled now.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//
// Errors:
//
//	When systemd is not there or a step fails.
func writeSystemd(ctx context.Context, st *state.State) error {
	if !system.Has("systemctl") {
		return errors.New("systemctl not found")
	}
	podman, err := system.Output(ctx, "", "sh", "-c", "command -v podman")
	if err != nil {
		return err
	}
	unit := fmt.Sprintf(`[Unit]
Description=AnsibleForms (%s)
Wants=network-online.target
After=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=%s
ExecStart=%s compose -p %s up -d
ExecStop=%s compose -p %s stop
TimeoutStartSec=600

[Install]
WantedBy=%s
`, st.Name, st.Dir, podman, st.Name, podman, st.Name, map[bool]string{true: "multi-user.target", false: "default.target"}[os.Geteuid() == 0])
	p := unitPath(st)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(unit), 0o644); err != nil {
		return err
	}
	ctl := []string{}
	if os.Geteuid() != 0 {
		ctl = append(ctl, "--user")
		// a user's units only run while the user is logged in, unless it lingers
		if user := os.Getenv("USER"); user != "" {
			_ = system.Run(ctx, "", "loginctl", "enable-linger", user)
		}
	}
	if err := system.Run(ctx, "", "systemctl", append(ctl, "daemon-reload")...); err != nil {
		return err
	}
	return system.Run(ctx, "", "systemctl", append(ctl, "enable", unitName(st))...)
}

// removeSystemd takes the unit away again (uninstall); nothing when there is none.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
func removeSystemd(ctx context.Context, st *state.State) {
	p := unitPath(st)
	if _, err := os.Stat(p); err != nil {
		return
	}
	ctl := []string{}
	if os.Geteuid() != 0 {
		ctl = append(ctl, "--user")
	}
	_ = system.Run(ctx, "", "systemctl", append(ctl, "disable", unitName(st))...)
	_ = os.Remove(p)
	_ = system.Run(ctx, "", "systemctl", append(ctl, "daemon-reload")...)
}

// ─── runners ──────────────────────────────────────────────────────────────────

// joinPrefix starts what Connections > Runners > Join tokens shows for the installer : the app's
// address and a join token, in one string (afjoin2.<base64url JSON {u, t}>).
const joinPrefix = "afjoin2."

// Join is what a runner gets from the app's join string.
type Join struct {
	// AppURL is the app's address.
	AppURL string `json:"u"`
	// Token is the join token (afj_...).
	Token string `json:"t"`
}

// ParseJoin reads the join string Connections > Runners shows ; a bare join token (afj_...) is
// taken too, the app's address then asked separately.
//
// Args:
//
//	s: the string pasted.
//
// Returns:
//
//	What it holds ; AppURL empty for a bare token.
//
// Errors:
//
//	When it is neither.
func ParseJoin(s string) (Join, error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "afj_") {
		return Join{Token: s}, nil
	}
	var j Join
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(s, joinPrefix))
	if err != nil || !strings.HasPrefix(s, joinPrefix) {
		return j, errors.New(i18n.T("join.invalid"))
	}
	if err := json.Unmarshal(raw, &j); err != nil || !strings.HasPrefix(j.Token, "afj_") || j.AppURL == "" {
		return j, errors.New(i18n.T("join.invalid"))
	}
	return j, nil
}

// OutboundIP is this server's address on the network : the one its default route leaves from
// (nothing is sent).
//
// Returns:
//
//	The address, "" when there is no network.
func OutboundIP() string {
	conn, err := net.Dial("udp", "203.0.113.1:9") // TEST-NET-3 : never reached, only routed
	if err != nil {
		return ""
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

// FreePort finds a free port on the host, from a preferred one upwards.
//
// Args:
//
//	host: the address to bind (127.0.0.1, 0.0.0.0).
//	from: the preferred port.
//
// Returns:
//
//	The first port that can be bound.
func FreePort(host string, from int) int {
	for p := from; p < from+100; p++ {
		l, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p)))
		if err == nil {
			l.Close()
			return p
		}
	}
	return from
}
