package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ansibleforms/installer/internal/state"
	"github.com/ansibleforms/installer/internal/system"
)

// a folder's files, by name and content
func folder(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// a project taken over from docker-compose.yaml has it renamed docker-compose.yml, and the
// compose files compose would not read moved aside: the upgrade's file is then the one read
func TestRepoComposeName(t *testing.T) {
	dir := folder(t, map[string]string{"docker-compose.yaml": "read", "docker-compose.yml": "stale"})
	if err := repoComposeName(dir); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "docker-compose.yml")); string(data) != "read" {
		t.Errorf("docker-compose.yml holds %q, want the file compose read", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.yaml")); err == nil {
		t.Errorf("docker-compose.yaml left beside it")
	}
	entries, _ := os.ReadDir(dir)
	aside := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "docker-compose.yml.") && strings.HasSuffix(e.Name(), ".bak") {
			aside++
		}
	}
	if aside != 1 {
		t.Errorf("%d files moved aside, want 1: %v", aside, entries)
	}
}

// docker-compose.yml alone, or no compose file: nothing changes
func TestRepoComposeNameUnchanged(t *testing.T) {
	dir := folder(t, map[string]string{"docker-compose.yml": "x"})
	if err := repoComposeName(dir); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("files changed: %v", entries)
	}
	if err := repoComposeName(t.TempDir()); err != nil {
		t.Errorf("no compose file: %v", err)
	}
}

// ─── an installation to work on ───────────────────────────────────────────────

// installedOn installs a controller (or an outbound Runner) on the fake host.
//
// Args:
//
//	t: the test.
//	h: the host.
//	role: controller or runner.
//
// Returns:
//
//	Its state.
func installedOn(t *testing.T, h *host, role string) *state.State {
	t.Helper()
	o := h.options(system.Docker, "off")
	if role == RoleRunner {
		o.Role, o.Runner = role, RunnerOptions{Mode: "outbound"}
	}
	st, _, err := Install(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// readOnly makes a file or a folder read-only for the rest of the test.
func readOnly(t *testing.T, p string) {
	_ = os.Chmod(p, 0o500)
	t.Cleanup(func() { _ = os.Chmod(p, 0o755) })
}

// ─── upgrade and rollback ─────────────────────────────────────────────────────

// TestUpgradeRollbackController checks a controller's upgrade: a backup, the new files and
// the ones added given to the containers, the images pinned, pulled and started, the state
// saved ; and its rollback with the database restored.
func TestUpgradeRollbackController(t *testing.T) {
	h := newHost(t)
	st := installedOn(t, h, RoleController)
	put(t, h.repo, map[string]string{"data/new.txt": "new"})
	t.Setenv("ANSWERS", "7.4.0")
	undo, err := Upgrade(context.Background(), st, "7.4.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Version != "7.4.0" || undo.Dump == "" || !undo.Started {
		t.Errorf("state %+v, undo %+v", st, undo)
	}
	if err := Rollback(context.Background(), st, undo); err != nil || st.Version != "7.3.0" {
		t.Errorf("rollback %v", err)
	}
	if !h.bin.Called("docker compose -p af exec -T mysqldb sh -c MYSQL_PWD") {
		t.Error("no dump nor restore")
	}
}

// TestUpgradeRollbackRunner checks a Runner moved to the new image and waited for, and moved
// back.
func TestUpgradeRollbackRunner(t *testing.T) {
	h := newHost(t)
	st := installedOn(t, h, RoleRunner)
	undo, err := Upgrade(context.Background(), st, "7.4.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := os.ReadFile(filepath.Join(h.dir, "docker-compose.yml")); !strings.Contains(string(c), "rte-full:7.4.0") {
		t.Errorf("compose %s", c)
	}
	if err := Rollback(context.Background(), st, undo); err != nil {
		t.Error(err)
	}
}

// TestUpgradeRollbackMySQL checks an upgrade that moves MySQL to a new major: the users that
// are not the installer's saved, the data's folder remembered ; its rollback puts the new
// data aside, restores the dump and replays those users.
func TestUpgradeRollbackMySQL(t *testing.T) {
	h := newHost(t)
	st := mysqlFixture(t, "mysql:9.7.4")
	db := &fakeMySQL{users: knownUsersOut, all: read(t, "mysql-all-users.txt"), shown: read(t, "mysql-show-users.txt")}
	db.use(t)
	undo, err := Upgrade(context.Background(), st, "7.3.0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if undo.Users == "" || undo.MySQLData == "" || undo.MySQLTo != "9.7.4" {
		t.Errorf("undo %+v", undo)
	}
	if err := Rollback(context.Background(), st, undo); err != nil {
		t.Fatal(err)
	}
	if len(db.replayed) != 1 {
		t.Errorf("replayed %v", db.replayed)
	}
	db.replayErr, db.replayOut = errors.New("mysql failed"), "ERROR 1396"
	undo.Started = true
	_ = os.MkdirAll(undo.MySQLData, 0o755)
	if err := Rollback(context.Background(), st, undo); err != nil {
		t.Errorf("a replay that fails only warns: %v", err)
	}
	_ = h
}

// TestUpgradeMySQLCnfFails checks a MySQL move whose my.cnf cannot be read.
func TestUpgradeMySQLCnfFails(t *testing.T) {
	newHost(t)
	st := mysqlFixture(t, "mysql:9.7.4")
	(&fakeMySQL{users: knownUsersOut}).use(t)
	cnf := filepath.Join(st.Dir, filepath.FromSlash(myCnfPath))
	_ = os.Remove(cnf)
	_ = os.MkdirAll(filepath.Join(cnf, "x"), 0o755)
	if _, err := Upgrade(context.Background(), st, "7.3.0", &Prep{MySQLTo: "9.7.4"}); err == nil {
		t.Error("upgraded")
	}
}

// TestUpgradeFails checks that each step that fails stops the upgrade.
func TestUpgradeFails(t *testing.T) {
	type c = func(t *testing.T, h *host, st *state.State) *Prep
	cases := map[string]c{
		"no compose": func(t *testing.T, h *host, st *state.State) *Prep {
			_ = os.Remove(filepath.Join(h.dir, "docker-compose.yml"))
			return nil
		},
		"compose names": func(t *testing.T, h *host, st *state.State) *Prep {
			_ = os.Rename(filepath.Join(h.dir, "docker-compose.yml"), filepath.Join(h.dir, "compose.yaml"))
			readOnly(t, h.dir)
			return nil
		},
		"prepare": func(t *testing.T, h *host, st *state.State) *Prep {
			Source = filepath.Join(t.TempDir(), "none")
			return nil
		},
		"blocked": func(t *testing.T, h *host, st *state.State) *Prep { return &Prep{MySQLBlocked: "unknown users"} },
		"mysql": func(t *testing.T, h *host, st *state.State) *Prep {
			(&fakeMySQL{users: "error"}).use(t)
			return &Prep{MySQLTo: "9.7.4"}
		},
		"backup": func(t *testing.T, h *host, st *state.State) *Prep { t.Setenv("FAIL", "mysqldump"); return nil },
		"users": func(t *testing.T, h *host, st *state.State) *Prep {
			(&fakeMySQL{users: "", all: "error"}).use(t)
			return &Prep{MySQLTo: "9.7.4"}
		},
		"move mysql": func(t *testing.T, h *host, st *state.State) *Prep {
			db := &fakeMySQL{users: "", all: "", shown: ""}
			db.use(t)
			_ = os.Remove(filepath.Join(h.dir, ".env"))
			_ = os.WriteFile(filepath.Join(h.dir, ".env"), nil, 0o000)
			return &Prep{MySQLTo: "9.7.4"}
		},
		"fetch compose": func(t *testing.T, h *host, st *state.State) *Prep {
			p, _ := Prepare(context.Background(), st, "7.4.0")
			Source = filepath.Join(t.TempDir(), "none")
			return p
		},
		"fetch data": func(t *testing.T, h *host, st *state.State) *Prep {
			put(t, h.repo, map[string]string{"data/sub/new.txt": "new"})
			_ = os.WriteFile(filepath.Join(h.dir, "data", "sub"), nil, 0o644)
			return nil
		},
		"own added": func(t *testing.T, h *host, st *state.State) *Prep {
			put(t, h.repo, map[string]string{"data/new.txt": "new"})
			t.Setenv("FAIL", "docker run")
			return nil
		},
		"apply": func(t *testing.T, h *host, st *state.State) *Prep {
			return &Prep{SeedAction: "custom", Seed: "s"}
		},
		"pin": func(t *testing.T, h *host, st *state.State) *Prep {
			hookOn(t, "mysqldump", `/bin/chmod 0444 "$DIR/docker-compose.yml"`)
			_ = os.Remove(filepath.Join(h.repo, "docker-compose.yml"))
			p, _ := Prepare(context.Background(), st, "7.4.0")
			return p
		},
		"pull":      func(t *testing.T, h *host, st *state.State) *Prep { t.Setenv("FAIL", "compose -p af pull"); return nil },
		"up":        func(t *testing.T, h *host, st *state.State) *Prep { t.Setenv("FAIL", "compose -p af up"); return nil },
		"no answer": func(t *testing.T, h *host, st *state.State) *Prep { t.Setenv("ANSWERS", ""); return nil },
		"old answer": func(t *testing.T, h *host, st *state.State) *Prep {
			t.Setenv("ANSWERS", "7.3.0")
			return nil
		},
		"state": func(t *testing.T, h *host, st *state.State) *Prep {
			t.Setenv("ANSWERS", "7.4.0")
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(h.dir, ".env"))
			return nil
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHost(t)
			st := installedOn(t, h, RoleController)
			p := setup(t, h, st)
			if name == "apply" {
				_ = os.Chmod(filepath.Join(h.dir, "data"), 0o500)
				hookOn(t, "compose -p af exec", `/bin/chmod 0500 "$DIR/data"`)
				t.Cleanup(func() { _ = os.Chmod(filepath.Join(h.dir, "data"), 0o755) })
				_ = os.Chmod(filepath.Join(h.dir, "data"), 0o755)
			}
			if _, err := Upgrade(context.Background(), st, "7.4.0", p); err == nil {
				t.Errorf("%s: upgraded", name)
			}
		})
	}
	t.Run("runner", func(t *testing.T) {
		h := newHost(t)
		st := installedOn(t, h, RoleRunner)
		t.Setenv("HEALTH", "starting")
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if _, err := Upgrade(ctx, st, "7.4.0", nil); err == nil {
			t.Error("an unhealthy Runner")
		}
		_ = os.Chmod(filepath.Join(h.dir, "docker-compose.yml"), 0o444)
		if _, err := Upgrade(context.Background(), st, "7.4.0", nil); err == nil {
			t.Error("a compose file that cannot be written")
		}
	})
}

// TestRollbackFails checks each step of a rollback that fails.
func TestRollbackFails(t *testing.T) {
	cases := map[string]func(t *testing.T, h *host, st *state.State) *Undo{
		"compose": func(t *testing.T, h *host, st *state.State) *Undo {
			_ = os.Chmod(filepath.Join(h.dir, "docker-compose.yml"), 0o444)
			return &Undo{Version: "7.2.0"}
		},
		"prep": func(t *testing.T, h *host, st *state.State) *Undo {
			_ = os.Chmod(filepath.Join(h.dir, ".env"), 0o400)
			return &Undo{Version: "7.2.0", Env: "X=1"}
		},
		"stop mysqldb": func(t *testing.T, h *host, st *state.State) *Undo {
			t.Setenv("FAIL", "stop mysqldb")
			return &Undo{Version: "7.2.0", Dump: "d.sql", MySQLData: filepath.Join(h.dir, "data", "mysql"), Started: true}
		},
		"aside": func(t *testing.T, h *host, st *state.State) *Undo {
			t.Setenv("FAIL", "busybox mv")
			readOnly(t, filepath.Join(h.dir, "data"))
			return &Undo{Version: "7.2.0", Dump: "d.sql", MySQLData: filepath.Join(h.dir, "data", "none"), Started: true}
		},
		"up mysqldb": func(t *testing.T, h *host, st *state.State) *Undo {
			t.Setenv("FAIL", "up -d mysqldb")
			return &Undo{Version: "7.2.0", Dump: "d.sql"}
		},
		"healthy": func(t *testing.T, h *host, st *state.State) *Undo {
			t.Setenv("HEALTH", "starting")
			return &Undo{Version: "7.2.0", Dump: "d.sql"}
		},
		"restore": func(t *testing.T, h *host, st *state.State) *Undo {
			return &Undo{Version: "7.2.0", Dump: filepath.Join(h.dir, "none.sql")}
		},
		"up": func(t *testing.T, h *host, st *state.State) *Undo {
			t.Setenv("FAIL", "af up -d")
			return &Undo{Version: "7.2.0"}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			h := newHost(t)
			st := installedOn(t, h, RoleController)
			_ = os.WriteFile(filepath.Join(h.dir, "d.sql"), []byte("--"), 0o600)
			undo := setup(t, h, st)
			if undo.Dump == "d.sql" {
				undo.Dump = filepath.Join(h.dir, "d.sql")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			if err := Rollback(ctx, st, undo); err == nil {
				t.Errorf("%s: rolled back", name)
			}
		})
	}
}

// ─── backup, status, uninstall ────────────────────────────────────────────────

// TestBackupFails checks a Runner, a compose file that cannot be renamed, backups that cannot
// be made, a dump that fails and settings that cannot be copied.
func TestBackupFails(t *testing.T) {
	ctx := context.Background()
	for name, setup := range map[string]func(t *testing.T, h *host, st *state.State){
		"runner": func(t *testing.T, h *host, st *state.State) { st.Role = RoleRunner },
		"names": func(t *testing.T, h *host, st *state.State) {
			_ = os.Rename(filepath.Join(h.dir, "docker-compose.yml"), filepath.Join(h.dir, "compose.yaml"))
			readOnly(t, h.dir)
		},
		"folder": func(t *testing.T, h *host, st *state.State) {
			_ = os.WriteFile(filepath.Join(h.dir, "installer-backups"), nil, 0o644)
		},
		"dump":     func(t *testing.T, h *host, st *state.State) { t.Setenv("FAIL", "mysqldump") },
		"settings": func(t *testing.T, h *host, st *state.State) { _ = os.Remove(filepath.Join(h.dir, ".env")) },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHost(t)
			st := installedOn(t, h, RoleController)
			setup(t, h, st)
			if _, err := Backup(ctx, st); err == nil {
				t.Errorf("%s: backed up", name)
			}
		})
	}
}

// TestStatusAndUninstall checks the status of a controller that answers or not and of a
// Runner, and uninstalls: kept data, purged data as root, through podman and through docker,
// a systemd unit removed, and each failure.
func TestStatusAndUninstall(t *testing.T) {
	ctx := context.Background()
	h := newHost(t)
	st := installedOn(t, h, RoleController)
	if err := Status(ctx, st); err != nil {
		t.Error(err)
	}
	t.Setenv("ANSWERS", "")
	_ = Status(ctx, st)
	st.Role = RoleRunner
	_ = Status(ctx, st)
	t.Setenv("FAIL", "af ps")
	if Status(ctx, st) == nil {
		t.Error("ps failed")
	}
	t.Setenv("FAIL", "af down")
	if Uninstall(ctx, st, false) == nil {
		t.Error("down failed")
	}
	t.Setenv("FAIL", "")

	// a unit as a user, then the data purged through docker
	_ = os.MkdirAll(filepath.Dir(unitPath(st)), 0o755)
	_ = os.WriteFile(unitPath(st), nil, 0o644)
	if err := Uninstall(ctx, st, true); err != nil || !h.bin.Called("systemctl --user disable") || !h.bin.Called("docker run --rm -v "+h.dir+":/d busybox rm") {
		t.Errorf("purge through docker: %v", err)
	}
	for name, setup := range map[string]func(st *state.State){
		"root":   func(st *state.State) { geteuid = func() int { return 0 } },
		"podman": func(st *state.State) { st.Runtime = "podman" },
	} {
		h := newHost(t)
		st := installedOn(t, h, RoleController)
		setup(st)
		if name == "root" {
			_ = os.MkdirAll(systemdDir, 0o755)
			_ = os.WriteFile(unitPath(st), nil, 0o644)
		}
		if err := Uninstall(ctx, st, true); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, fail := range map[string]string{"docker": "busybox rm", "podman": "unshare rm"} {
		h := newHost(t)
		st := installedOn(t, h, RoleController)
		st.Runtime = name
		t.Setenv("FAIL", fail)
		if Uninstall(ctx, st, true) == nil {
			t.Errorf("%s: purged", name)
		}
	}
}

// TestWriteSystemd checks the unit of a Podman installation as root and as a user, and each
// step that fails.
func TestWriteSystemd(t *testing.T) {
	ctx := context.Background()
	h := newHost(t)
	st := installedOn(t, h, RoleController)
	t.Setenv("USER", "")
	if err := writeSystemd(ctx, st); err != nil || h.bin.Called("loginctl") {
		t.Errorf("no user name: %v", err)
	}
	geteuid = func() int { return 0 }
	if err := writeSystemd(ctx, st); err != nil {
		t.Fatal(err)
	}
	if u, _ := os.ReadFile(unitPath(st)); !strings.Contains(string(u), "WantedBy=multi-user.target") {
		t.Errorf("unit %s", u)
	}
	for name, setup := range map[string]func(t *testing.T){
		"sh":     func(t *testing.T) { t.Setenv("FAIL", "sh -c") },
		"folder": func(t *testing.T) { systemdDir = filepath.Join(h.dir, ".env", "x") + "/" },
		"file": func(t *testing.T) {
			_ = os.MkdirAll(unitPath(st)+".d", 0o755)
			_ = os.Remove(unitPath(st))
			_ = os.MkdirAll(filepath.Join(unitPath(st), "x"), 0o755)
		},
		"daemon-reload": func(t *testing.T) { t.Setenv("FAIL", "daemon-reload") },
		"enable":        func(t *testing.T) { t.Setenv("FAIL", "systemctl enable") },
		"systemctl": func(t *testing.T) {
			_ = os.Remove(filepath.Join(h.bin.Dir, "systemctl"))
			t.Cleanup(func() { h.bin.Add(t, "systemctl", fakeProgram("systemctl", "")) })
		},
	} {
		t.Run(name, func(t *testing.T) {
			was := systemdDir
			defer func() { systemdDir = was }()
			setup(t)
			if writeSystemd(ctx, st) == nil {
				t.Errorf("%s: written", name)
			}
			_ = os.RemoveAll(unitPath(st))
		})
	}
}

// TestAddresses checks the address this server routes from, none when it cannot route, and
// a port taken when none is free.
func TestAddresses(t *testing.T) {
	was := outboundTarget
	defer func() { outboundTarget = was }()
	outboundTarget = "127.0.0.1:9"
	if OutboundIP() != "127.0.0.1" {
		t.Errorf("outbound %q", OutboundIP())
	}
	outboundTarget = "no address"
	if OutboundIP() != "" {
		t.Error("an address that is none")
	}
	if FreePort("256.0.0.1", 8000) != 8000 {
		t.Error("no free port: the first one")
	}
}
