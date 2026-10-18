package compose

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ansibleforms/installer/internal/state"
)

// ─── a Runner of 7.2 moved to a socket mode : its files ───────────────────────

// the token the 7.2 RTE had (RTE_TOKEN in its .env), kept by the move
const token72 = "the-72-rte-token-0123456789"

// readFile reads a file of a test, its content as text.
func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// put writes files under a folder.
func put(t *testing.T, dir string, files map[string]string) {
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

// runner72 is the folder of a 7.2 RTE set up by hand in database mode (the fixtures), with an
// ansible.cfg of its own, and a local copy of the docker repository to take the files from.
func runner72(t *testing.T) (*state.State, Migration) {
	t.Helper()
	repo := t.TempDir()
	put(t, repo, map[string]string{
		"docker-compose.yml":       "services: {}\n",
		"data/ansible/ansible.cfg": "[defaults]\n# the repository's\n",
		"data/ansible/roles/.keep": "",
		"data/ssh/.gitignore":      "*\n",
		"data/git/.gitconfig":      "[user]\n",
		"data/seed.yaml":           "# seed\n",
	})
	was := Source
	Source = repo
	t.Cleanup(func() { Source = was })

	dir := t.TempDir()
	put(t, dir, map[string]string{
		"docker-compose.yml":          read(t, "compose-7.2-rte.yml"),
		".env":                        read(t, "env-7.2-rte.env"),
		"docker-compose.override.yml": "services:\n  rte:\n    mem_limit: 2g\n",
		"data/ansible/ansible.cfg":    "[defaults]\nforks = 50\n",
	})
	st := &state.State{Mode: state.Compose, Role: RoleRunner, Runtime: "docker", Dir: dir, Name: "af", Project: "rte", Version: "7.2.0", Port: 8000, DatabaseMode: true}
	m := Migration{
		OldProject: "rte", OldContainers: []string{"af_rte"}, OldComposeFile: filepath.Join(dir, "docker-compose.yml"),
		Settings:   map[string]string{"RTE_MAX_JOBS": "4", "HTTPS_PROXY": "http://proxy.example.com:3128"},
		RunnerName: "rte72.example.com", OldRunnerName: "rte72.example.com", Token: token72,
		AppURL: "https://forms.example.com", Mode: "outbound", Port: 8001,
	}
	return st, m
}

// envLines are a .env's live lines, by key.
func envLines(content string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(content, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok {
			out[k] = v
		}
	}
	return out
}

// outbound, the runner's token kept : the installer's Runner layout, its name and token, the
// settings carried, no database settings ; the old files saved, and put back by a rollback
func TestPrepareMigrationOutbound(t *testing.T) {
	st, m := runner72(t)
	oldCompose, oldEnv := readFile(t, filepath.Join(st.Dir, "docker-compose.yml")), readFile(t, filepath.Join(st.Dir, ".env"))
	undo, res, next, err := prepareMigration(context.Background(), st, "7.3.0", m)
	if err != nil {
		t.Fatal(err)
	}
	// the compose file : the installer's Runner, at the version, its container af_rte
	compose := readFile(t, filepath.Join(st.Dir, "docker-compose.yml"))
	if !strings.Contains(compose, "written by the AnsibleForms installer") || !strings.Contains(compose, "container_name: af_rte") || !strings.Contains(compose, "ansibleforms-rte-full:7.3.0") {
		t.Errorf("compose file:\n%s", compose)
	}
	if _, err := os.Stat(filepath.Join(st.Dir, "docker-compose.override.yml")); !os.IsNotExist(err) {
		t.Errorf("the old override is still read by compose")
	}
	// the .env : the socket mode, the runner kept, the settings carried, nothing of the database
	env := envLines(readFile(t, filepath.Join(st.Dir, ".env")))
	want := map[string]string{"AF_ROLE": "rte", "RTE_MODE": "outbound", "APP_URL": "https://forms.example.com", "RTE_NAME": "rte72.example.com",
		"RTE_TOKEN": token72, "RTE_LOCAL_PORT": "127.0.0.1:8001", "RTE_MAX_JOBS": "4", "HTTPS_PROXY": "http://proxy.example.com:3128"}
	for k, v := range want {
		if env[k] != v {
			t.Errorf(".env %s = %q, want %q", k, env[k], v)
		}
	}
	for k := range env {
		if strings.HasPrefix(k, "DB_") || k == "ENCRYPTION_SECRET" || k == "RTE_URL" || k == "RTE_JOIN_TOKEN" {
			t.Errorf(".env still sets %s", k)
		}
	}
	if info, _ := os.Stat(filepath.Join(st.Dir, ".env")); info.Mode().Perm() != 0o600 {
		t.Errorf(".env is %v, want 0600", info.Mode().Perm())
	}
	// its own ansible.cfg kept, the repository's missing files added
	if got := readFile(t, filepath.Join(st.Dir, "data/ansible/ansible.cfg")); !strings.Contains(got, "forks = 50") {
		t.Errorf("ansible.cfg replaced: %s", got)
	}
	if _, err := os.Stat(filepath.Join(st.Dir, "data/git/.gitconfig")); err != nil {
		t.Errorf("data/git/.gitconfig not added")
	}
	// what the installation becomes : its own project (af is not the old project rte)
	if next.Project != "" || next.Name != "af" || next.Port != 8001 || next.HTTPS || next.DatabaseMode || next.Version != "7.3.0" {
		t.Errorf("next %+v", next)
	}
	if undo.NewProject != "af" || st.Version != "7.2.0" || !st.DatabaseMode {
		t.Errorf("undo %+v, state %+v", undo, st)
	}
	if res.Token != token72 || res.CertFingerprint != "" {
		t.Errorf("result %+v", res)
	}
	// the backups : the old compose file, override and .env, readable by their owner only
	backups, _ := filepath.Glob(filepath.Join(st.Dir, "installer-backups", "*.7.2.0.*"))
	if len(backups) != 3 {
		t.Fatalf("backups %v", backups)
	}
	for _, b := range backups {
		if info, _ := os.Stat(b); info.Mode().Perm() != 0o600 {
			t.Errorf("%s is %v", b, info.Mode().Perm())
		}
	}
	// a rollback puts every file back as it was
	if err := restoreFiles(undo.Files); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(st.Dir, "docker-compose.yml")) != oldCompose || readFile(t, filepath.Join(st.Dir, ".env")) != oldEnv {
		t.Errorf("the old files are not back")
	}
	if _, err := os.Stat(filepath.Join(st.Dir, "docker-compose.override.yml")); err != nil {
		t.Errorf("the override is not back")
	}
}

// inbound : served over HTTPS with a certificate of its address that the app pins, a
// certificate the folder had saved and put back ; the old project's name taken : another
func TestPrepareMigrationInbound(t *testing.T) {
	st, m := runner72(t)
	st.Name, m.OldContainers = "rte", []string{"rte_rte"}
	m.Mode, m.Host, m.Port = "inbound", "10.0.0.5", 8443
	put(t, st.Dir, map[string]string{"data/certificates/cert.pem": "the app's own certificate\n"})
	undo, res, next, err := prepareMigration(context.Background(), st, "7.3.0", m)
	if err != nil {
		t.Fatal(err)
	}
	env := envLines(readFile(t, filepath.Join(st.Dir, ".env")))
	if env["RTE_MODE"] != "inbound" || env["RTE_URL"] != "https://10.0.0.5:8443" || env["HTTPS"] != "1" || env["RTE_LOCAL_PORT"] != "8443" || env["RTE_NAME"] != "rte72.example.com" {
		t.Errorf(".env %v", env)
	}
	if res.CertFingerprint == "" || res.URL != "https://10.0.0.5:8443" {
		t.Errorf("result %+v", res)
	}
	if next.Project != "rte-socket" || undo.NewProject != "rte-socket" || !next.HTTPS {
		t.Errorf("next %+v", next)
	}
	if err := restoreFiles(undo.Files); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(st.Dir, "data/certificates/cert.pem")); got != "the app's own certificate\n" {
		t.Errorf("certificate not put back: %q", got)
	}
	if _, err := os.Stat(filepath.Join(st.Dir, "data/certificates/key.pem")); !os.IsNotExist(err) {
		t.Errorf("a key the folder did not have is left by the rollback")
	}
}

// a join token : a new runner under its own name, a new token, the join token presented once
func TestPrepareMigrationJoin(t *testing.T) {
	st, m := runner72(t)
	m.Token, m.JoinToken, m.RunnerName = "", "afj_0123456789", "rte72-socket"
	_, res, _, err := prepareMigration(context.Background(), st, "7.3.0", m)
	if err != nil {
		t.Fatal(err)
	}
	env := envLines(readFile(t, filepath.Join(st.Dir, ".env")))
	if env["RTE_JOIN_TOKEN"] != "afj_0123456789" || env["RTE_NAME"] != "rte72-socket" || env["RTE_TOKEN"] == token72 || len(env["RTE_TOKEN"]) < 16 {
		t.Errorf(".env %v", env)
	}
	if res.Token != env["RTE_TOKEN"] {
		t.Errorf("the result's token is not the .env's")
	}
}

// the old files are copied to the backups before anything changes ; a file the folder did not
// have is removed again by a rollback
func TestSaveAndRestoreFiles(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, map[string]string{"compose.yaml": "old\n"})
	paths := filesToSave(dir, filepath.Join(dir, "compose.yaml"), false)
	if len(paths) != len(composeNames)+1 {
		t.Fatalf("paths %v", paths)
	}
	saved, err := saveFiles(filepath.Join(dir, "installer-backups"), paths, "7.2.0")
	if err != nil {
		t.Fatal(err)
	}
	put(t, dir, map[string]string{"docker-compose.yml": "new\n", ".env": "new\n"})
	if err := os.Remove(filepath.Join(dir, "compose.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := restoreFiles(saved); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(dir, "compose.yaml")) != "old\n" {
		t.Errorf("compose.yaml not back")
	}
	for _, gone := range []string{"docker-compose.yml", ".env"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s left by the rollback", gone)
		}
	}
}

// the old RTE's settings carried in a stable order, under a comment ; none : unchanged
func TestCarriedEnv(t *testing.T) {
	if got := carriedEnv("A=1\n", nil); got != "A=1\n" {
		t.Errorf("no settings : %q", got)
	}
	got := carriedEnv("A=1", map[string]string{"TZ": "UTC", "RTE_MAX_JOBS": "4"})
	if !strings.HasSuffix(got, "# carried over from the RTE of 7.2 (database mode)\nRTE_MAX_JOBS=4\nTZ=UTC\n") || !strings.HasPrefix(got, "A=1\n") {
		t.Errorf("carried : %q", got)
	}
}

// the new project beside the old one, never the old one itself
func TestNewProjectName(t *testing.T) {
	if NewProjectName("af", "rte") != "af" || NewProjectName("rte", "rte") != "rte-socket" {
		t.Errorf("NewProjectName")
	}
}

// what the new RTE's log says of its connection (rte/socket/rte-socket.js)
func TestConnection(t *testing.T) {
	cases := []struct{ log, want string }{
		{"", ""},
		{"2026-10-18 notice: RTE 'rte72': dialing the app at wss://forms.example.com/api/v2/rte/socket\n", ""},
		{"notice: RTE 'rte72': dialing the app\nnotice: RTE 'rte72': connected to the app (node af-forms-443)\n", connConnected},
		// an unknown name : it asked to join and waits
		{"notice: RTE 'rte72': connected to the app (node af-1)\nwarning: RTE 'rte72': waiting for an admin to approve it under Connections > Runners (fingerprint ab12-cd34)\n", connPending},
		{"warning: RTE 'rte72': asked to join as inbound runner 'rte72': the app dials it once an admin approves it\n", connPending},
		// refused : the seed, the token
		{"warning: RTE 'rte72': the app refused the socket (409): {\"error\":\"runner 'rte72' is managed by the config seed in database mode: set rte_mode: outbound for it in the seed\"}\n", connRefused},
		{"error: RTE 'rte72': the app refused the registration (401): the token of RTE 'rte72' is not the one stored for its runner\r\n", connRefused},
	}
	for _, c := range cases {
		got, line := Connection(c.log)
		if got != c.want {
			t.Errorf("Connection(%q) = %q, want %q", c.log, got, c.want)
		}
		if got == connRefused && !strings.Contains(line, "refused") {
			t.Errorf("the refusal's line is not said: %q", line)
		}
		if strings.Contains(line, token72) {
			t.Errorf("a token in what is said")
		}
	}
}

// a rollback after the new RTE connected : refused, the app moved the runner already
func TestRollbackAfterConnected(t *testing.T) {
	err := RollbackMigration(context.Background(), &state.State{Runtime: "docker"}, &MigrationUndo{Connected: true, RunnerName: "rte72"})
	if err == nil || !strings.Contains(err.Error(), "rte72") {
		t.Fatalf("err = %v", err)
	}
}

// the old containers by the names they have now : renamed aside or not
func TestUndoCurrent(t *testing.T) {
	u := &MigrationUndo{OldContainers: []string{"af_rte", "af_other"}, Renamed: map[string]string{"af_rte-old": "af_rte"}}
	if got := strings.Join(u.current(), ","); got != "af_rte-old,af_other" {
		t.Errorf("current = %s", got)
	}
}
