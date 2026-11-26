package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ansibleforms/installer/internal/compose"
	"github.com/ansibleforms/installer/internal/detect"
	"github.com/ansibleforms/installer/internal/releases"
	"github.com/ansibleforms/installer/internal/state"
	"github.com/ansibleforms/installer/internal/tui"
)

// ─── a Runner of 7.2 in database mode ─────────────────────────────────────────

// the 7.2 RTE's token, and a setting of its own whose value is a secret
const (
	oldToken = "the-72-rte-token-0123456789"
	metrics  = "metrics-secret-0123456789"
)

// dbFound is a 7.2 Runner in database mode found by hand, as detect finds it
func dbFound() detect.Install {
	return detect.Install{Engine: detect.Docker, Role: detect.RoleRunner, Version: "7.2.0", Running: true,
		Dir: "/opt/rte", Project: "rte", Name: "af", Port: 8000, Containers: []string{"af_rte"},
		Database: &detect.DatabaseRunner{Container: "af_rte", ComposeFile: "/opt/rte/docker-compose.yml", RunnerName: "rte72", NameKnown: true,
			Token: oldToken, URL: "http://rte72:8000", Settings: map[string]string{"RTE_MAX_JOBS": "4", "METRICS_TOKEN": metrics}}}
}

// upgrade --yes takes it over, remembering that its next upgrade moves it
func TestOfferFoundAdoptsDatabaseRunner(t *testing.T) {
	unattended(t)
	st, err := offerFound(context.Background(), Flags{Yes: true}, []detect.Install{dbFound()}, false)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := state.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !saved.DatabaseMode || saved.Role != compose.RoleRunner || saved.Project != "rte" || !migrating(st) {
		t.Errorf("saved %+v", saved)
	}
	if d := describe(dbFound()); !strings.Contains(d, "database mode") {
		t.Errorf("describe = %q", d)
	}
}

// the review : the runner, the mode, the app, the settings by name ; never a token or a value
func TestMigrationReviewShowsNoSecret(t *testing.T) {
	old := dbFound()
	mp := &migrationPlan{old: &old, name: "rte72", token: oldToken, how: tokenKept, mode: "inbound", host: "10.0.0.5", port: 8443,
		app: &plan{appURL: "https://forms.example.com", version: releases.Version{Major: 7, Minor: 3}}}
	st := &state.State{Dir: "/opt/rte"}
	text := strings.Join(migrationReview(st, mp, releases.Version{Major: 7, Minor: 2}), "\n")
	for _, want := range []string{"rte72", "inbound", "https://forms.example.com", "https://10.0.0.5:8443", "7.2.0 → 7.3.0", "METRICS_TOKEN, RTE_MAX_JOBS", "af_rte"} {
		if !strings.Contains(text, want) {
			t.Errorf("review lacks %q:\n%s", want, text)
		}
	}
	for _, secret := range []string{oldToken, metrics} {
		if strings.Contains(text, secret) {
			t.Errorf("review shows a secret:\n%s", text)
		}
	}
}

// the move the answers make : the runner's token kept, or a join token for a new runner
func TestMigrationFromAnswers(t *testing.T) {
	old := dbFound()
	mp := &migrationPlan{old: &old, name: "rte72", token: oldToken, how: tokenKept, app: &plan{appURL: "https://forms.example.com", trust: true}, port: 8001}
	m := mp.migration()
	if m.Token != oldToken || m.JoinToken != "" || m.Mode != "outbound" || m.RunnerName != "rte72" || m.OldRunnerName != "rte72" ||
		m.OldProject != "rte" || m.OldComposeFile != "/opt/rte/docker-compose.yml" || !m.TrustAppCert || m.Settings["RTE_MAX_JOBS"] != "4" {
		t.Errorf("kept : %+v", m)
	}
	mp.how, mp.join, mp.name, mp.token = tokenJoin, "afj_x", "rte72-socket", ""
	if m := mp.migration(); m.Token != "" || m.JoinToken != "afj_x" || m.RunnerName != "rte72-socket" {
		t.Errorf("join : %+v", m)
	}
}

// no token to read : unattended it needs --join ; with it a new runner under another name
func TestAskTokenUnattended(t *testing.T) {
	unattended(t)
	old := dbFound()
	old.Database.Token = ""
	mp := &migrationPlan{old: &old, name: "rte72"}
	if err := askToken(Flags{Yes: true}, mp); err == nil {
		t.Fatal("no token and no join token went on")
	}
	if err := askToken(Flags{Yes: true, JoinToken: "afj_0123"}, mp); err != nil {
		t.Fatal(err)
	}
	if mp.how != tokenJoin || mp.join != "afj_0123" || mp.name == "rte72" || mp.name == "" {
		t.Errorf("join : %+v", mp)
	}
	// a join token that is not one
	mp = &migrationPlan{old: &old, name: "rte72"}
	if err := askToken(Flags{Yes: true, JoinToken: "nope"}, mp); err == nil {
		t.Fatal("an invalid join token was taken")
	}
}

// only a compose Runner taken over in database mode is moved
func TestMigrating(t *testing.T) {
	if migrating(&state.State{Mode: state.Compose, Role: compose.RoleRunner}) ||
		migrating(&state.State{Mode: state.Native, Role: compose.RoleRunner, DatabaseMode: true}) ||
		!migrating(&state.State{Mode: state.Compose, Role: compose.RoleRunner, DatabaseMode: true}) {
		t.Errorf("migrating")
	}
	if validToken("short") == nil || validToken(oldToken) != nil || validRunnerName("a b") == nil || validRunnerName("rte-1") != nil {
		t.Errorf("validators")
	}
}

// ─── the move, answered ───────────────────────────────────────────────────────

// dbState saves the state of a Runner of 7.2 in database mode, and makes the fake server find
// it with its token, or without one.
//
// Args:
//
//	s: the fake server.
//	token: its token ("" : none kept).
//
// Returns:
//
//	Its state.
func dbState(s *server, token string) *state.State {
	st := &state.State{Mode: state.Compose, Role: "runner", Version: "7.2.0", Dir: "/opt/rte", Name: "af", Project: "rte", Runtime: "docker", DatabaseMode: true}
	_ = st.Save()
	found := dbFound()
	found.Database.Token = token
	s.db = &found
	return st
}

// TestMigrateKeptToken checks a Runner of 7.2 moved outbound with its token kept, and inbound
// with its port and address asked and a join token given.
func TestMigrateKeptToken(t *testing.T) {
	ctx := context.Background()
	s := fakeServer(t)
	dbState(s, oldToken)
	answering(t, map[string][]tui.Answer{title("runner.appUrl"): {{Value: "https://app"}}})
	if err := upgrade(ctx, Flags{Lang: "en", Version: "7.3.0"}); err != nil || !s.called("composeMigrateRunner") {
		t.Fatalf("outbound: %v, %v", err, s.calls)
	}
	s = fakeServer(t)
	dbState(s, oldToken)
	answering(t, map[string][]tui.Answer{
		title("runner.appUrl"): {{Value: "https://app"}},
		title("runner.mode"):   {pick("inbound")},
		title("runner.port"):   {{Value: "8001"}},
	})
	if err := upgrade(ctx, Flags{Lang: "en", Version: "7.3.0", JoinToken: "afj_x"}); err != nil {
		t.Fatalf("inbound: %v", err)
	}
	s = fakeServer(t)
	dbState(s, oldToken)
	answering(t, map[string][]tui.Answer{})
	if err := upgrade(ctx, Flags{Lang: "en", Version: "7.3.0", AppURL: "https://app", RunnerMode: "inbound", Port: 8001, Host: "10.0.0.9", Yes: true}); err != nil {
		t.Fatalf("inbound by flags: %v", err)
	}
}

// TestMigrateTokenChoices checks a Runner whose token is not kept: typed, or replaced by a
// join token under another name ; and each answer refused.
func TestMigrateTokenChoices(t *testing.T) {
	ctx := context.Background()
	choice, typed, join, joinName := title("migrate.tokenChoice"), title("migrate.token"), title("migrate.joinToken"), title("migrate.joinName")
	for name, c := range map[string]struct {
		answers map[string][]tui.Answer
		ok      bool
	}{
		"typed":           {map[string][]tui.Answer{choice: {pick(tokenTyped)}, typed: {{Value: "a-token-of-sixteen-chars"}}}, true},
		"typed short":     {map[string][]tui.Answer{choice: {pick(tokenTyped)}, typed: {{Value: "short"}}}, false},
		"typed aborted":   {map[string][]tui.Answer{choice: {pick(tokenTyped)}, typed: {abort}}, false},
		"join":            {map[string][]tui.Answer{choice: {pick(tokenJoin)}, join: {{Value: "afj_x"}}}, true},
		"join aborted":    {map[string][]tui.Answer{choice: {pick(tokenJoin)}, join: {abort}}, false},
		"join same name":  {map[string][]tui.Answer{choice: {pick(tokenJoin)}, join: {{Value: "afj_x"}}, joinName: {{Value: "rte72"}}}, false},
		"join bad name":   {map[string][]tui.Answer{choice: {pick(tokenJoin)}, join: {{Value: "afj_x"}}, joinName: {{Value: "a b"}}}, false},
		"join name abort": {map[string][]tui.Answer{choice: {pick(tokenJoin)}, join: {{Value: "afj_x"}}, joinName: {abort}}, false},
		"choice aborted":  {map[string][]tui.Answer{choice: {abort}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			s := fakeServer(t)
			dbState(s, "")
			c.answers[title("runner.appUrl")] = []tui.Answer{{Value: "https://app"}}
			answering(t, c.answers)
			if err := upgrade(ctx, Flags{Lang: "en", Version: "7.3.0"}); (err == nil) != c.ok {
				t.Errorf("%s: %v", name, err)
			}
		})
	}
}

// TestMigrateRefusals checks a Runner gone, a name refused or aborted, an app of another
// major, the review changed then aborted, and unattended without a name or a token.
func TestMigrateRefusals(t *testing.T) {
	ctx := context.Background()
	s := fakeServer(t)
	dbState(s, oldToken)
	s.db = nil
	answering(t, map[string][]tui.Answer{})
	if err := upgrade(ctx, Flags{Lang: "en", Version: "7.3.0"}); err == nil {
		t.Error("gone")
	}
	name := title("migrate.name")
	for what, answers := range map[string]map[string][]tui.Answer{
		"bad name":     {name: {{Value: "a b"}}},
		"name aborted": {name: {abort}},
		"review abort": {title("runner.appUrl"): {{Value: "https://app"}}, title("migrate.confirm", "mode", title("runner.mode_outbound"), "to", "7.3.0"): {no, abort}},
		"app aborted":  {title("runner.appUrl"): {abort}},
		"mode aborted": {title("runner.appUrl"): {{Value: "https://app"}}, title("runner.mode"): {abort}},
		"port aborted": {title("runner.appUrl"): {{Value: "https://app"}}, title("runner.mode"): {pick("inbound")}, title("runner.port"): {abort}},
		"other major":  {title("runner.appUrl"): {{Value: "https://app"}}},
	} {
		t.Run(what, func(t *testing.T) {
			s := fakeServer(t)
			if what == "other major" {
				s.app = "8.0.0"
			}
			dbState(s, oldToken)
			answering(t, answers)
			if err := upgrade(ctx, Flags{Lang: "en", Version: "7.3.0"}); err == nil {
				t.Errorf("%s: moved", what)
			}
		})
	}
	s = fakeServer(t)
	unattended(t)
	dbState(s, "")
	s.db.Database.NameKnown = false
	if err := upgrade(ctx, Flags{Lang: "en", Version: "7.3.0", AppURL: "https://app"}); err == nil {
		t.Error("unattended without a name")
	}
	s.db.Database.NameKnown = true
	if err := upgrade(ctx, Flags{Lang: "en", Version: "7.3.0", AppURL: "https://app"}); err == nil {
		t.Error("unattended without a token")
	}
}

// TestMigrateGuessedName checks a Runner whose name the installer guessed: said so, and
// confirmed.
func TestMigrateGuessedName(t *testing.T) {
	s := fakeServer(t)
	dbState(s, oldToken)
	s.db.Database.NameKnown = false
	answering(t, map[string][]tui.Answer{title("runner.appUrl"): {{Value: "https://app"}}})
	if err := upgrade(context.Background(), Flags{Lang: "en", Version: "7.3.0"}); err != nil {
		t.Error(err)
	}
}

// TestUpgradeMigrationFails checks a move that fails before anything changed, after the new
// RTE connected, and rolled back (or not, or failing).
func TestUpgradeMigrationFails(t *testing.T) {
	ctx := context.Background()
	m := &compose.Migration{RunnerName: "r", Mode: "inbound", AppURL: "https://app", JoinToken: "afj_x"}
	from, to := releases.Version{Major: 7, Minor: 2}, releases.Version{Major: 7, Minor: 3}
	s := fakeServer(t)
	answering(t, map[string][]tui.Answer{title("migrate.rollback"): {yes, no, yes}})
	st := &state.State{Version: "7.2.0", Dir: "/opt/rte"}
	s.fail["composeMigrateRunner"] = errors.New("broken")
	if err := upgradeMigration(ctx, st, m, from, to); err == nil {
		t.Error("nothing to roll back")
	}
	s.undo = true
	if err := upgradeMigration(ctx, st, m, from, to); err == nil || !s.called("composeRollbackMigration") {
		t.Errorf("rolled back: %v", err)
	}
	if err := upgradeMigration(ctx, st, m, from, to); err == nil {
		t.Error("rollback declined")
	}
	s.fail["composeRollbackMigration"] = errors.New("rb")
	if err := upgradeMigration(ctx, st, m, from, to); err == nil || !strings.Contains(err.Error(), "rb") {
		t.Errorf("rollback failed: %v", err)
	}
	composeMigrateRunner = func(context.Context, *state.State, string, compose.Migration) (*compose.MigrationUndo, *compose.Result, error) {
		return &compose.MigrationUndo{Connected: true}, nil, errors.New("late")
	}
	if err := upgradeMigration(ctx, st, m, from, to); err == nil {
		t.Error("connected")
	}
	composeMigrateRunner = func(context.Context, *state.State, string, compose.Migration) (*compose.MigrationUndo, *compose.Result, error) {
		return &compose.MigrationUndo{}, &compose.Result{URL: "https://10.0.0.5:8000", CertFingerprint: "ab"}, nil
	}
	if err := upgradeMigration(ctx, st, m, from, to); err != nil {
		t.Error(err)
	}
}
