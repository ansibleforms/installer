package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/ansibleforms/installer/internal/compose"
	"github.com/ansibleforms/installer/internal/detect"
	"github.com/ansibleforms/installer/internal/releases"
	"github.com/ansibleforms/installer/internal/state"
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
