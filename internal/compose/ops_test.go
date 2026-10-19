package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// a folder's files, by name and content
func folder(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// a project taken over from docker-compose.yaml has it renamed docker-compose.yml, and the
// compose files compose would not read moved aside: the upgrade's file is then the one read
func TestRepoComposeName(t *testing.T) {
	dir := folder(t, map[string]string{"docker-compose.yaml": "read", "docker-compose.yml": "stale"})
	if err := repoComposeName(dir); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "docker-compose.yml")); string(data) != "read" {
		t.Errorf("docker-compose.yml holds %q, want the file compose read", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "docker-compose.yaml")); err == nil {
		t.Errorf("docker-compose.yaml left beside it")
	}
	entries, _ := os.ReadDir(dir)
	aside := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "docker-compose.yml.") && strings.HasSuffix(e.Name(), ".bak") {
			aside++
		}
	}
	if aside != 1 {
		t.Errorf("%d files moved aside, want 1: %v", aside, entries)
	}
}

// docker-compose.yml alone, or no compose file: nothing changes
func TestRepoComposeNameUnchanged(t *testing.T) {
	dir := folder(t, map[string]string{"docker-compose.yml": "x"})
	if err := repoComposeName(dir); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("files changed: %v", entries)
	}
	if err := repoComposeName(t.TempDir()); err != nil {
		t.Errorf("no compose file: %v", err)
	}
}
