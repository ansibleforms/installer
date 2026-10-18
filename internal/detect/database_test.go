package detect

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── a Runner of 7.2 in database mode ─────────────────────────────────────────

// dbRunner is an RTE alone set up by hand from the docker repository of 7.2, in database mode:
// its compose file and .env in the folder, its container af_rte of the project rte.
func dbRunner(t *testing.T, env ...string) (*fake, string) {
	t.Helper()
	dir := t.TempDir()
	compose, err := os.ReadFile(filepath.Join("..", "compose", "testdata", "compose-7.2-rte.yml"))
	if err != nil {
		t.Fatal(err)
	}
	dotenv, err := os.ReadFile(filepath.Join("..", "compose", "testdata", "env-7.2-rte.env"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, map[string]string{"docker-compose.yml": string(compose), ".env": string(dotenv)})
	inspect := fixture(t, "docker-inspect-runner-7.2.json", "{{DIR}}", dir)
	// a variable changed : its line in the container's environment replaced
	for i := 0; i+1 < len(env); i += 2 {
		inspect = replaceEnv(inspect, env[i], env[i+1])
	}
	f := &fake{t: t, has: map[string]bool{"docker": true}, out: map[string]string{
		"docker ps -a -q --no-trunc": "fff666",
		"docker inspect":             inspect,
	}}
	return f, dir
}

// replaceEnv sets a variable of the fixture's environment ("" : removed).
func replaceEnv(inspect, key, value string) string {
	lines := strings.Split(inspect, "\n")
	out := lines[:0]
	found := false
	for _, l := range lines {
		if strings.Contains(l, `"`+key+`=`) {
			found = true
			if value == "" {
				continue
			}
			l = `        "` + key + `=` + value + `",`
		}
		out = append(out, l)
	}
	s := strings.Join(out, "\n")
	if !found && value != "" {
		s = strings.Replace(s, `"Env": [`, `"Env": [`+"\n        \""+key+"="+value+"\",", 1)
	}
	return s
}

// a 7.2 Runner in database mode is found, can be taken over, and says what its move needs
func TestFindDatabaseRunner(t *testing.T) {
	f, dir := dbRunner(t)
	got := find(context.Background(), f.env())
	if len(got) != 1 {
		t.Fatalf("found %+v, want 1", got)
	}
	i := got[0]
	if i.Role != RoleRunner || !i.Adoptable() || i.Database == nil {
		t.Fatalf("found %+v (problem %q)", i, i.Problem)
	}
	// the floating tag 7 says no version : a database-mode RTE is upgraded as a 7.2 one
	if i.Version != DatabaseModeVersion || i.Name != "af" || i.Project != "rte" || i.Port != 8000 {
		t.Errorf("found %+v", i)
	}
	d := i.Database
	// named after the host of RTE_URL, as the 7.2 RTE registered itself
	if d.RunnerName != "rte72.example.com" || !d.NameKnown || d.Container != "af_rte" {
		t.Errorf("database runner %+v", d)
	}
	if d.Token != "the-72-rte-token-0123456789" || d.URL != "http://rte72.example.com:8000" {
		t.Errorf("token or url not read")
	}
	if d.ComposeFile != filepath.Join(dir, "docker-compose.yml") {
		t.Errorf("compose file %s", d.ComposeFile)
	}
	// carried : what a socket-mode RTE still reads ; never the database, its key, the log level
	if d.Settings["RTE_MAX_JOBS"] != "4" || d.Settings["HTTPS_PROXY"] == "" || d.Settings["NO_PROXY"] != "forms.example.com" {
		t.Errorf("settings %v", d.Settings)
	}
	for _, k := range []string{"DB_HOST", "DB_USER", "DB_PASSWORD", "DB_PORT", "ENCRYPTION_SECRET", "RTE_TOKEN", "RTE_URL", "LOG_LEVEL", "PORT", "HTTPS", "AF_ROLE", "PATH"} {
		if _, ok := d.Settings[k]; ok {
			t.Errorf("%s carried", k)
		}
	}
	st := i.State()
	if !st.DatabaseMode || st.Project != "rte" || st.Name != "af" || st.Dir != dir {
		t.Errorf("state %+v", st)
	}
}

// without RTE_URL it registered under its node id ; RTE_REGISTER=0 : its name is not known
func TestDatabaseRunnerNames(t *testing.T) {
	f, _ := dbRunner(t, "RTE_URL", "")
	d := find(context.Background(), f.env())[0].Database
	if d == nil || d.RunnerName != "rte-3f2a9c1b7d4e-8000" || !d.NameKnown {
		t.Fatalf("database runner %+v", d)
	}
	f, _ = dbRunner(t, "RTE_REGISTER", "0")
	if d := find(context.Background(), f.env())[0].Database; d == nil || d.NameKnown {
		t.Fatalf("RTE_REGISTER=0 : %+v", d)
	}
	// RTE_NAME (read by 7.3) wins, and is known
	f, _ = dbRunner(t, "RTE_REGISTER", "0", "RTE_NAME", "dmz-1")
	if d := find(context.Background(), f.env())[0].Database; d == nil || d.RunnerName != "dmz-1" || !d.NameKnown {
		t.Fatalf("RTE_NAME : %+v", d)
	}
}

// a Runner that already runs in a socket mode, or that has no database settings, is not one
func TestNotDatabaseRunner(t *testing.T) {
	f, _ := dbRunner(t, "RTE_MODE", "outbound")
	if i := find(context.Background(), f.env())[0]; i.Database != nil || i.Problem != RunnerLayout {
		t.Fatalf("socket mode : %+v", i)
	}
	f, _ = dbRunner(t, "RTE_MODE", "database")
	if i := find(context.Background(), f.env())[0]; i.Database == nil || !i.Adoptable() {
		t.Fatalf("RTE_MODE=database : %+v", i)
	}
	f, _ = dbRunner(t, "DB_HOST", "", "DB_USER", "")
	if i := find(context.Background(), f.env())[0]; i.Database != nil {
		t.Fatalf("no database settings : %+v", i)
	}
}

// its compose file gone : it cannot be taken over, and says why
func TestDatabaseRunnerWithoutComposeFile(t *testing.T) {
	f, dir := dbRunner(t)
	if err := os.Remove(filepath.Join(dir, "docker-compose.yml")); err != nil {
		t.Fatal(err)
	}
	if i := find(context.Background(), f.env())[0]; i.Problem != NoComposeFile {
		t.Fatalf("found %+v, want %s", i, NoComposeFile)
	}
}

// the name a 7.2 RTE registered itself under (rte/register.js of 7.2)
func TestRegisteredName(t *testing.T) {
	cases := []struct{ name, url, host, port, want string }{
		{"", "http://rte:8000", "abc", "8000", "rte"},
		{"", "https://rte.example.com:8443/", "abc", "8000", "rte.example.com-8443"},
		{"", "http://10.0.0.5", "abc", "8000", "10.0.0.5"},
		{"", "", "abc123", "", "rte-abc123-8000"},
		{"", "", "abc123", "8010", "rte-abc123-8010"},
		{"", "not a url", "abc123", "", "rte-abc123-8000"},
		{" own ", "http://rte:8000", "abc", "8000", "own"},
	}
	for _, c := range cases {
		if got := registeredName(c.name, c.url, c.host, c.port); got != c.want {
			t.Errorf("registeredName(%q, %q, %q, %q) = %q, want %q", c.name, c.url, c.host, c.port, got, c.want)
		}
	}
	if !ValidRunnerName("rte-abc123-8000") || ValidRunnerName("bad name") || ValidRunnerName("") {
		t.Errorf("ValidRunnerName")
	}
}
