package compose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/ansibleforms/installer/internal/i18n"
	"github.com/ansibleforms/installer/internal/state"
	"github.com/ansibleforms/installer/internal/ui"
)

// ─── what a new version's files change ────────────────────────────────────────

// Prep is what an upgrade of a controller changes besides the compose file, worked out
// before the person confirms: the keys the new version's .env adds, and its seed.
type Prep struct {
	// Env is the .env with the keys added (every existing line kept as it is), "" when the
	// new version adds none.
	Env string
	// Added are the keys added, by name only (their values may be secrets).
	Added []string
	// Blocked are database settings the new version needs and the .env lacks: they cannot be
	// made up for a database that exists, so the upgrade stops before any change.
	Blocked []string
	// Seed is the new version's data/seed.yaml; SeedAction says what is done with it:
	// "replace" (the seed is one the docker repository shipped, unchanged), "custom" (changed
	// by hand: kept, the new one saved beside it), "" (nothing to do).
	Seed       string
	SeedAction string
}

// generatedKeys are the keys a missing value is made for, as a fresh install makes it: the
// token the app and its RTE share (the seed registers it again at every start) and the
// first admin's password (read on the first start only).
var generatedKeys = map[string]int{"RTE_TOKEN": 32, "ADMIN_PASSWORD": 16}

// databaseKeys are the database's settings: a database that exists has its own user and
// passwords, which no new value would match.
var databaseKeys = map[string]bool{"MYSQLDB_USER": true, "MYSQLDB_PASSWORD": true, "MYSQLDB_ROOT_PASSWORD": true}

// knownSeeds are the data/seed.yaml files the docker repository has shipped (SHA-256 of
// their text, its line ends and trailing spaces left out): one of them, unchanged, is
// replaced by the new version's; anything else was changed by hand and is kept.
var knownSeeds = map[string]bool{
	// 7.2 : the rte container as the default runner, its token in the runner (fa126fa)
	"834823c97f1d7e654a85b8aa3a232640c94b70eba0758eb9846b404f9022b2bd": true,
	// 7.3 : the app dials the rte (rte_mode inbound), its token in a credential (54fc5ce)
	"478a317a27e709977b17a3a13b9b4844e26a34701000043d9ec1dd8d73bbf379": true,
}

// liveKeys are the keys a .env sets (uncommented KEY=value lines), in order.
//
// Args:
//
//	content: the file's content.
//
// Returns:
//
//	The keys.
func liveKeys(content string) []string {
	var keys []string
	for _, line := range strings.Split(content, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		if k, _, ok := strings.Cut(t, "="); ok && k != "" && !strings.ContainsAny(k, " \t") {
			keys = append(keys, k)
		}
	}
	return keys
}

// MergeEnv adds to a .env the keys the new version's template sets and it lacks: a secret
// made for each of generatedKeys, the template's value for the others. Every existing key
// and value is kept as it is.
//
// Args:
//
//	old: the installation's .env.
//	template: the new version's .env, as the docker repository has it.
//
// Returns:
//
//	The merged .env ("" when nothing is added), the keys added, and the database keys
//	missing (nothing is added then).
func MergeEnv(old, template string) (string, []string, []string) {
	have := map[string]bool{}
	for _, k := range liveKeys(old) {
		have[k] = true
	}
	values := map[string]string{}
	var added, blocked []string
	for _, k := range liveKeys(template) {
		if have[k] {
			continue
		}
		have[k] = true
		if databaseKeys[k] {
			blocked = append(blocked, k)
			continue
		}
		v := GetEnv(template, k)
		if n, ok := generatedKeys[k]; ok {
			v = Secret(n)
		}
		values[k] = v
		added = append(added, k)
	}
	if len(blocked) > 0 || len(added) == 0 {
		return "", nil, blocked
	}
	return SetEnv(old, values), added, nil
}

// seedHash is the SHA-256 of a seed's text, its line ends and trailing spaces left out.
//
// Args:
//
//	content: the seed.
//
// Returns:
//
//	The hash, hexadecimal.
func seedHash(content string) string {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(content, "\r\n", "\n")), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// seedAction tells what to do with the installation's seed for the new version's.
//
// Args:
//
//	current: the installation's data/seed.yaml ("" when it has none).
//	next: the new version's.
//	previous: the installed version's own, from its tag ("" when it could not be fetched).
//
// Returns:
//
//	"replace", "custom" or "".
func seedAction(current, next, previous string) string {
	if current == "" || next == "" || seedHash(current) == seedHash(next) {
		return "" // none yet (the new files bring it), or already the new one
	}
	if knownSeeds[seedHash(current)] || (previous != "" && seedHash(current) == seedHash(previous)) {
		return "replace"
	}
	return "custom"
}

// Prepare works out what an upgrade of a controller changes in its .env and its seed, from
// the new version's files (and the installed version's seed, from its tag when it has one).
// Nothing is written in the installation.
//
// Args:
//
//	ctx: the context, to cancel.
//	st: the installation.
//	to: the version upgraded to.
//
// Returns:
//
//	What changes; nil for a Runner (its files are the installer's own).
//
// Errors:
//
//	When the files cannot be fetched or the .env cannot be read.
func Prepare(ctx context.Context, st *state.State, to string) (*Prep, error) {
	if st.Role == RoleRunner {
		return nil, nil
	}
	old, err := os.ReadFile(filepath.Join(st.Dir, ".env"))
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "ansibleforms-upgrade-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	files := func(rel string) bool { return rel == ".env" || rel == "data/seed.yaml" }
	next := filepath.Join(tmp, "next")
	if _, _, err := Fetch(ctx, to, next, files, false); err != nil {
		return nil, err
	}
	template, err := os.ReadFile(filepath.Join(next, ".env"))
	if err != nil {
		return nil, err
	}
	p := &Prep{}
	p.Env, p.Added, p.Blocked = MergeEnv(string(old), string(template))

	nextSeed, _ := os.ReadFile(filepath.Join(next, "data", "seed.yaml"))
	current, _ := os.ReadFile(filepath.Join(st.Dir, "data", "seed.yaml"))
	previous := ""
	if len(current) > 0 && !knownSeeds[seedHash(string(current))] && Source == "" {
		// the installed version's own seed, only from its exact tag (main may be newer)
		prev := filepath.Join(tmp, "previous")
		if ref, _, err := Fetch(ctx, st.Version, prev, func(rel string) bool { return rel == "data/seed.yaml" }, false); err == nil && ref == "v"+st.Version {
			if data, err := os.ReadFile(filepath.Join(prev, "data", "seed.yaml")); err == nil {
				previous = string(data)
			}
		}
	}
	p.Seed = string(nextSeed)
	p.SeedAction = seedAction(string(current), p.Seed, previous)
	return p, nil
}

// Describe is what the review before an upgrade says of the changes: the keys added (names
// only) and the seed.
//
// Args:
//
//	p: the changes (nil: none).
//	to: the version upgraded to.
//
// Returns:
//
//	One paragraph per change.
func (p *Prep) Describe(to string) []string {
	if p == nil {
		return nil
	}
	var out []string
	if len(p.Added) > 0 {
		out = append(out, i18n.T("compose.envAdded", "keys", strings.Join(p.Added, ", ")))
	}
	switch p.SeedAction {
	case "replace":
		out = append(out, i18n.T("compose.seedReplaced"))
	case "custom":
		out = append(out, i18n.T("compose.seedCustom", "file", "data/"+seedNewName(to)))
	}
	return out
}

// Check stops an upgrade whose .env lacks database settings, before anything changes.
//
// Errors:
//
//	Which keys are missing and what to do.
func (p *Prep) Check() error {
	if p == nil || len(p.Blocked) == 0 {
		return nil
	}
	return errors.New(i18n.T("compose.envBlocked", "keys", strings.Join(p.Blocked, ", ")))
}

// seedNewName is the name the new version's seed is saved under beside one changed by hand.
func seedNewName(to string) string { return "seed.yaml." + to + ".new" }

// applyPrep writes the changes into the installation: the old .env and seed saved beside
// them first (.env.<version>.bak, data/seed.yaml.<version>.bak) and kept in undo for a
// rollback, then the merged .env and the new seed (or the new seed beside one changed by
// hand).
//
// Args:
//
//	st: the installation.
//	p: the changes (nil: none).
//	to: the version upgraded to.
//	undo: where the old contents are kept.
//
// Errors:
//
//	When a file cannot be read or written.
func applyPrep(st *state.State, p *Prep, to string, undo *Undo) error {
	if p == nil {
		return nil
	}
	if p.Env != "" {
		envPath := filepath.Join(st.Dir, ".env")
		old, err := os.ReadFile(envPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(envPath+"."+st.Version+".bak", old, 0o600); err != nil {
			return err
		}
		undo.Env = string(old)
		ui.Step(i18n.T("compose.envWriting", "keys", strings.Join(p.Added, ", ")))
		if err := os.WriteFile(envPath, []byte(p.Env), 0o600); err != nil {
			return err
		}
	}
	seedPath := filepath.Join(st.Dir, "data", "seed.yaml")
	switch p.SeedAction {
	case "replace":
		old, err := os.ReadFile(seedPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(seedPath+"."+st.Version+".bak", old, 0o644); err != nil {
			return err
		}
		undo.Seed = string(old)
		ui.Step(i18n.T("compose.seedWriting"))
		if err := os.WriteFile(seedPath, []byte(p.Seed), 0o644); err != nil {
			return err
		}
	case "custom":
		if err := os.WriteFile(filepath.Join(st.Dir, "data", seedNewName(to)), []byte(p.Seed), 0o644); err != nil {
			return err
		}
		ui.Warn(i18n.T("compose.seedCustom", "file", "data/"+seedNewName(to)))
	}
	return nil
}

// restorePrep puts back the .env and the seed an upgrade changed (Rollback).
//
// Args:
//
//	st: the installation.
//	undo: what Upgrade kept.
//
// Errors:
//
//	When a file cannot be written.
func restorePrep(st *state.State, undo *Undo) error {
	if undo.Env != "" {
		if err := os.WriteFile(filepath.Join(st.Dir, ".env"), []byte(undo.Env), 0o600); err != nil {
			return err
		}
	}
	if undo.Seed != "" {
		if err := os.WriteFile(filepath.Join(st.Dir, "data", "seed.yaml"), []byte(undo.Seed), 0o644); err != nil {
			return err
		}
	}
	return nil
}
