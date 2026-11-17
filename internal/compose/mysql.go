package compose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ansibleforms/installer/internal/i18n"
	"github.com/ansibleforms/installer/internal/state"
	"github.com/ansibleforms/installer/internal/system"
	"github.com/ansibleforms/installer/internal/ui"
)

// ─── the database moved to a MySQL without mysql_native_password ──────────────
//
// MySQL 9 removed the mysql_native_password plugin. A server whose my.cnf still turns it on
// (mysql_native_password=ON, as the docker repository's my.cnf did up to MySQL 8.4) refuses
// to start, and a user that still signs in with it can no longer sign in. MySQL 8.4 LTS to
// 9.7 LTS is a supported upgrade in place: the new server upgrades the data directory on its
// first start, so all an upgrade has to do is get the users and my.cnf ready first.
//
// When the new compose file runs a MySQL of a higher line than the current one, and that line
// no longer has the plugin, the upgrade does this while the current MySQL still runs:
//
//  1. asks MySQL which users sign in with mysql_native_password;
//  2. stops before anything changes when one of them is neither root nor the app's user (the
//     installer does not know its password), saying how to move it by hand;
//  3. after the backup, moves root and the app's user to caching_sha2_password with their
//     passwords from the .env (MySQL 8.4 has it too, so a rollback is not hurt by it);
//  4. takes the mysql_native_password line out of data/mysql/my.cnf, the old file kept for a
//     rollback.
//
// A rollback after the new MySQL started cannot give its data directory to the old MySQL (no
// MySQL starts on the data of a newer line): the directory is moved aside, the old MySQL starts
// on a new one and the backup is loaded into it. The backup holds the AnsibleForms schema only,
// and the old MySQL makes only root and the app's user again, so the upgrade also saves the other
// database users (SHOW CREATE USER, SHOW GRANTS) before the switch, next to the backup, and the
// rollback makes them again from that file.

// MySQLUser is a database account, as mysql.user lists it.
type MySQLUser struct {
	User string
	Host string
}

// String is the account as SQL writes it: 'user'@'host', each part quoted and escaped.
//
// Returns:
//
//	The account.
func (u MySQLUser) String() string {
	return sqlString(u.User) + "@" + sqlString(u.Host)
}

// the first MySQL major version without mysql_native_password
const nativeRemovedMajor = 9

// the query of the users that sign in with mysql_native_password, one per line (user, host)
const nativeUsersSQL = "SELECT user, host FROM mysql.user WHERE plugin='mysql_native_password' ORDER BY user, host;\n"

// the SQL session the installer's statements run in, backslashes read as escapes whatever
// the server's sql_mode says (sqlString escapes with them)
const sessionSQL = "SET SESSION sql_mode = REPLACE(@@SESSION.sql_mode, 'NO_BACKSLASH_ESCAPES', '');\n"

// mysql as root in the database's container, with the root password MySQL itself holds, the
// statements read from its standard input and the rows written tab-separated without headers
const mysqlScript = `MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot --batch --skip-column-names`

// myCnfPath is the database's my.cnf in an installation folder, as the docker repository has it.
const myCnfPath = "data/mysql/my.cnf"

// mysqlRun runs SQL as root in an installation's database, the statements on the standard
// input of mysql in its container (they may hold passwords: never on a command line). A test
// puts a fake in its place.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//	sql: the statements.
//
// Returns:
//
//	What mysql printed: the rows, tab-separated.
//
// Errors:
//
//	When the container or a statement fails.
var mysqlRun = func(ctx context.Context, st *state.State, sql string) (string, error) {
	return mysqlExec(ctx, st, mysqlScript, sql)
}

// mysql as mysqlScript runs it, every statement tried even after one that failed (--force), the
// errors written with the rows (mysql still ends without an error then: they are read from them)
const mysqlForceScript = mysqlScript + " --force 2>&1"

// mysqlReplay runs SQL as root in an installation's database as mysqlRun does, but goes on past
// a statement that fails: a statement that cannot be made again (a grant on a table only the
// data moved aside has) does not keep the next ones from it. A test puts a fake in its place.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//	sql: the statements.
//
// Returns:
//
//	What mysql printed: the rows and the errors (lines that start with ERROR).
//
// Errors:
//
//	When the container fails.
var mysqlReplay = func(ctx context.Context, st *state.State, sql string) (string, error) {
	return mysqlExec(ctx, st, mysqlForceScript, sql)
}

// mysqlExec runs a mysql command line in an installation's database container, the statements
// on its standard input.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//	script: the command line, run by sh in the container (mysqlScript, mysqlForceScript).
//	sql: the statements.
//
// Returns:
//
//	What it printed.
//
// Errors:
//
//	When the container or the command fails.
func mysqlExec(ctx context.Context, st *state.State, script, sql string) (string, error) {
	name, args := composeCmd(system.Runtime(st.Runtime), Project(st), "exec", "-T", "mysqldb", "sh", "-c", script)
	return system.Input(ctx, st.Dir, sql, name, args...)
}

// ─── which MySQL a compose file runs ──────────────────────────────────────────

// mysqlImage is the image the mysqldb service of a compose file runs.
//
// Args:
//
//	content: the compose file.
//
// Returns:
//
//	The image (mysql:8.4.11), "" when the file has no mysqldb service or cannot be read.
func mysqlImage(content string) string {
	var f struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(content), &f); err != nil {
		return ""
	}
	return strings.TrimSpace(f.Services["mysqldb"].Image)
}

// the start of a MySQL image's tag: its major and minor version (8.4 of 8.4.11-oracle)
var mysqlTagRe = regexp.MustCompile(`^(\d+)(?:\.(\d+))?`)

// mysqlLine is the MySQL line an image runs: its major and minor version (8.4, 9.7). The patch
// version does not count: 8.4.11 and 8.4.13 are the same line.
//
// Args:
//
//	image: the image, as a compose file names it (mysql:8.4.11, docker.io/library/mysql:9.7).
//
// Returns:
//
//	The major and minor version (minor 0 for a tag of the major alone, mysql:8), and false
//	when it is not a MySQL image or its tag names no version (latest, lts, a variable).
func mysqlLine(image string) (int, int, bool) {
	image, _, _ = strings.Cut(image, "@") // a digest says nothing of the version
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon <= slash {
		return 0, 0, false // no tag (a registry's port is before the last slash)
	}
	repo, tag := image[slash+1:colon], image[colon+1:]
	if !strings.Contains(strings.ToLower(repo), "mysql") {
		return 0, 0, false // mariadb or another database: not this move
	}
	m := mysqlTagRe.FindStringSubmatch(tag)
	if m == nil {
		return 0, 0, false
	}
	major, _ := strconv.Atoi(m[1])
	minor := 0
	if m[2] != "" {
		minor, _ = strconv.Atoi(m[2])
	}
	return major, minor, true
}

// nativeRemoved tells whether an upgrade from one MySQL image to another moves the database to
// a higher line that no longer has mysql_native_password (8.4.x to 9.7.x): its users and its
// my.cnf have to be made ready first. A new patch version of the same line (9.7 to 9.7.5,
// 8.4.11 to 8.4.13), a line that still has the plugin, or an image that names no version is
// not.
//
// Args:
//
//	from: the image the installation runs.
//	to: the image the new compose file runs.
//
// Returns:
//
//	True when the database has to be made ready.
func nativeRemoved(from, to string) bool {
	fMajor, fMinor, fok := mysqlLine(from)
	tMajor, tMinor, tok := mysqlLine(to)
	if !fok || !tok || tMajor < nativeRemovedMajor {
		return false
	}
	return tMajor > fMajor || (tMajor == fMajor && tMinor > fMinor)
}

// mysqlVersion is the version an image's tag names, for what the upgrade says (8.4.11).
//
// Args:
//
//	image: the image.
//
// Returns:
//
//	Its tag.
func mysqlVersion(image string) string {
	image, _, _ = strings.Cut(image, "@")
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		return image[i+1:]
	}
	return image
}

// ─── the users ────────────────────────────────────────────────────────────────

// sqlString quotes a text as an SQL string: in single quotes, its backslashes and quotes
// escaped (read so in the installer's session, sessionSQL), and the characters mysql's batch
// mode would read apart or a person would not see (a line end, a tab, a NUL) escaped too.
//
// Args:
//
//	s: the text.
//
// Returns:
//
//	The SQL string.
func sqlString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\x00", `\0`, "\n", `\n`, "\r", `\r`, "\t", `\t`, "\x1a", `\Z`)
	return "'" + r.Replace(s) + "'"
}

// unescapeBatch reads a field of mysql's batch output, where a tab, a line end, a NUL and a
// backslash are written escaped (\t, \n, \0, \\).
//
// Args:
//
//	s: the field as printed.
//
// Returns:
//
//	The field.
func unescapeBatch(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case '0':
			b.WriteByte(0)
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// parseUsers reads the users of the query nativeUsersSQL, as mysql prints them in batch mode:
// one line each, the user and the host separated by a tab.
//
// Args:
//
//	out: what mysql printed.
//
// Returns:
//
//	The users, in their order.
//
// Errors:
//
//	When a line is not a user and a host.
func parseUsers(out string) ([]MySQLUser, error) {
	var users []MySQLUser
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		user, host, ok := strings.Cut(line, "\t")
		if !ok || strings.Contains(host, "\t") {
			return nil, fmt.Errorf("unexpected line from mysql.user: %q", line)
		}
		users = append(users, MySQLUser{User: unescapeBatch(user), Host: unescapeBatch(host)})
	}
	return users, nil
}

// envValue is a value of the .env as compose reads it: a value in matching quotes without them.
//
// Args:
//
//	env: the .env.
//	key: the variable.
//
// Returns:
//
//	Its value, "" when it is not set.
func envValue(env, key string) string {
	v := GetEnv(env, key)
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}

// knownPasswords are the passwords of the database's users the .env holds: root's
// (MYSQLDB_ROOT_PASSWORD) and the app's user's (MYSQLDB_USER, MYSQLDB_PASSWORD; an older
// installation connects the app as root, without MYSQLDB_USER). A user without a password in
// the .env is not in it.
//
// Args:
//
//	env: the installation's .env.
//
// Returns:
//
//	The passwords, by user name.
func knownPasswords(env string) map[string]string {
	known := map[string]string{}
	if pw := envValue(env, "MYSQLDB_ROOT_PASSWORD"); pw != "" {
		known["root"] = pw
	}
	if user, pw := envValue(env, "MYSQLDB_USER"), envValue(env, "MYSQLDB_PASSWORD"); user != "" && user != "root" && pw != "" {
		known[user] = pw
	}
	return known
}

// splitUsers parts the users on mysql_native_password into those whose password the .env
// holds, moved by the upgrade, and the others, which stop it.
//
// Args:
//
//	users: the users on mysql_native_password.
//	known: the passwords the .env holds, by user name (knownPasswords).
//
// Returns:
//
//	The users moved, and the users unknown.
func splitUsers(users []MySQLUser, known map[string]string) ([]MySQLUser, []MySQLUser) {
	var moved, unknown []MySQLUser
	for _, u := range users {
		if _, ok := known[u.User]; ok {
			moved = append(moved, u)
		} else {
			unknown = append(unknown, u)
		}
	}
	return moved, unknown
}

// alterSQL is the SQL that moves users to caching_sha2_password, each with its password.
//
// Args:
//
//	users: the users.
//	known: their passwords, by user name.
//
// Returns:
//
//	The statements, the session's first.
func alterSQL(users []MySQLUser, known map[string]string) string {
	var b strings.Builder
	b.WriteString(sessionSQL)
	for _, u := range users {
		fmt.Fprintf(&b, "ALTER USER %s IDENTIFIED WITH caching_sha2_password BY %s;\n", u, sqlString(known[u.User]))
	}
	return b.String()
}

// hideSecrets takes passwords out of an error (mysql may quote a statement it did not take),
// as written and as the SQL escaped them.
//
// Args:
//
//	err: the error.
//	known: the passwords.
//
// Returns:
//
//	The error, each password replaced by ***.
func hideSecrets(err error, known map[string]string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, pw := range known {
		quoted := sqlString(pw)
		msg = strings.ReplaceAll(msg, quoted[1:len(quoted)-1], "***")
		msg = strings.ReplaceAll(msg, pw, "***")
	}
	return errors.New(msg)
}

// nativeUsers asks the installation's database which users sign in with mysql_native_password.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation (its database running).
//
// Returns:
//
//	The users.
//
// Errors:
//
//	When the database cannot be asked.
func nativeUsers(ctx context.Context, st *state.State) ([]MySQLUser, error) {
	out, err := mysqlRun(ctx, st, nativeUsersSQL)
	if err != nil {
		return nil, err
	}
	return parseUsers(out)
}

// mysqlExecHint is the command that opens mysql as root in the installation's database, for
// the person to move a user by hand.
//
// Args:
//
//	st: the installation.
//
// Returns:
//
//	The command line.
func mysqlExecHint(st *state.State) string {
	name, args := composeCmd(system.Runtime(st.Runtime), Project(st), "exec", "mysqldb", "mysql", "-uroot", "-p")
	return name + " " + strings.Join(args, " ")
}

// mysqlBlocked is the message that stops an upgrade on users whose password the installer does
// not know: who they are, and the statements that move them by hand.
//
// Args:
//
//	to: the MySQL version upgraded to.
//	users: the users.
//	exec: the command that opens mysql in the database (mysqlExecHint).
//
// Returns:
//
//	The message.
func mysqlBlocked(to string, users []MySQLUser, exec string) string {
	names := make([]string, len(users))
	commands := make([]string, len(users))
	for i, u := range users {
		names[i] = u.String()
		commands[i] = fmt.Sprintf("  ALTER USER %s IDENTIFIED WITH caching_sha2_password BY '%s';", u, i18n.T("compose.mysqlPasswordHolder"))
	}
	return i18n.T("compose.mysqlBlocked", "to", to, "users", strings.Join(names, ", "),
		"exec", "  "+exec, "commands", strings.Join(commands, "\n"))
}

// prepareMySQL works out whether an upgrade moves the database to a MySQL without
// mysql_native_password, and if so which of its users stop it. Nothing is changed.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation (its database running).
//	p: the upgrade's changes, given the move.
//	current: the installation's compose file.
//	next: the new version's compose file.
//	env: the installation's .env.
//
// Errors:
//
//	When the database cannot be asked.
func prepareMySQL(ctx context.Context, st *state.State, p *Prep, current, next, env string) error {
	from, to := mysqlImage(current), mysqlImage(next)
	if !nativeRemoved(from, to) {
		return nil
	}
	users, err := nativeUsers(ctx, st)
	if err != nil {
		return err
	}
	moved, unknown := splitUsers(users, knownPasswords(env))
	p.MySQLFrom, p.MySQLTo = mysqlVersion(from), mysqlVersion(to)
	p.MySQLUsers = nil
	for _, u := range moved {
		p.MySQLUsers = append(p.MySQLUsers, u.String())
	}
	if cnf, err := os.ReadFile(filepath.Join(st.Dir, filepath.FromSlash(myCnfPath))); err == nil {
		_, p.MySQLCnf = dropNativePassword(string(cnf))
	}
	if len(unknown) > 0 {
		p.MySQLBlocked = mysqlBlocked(p.MySQLTo, unknown, mysqlExecHint(st))
	}
	return nil
}

// dropNativePassword takes out of a my.cnf the lines that set mysql_native_password (any case,
// spaces around the =, written with _ or -, with or without a value): a MySQL without the
// plugin refuses to start on them. Every other line is kept as it is.
//
// Args:
//
//	content: the my.cnf.
//
// Returns:
//
//	The my.cnf without them, and whether a line was taken out.
func dropNativePassword(content string) (string, bool) {
	lines := strings.SplitAfter(content, "\n")
	kept := lines[:0]
	dropped := false
	for _, line := range lines {
		if nativeLineRe.MatchString(strings.TrimRight(line, "\r\n")) {
			dropped = true
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, ""), dropped
}

// a my.cnf line that sets mysql_native_password (loose-mysql_native_password is ignored by a
// MySQL without it, and kept)
var nativeLineRe = regexp.MustCompile(`(?i)^[ \t]*mysql[_-]native[_-]password[ \t]*(=.*)?$`)

// checkMySQL asks the database again which users are on mysql_native_password, right before an
// upgrade that moves it to a MySQL without it changes anything: one put on it since the review
// stops the upgrade.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//	p: the upgrade's changes (nothing to do unless p.MySQLTo is set).
//
// Errors:
//
//	When the database cannot be asked or a user is unknown.
func checkMySQL(ctx context.Context, st *state.State, p *Prep) error {
	if p == nil || p.MySQLTo == "" {
		return nil
	}
	envData, err := os.ReadFile(filepath.Join(st.Dir, ".env"))
	if err != nil {
		return err
	}
	users, err := nativeUsers(ctx, st)
	if err != nil {
		return err
	}
	if _, unknown := splitUsers(users, knownPasswords(string(envData))); len(unknown) > 0 {
		return errors.New(mysqlBlocked(p.MySQLTo, unknown, mysqlExecHint(st)))
	}
	return nil
}

// moveMySQL gets the database ready for a MySQL without mysql_native_password, after the backup
// and while the current MySQL still runs: its users asked again (a user added since the review
// stops the upgrade, nothing changed), root and the app's user moved to caching_sha2_password,
// and the mysql_native_password line taken out of data/mysql/my.cnf (the old file saved beside
// it and kept in undo).
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//	p: the upgrade's changes (nothing to do unless p.MySQLTo is set).
//	undo: where the old my.cnf is kept.
//
// Errors:
//
//	When the database cannot be asked or changed, a user is unknown, or my.cnf cannot be
//	written.
func moveMySQL(ctx context.Context, st *state.State, p *Prep, undo *Undo) error {
	if p == nil || p.MySQLTo == "" {
		return nil
	}
	envData, err := os.ReadFile(filepath.Join(st.Dir, ".env"))
	if err != nil {
		return err
	}
	known := knownPasswords(string(envData))
	users, err := nativeUsers(ctx, st)
	if err != nil {
		return err
	}
	moved, unknown := splitUsers(users, known)
	if len(unknown) > 0 {
		return errors.New(mysqlBlocked(p.MySQLTo, unknown, mysqlExecHint(st)))
	}
	if len(moved) > 0 {
		names := make([]string, len(moved))
		for i, u := range moved {
			names[i] = u.String()
		}
		ui.Step(i18n.T("compose.mysqlUsers", "users", strings.Join(names, ", ")))
		if _, err := mysqlRun(ctx, st, alterSQL(moved, known)); err != nil {
			return hideSecrets(err, known)
		}
	}
	return dropMyCnfLine(st, undo)
}

// dropMyCnfLine takes the mysql_native_password line out of the installation's my.cnf, the old
// file saved beside it (my.cnf.<version>.bak) and kept in undo for a rollback; nothing when it
// has no such line or no my.cnf.
//
// Args:
//
//	st: the installation.
//	undo: where the old my.cnf is kept.
//
// Errors:
//
//	When the file cannot be read or written.
func dropMyCnfLine(st *state.State, undo *Undo) error {
	path := filepath.Join(st.Dir, filepath.FromSlash(myCnfPath))
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	next, dropped := dropNativePassword(string(old))
	if !dropped {
		return nil
	}
	if err := os.WriteFile(path+"."+st.Version+".bak", old, info.Mode().Perm()); err != nil {
		return err
	}
	undo.MyCnf = string(old)
	ui.Step(i18n.T("compose.mysqlCnf"))
	return os.WriteFile(path, []byte(next), info.Mode().Perm())
}

// ─── the other users, saved for a rollback ────────────────────────────────────

// the query of every database account, one per line (user, host)
const allUsersSQL = "SELECT user, host FROM mysql.user ORDER BY user, host;\n"

// the session in which SHOW CREATE USER writes a password hash as a hex literal (MySQL 8.0.17 and
// later): a caching_sha2_password hash holds bytes a string literal would not carry through
// mysql's output and back
const hexSQL = "SET SESSION print_identified_with_as_hex = ON;\n"

// usersSuffix ends the name of the file of the other users, next to the backup it goes with
// (ansibleforms-7.2.0-20261117-101500.users.sql beside ansibleforms-7.2.0-20261117-101500.sql).
const usersSuffix = ".users.sql"

// otherUsers are the database accounts the old MySQL would not make again on a new data folder:
// all but root and the app's user (MYSQLDB_USER), which its entrypoint makes from the .env, and
// MySQL's own accounts (mysql.sys, mysql.session, mysql.infoschema, any mysql.%), which it makes
// itself.
//
// Args:
//
//	users: every account of the database.
//	env: the installation's .env.
//
// Returns:
//
//	The accounts to save, in their order.
func otherUsers(users []MySQLUser, env string) []MySQLUser {
	app := envValue(env, "MYSQLDB_USER")
	var out []MySQLUser
	for _, u := range users {
		if u.User == "root" || (app != "" && u.User == app) || strings.HasPrefix(u.User, "mysql.") {
			continue
		}
		out = append(out, u)
	}
	return out
}

// exportUsersSQL is the SQL that prints how to make accounts again: SHOW CREATE USER and SHOW
// GRANTS for each, the password hashes as hex literals.
//
// Args:
//
//	users: the accounts.
//
// Returns:
//
//	The statements, the session's first.
func exportUsersSQL(users []MySQLUser) string {
	var b strings.Builder
	b.WriteString(sessionSQL)
	b.WriteString(hexSQL)
	for _, u := range users {
		fmt.Fprintf(&b, "SHOW CREATE USER %s;\nSHOW GRANTS FOR %s;\n", u, u)
	}
	return b.String()
}

// usersScript makes what exportUsersSQL printed a script that makes the accounts again, and that
// can be run more than once: every CREATE USER first, as CREATE USER IF NOT EXISTS (a role is
// an account a grant names), then the grants in their order, then FLUSH PRIVILEGES.
//
// Args:
//
//	out: what mysql printed, one statement a line (batch mode, escaped).
//	header: a comment line for the top of the file ("" : none).
//
// Returns:
//
//	The script.
//
// Errors:
//
//	When a line is neither a CREATE USER, a GRANT nor a REVOKE.
func usersScript(out, header string) (string, error) {
	var creates, grants []string
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		stmt := unescapeBatch(line)
		switch {
		case strings.HasPrefix(stmt, "CREATE USER "):
			creates = append(creates, "CREATE USER IF NOT EXISTS "+strings.TrimPrefix(stmt, "CREATE USER "))
		case strings.HasPrefix(stmt, "GRANT "), strings.HasPrefix(stmt, "REVOKE "):
			grants = append(grants, stmt)
		default:
			return "", fmt.Errorf("unexpected line from SHOW CREATE USER or SHOW GRANTS: %q", line)
		}
	}
	var b strings.Builder
	if header != "" {
		b.WriteString("-- " + header + "\n")
	}
	b.WriteString(sessionSQL)
	for _, stmt := range append(creates, grants...) {
		b.WriteString(stmt + ";\n")
	}
	b.WriteString("FLUSH PRIVILEGES;\n")
	return b.String(), nil
}

// usersFile is the file of the other users that goes with a backup.
//
// Args:
//
//	dump: the backup's path.
//
// Returns:
//
//	The file's path, beside it.
func usersFile(dump string) string {
	return strings.TrimSuffix(dump, ".sql") + usersSuffix
}

// saveUsers saves the database accounts the old MySQL would not make again (otherUsers) next to
// the backup, as the SQL that makes them again (usersScript), readable by its owner only like
// the backup: it holds their password hashes. Run before the switch to a newer MySQL line, while
// the current one still runs.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//	dump: the backup's path.
//
// Returns:
//
//	The file's path, "" when the database has no such account (no file then).
//
// Errors:
//
//	When the database cannot be asked or the file cannot be written.
func saveUsers(ctx context.Context, st *state.State, dump string) (string, error) {
	envData, err := os.ReadFile(filepath.Join(st.Dir, ".env"))
	if err != nil {
		return "", err
	}
	out, err := mysqlRun(ctx, st, allUsersSQL)
	if err != nil {
		return "", err
	}
	all, err := parseUsers(out)
	if err != nil {
		return "", err
	}
	users := otherUsers(all, string(envData))
	if len(users) == 0 {
		return "", nil
	}
	out, err = mysqlRun(ctx, st, exportUsersSQL(users))
	if err != nil {
		return "", err
	}
	script, err := usersScript(out, "Database users of "+st.Dir+" on MySQL, besides root and the app's user: load as root to make them again")
	if err != nil {
		return "", err
	}
	path := usersFile(dump)
	names := make([]string, len(users))
	for i, u := range users {
		names[i] = u.String()
	}
	ui.Step(i18n.T("compose.mysqlUsersSaving", "users", strings.Join(names, ", "), "file", path))
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// replayUsers makes the saved accounts again in the database after a rollback loaded the backup
// into a new data folder. It never fails the rollback: what went wrong is said in a warning with
// the file's path, to load it by hand.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation (its database running).
//	file: the file saveUsers wrote.
//
// Returns:
//
//	The warning to show, "" when every statement went through.
func replayUsers(ctx context.Context, st *state.State, file string) string {
	data, err := os.ReadFile(file)
	if err == nil {
		ui.Step(i18n.T("compose.mysqlUsersRestoring", "file", file))
		var out string
		out, err = mysqlReplay(ctx, st, string(data))
		if err == nil {
			err = replayErrors(out)
		}
	}
	if err != nil {
		return i18n.T("compose.mysqlUsersFailed", "file", file, "error", err)
	}
	return ""
}

// replayErrors reads the errors mysql --force printed among its rows (ERROR 1146 (42S02) at
// line 7: ...).
//
// Args:
//
//	out: what mysql printed.
//
// Errors:
//
//	The errors, one a line, when there is one.
func replayErrors(out string) error {
	var errs []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "ERROR") {
			errs = append(errs, line)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.New(strings.Join(errs, "\n"))
}

// ─── a rollback after the new MySQL started ───────────────────────────────────

// mysqlDataDir is the host folder of the database's data in a compose file: the bind mount of
// the mysqldb service on /var/lib/mysql (./data/mysql/db).
//
// Args:
//
//	content: the compose file.
//	dir: the installation folder, a relative path is read from.
//
// Returns:
//
//	The folder, "" when the data is in a named volume or the file says nothing of it.
func mysqlDataDir(content, dir string) string {
	var f struct {
		Services map[string]struct {
			Volumes []any `yaml:"volumes"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(content), &f); err != nil {
		return ""
	}
	for _, v := range f.Services["mysqldb"].Volumes {
		var source, target string
		switch m := v.(type) {
		case string:
			parts := strings.Split(m, ":")
			if len(parts) < 2 {
				continue
			}
			source, target = parts[0], parts[1]
		case map[string]any:
			source, _ = m["source"].(string)
			target, _ = m["target"].(string)
		}
		if strings.TrimRight(target, "/") != "/var/lib/mysql" {
			continue
		}
		switch {
		case filepath.IsAbs(source):
			return filepath.Clean(source)
		case strings.HasPrefix(source, "."):
			return filepath.Join(dir, source)
		}
		return "" // a named volume
	}
	return ""
}

// moveDataAside moves the database's data folder aside (<folder>.<mysql version>.<time>), so
// that an older MySQL starts on a new one: no MySQL starts on the data of a newer line. The
// folder belongs to the database's user, so the move goes through Podman's user namespace or
// a short container when the installer is not root.
//
// Args:
//
//	ctx: the context, to cancel.
//	r: the runtime.
//	data: the data folder.
//	version: the MySQL version that used it last.
//
// Returns:
//
//	Where it went.
//
// Errors:
//
//	When it cannot be moved.
func moveDataAside(ctx context.Context, r system.Runtime, data, version string) (string, error) {
	aside := fmt.Sprintf("%s.%s.%s", data, version, time.Now().Format("20060102-150405"))
	if err := os.Rename(data, aside); err == nil || os.Geteuid() == 0 {
		return aside, err
	}
	if r == system.Podman {
		return aside, system.Run(ctx, "", "podman", "unshare", "mv", data, aside)
	}
	parent := filepath.Dir(data)
	return aside, system.Run(ctx, "", "docker", "run", "--rm", "-v", parent+":/d", "busybox",
		"mv", "/d/"+filepath.Base(data), "/d/"+filepath.Base(aside))
}
