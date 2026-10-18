package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ansibleforms/installer/internal/compose"
	"github.com/ansibleforms/installer/internal/detect"
	"github.com/ansibleforms/installer/internal/i18n"
	"github.com/ansibleforms/installer/internal/releases"
	"github.com/ansibleforms/installer/internal/state"
	"github.com/ansibleforms/installer/internal/ui"
)

// ─── a Runner of 7.2, moved to a socket mode ──────────────────────────────────

// How the new RTE signs in: with the runner's token read from the old RTE, typed by the
// person (the password of the runner's credential), or with a join token as a new runner.
const (
	tokenKept  = "kept"
	tokenTyped = "typed"
	tokenJoin  = "join"
)

// migrationPlan is every answer of the move of a database-mode Runner, kept across Esc.
type migrationPlan struct {
	// old is the database-mode Runner as found.
	old *detect.Install

	// name is the runner's name (RTE_NAME), token its token, how how it signs in, join the
	// join token (how = join: name is the new runner's).
	name, token, how, join string
	// the app, the mode, and an inbound one's host and port
	app  *plan
	mode string
	host string
	port int
}

// migrating tells whether an upgrade is the move of a Runner of 7.2 in database mode.
//
// Args:
//
//	st: the installation.
//
// Returns:
//
//	True for a compose Runner taken over in database mode.
func migrating(st *state.State) bool {
	return st.Mode == state.Compose && st.Role == compose.RoleRunner && st.DatabaseMode
}

// validRunnerName checks a runner's name as the app accepts it from an RTE.
func validRunnerName(s string) error {
	if !detect.ValidRunnerName(strings.TrimSpace(s)) {
		return errors.New(i18n.T("check.runnerName"))
	}
	return nil
}

// validToken checks an RTE token: at least 16 characters, as an RTE refuses to start without.
func validToken(s string) error {
	if len(strings.TrimSpace(s)) < 16 {
		return errors.New(i18n.T("check.token"))
	}
	return nil
}

// askMigration asks what the move needs, a step at a time (back with Esc): the runner's name,
// how it signs in (its token, kept when the old RTE has one), the app, the mode, an inbound
// one's port and address, then the review.
//
// Args:
//
//	ctx: the context, to cancel.
//	f: the flags.
//	st: the installation.
//	from: the version installed.
//	to: the version picked (the app's wins once it is reached).
//
// Returns:
//
//	The move, and the version it runs (the app's).
//
// Errors:
//
//	When the old RTE is gone, a token is needed unattended, the app cannot be reached, or the
//	person stops.
func askMigration(ctx context.Context, f Flags, st *state.State, from, to releases.Version) (*compose.Migration, releases.Version, error) {
	old := detect.FindDatabaseRunner(ctx, st)
	if old == nil {
		return nil, to, errors.New(i18n.T("migrate.gone", "dir", st.Dir))
	}
	db := old.Database
	mp := &migrationPlan{old: old, name: db.RunnerName, mode: f.RunnerMode, host: f.Host, port: f.Port,
		app: &plan{version: to, appURL: f.AppURL}}
	if len(db.Token) >= 16 {
		mp.token, mp.how = db.Token, tokenKept
	}
	list := []wstep{
		// the runner's name : the one the RTE registered itself as, asked when it did not
		{label: "runner", ask: func() error {
			help := i18n.T("migrate.nameHelp")
			if !db.NameKnown {
				help = i18n.T("migrate.nameHelpGuessed")
			}
			ui.Explain(i18n.T("migrate.intro", "name", db.RunnerName) + "\n\n" + help)
			// a runner added by hand : its name is the admin's, never guessed unattended
			if ui.Unattended && !db.NameKnown {
				return errors.New(i18n.T("migrate.needName"))
			}
			n, err := ui.Input(i18n.T("migrate.name"), mp.name, false, validRunnerName)
			if err != nil {
				return err
			}
			mp.name = strings.TrimSpace(n)
			return validRunnerName(mp.name)
		}},
		// no token the old RTE says : typed, or a join token for a new runner
		{label: "runner", skip: func() bool { return len(db.Token) >= 16 }, ask: func() error {
			return askToken(f, mp)
		}},
		{label: "app", ask: func() error {
			if err := askApp(ctx, f, mp.app); err != nil {
				return err
			}
			if mp.app.version.Major != from.Major {
				return errors.New(i18n.T("upgrade.major", "from", from.String(), "to", mp.app.version.String()))
			}
			return nil
		}},
		{label: "mode", skip: func() bool { return f.RunnerMode != "" }, ask: func() error {
			ui.Explain(i18n.T("runner.modeHelp"))
			m, err := ui.Select(i18n.T("runner.mode"), []ui.Option{
				{Label: i18n.T("runner.modeOutbound"), Value: "outbound", Hint: i18n.T("runner.modeOutboundHint")},
				{Label: i18n.T("runner.modeInbound"), Value: "inbound", Hint: i18n.T("runner.modeInboundHint")},
			}, orStr(mp.mode, "outbound"))
			mp.mode = m
			if err == nil {
				ui.Answered(i18n.T("runner.mode_" + m))
			}
			return err
		}},
		// inbound : the port the app reaches it on (a free one by default : the old RTE runs
		// until the new one is connected) and this server's address
		{label: "network", skip: func() bool { return mp.mode != "inbound" || f.Port != 0 }, ask: func() error {
			ui.Explain(i18n.T("migrate.portHelp"))
			def := mp.port
			if def == 0 {
				def = compose.FreePort("0.0.0.0", 8000)
			}
			s, err := ui.Input(i18n.T("runner.port"), strconv.Itoa(def), false, validPort)
			if err != nil {
				return err
			}
			mp.port, _ = strconv.Atoi(strings.TrimSpace(s))
			return nil
		}},
		{label: "network", skip: func() bool { return mp.mode != "inbound" }, ask: func() error {
			ui.Explain(i18n.T("runner.addressHelp"))
			h, err := textAgain(f.Host, i18n.T("runner.address"), orStr(mp.host, compose.OutboundIP()), notEmpty)
			mp.host = h
			if err == nil {
				ui.Answered(fmt.Sprintf("%s:%d", h, mp.port))
			}
			return err
		}},
		{label: "review", skip: func() bool { return ui.Unattended }, ask: func() error {
			ui.Explain(i18n.T("migrate.reviewHelp", "dir", st.Dir+"/installer-backups") + "\n\n" + strings.Join(migrationReview(st, mp, from), "\n"))
			ok, err := ui.Confirm(i18n.T("migrate.confirm", "mode", i18n.T("runner.mode_"+orStr(mp.mode, "outbound")), "to", mp.app.version.String()),
				i18n.T("upgrade.goOn"), i18n.T("install.changeAnswers"), true)
			if err != nil {
				return err
			}
			if !ok {
				return ui.ErrBack
			}
			ui.Answered(i18n.T("install.reviewed"))
			return nil
		}},
		{label: "upgrade", ask: func() error { return nil }},
	}
	if err := runWizard(list); err != nil {
		return nil, to, err
	}
	mp.mode = orStr(mp.mode, "outbound")
	if mp.port == 0 {
		// outbound : its health checks on this server only, on a port the old RTE does not hold
		mp.port = compose.FreePort("127.0.0.1", 8000)
	}
	return mp.migration(), mp.app.version, nil
}

// askToken asks how the new RTE signs in when the old one has no token the installer can read:
// the runner's token typed (masked), or a join token that adds a new runner under another name.
//
// Args:
//
//	f: the flags.
//	mp: the answers.
//
// Errors:
//
//	When it is needed unattended without --join, or the person stops.
func askToken(f Flags, mp *migrationPlan) error {
	old := mp.old.Database.RunnerName
	how := mp.how
	if f.JoinToken != "" {
		how = tokenJoin
	}
	if how == "" && ui.Unattended {
		return errors.New(i18n.T("migrate.needToken"))
	}
	if f.JoinToken == "" {
		ui.Explain(i18n.T("migrate.tokenMissing"))
		v, err := ui.Select(i18n.T("migrate.tokenChoice"), []ui.Option{
			{Label: i18n.T("migrate.tokenType"), Value: tokenTyped, Hint: i18n.T("migrate.tokenTypeHint")},
			{Label: i18n.T("migrate.tokenJoin"), Value: tokenJoin, Hint: i18n.T("migrate.tokenJoinHint")},
		}, orStr(how, tokenTyped))
		if err != nil {
			return err
		}
		how = v
	}
	if how == tokenTyped {
		t, err := ui.Input(i18n.T("migrate.token"), "", true, validToken)
		if err != nil {
			return err
		}
		if err := validToken(t); err != nil {
			return err
		}
		mp.token, mp.how, mp.join = strings.TrimSpace(t), tokenTyped, ""
		return nil
	}
	// a join token : a new runner, under a name of its own (a join token never gives an
	// approved runner a new token)
	j, err := text(f.JoinToken, i18n.T("migrate.joinToken"), mp.join, func(s string) error {
		_, err := compose.ParseJoin(s)
		return err
	})
	if err != nil {
		return err
	}
	ui.Explain(i18n.T("migrate.joinNameHelp", "old", old))
	def := mp.name
	if def == old {
		def = old + "-socket"
	}
	other := func(s string) error {
		if err := validRunnerName(s); err != nil {
			return err
		}
		if strings.TrimSpace(s) == old {
			return errors.New(i18n.T("check.otherName", "name", old))
		}
		return nil
	}
	n, err := ui.Input(i18n.T("migrate.joinName"), def, false, other)
	if err != nil {
		return err
	}
	if err := other(n); err != nil {
		return err
	}
	mp.join, _ = compose.ParseJoin(j)
	mp.name, mp.how, mp.token = strings.TrimSpace(n), tokenJoin, ""
	return nil
}

// migration is the move the answers make.
//
// Returns:
//
//	The move, for compose.MigrateRunner.
func (mp *migrationPlan) migration() *compose.Migration {
	db := mp.old.Database
	m := &compose.Migration{
		OldProject: mp.old.Project, OldContainers: mp.old.Containers, OldComposeFile: db.ComposeFile,
		OldRunnerName: db.RunnerName, Settings: db.Settings, RunnerName: mp.name, AppURL: mp.app.appURL,
		Mode: orStr(mp.mode, "outbound"), TrustAppCert: mp.app.trust, Host: mp.host, Port: mp.port,
	}
	if mp.how == tokenJoin {
		m.JoinToken = mp.join
	} else {
		m.Token = mp.token
	}
	return m
}

// migrationReview lists the move, as the review shows it: never a token, never a setting's value.
//
// Args:
//
//	st: the installation.
//	mp: the answers.
//	from: the version installed.
//
// Returns:
//
//	One line per answer.
func migrationReview(st *state.State, mp *migrationPlan, from releases.Version) []string {
	row := func(k, v string) string { return ui.Row(i18n.T("review."+k), v, 15) }
	mode := orStr(mp.mode, "outbound")
	out := []string{
		row("version", from.String()+" → "+mp.app.version.String()),
		row("role", i18n.T("install.roleRunnerShort")),
		row("folder", st.Dir),
		row("name", mp.name),
		row("app", mp.app.appURL),
		row("mode", i18n.T("runner.mode_"+mode)),
	}
	if mode == "inbound" {
		out = append(out, row("address", compose.InboundAddress(mp.host, mp.port)))
	}
	out = append(out, row("token", i18n.T(map[string]string{tokenKept: "migrate.tokenKeptShort", tokenTyped: "migrate.tokenTypedShort", tokenJoin: "migrate.tokenJoinShort"}[orStr(mp.how, tokenKept)])))
	if keys := settingNames(mp.old.Database.Settings); keys != "" {
		out = append(out, row("carried", keys))
	}
	out = append(out, row("old", i18n.T("migrate.oldRemoved", "containers", strings.Join(mp.old.Containers, ", "))))
	return out
}

// settingNames are the carried settings' names, sorted, without their values.
//
// Args:
//
//	settings: the carried settings.
//
// Returns:
//
//	Their names, comma separated; "" for none.
func settingNames(settings map[string]string) string {
	keys := make([]string, 0, len(settings))
	for k := range settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// upgradeMigration moves the Runner, and offers to go back when the new RTE does not connect.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//	m: the move.
//	from: the version installed.
//	to: the version it runs.
//
// Errors:
//
//	When a step fails (after the rollback, when the person chose it).
func upgradeMigration(ctx context.Context, st *state.State, m *compose.Migration, from, to releases.Version) error {
	undo, res, err := compose.MigrateRunner(ctx, st, to.String(), *m)
	if err != nil {
		if undo == nil {
			return err
		}
		ui.Fail(i18n.T("upgrade.failed", "error", err))
		if undo.Connected {
			return err
		}
		ok, cerr := ui.Confirm(i18n.T("migrate.rollback"), i18n.T("app.yes"), i18n.T("app.no"), true)
		if cerr != nil || !ok {
			return err
		}
		if rerr := compose.RollbackMigration(ctx, st, undo); rerr != nil {
			return fmt.Errorf("%s: %w", i18n.T("upgrade.rollbackFailed"), rerr)
		}
		ui.Done(i18n.T("migrate.rolledBack"))
		return err
	}
	lines := []string{
		i18n.T("runner.doneName", "name", m.RunnerName),
		i18n.T("runner.doneMode", "mode", i18n.T("runner.mode_"+m.Mode)),
		i18n.T("runner.doneApp", "url", m.AppURL),
	}
	if m.Mode == "inbound" && res != nil {
		lines = append(lines, i18n.T("runner.doneAddress", "url", res.URL))
	}
	lines = append(lines, "")
	if m.JoinToken != "" {
		lines = append(lines, i18n.T("migrate.doneJoined", "name", m.RunnerName, "old", m.OldRunnerName))
	} else {
		lines = append(lines, i18n.T("migrate.doneKept", "name", m.RunnerName))
	}
	if res != nil && res.CertFingerprint != "" {
		lines = append(lines, "", i18n.T("runner.doneCertFingerprint", "fingerprint", res.CertFingerprint))
	}
	lines = append(lines, "", i18n.T("done.folder", "dir", st.Dir), i18n.T("migrate.doneBackups", "dir", st.Dir+"/installer-backups"),
		i18n.T("upgrade.doneFrom", "from", from.String()))
	ui.Summary(i18n.T("migrate.done", "version", to.String()), lines)
	return nil
}
