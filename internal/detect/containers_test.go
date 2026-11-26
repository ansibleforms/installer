package detect

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ansibleforms/installer/internal/state"
)

// the compose file of a project: its label's when it is one of the folder's own, else the
// first compose reads by itself
func TestProjectComposeFile(t *testing.T) {
	dir := t.TempDir()
	if got := ProjectComposeFile(dir, dir+"/docker-compose.yaml"); got != filepath.Join(dir, "docker-compose.yaml") {
		t.Errorf("label: %s", got)
	}
	if got := ProjectComposeFile(dir, "/elsewhere/docker-compose.yml"); got != "" {
		t.Errorf("another folder: %s", got)
	}
	if got := ProjectComposeFile(dir, ""); got != filepath.Join(dir, "docker-compose.yml") {
		t.Errorf("none: %s", got)
	}
	write(t, dir, map[string]string{"compose.yml": "services: {}\n", "docker-compose.yml": "services: {}\n"})
	if got := ProjectComposeFile(dir, ""); got != filepath.Join(dir, "compose.yml") {
		t.Errorf("no label: %s, want compose.yml (read first)", got)
	}
}

// a controller started from docker-compose.yaml (or compose.yaml): read from that file and
// taken over, as compose reads it
func TestFindComposeYaml(t *testing.T) {
	for _, name := range []string{"docker-compose.yaml", "compose.yaml", "compose.yml"} {
		f, dir := composeController(t)
		if err := os.Rename(filepath.Join(dir, "docker-compose.yml"), filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
		f.out["docker inspect"] = strings.ReplaceAll(f.out["docker inspect"], dir+"/docker-compose.yml", dir+"/"+name)
		if got := find(context.Background(), f.env()); len(got) != 1 || !got[0].Adoptable() {
			t.Fatalf("%s: found %+v, want it adoptable", name, got)
		}
	}
}

// a Runner alone (the RTE's image, no app): on Podman, stopped, taken over
func TestFindRunnerOnly(t *testing.T) {
	f := &fake{t: t, has: map[string]bool{}, out: map[string]string{}}
	dir := podmanRunner(t, f)
	got := find(context.Background(), f.env())
	if len(got) != 1 {
		t.Fatalf("found %+v, want 1", got)
	}
	i := got[0]
	if i.Engine != Podman || i.Role != RoleRunner || i.Version != "7.3.0" || i.Running || i.Name != "runner" || i.Port != 8000 || !i.Adoptable() {
		t.Fatalf("found %+v", i)
	}
	st := i.State()
	if st.Mode != state.Compose || st.Runtime != "podman" || st.Role != RoleRunner || st.Dir != dir || st.Project != "" {
		t.Errorf("state: %+v", st)
	}
	// a Runner whose .env lacks what the installer's Runner file needs
	write(t, dir, map[string]string{".env": "APP_URL=https://forms.example.com\n"})
	if got := find(context.Background(), f.env()); len(got) != 1 || got[0].Problem != RunnerLayout {
		t.Fatalf("found %+v, want %s", got, RunnerLayout)
	}
}

// TestContainerHelpers checks a container's name, its variables and the first host port it
// publishes.
func TestContainerHelpers(t *testing.T) {
	var c container
	if err := json.Unmarshal([]byte(`{"Name":"/af_app","Config":{"Env":["A=1","PORT=8443","EMPTY="]},
		"NetworkSettings":{"Ports":{"8443/tcp":[{"HostPort":""},{"HostPort":"443"}],"3000/tcp":null}}}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.name() != "af_app" || c.env("PORT") != "8443" || c.env("EMPTY") != "" || c.env("NONE") != "" {
		t.Errorf("name %s, env %s", c.name(), c.env("PORT"))
	}
	if c.published() != 443 {
		t.Errorf("published %d", c.published())
	}
	if (container{}).published() != 0 {
		t.Error("no port published")
	}
}

// TestComposeProblem checks why a compose project cannot be taken over: not compose, another
// layout, an external database, and an unknown version.
func TestComposeProblem(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"docker-compose.yml": "services:\n  app:\n    image: ghcr.io/ansibleforms/ansibleforms:7.3.0\n", ".env": "X=1\n"})
	main := &container{}
	main.Config.Labels = map[string]string{labelFiles: filepath.Join(dir, "docker-compose.yml")}
	ok := map[string]string{"app": "af_app", "rte": "af_rte", "mysqldb": "af_db"}
	base := Install{Project: "af", Dir: dir, Name: "af", Role: RoleController, Version: "7.3.0"}
	cases := []struct {
		name     string
		i        Install
		services map[string]string
		env      map[string]string
		want     string
	}{
		{"its layout", base, ok, map[string]string{}, ""},
		{"not compose", Install{}, ok, map[string]string{}, NotCompose},
		{"no .env read", base, ok, nil, NoComposeFile},
		{"other names", base, map[string]string{"app": "x", "rte": "af_rte"}, map[string]string{}, NotRepoLayout},
		{"external db", base, map[string]string{"app": "af_app", "rte": "af_rte"}, map[string]string{}, ExternalDB},
		{"db of another name", base, map[string]string{"app": "af_app", "rte": "af_rte", "mysqldb": "db"}, map[string]string{}, NotRepoLayout},
		{"runner layout", Install{Project: "af", Dir: dir, Name: "af", Role: RoleRunner, Version: "7.3.0"}, map[string]string{"rte": "af_rte"}, map[string]string{"RTE_MODE": "outbound"}, RunnerLayout},
		{"no version", Install{Project: "af", Dir: dir, Name: "af", Role: RoleController}, ok, map[string]string{}, UnknownVersion},
	}
	for _, c := range cases {
		if got, _ := composeProblem(c.i, main, c.services, c.env); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	other := &container{}
	other.Config.Labels = map[string]string{labelFiles: "/elsewhere/compose.yml"}
	if got, _ := composeProblem(base, other, ok, map[string]string{}); got != NotRepoLayout {
		t.Errorf("another file: %q", got)
	}
}

// TestContainersUnread checks that an inspect that fails or says no JSON finds nothing.
func TestContainersUnread(t *testing.T) {
	for _, out := range []string{"error", "not json"} {
		f := &fake{t: t, has: map[string]bool{"docker": true}, out: map[string]string{"docker ps -a -q --no-trunc": "a1", "docker inspect": out}}
		if got := containers(context.Background(), f.env(), "docker"); got != nil {
			t.Errorf("inspect %q: %+v", out, got)
		}
	}
}

// TestComposeProblemDatabaseRunner checks a 7.2 Runner in database mode: taken over alone in
// its project, its own layout otherwise.
func TestComposeProblemDatabaseRunner(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"docker-compose.yml": "services: {}\n"})
	i := Install{Project: "af", Dir: dir, Database: &DatabaseRunner{ComposeFile: filepath.Join(dir, "docker-compose.yml")}}
	if p, _ := composeProblem(i, &container{}, map[string]string{"rte": "af_rte"}, nil); p != "" {
		t.Errorf("alone: %q", p)
	}
	if p, _ := composeProblem(i, &container{}, map[string]string{"rte": "af_rte", "x": "y"}, nil); p != RunnerLayout {
		t.Errorf("with another service: %q", p)
	}
}

// TestUnreadable checks a compose file that is there, missing, or cannot be opened.
func TestUnreadable(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"file": "x"})
	if p := unreadable(filepath.Join(dir, "file")); p != "" {
		t.Errorf("there: %q", p)
	}
	if p := unreadable(filepath.Join(dir, "missing")); p != MissingComposeFile {
		t.Errorf("missing: %q", p)
	}
	if p := unreadable(filepath.Join(dir, "file", "under")); p != NoComposeFile {
		t.Errorf("under a file: %q", p)
	}
}
