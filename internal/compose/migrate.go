package compose

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ansibleforms/installer/internal/i18n"
	"github.com/ansibleforms/installer/internal/state"
	"github.com/ansibleforms/installer/internal/system"
	"github.com/ansibleforms/installer/internal/ui"
)

// ─── a Runner of 7.2, moved to a socket mode ──────────────────────────────────
//
// A Runner of 7.2 ran in database mode: its RTE held the app's database settings and wrote
// its own runner under Connections > Runners. From 7.3 an RTE talks to the app over one
// WebSocket instead (outbound: it dials the app ; inbound: the app dials it) and holds no
// database settings. The move keeps the runner: the new RTE signs in under the runner's name
// (RTE_NAME) with the runner's own token, and the app moves that runner to the new mode by
// itself, without an approval. Without the token, a join token adds a new runner instead.
//
// The new RTE is the installer's Runner layout (the files a fresh Runner install writes), in
// the same folder, as a compose project of its own : it starts beside the old container, and
// only once it is connected to the app is the old one stopped and removed. Until then a
// rollback puts the old files back and starts the old container again.

// Migration is what the move of a database-mode Runner needs: what the old one was, and the
// answers for the new one.
type Migration struct {
	// OldProject is the old compose project, OldContainers its containers, OldComposeFile the
	// compose file it was started from.
	OldProject     string
	OldContainers  []string
	OldComposeFile string
	// OldRunnerName is the runner the old RTE was (the one to retire after a join token).
	OldRunnerName string
	// Settings are the old RTE's settings a socket-mode RTE still reads (carried), copied to
	// the new .env; their values may be secrets.
	Settings map[string]string

	// RunnerName is its runner's name under Connections > Runners (RTE_NAME).
	RunnerName string
	// Token is the runner's own token, kept (RTE_TOKEN): the app moves the runner itself.
	// Empty with JoinToken: a new token, and the join token adds a new runner.
	Token     string
	JoinToken string
	// AppURL is the app's address, Mode outbound or inbound, TrustAppCert accepts the app's
	// self-signed certificate.
	AppURL       string
	Mode         string
	TrustAppCert bool
	// Host and Port are where the app reaches an inbound Runner (Port is also the port of an
	// outbound one's health checks, on this server only).
	Host string
	Port int
}

// MigrationUndo is what a failed move needs to go back: the files as they were, the old
// containers (renamed, stopped) and the new compose project.
type MigrationUndo struct {
	// Version is the version before.
	Version string
	// Files are the files the move wrote or removed, with what they held ("" with Missing:
	// it did not exist).
	Files []SavedFile
	// Renamed are the old containers given another name, new name -> their own.
	Renamed map[string]string
	// OldContainers are the old containers, started again on a rollback.
	OldContainers []string
	// NewProject is the new compose project, taken down on a rollback.
	NewProject string
	// Connected: the new RTE connected to the app, which moved the runner (or added the new
	// one): going back is no longer the installer's to do.
	Connected bool
	// Mode is the new RTE's mode, RunnerName its runner, for what the rollback says.
	Mode       string
	RunnerName string
}

// SavedFile is a file as it was before the move.
type SavedFile struct {
	Path    string
	Content []byte
	Mode    os.FileMode
	Missing bool
}

// the compose files compose reads by itself in a folder, the overrides with them: all of them
// make way for the new docker-compose.yml (an old compose.yaml would be read before it)
var composeNames = []string{
	"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml",
	"compose.override.yaml", "compose.override.yml", "docker-compose.override.yaml", "docker-compose.override.yml",
}

// NewProjectName is the compose project of the moved Runner: its instance name, unless that is
// the old project's (compose would then recreate the old container in place of starting the
// new one beside it).
//
// Args:
//
//	name: the instance's name.
//	oldProject: the old compose project.
//
// Returns:
//
//	The new compose project.
func NewProjectName(name, oldProject string) string {
	if name == oldProject {
		return name + "-socket"
	}
	return name
}

// filesToSave are the files the move writes over or removes: the compose files of the folder
// (and the one the old project was started from), the .env, and inbound the certificate and
// key the new RTE serves with.
//
// Args:
//
//	dir: the folder.
//	oldComposeFile: the compose file the old project was started from.
//	inbound: the new RTE is inbound.
//
// Returns:
//
//	Their paths, each once.
func filesToSave(dir, oldComposeFile string, inbound bool) []string {
	var paths []string
	seen := map[string]bool{}
	add := func(p string) {
		if p != "" && !seen[filepath.Clean(p)] {
			seen[filepath.Clean(p)] = true
			paths = append(paths, filepath.Clean(p))
		}
	}
	for _, n := range composeNames {
		add(filepath.Join(dir, n))
	}
	add(oldComposeFile)
	add(filepath.Join(dir, ".env"))
	if inbound {
		add(filepath.Join(dir, "data/certificates/cert.pem"))
		add(filepath.Join(dir, "data/certificates/key.pem"))
	}
	return paths
}

// saveFiles reads the files the move changes, and copies those that exist to the installer's
// backups (readable by their owner only: the .env holds the old database password).
//
// Args:
//
//	dir: the backups' folder.
//	paths: the files.
//	version: the version before, in the copies' names.
//
// Returns:
//
//	The files as they were.
//
// Errors:
//
//	When a file that exists cannot be read or copied.
func saveFiles(dir string, paths []string, version string) ([]SavedFile, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	stamp := time.Now().Format("20060102-150405")
	var saved []SavedFile
	for _, p := range paths {
		info, err := os.Stat(p)
		if errors.Is(err, os.ErrNotExist) {
			saved = append(saved, SavedFile{Path: p, Missing: true})
			continue
		}
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		saved = append(saved, SavedFile{Path: p, Content: data, Mode: info.Mode().Perm()})
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s.%s.%s", filepath.Base(p), version, stamp)), data, 0o600); err != nil {
			return nil, err
		}
	}
	return saved, nil
}

// restoreFiles puts the files back as they were: the content they had, or removed when they
// did not exist.
//
// Args:
//
//	files: the files as saved.
//
// Errors:
//
//	The first file that could not be put back (the others are still tried).
func restoreFiles(files []SavedFile) error {
	var first error
	for _, f := range files {
		var err error
		if f.Missing {
			if err = os.Remove(f.Path); errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		} else {
			err = os.WriteFile(f.Path, f.Content, f.Mode)
		}
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

// carriedEnv adds the old RTE's carried settings to the new .env, under a comment that says
// where they come from.
//
// Args:
//
//	env: the new .env.
//	settings: the carried settings.
//
// Returns:
//
//	The .env with them, in a stable order; as it was when there are none.
func carriedEnv(env string, settings map[string]string) string {
	if len(settings) == 0 {
		return env
	}
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := []string{"# carried over from the RTE of 7.2 (database mode)"}
	for _, k := range keys {
		lines = append(lines, k+"="+settings[k])
	}
	if !strings.HasSuffix(env, "\n") {
		env += "\n"
	}
	return env + strings.Join(lines, "\n") + "\n"
}

// prepareMigration writes the moved Runner's files: the old ones saved (and copied to the
// backups), the old compose files out of the way, the installer's Runner layout with the
// runner's name and token, and the old RTE's carried settings. Nothing runs yet.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation (unchanged).
//	to: the version to upgrade to.
//	m: the old Runner and the answers.
//
// Returns:
//
//	What a rollback needs (nil when nothing was saved yet), what to tell at the end, and the
//	installation as it will be once the new RTE is connected.
//
// Errors:
//
//	When a file cannot be read, saved or written.
func prepareMigration(ctx context.Context, st *state.State, to string, m Migration) (*MigrationUndo, *Result, *state.State, error) {
	inbound := m.Mode == "inbound"
	undo := &MigrationUndo{Version: st.Version, Renamed: map[string]string{}, OldContainers: m.OldContainers, Mode: m.Mode, RunnerName: m.RunnerName}
	next := *st
	next.Version, next.Port, next.HTTPS, next.DatabaseMode = to, m.Port, inbound, false
	next.Project = ""
	if p := NewProjectName(st.Name, m.OldProject); p != st.Name {
		next.Project = p
	}
	undo.NewProject = Project(&next)

	// step 1 : the files as they are, kept for a rollback and copied to the backups
	saved, err := saveFiles(backupDir(st), filesToSave(st.Dir, m.OldComposeFile, inbound), st.Version)
	if err != nil {
		return nil, nil, nil, err
	}
	undo.Files = saved
	ui.Done(i18n.T("migrate.backedUp", "dir", backupDir(st)))

	// step 2 : the old compose files make way : compose reads compose.yaml before the new
	// docker-compose.yml, and an override would change the new RTE (all of them are saved)
	dir := filepath.Clean(st.Dir)
	for _, f := range saved {
		if f.Missing || f.Path == filepath.Join(dir, ".env") || strings.HasPrefix(f.Path, filepath.Join(dir, "data")+string(os.PathSeparator)) {
			continue
		}
		if err := os.Remove(f.Path); err != nil {
			return undo, nil, nil, err
		}
	}

	// step 3 : the installer's Runner layout, its data files kept, the runner's name and token,
	// and what the old RTE set that a socket-mode one still reads ; never its DB_*
	ro := RunnerOptions{AppURL: m.AppURL, Mode: m.Mode, JoinToken: m.JoinToken, TrustAppCert: m.TrustAppCert, Token: m.Token, RteName: m.RunnerName}
	if inbound {
		ro.Host = m.Host
		ro.Address = InboundAddress(m.Host, m.Port)
	}
	if ro.Token == "" {
		ro.Token = Secret(32)
	}
	res, err := writeRunner(ctx, Options{Role: RoleRunner, Runtime: system.Runtime(st.Runtime), Dir: st.Dir, Name: st.Name, Version: to, Port: m.Port, Runner: ro, KeepData: true})
	if err != nil {
		return undo, nil, nil, err
	}
	envPath := filepath.Join(st.Dir, ".env")
	env, err := os.ReadFile(envPath)
	if err != nil {
		return undo, nil, nil, err
	}
	if err := os.WriteFile(envPath, []byte(carriedEnv(string(env), m.Settings)), 0o600); err != nil {
		return undo, nil, nil, err
	}
	// the old .env may have been readable by all : the new one holds the runner's token
	if err := os.Chmod(envPath, 0o600); err != nil {
		return undo, nil, nil, err
	}
	return undo, res, &next, nil
}

// MigrateRunner moves a database-mode Runner of 7.2 to a socket mode: its files backed up,
// the installer's Runner layout written, the new RTE started beside the old one, and once it is
// connected to the app the old container stopped and removed.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation (saved with the new version, project and port once connected).
//	to: the version to upgrade to (the app's).
//	m: the old Runner and the answers.
//
// Returns:
//
//	What RollbackMigration needs (set as soon as the files are saved, also when an error
//	follows), and what to tell at the end.
//
// Errors:
//
//	When a step before the connection fails; after it only a warning is said.
func MigrateRunner(ctx context.Context, st *state.State, to string, m Migration) (*MigrationUndo, *Result, error) {
	r := system.Runtime(st.Runtime)
	inbound := m.Mode == "inbound"
	// steps 1 to 3 : the files (prepareMigration)
	undo, res, next, err := prepareMigration(ctx, st, to, m)
	if err != nil {
		return undo, nil, err
	}
	if err := ownData(ctx, r, filepath.Join(st.Dir, "data")); err != nil {
		return undo, nil, err
	}

	// step 4 : an old container by the new one's name (<name>_rte) steps aside, still running
	newContainer := st.Name + "_rte"
	for _, c := range m.OldContainers {
		if c == newContainer {
			aside := c + "-old"
			if err := system.Run(ctx, "", string(r), "rename", c, aside); err != nil {
				return undo, nil, err
			}
			undo.Renamed[aside] = c
		}
	}

	// step 5 : the new image, and the new RTE beside the old one ; an inbound port the old one
	// holds is freed first (the app reaches the new one there)
	ui.Step(i18n.T("compose.pulling"))
	if err := runCompose(ctx, next, pullArgs(next)...); err != nil {
		return undo, nil, err
	}
	if inbound && FreePort("0.0.0.0", m.Port) != m.Port {
		ui.Step(i18n.T("migrate.stoppingFirst", "port", m.Port))
		if err := stopContainers(ctx, r, undo.current()); err != nil {
			return undo, nil, err
		}
	}
	since := time.Now().Add(-2 * time.Second)
	ui.Step(i18n.T("compose.starting"))
	if err := runCompose(ctx, next, "up", "-d"); err != nil {
		return undo, nil, err
	}

	// step 6 : connected to the app, under its runner
	ui.Step(i18n.T("migrate.waiting", "url", m.AppURL))
	if err := waitConnected(ctx, r, newContainer, since, m.Port, inbound, 4*time.Minute); err != nil {
		return undo, nil, err
	}
	undo.Connected = true
	ui.Done(i18n.T("migrate.connected", "name", m.RunnerName, "mode", i18n.T("runner.mode_"+m.Mode)))

	// step 7 : the old RTE goes ; what fails now is only said, the new one runs the jobs
	ui.Step(i18n.T("migrate.removingOld", "containers", strings.Join(undo.current(), ", ")))
	if err := stopContainers(ctx, r, undo.current()); err == nil {
		if err := system.Run(ctx, "", string(r), append([]string{"rm"}, undo.current()...)...); err != nil {
			ui.Warn(i18n.T("migrate.removeFailed", "error", err))
		}
		// the old project's network, when nothing else uses it
		_, _ = system.Output(ctx, "", string(r), "network", "rm", m.OldProject+"_default")
	} else {
		ui.Warn(i18n.T("migrate.removeFailed", "error", err))
	}
	if r == system.Podman {
		if err := writeSystemd(ctx, next); err != nil {
			ui.Warn(i18n.T("compose.systemdFailed", "error", err))
		}
	}
	next.UpdatedAt = time.Now().UTC()
	*st = *next
	return undo, res, st.Save()
}

// current are the old containers by the names they have now (renamed aside or not).
//
// Returns:
//
//	Their names.
func (u *MigrationUndo) current() []string {
	back := map[string]string{}
	for aside, own := range u.Renamed {
		back[own] = aside
	}
	out := make([]string, 0, len(u.OldContainers))
	for _, c := range u.OldContainers {
		if aside, ok := back[c]; ok {
			c = aside
		}
		out = append(out, c)
	}
	return out
}

// stopContainers stops containers (nothing to do for none).
//
// Args:
//
//	ctx: the context, to cancel.
//	r: the runtime.
//	names: the containers.
//
// Errors:
//
//	When the runtime fails.
func stopContainers(ctx context.Context, r system.Runtime, names []string) error {
	if len(names) == 0 {
		return nil
	}
	return system.Run(ctx, "", string(r), append([]string{"stop"}, names...)...)
}

// RollbackMigration goes back to the database-mode Runner after a move that did not connect:
// the new project taken down, the old files put back, the old containers by their own names
// and started again.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation (unchanged by a failed move).
//	undo: what MigrateRunner kept.
//
// Errors:
//
//	When the new RTE connected already (the app moved the runner), or a step fails; the
//	backups stay in installer-backups for a restore by hand.
func RollbackMigration(ctx context.Context, st *state.State, undo *MigrationUndo) error {
	if undo.Connected {
		return errors.New(i18n.T("migrate.noRollback", "name", undo.RunnerName))
	}
	r := system.Runtime(st.Runtime)
	ui.Step(i18n.T("compose.rollingBack", "version", undo.Version))
	// step 1 : the new RTE goes, with its project's network (its files are still in place)
	if undo.NewProject != "" {
		name, args := composeCmd(r, undo.NewProject, "down")
		_ = system.Run(ctx, st.Dir, name, args...)
	}
	// step 2 : the old files
	if err := restoreFiles(undo.Files); err != nil {
		return err
	}
	// step 3 : the old containers by their own names, started again
	for aside, own := range undo.Renamed {
		if err := system.Run(ctx, "", string(r), "rename", aside, own); err != nil {
			return err
		}
	}
	undo.Renamed = map[string]string{}
	if len(undo.OldContainers) > 0 {
		if err := system.Run(ctx, "", string(r), append([]string{"start"}, undo.OldContainers...)...); err != nil {
			return err
		}
	}
	ui.Muted(i18n.T("migrate.rolledBackNote", "name", undo.RunnerName, "mode", i18n.T("runner.mode_"+undo.Mode)))
	return nil
}

// ─── connected ────────────────────────────────────────────────────────────────

// What a socket-mode RTE's log says of its connection (rte/socket/rte-socket.js), checked in
// order: refused first (the app said why), then waiting for an approval (the app did not know
// its runner by that name), then connected.
const (
	connRefused   = "refused"
	connPending   = "pending"
	connConnected = "connected"
)

// Connection reads a socket-mode RTE's log for how its connection to the app went.
//
// Args:
//
//	log: its log since it started.
//
// Returns:
//
//	refused, pending, connected or "" (nothing yet), and the line that says so (a refusal's
//	reason, as the app gave it; an RTE never logs its token).
func Connection(log string) (string, string) {
	lines := strings.Split(strings.ReplaceAll(log, "\r\n", "\n"), "\n")
	find := func(marks ...string) string {
		for _, l := range lines {
			for _, m := range marks {
				if strings.Contains(l, m) {
					return strings.TrimSpace(l)
				}
			}
		}
		return ""
	}
	if l := find("the app refused"); l != "" {
		return connRefused, l
	}
	if l := find("waiting for an admin to approve", "asked to join as inbound runner"); l != "" {
		return connPending, l
	}
	if l := find("connected to the app"); l != "" {
		return connConnected, l
	}
	return "", ""
}

// waitConnected waits for the new RTE to connect to the app under its runner: its log says
// so (or, outbound, its /ready answers when its log cannot be read). Refused, waiting for an
// approval, or its container stopped: it fails at once with why.
//
// Args:
//
//	ctx: the context, to cancel.
//	r: the runtime.
//	container: the new RTE's container.
//	since: when it was started (its log from then on).
//	port: its port on this server.
//	inbound: the app dials it (its /ready says nothing of the connection).
//	timeout: how long to wait.
//
// Errors:
//
//	When it is not connected in time, refused, waiting for an approval, or stopped.
func waitConnected(ctx context.Context, r system.Runtime, container string, since time.Time, port int, inbound bool, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 3 * time.Second}
	for time.Now().Before(deadline) {
		log, err := system.Combined(ctx, "", string(r), "logs", "--since", since.UTC().Format(time.RFC3339), container)
		if err == nil {
			switch state, line := Connection(log); state {
			case connRefused:
				return errors.New(i18n.T("migrate.refused", "reason", line))
			case connPending:
				return errors.New(i18n.T("migrate.pending", "container", container))
			case connConnected:
				return nil
			}
		} else if !inbound {
			if res, herr := client.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/ready"); herr == nil {
				res.Body.Close()
				if res.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		if s, _ := system.Output(ctx, "", string(r), "inspect", "--format", "{{.State.Status}}", container); s == "exited" || s == "dead" {
			return errors.New(i18n.T("migrate.exited", "container", container, "runtime", string(r)))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
	return errors.New(i18n.T("migrate.notConnected", "container", container, "runtime", string(r)))
}
