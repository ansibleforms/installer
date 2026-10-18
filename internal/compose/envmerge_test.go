package compose

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/ansibleforms/installer/internal/state"
)

// read reads a file of testdata.
func read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// a 7.2 .env made by hand gets what the 7.3 template sets and it lacks, every line of its own
// kept as it was, the RTE's token made as a fresh install makes it
func TestMergeEnv72To73(t *testing.T) {
	old, template := read(t, "env-7.2-install.env"), read(t, "env-7.3.template")
	merged, added, blocked := MergeEnv(old, template)
	if len(blocked) != 0 {
		t.Fatalf("blocked %v", blocked)
	}
	sort.Strings(added)
	if got := strings.Join(added, " "); got != "HOME_DIR LOG_CONSOLE_LEVEL RTE_TOKEN" {
		t.Errorf("added %s", got)
	}
	// every key and value of before, untouched
	for _, k := range liveKeys(old) {
		if GetEnv(merged, k) != GetEnv(old, k) {
			t.Errorf("%s changed: %q", k, GetEnv(merged, k))
		}
	}
	// the commented example uncommented with a token of its own, never the template's
	token := GetEnv(merged, "RTE_TOKEN")
	if len(token) != 32 || token == GetEnv(template, "RTE_TOKEN") {
		t.Errorf("RTE_TOKEN not generated: %d characters", len(token))
	}
	if GetEnv(merged, "HOME_DIR") != "/home/node" || GetEnv(merged, "LOG_CONSOLE_LEVEL") != "info" {
		t.Errorf("template values not taken")
	}
	// the variables the 7.3 compose file reads without a default are all set now
	for _, v := range composeVars(read(t, "compose-7.3.yml")) {
		if GetEnv(merged, v) == "" {
			t.Errorf("the 7.3 compose file reads %s, which the merged .env lacks", v)
		}
	}
	// merged again, nothing more to add
	if again, added, _ := MergeEnv(merged, template); again != "" || len(added) != 0 {
		t.Errorf("a second merge added %v", added)
	}
}

// the docker repository's own 7.2 .env already has every key of the 7.3 one
func TestMergeEnvTemplateUnchanged(t *testing.T) {
	if merged, added, blocked := MergeEnv(read(t, "env-7.2.env"), read(t, "env-7.3.template")); merged != "" || len(added) != 0 || len(blocked) != 0 {
		t.Errorf("added %v, blocked %v", added, blocked)
	}
}

// database settings cannot be made up for a database that exists: nothing is added
func TestMergeEnvBlocked(t *testing.T) {
	old := strings.Replace(read(t, "env-7.2-install.env"), "MYSQLDB_USER=ansibleforms\n", "", 1)
	merged, added, blocked := MergeEnv(old, read(t, "env-7.3.template"))
	if merged != "" || len(added) != 0 || strings.Join(blocked, " ") != "MYSQLDB_USER" {
		t.Errorf("merged %t, added %v, blocked %v", merged != "", added, blocked)
	}
	if err := (&Prep{Blocked: blocked}).Check(); err == nil || strings.Contains(err.Error(), "Secret") {
		t.Errorf("Check: %v", err)
	}
}

// composeVars are the variables a compose file reads without a default ($X, ${X}).
func composeVars(content string) []string {
	re := regexp.MustCompile(`\$\{?([A-Z_][A-Z0-9_]*)(:?-)?`)
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllStringSubmatch(content, -1) {
		if m[2] != "" || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out
}

// the 7.2 seed the docker repository shipped is replaced; one changed by hand is kept; the
// 7.3 one already is the new one
func TestSeedAction(t *testing.T) {
	s72, s73 := read(t, "seed-7.2.yaml"), read(t, "seed-7.3.yaml")
	if got := seedAction(s72, s73, ""); got != "replace" {
		t.Errorf("7.2 seed: %q", got)
	}
	if got := seedAction(strings.ReplaceAll(s72, "\n", "\r\n")+"  \n", s73, ""); got != "replace" {
		t.Errorf("7.2 seed, other line ends: %q", got)
	}
	custom := s72 + "forms:\n  items:\n    - name: mine\n"
	if got := seedAction(custom, s73, ""); got != "custom" {
		t.Errorf("custom seed: %q", got)
	}
	if got := seedAction(custom, s73, custom); got != "replace" {
		t.Errorf("seed as the installed version's tag has it: %q", got)
	}
	if got := seedAction(s73, s73, ""); got != "" {
		t.Errorf("7.3 seed: %q", got)
	}
	if got := seedAction("", s73, ""); got != "" {
		t.Errorf("no seed: %q", got)
	}
}

// a fake docker repository at 7.3 and a 7.2 installation made by hand from it
func upgradeFixture(t *testing.T, seed string) *state.State {
	t.Helper()
	repo, dir := t.TempDir(), t.TempDir()
	for name, content := range map[string]string{
		".env":               read(t, "env-7.3.template"),
		"docker-compose.yml": read(t, "compose-7.3.yml"),
		"data/seed.yaml":     read(t, "seed-7.3.yaml"),
	} {
		p := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(read(t, "env-7.2-install.env")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data", "seed.yaml"), []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	was := Source
	Source = repo
	t.Cleanup(func() { Source = was })
	return &state.State{Mode: state.Compose, Role: RoleController, Version: "7.2.0", Dir: dir, Name: "af", Project: "docker"}
}

// Prepare reads the new version's files, applyPrep writes them with the old ones saved beside
// them, and restorePrep (the rollback) puts the old ones back
func TestPrepareApplyRestore(t *testing.T) {
	st := upgradeFixture(t, read(t, "seed-7.2.yaml"))
	p, err := Prepare(t.Context(), st, "7.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Added) != 3 || p.SeedAction != "replace" {
		t.Fatalf("prep: added %v, seed %q", p.Added, p.SeedAction)
	}
	// the review names the keys, never their values
	review := strings.Join(p.Describe("7.3.0"), "\n")
	if !strings.Contains(review, "RTE_TOKEN") || strings.Contains(review, GetEnv(p.Env, "RTE_TOKEN")) {
		t.Errorf("review: %s", review)
	}
	oldEnv, _ := os.ReadFile(filepath.Join(st.Dir, ".env"))
	undo := &Undo{Version: st.Version}
	if err := applyPrep(st, p, "7.3.0", undo); err != nil {
		t.Fatal(err)
	}
	env, _ := os.ReadFile(filepath.Join(st.Dir, ".env"))
	if GetEnv(string(env), "RTE_TOKEN") == "" || GetEnv(string(env), "MYSQLDB_PASSWORD") != "App!Secret!Of!Mine" {
		t.Errorf(".env not merged")
	}
	if info, err := os.Stat(filepath.Join(st.Dir, ".env")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf(".env permissions: %v", info.Mode().Perm())
	}
	bak, _ := os.ReadFile(filepath.Join(st.Dir, ".env.7.2.0.bak"))
	if string(bak) != string(oldEnv) {
		t.Errorf("old .env not saved beside it")
	}
	seed, _ := os.ReadFile(filepath.Join(st.Dir, "data", "seed.yaml"))
	if string(seed) != read(t, "seed-7.3.yaml") {
		t.Errorf("seed not replaced")
	}
	if b, _ := os.ReadFile(filepath.Join(st.Dir, "data", "seed.yaml.7.2.0.bak")); string(b) != read(t, "seed-7.2.yaml") {
		t.Errorf("old seed not saved beside it")
	}
	if err := restorePrep(st, undo); err != nil {
		t.Fatal(err)
	}
	env, _ = os.ReadFile(filepath.Join(st.Dir, ".env"))
	seed, _ = os.ReadFile(filepath.Join(st.Dir, "data", "seed.yaml"))
	if string(env) != string(oldEnv) || string(seed) != read(t, "seed-7.2.yaml") {
		t.Errorf("rollback did not put the .env and the seed back")
	}
}

// a seed changed by hand is kept, the new version's saved beside it
func TestApplyCustomSeed(t *testing.T) {
	custom := read(t, "seed-7.2.yaml") + "# mine\n"
	st := upgradeFixture(t, custom)
	p, err := Prepare(t.Context(), st, "7.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if p.SeedAction != "custom" {
		t.Fatalf("seed: %q", p.SeedAction)
	}
	undo := &Undo{}
	if err := applyPrep(st, p, "7.3.0", undo); err != nil {
		t.Fatal(err)
	}
	if seed, _ := os.ReadFile(filepath.Join(st.Dir, "data", "seed.yaml")); string(seed) != custom || undo.Seed != "" {
		t.Errorf("custom seed not kept")
	}
	if next, _ := os.ReadFile(filepath.Join(st.Dir, "data", "seed.yaml.7.3.0.new")); string(next) != read(t, "seed-7.3.yaml") {
		t.Errorf("new seed not saved beside it")
	}
}
