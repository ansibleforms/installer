package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ansibleforms/installer/internal/state"
)

// ─── a fake database ──────────────────────────────────────────────────────────

// fakeMySQL is a database whose answers come from fixtures: the query of the users on
// mysql_native_password answers users (an error when it is "error"), the query of every
// account answers all, SHOW CREATE USER and SHOW GRANTS answer shown, a replay answers
// replayOut (replayErr its error), every statement is kept.
type fakeMySQL struct {
	users     string
	all       string
	shown     string
	replayOut string
	replayErr error
	ran       []string
	replayed  []string
}

// use puts the fake in place of mysqlRun for a test.
func (f *fakeMySQL) use(t *testing.T) {
	t.Helper()
	was := mysqlRun
	mysqlRun = func(ctx context.Context, st *state.State, sql string) (string, error) {
		f.ran = append(f.ran, sql)
		if sql == nativeUsersSQL {
			if f.users == "error" {
				return "", errors.New("mysql: cannot connect")
			}
			return f.users, nil
		}
		if sql == allUsersSQL {
			return f.all, nil
		}
		if strings.HasPrefix(sql, sessionSQL+hexSQL) {
			return f.shown, nil
		}
		return "", nil
	}
	wasReplay := mysqlReplay
	mysqlReplay = func(ctx context.Context, st *state.State, sql string) (string, error) {
		f.replayed = append(f.replayed, sql)
		return f.replayOut, f.replayErr
	}
	t.Cleanup(func() { mysqlRun, mysqlReplay = was, wasReplay })
}

// alters are the statements the fake ran besides the query of the users.
func (f *fakeMySQL) alters() []string {
	var out []string
	for _, sql := range f.ran {
		if sql != nativeUsersSQL {
			out = append(out, sql)
		}
	}
	return out
}

// the docker repository's my.cnf up to MySQL 8.4
const myCnf84 = "[mysqld]\nmysql_native_password=ON"

// the installation's users on mysql_native_password: root and the app's user, whose
// passwords the .env holds (the fixture's report\tbot is left out)
const knownUsersOut = "ansibleforms\t%\nroot\t%\nroot\tlocalhost\n"

// mysqlFixture is a 7.2 installation on MySQL 8.4 (upgradeFixture) and a docker repository
// whose compose file runs the given MySQL image, with the docker repository's my.cnf of 8.4.
func mysqlFixture(t *testing.T, image string) *state.State {
	t.Helper()
	st := upgradeFixture(t, read(t, "seed-7.2.yaml"))
	compose := read(t, "compose-7.3.yml")
	if err := os.WriteFile(filepath.Join(Source, "docker-compose.yml"), []byte(strings.Replace(compose, "mysql:8.4.11", image, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.Dir, "docker-compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(st.Dir, "data", "mysql"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.Dir, filepath.FromSlash(myCnfPath)), []byte(myCnf84), 0o644); err != nil {
		t.Fatal(err)
	}
	st.Runtime = "docker"
	return st
}

// snapshot is every file of a folder and its content, to tell that nothing changed.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		files[p] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// ─── which MySQL a compose file runs ──────────────────────────────────────────

// the move is made for a higher MySQL line without mysql_native_password, never for a new
// patch version of the same line, a line that still has it, or an image without a version
func TestNativeRemoved(t *testing.T) {
	cases := []struct {
		from, to string
		want     bool
	}{
		{"mysql:8.4", "mysql:9.7", true},
		{"mysql:8.4.11", "mysql:9.7.4", true},
		{"mysql:8", "mysql:9.7.4", true},
		{"docker.io/library/mysql:8.4.11", "mysql:9.7.4-oracle", true},
		{"registry.local:5000/mysql:8.4.11@sha256:abc", "registry.local:5000/mysql:9.7.4", true},
		{"mysql:9.7", "mysql:9.7.5", false},
		{"mysql:9.7.4", "mysql:9.7.4", false},
		{"mysql:8.4.11", "mysql:8.4.13", false},
		{"mysql:8.0", "mysql:8.4", false},
		{"mysql:9.7.4", "mysql:8.4.11", false},
		{"mysql:latest", "mysql:9.7.4", false},
		{"mysql", "mysql:9.7.4", false},
		{"mysql:8.4.11", "mysql:${MYSQL_VERSION}", false},
		{"mariadb:10.11", "mysql:9.7.4", false},
		{"", "mysql:9.7.4", false},
	}
	for _, c := range cases {
		if got := nativeRemoved(c.from, c.to); got != c.want {
			t.Errorf("%s -> %s: %t, want %t", c.from, c.to, got, c.want)
		}
	}
}

// the image is the mysqldb service's, whatever the other services run
func TestMySQLImage(t *testing.T) {
	if got := mysqlImage(read(t, "compose-7.3.yml")); got != "mysql:8.4.11" {
		t.Errorf("image %q", got)
	}
	if got := mysqlImage("services:\n  app:\n    image: x:1\n"); got != "" {
		t.Errorf("no mysqldb: %q", got)
	}
	if got := mysqlVersion("docker.io/library/mysql:9.7.4@sha256:abc"); got != "9.7.4" {
		t.Errorf("version %q", got)
	}
}

// the data folder is the mysqldb service's bind mount on /var/lib/mysql
func TestMySQLDataDir(t *testing.T) {
	if got := mysqlDataDir(read(t, "compose-7.3.yml"), "/srv/af"); got != "/srv/af/data/mysql/db" {
		t.Errorf("data %q", got)
	}
	long := "services:\n  mysqldb:\n    volumes:\n      - type: bind\n        source: /data/db\n        target: /var/lib/mysql/\n"
	if got := mysqlDataDir(long, "/srv/af"); got != "/data/db" {
		t.Errorf("long syntax %q", got)
	}
	if got := mysqlDataDir("services:\n  mysqldb:\n    volumes:\n      - dbdata:/var/lib/mysql\n", "/srv/af"); got != "" {
		t.Errorf("named volume %q", got)
	}
}

// ─── the my.cnf ───────────────────────────────────────────────────────────────

// the line goes in any case and spacing, written with _ or -, every other line kept as it is
func TestDropNativePassword(t *testing.T) {
	cases := []struct {
		name, in, want string
		dropped        bool
	}{
		{"docker repository", "[mysqld]\nmysql_native_password=ON", "[mysqld]\n", true},
		{"spaces and case", "[mysqld]\n  MySQL_Native_Password = on\nmax_connections=200\n", "[mysqld]\nmax_connections=200\n", true},
		{"dashes", "[mysqld]\nmysql-native-password=1\n", "[mysqld]\n", true},
		{"no value", "[mysqld]\nmysql_native_password\nport=3306\n", "[mysqld]\nport=3306\n", true},
		{"windows line ends", "[mysqld]\r\nmysql_native_password=ON\r\nport=3306\r\n", "[mysqld]\r\nport=3306\r\n", true},
		{"loose kept", "[mysqld]\nloose-mysql_native_password=ON\n", "[mysqld]\nloose-mysql_native_password=ON\n", false},
		{"comment kept", "[mysqld]\n# mysql_native_password=ON\n", "[mysqld]\n# mysql_native_password=ON\n", false},
		{"none", "[mysqld]\nport=3306\n", "[mysqld]\nport=3306\n", false},
	}
	for _, c := range cases {
		got, dropped := dropNativePassword(c.in)
		if got != c.want || dropped != c.dropped {
			t.Errorf("%s: %q (%t), want %q (%t)", c.name, got, dropped, c.want, c.dropped)
		}
	}
}

// the old my.cnf is saved beside it and kept for the rollback, which puts it back
func TestDropMyCnfLineAndRestore(t *testing.T) {
	st := mysqlFixture(t, "mysql:9.7.4")
	path := filepath.Join(st.Dir, filepath.FromSlash(myCnfPath))
	undo := &Undo{Version: st.Version}
	if err := dropMyCnfLine(st, undo); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "[mysqld]\n" {
		t.Errorf("my.cnf %q", data)
	}
	if bak, _ := os.ReadFile(path + ".7.2.0.bak"); string(bak) != myCnf84 || undo.MyCnf != myCnf84 {
		t.Errorf("old my.cnf not kept: %q", bak)
	}
	if err := restorePrep(st, undo); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != myCnf84 {
		t.Errorf("rollback did not put my.cnf back: %q", data)
	}
	// without the line, or without a my.cnf: nothing
	again := &Undo{}
	if err := os.WriteFile(path, []byte("[mysqld]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := dropMyCnfLine(st, again); err != nil || again.MyCnf != "" {
		t.Errorf("no line: %v, %q", err, again.MyCnf)
	}
	os.Remove(path)
	if err := dropMyCnfLine(st, again); err != nil {
		t.Errorf("no my.cnf: %v", err)
	}
}

// ─── the users ────────────────────────────────────────────────────────────────

// mysql's batch output is read back with its escapes (a tab in a user name)
func TestParseUsers(t *testing.T) {
	users, err := parseUsers(read(t, "mysql-native-users.txt"))
	if err != nil {
		t.Fatal(err)
	}
	want := []MySQLUser{{"ansibleforms", "%"}, {"report\tbot", "10.0.0.%"}, {"root", "%"}, {"root", "localhost"}}
	if len(users) != len(want) {
		t.Fatalf("users %v", users)
	}
	for i := range want {
		if users[i] != want[i] {
			t.Errorf("user %d: %q, want %q", i, users[i], want[i])
		}
	}
	if users, err := parseUsers(""); err != nil || len(users) != 0 {
		t.Errorf("no users: %v, %v", users, err)
	}
	if _, err := parseUsers("no host here\n"); err == nil {
		t.Errorf("a line without a host was read")
	}
}

// names and passwords are quoted so that no quote or backslash ends the SQL string
func TestSQLString(t *testing.T) {
	cases := map[string]string{
		"plain":       `'plain'`,
		`it's`:        `'it\'s'`,
		`back\slash`:  `'back\\slash'`,
		`\'; DROP --`: `'\\\'; DROP --'`,
		"line\nend":   `'line\nend'`,
	}
	for in, want := range cases {
		if got := sqlString(in); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
	if got := (MySQLUser{User: "o'neil", Host: "%"}).String(); got != `'o\'neil'@'%'` {
		t.Errorf("user %s", got)
	}
}

// root and the app's user are known from the .env (an older one connects the app as root)
func TestKnownPasswords(t *testing.T) {
	known := knownPasswords(read(t, "env-7.2-install.env"))
	if len(known) != 2 || known["root"] != "Root!Secret!Of!Mine" || known["ansibleforms"] != "App!Secret!Of!Mine" {
		t.Errorf("known %v", known)
	}
	asRoot := knownPasswords("MYSQLDB_ROOT_PASSWORD=\"quoted pw\"\nMYSQLDB_USER=root\nMYSQLDB_PASSWORD=x\n")
	if len(asRoot) != 1 || asRoot["root"] != "quoted pw" {
		t.Errorf("app as root %v", asRoot)
	}
	moved, unknown := splitUsers([]MySQLUser{{"root", "%"}, {"ansibleforms", "%"}, {"report", "%"}}, known)
	if len(moved) != 2 || len(unknown) != 1 || unknown[0].User != "report" {
		t.Errorf("moved %v, unknown %v", moved, unknown)
	}
}

// a password never shows in an error mysql gives back
func TestHideSecrets(t *testing.T) {
	known := map[string]string{"root": "it's-secret", "ansibleforms": "App!Secret"}
	err := hideSecrets(errors.New(`ERROR 1064: near 'it\'s-secret' and App!Secret`), known)
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "App!Secret") {
		t.Errorf("error %v", err)
	}
}

// ─── the upgrade ──────────────────────────────────────────────────────────────

// 8.4 to 9.7: the review says so, root and the app's user move to caching_sha2_password with
// their passwords from the .env (on mysql's standard input), and my.cnf loses the line
func TestMoveMySQLKnownUsers(t *testing.T) {
	st := mysqlFixture(t, "mysql:9.7.4")
	db := &fakeMySQL{users: knownUsersOut}
	db.use(t)
	p, err := Prepare(t.Context(), st, "7.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if p.MySQLFrom != "8.4.11" || p.MySQLTo != "9.7.4" || p.MySQLBlocked != "" {
		t.Fatalf("prep: %q -> %q, blocked %q", p.MySQLFrom, p.MySQLTo, p.MySQLBlocked)
	}
	if err := p.Check(); err != nil {
		t.Fatal(err)
	}
	review := strings.Join(p.Describe("7.3.0"), "\n")
	if !strings.Contains(review, "8.4.11") || !strings.Contains(review, "9.7.4") || strings.Contains(review, "Secret") {
		t.Errorf("review: %s", review)
	}
	if len(db.alters()) != 0 {
		t.Fatalf("Prepare changed the database: %v", db.alters())
	}
	undo := &Undo{Version: st.Version}
	if err := moveMySQL(t.Context(), st, p, undo); err != nil {
		t.Fatal(err)
	}
	alters := db.alters()
	if len(alters) != 1 {
		t.Fatalf("statements %v", alters)
	}
	want := sessionSQL +
		"ALTER USER 'ansibleforms'@'%' IDENTIFIED WITH caching_sha2_password BY 'App!Secret!Of!Mine';\n" +
		"ALTER USER 'root'@'%' IDENTIFIED WITH caching_sha2_password BY 'Root!Secret!Of!Mine';\n" +
		"ALTER USER 'root'@'localhost' IDENTIFIED WITH caching_sha2_password BY 'Root!Secret!Of!Mine';\n"
	if alters[0] != want {
		t.Errorf("SQL:\n%s\nwant:\n%s", alters[0], want)
	}
	if data, _ := os.ReadFile(filepath.Join(st.Dir, filepath.FromSlash(myCnfPath))); string(data) != "[mysqld]\n" || undo.MyCnf != myCnf84 {
		t.Errorf("my.cnf %q", data)
	}
}

// no user left on mysql_native_password: no statement, only my.cnf
func TestMoveMySQLNoUsers(t *testing.T) {
	st := mysqlFixture(t, "mysql:9.7.4")
	db := &fakeMySQL{users: ""}
	db.use(t)
	if err := moveMySQL(t.Context(), st, &Prep{MySQLFrom: "8.4.11", MySQLTo: "9.7.4"}, &Undo{Version: st.Version}); err != nil {
		t.Fatal(err)
	}
	if len(db.alters()) != 0 {
		t.Errorf("statements %v", db.alters())
	}
	if data, _ := os.ReadFile(filepath.Join(st.Dir, filepath.FromSlash(myCnfPath))); string(data) != "[mysqld]\n" {
		t.Errorf("my.cnf %q", data)
	}
}

// a user whose password the installer does not know stops the upgrade before anything
// changes, saying who and how to move it by hand; so does one added after the review
func TestMoveMySQLUnknownUsers(t *testing.T) {
	st := mysqlFixture(t, "mysql:9.7.4")
	db := &fakeMySQL{users: read(t, "mysql-native-users.txt")}
	db.use(t)
	before := snapshot(t, st.Dir)
	p, err := Prepare(t.Context(), st, "7.3.0")
	if err != nil {
		t.Fatal(err)
	}
	err = p.Check()
	if err == nil {
		t.Fatal("the upgrade was not stopped")
	}
	for _, s := range []string{
		`'report\tbot'@'10.0.0.%'`,
		`ALTER USER 'report\tbot'@'10.0.0.%' IDENTIFIED WITH caching_sha2_password BY '<password>';`,
		"docker compose -p docker exec mysqldb mysql -uroot -p",
		"9.7.4",
	} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("message lacks %s:\n%s", s, err)
		}
	}
	if strings.Contains(err.Error(), "'root'@") || strings.Contains(err.Error(), "Secret") {
		t.Errorf("message names a known user or a password:\n%s", err)
	}
	// the review stopped it, and asked again right before the backup and before the users
	// move, the database stops it too
	if err := checkMySQL(t.Context(), st, &Prep{MySQLFrom: p.MySQLFrom, MySQLTo: p.MySQLTo}); err == nil {
		t.Error("checkMySQL went on")
	}
	if err := moveMySQL(t.Context(), st, &Prep{MySQLFrom: p.MySQLFrom, MySQLTo: p.MySQLTo}, &Undo{Version: st.Version}); err == nil {
		t.Error("moveMySQL went on")
	}
	if len(db.alters()) != 0 {
		t.Errorf("the database was changed: %v", db.alters())
	}
	after := snapshot(t, st.Dir)
	if len(after) != len(before) {
		t.Errorf("files added or removed: %d before, %d after", len(before), len(after))
	}
	for p, content := range before {
		if after[p] != content {
			t.Errorf("%s changed", p)
		}
	}
}

// a new patch version of the same MySQL line asks nothing of the database
func TestPrepareSameMySQLLine(t *testing.T) {
	for _, image := range []string{"mysql:8.4.13", "mysql:8.4.11"} {
		st := mysqlFixture(t, image)
		db := &fakeMySQL{users: "error"}
		db.use(t)
		p, err := Prepare(t.Context(), st, "7.3.0")
		if err != nil {
			t.Fatal(err)
		}
		if p.MySQLTo != "" || len(db.ran) != 0 {
			t.Errorf("%s: move %q, statements %v", image, p.MySQLTo, db.ran)
		}
	}
}

// the database cannot be asked: the upgrade stops before anything changes
func TestPrepareDatabaseDown(t *testing.T) {
	st := mysqlFixture(t, "mysql:9.7.4")
	(&fakeMySQL{users: "error"}).use(t)
	if _, err := Prepare(t.Context(), st, "7.3.0"); err == nil {
		t.Error("Prepare went on without the database's users")
	}
}

// the review says the users move only when some do, and the my.cnf line only when it is there
func TestDescribeMySQLMove(t *testing.T) {
	st := mysqlFixture(t, "mysql:9.7.4")
	(&fakeMySQL{users: knownUsersOut}).use(t)
	p, err := Prepare(t.Context(), st, "7.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.MySQLUsers) != 3 || p.MySQLUsers[0] != "'ansibleforms'@'%'" || !p.MySQLCnf {
		t.Fatalf("users %v, my.cnf %t", p.MySQLUsers, p.MySQLCnf)
	}
	review := strings.Join(p.Describe("7.3.0"), "\n")
	for _, s := range []string{"MySQL 8.4.11 to 9.7.4", "upgrades its data in place", "'ansibleforms'@'%', 'root'@'%'", "caching_sha2_password", "data/mysql/my.cnf"} {
		if !strings.Contains(review, s) {
			t.Errorf("review lacks %q:\n%s", s, review)
		}
	}

	// a default 8.4 install: no user on mysql_native_password, no line in my.cnf
	st = mysqlFixture(t, "mysql:9.7.4")
	if err := os.WriteFile(filepath.Join(st.Dir, filepath.FromSlash(myCnfPath)), []byte("[mysqld]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	(&fakeMySQL{users: ""}).use(t)
	if p, err = Prepare(t.Context(), st, "7.3.0"); err != nil {
		t.Fatal(err)
	}
	if len(p.MySQLUsers) != 0 || p.MySQLCnf {
		t.Fatalf("users %v, my.cnf %t", p.MySQLUsers, p.MySQLCnf)
	}
	review = strings.Join(p.Describe("7.3.0"), "\n")
	if !strings.Contains(review, "MySQL 8.4.11 to 9.7.4") || !strings.Contains(review, "upgrades its data in place") {
		t.Errorf("review: %s", review)
	}
	for _, s := range []string{"users", "caching_sha2_password", "my.cnf"} {
		if strings.Contains(review, s) {
			t.Errorf("review says %q without a user or a line to move:\n%s", s, review)
		}
	}

	// users but no line, and the other way round
	only := (&Prep{MySQLFrom: "8.4.11", MySQLTo: "9.7.4", MySQLCnf: true}).Describe("7.3.0")
	if len(only) != 1 || strings.Contains(only[0], "caching_sha2_password") || !strings.Contains(only[0], "my.cnf") {
		t.Errorf("line only: %q", only)
	}
	only = (&Prep{MySQLFrom: "8.4.11", MySQLTo: "9.7.4", MySQLUsers: []string{"'root'@'%'"}}).Describe("7.3.0")
	if len(only) != 1 || !strings.Contains(only[0], "'root'@'%'") || strings.Contains(only[0], "my.cnf") {
		t.Errorf("users only: %q", only)
	}
}

// ─── the other users, saved for a rollback ────────────────────────────────────

// root, the app's user and MySQL's own accounts are made again by the old MySQL: not saved
func TestOtherUsers(t *testing.T) {
	all, err := parseUsers(read(t, "mysql-all-users.txt"))
	if err != nil {
		t.Fatal(err)
	}
	all = append(all, MySQLUser{"mysql.custom", "%"}, MySQLUser{"ansibleforms", "localhost"}, MySQLUser{"", "localhost"})
	got := otherUsers(all, read(t, "env-7.2-install.env"))
	want := []MySQLUser{{"readers", "%"}, {"report", "%"}, {"", "localhost"}}
	if len(got) != len(want) {
		t.Fatalf("saved %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("user %d: %q, want %q", i, got[i], want[i])
		}
	}
	// an older installation connects the app as root: every other account is saved
	if got := otherUsers([]MySQLUser{{"root", "%"}, {"ansibleforms", "%"}}, "MYSQLDB_ROOT_PASSWORD=x\n"); len(got) != 1 || got[0].User != "ansibleforms" {
		t.Errorf("app as root: %v", got)
	}
}

// each account is shown by SHOW CREATE USER and SHOW GRANTS, its hash as a hex literal
func TestExportUsersSQL(t *testing.T) {
	got := exportUsersSQL([]MySQLUser{{"report", "%"}, {"o'neil", "10.0.0.%"}})
	want := sessionSQL + "SET SESSION print_identified_with_as_hex = ON;\n" +
		"SHOW CREATE USER 'report'@'%';\nSHOW GRANTS FOR 'report'@'%';\n" +
		"SHOW CREATE USER 'o\\'neil'@'10.0.0.%';\nSHOW GRANTS FOR 'o\\'neil'@'10.0.0.%';\n"
	if got != want {
		t.Errorf("SQL:\n%s\nwant:\n%s", got, want)
	}
}

// the saved SQL makes every account first (IF NOT EXISTS: it can be run again), then grants,
// mysql's batch escapes read back, and ends with FLUSH PRIVILEGES
func TestUsersScript(t *testing.T) {
	got, err := usersScript(read(t, "mysql-show-users.txt"), "users")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 11 || lines[0] != "-- users" || lines[1]+"\n" != sessionSQL || lines[10] != "FLUSH PRIVILEGES;" {
		t.Fatalf("script:\n%s", got)
	}
	if !strings.HasPrefix(lines[2], "CREATE USER IF NOT EXISTS `readers`@`%` IDENTIFIED WITH 'caching_sha2_password' REQUIRE NONE") {
		t.Errorf("role: %s", lines[2])
	}
	report := lines[3]
	for _, s := range []string{
		"CREATE USER IF NOT EXISTS `report`@`%` IDENTIFIED WITH 'caching_sha2_password' AS 0x2441243030352",
		`ATTRIBUTE '{"comment": "multi\\nline; it\'s"}';`,
	} {
		if !strings.Contains(report, s) {
			t.Errorf("account lacks %s:\n%s", s, report)
		}
	}
	for i, want := range []string{
		"GRANT USAGE ON *.* TO `readers`@`%`;",
		"GRANT SELECT ON `reports`.* TO `readers`@`%`;",
		"GRANT USAGE ON *.* TO `report`@`%`;",
		"GRANT SELECT ON `AnsibleForms`.* TO `report`@`%`;",
		"GRANT INSERT ON `reports`.`t` TO `report`@`%`;",
		"GRANT `readers`@`%` TO `report`@`%`;",
	} {
		if lines[4+i] != want {
			t.Errorf("line %d: %s, want %s", 4+i, lines[4+i], want)
		}
	}
	if _, err := usersScript("+----+\n", ""); err == nil {
		t.Error("a line that is no statement was taken")
	}
	if got, err := usersScript("", ""); err != nil || got != sessionSQL+"FLUSH PRIVILEGES;\n" {
		t.Errorf("no account: %q, %v", got, err)
	}
}

// the other accounts are saved next to the backup, readable by their owner only; none, no file
func TestSaveUsers(t *testing.T) {
	st := mysqlFixture(t, "mysql:9.7.4")
	db := &fakeMySQL{all: read(t, "mysql-all-users.txt"), shown: read(t, "mysql-show-users.txt")}
	db.use(t)
	dump := filepath.Join(st.Dir, "installer-backups", "ansibleforms-7.2.0-20261117-101500.sql")
	if err := os.MkdirAll(filepath.Dir(dump), 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := saveUsers(t.Context(), st, dump)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(st.Dir, "installer-backups", "ansibleforms-7.2.0-20261117-101500.users.sql") {
		t.Errorf("file %s", path)
	}
	if len(db.ran) != 2 || db.ran[0] != allUsersSQL || db.ran[1] != exportUsersSQL([]MySQLUser{{"readers", "%"}, {"report", "%"}}) {
		t.Errorf("statements %q", db.ran)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "CREATE USER IF NOT EXISTS `report`@`%`") || !strings.HasSuffix(string(data), "FLUSH PRIVILEGES;\n") {
		t.Errorf("file:\n%s", data)
	}

	// only root, the app's user and MySQL's own: nothing to save
	(&fakeMySQL{all: "ansibleforms\t%\nmysql.sys\tlocalhost\nroot\t%\n"}).use(t)
	if path, err := saveUsers(t.Context(), st, dump+".2"); err != nil || path != "" {
		t.Errorf("no other user: %q, %v", path, err)
	}
}

// the rollback replays the saved file as it is; mysql's errors make a warning with the file's
// path, never a failure
func TestReplayUsers(t *testing.T) {
	st := mysqlFixture(t, "mysql:9.7.4")
	file := filepath.Join(t.TempDir(), "ansibleforms-7.2.0-20261117-101500.users.sql")
	script, err := usersScript(read(t, "mysql-show-users.txt"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	db := &fakeMySQL{}
	db.use(t)
	if msg := replayUsers(t.Context(), st, file); msg != "" {
		t.Errorf("warning %s", msg)
	}
	if len(db.replayed) != 1 || db.replayed[0] != script {
		t.Errorf("replayed %q", db.replayed)
	}

	// a statement that fails (a grant on a table only the data moved aside has)
	db = &fakeMySQL{replayOut: "ERROR 1146 (42S02) at line 8: Table 'reports.t' doesn't exist\n"}
	db.use(t)
	msg := replayUsers(t.Context(), st, file)
	if !strings.Contains(msg, file) || !strings.Contains(msg, "ERROR 1146") {
		t.Errorf("warning %q", msg)
	}
	// the container fails, or the file is gone
	(&fakeMySQL{replayErr: errors.New("exec failed")}).use(t)
	if msg := replayUsers(t.Context(), st, file); !strings.Contains(msg, file) || !strings.Contains(msg, "exec failed") {
		t.Errorf("warning %q", msg)
	}
	if msg := replayUsers(t.Context(), st, file+".gone"); !strings.Contains(msg, file+".gone") {
		t.Errorf("warning %q", msg)
	}
}
