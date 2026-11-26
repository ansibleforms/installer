package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ownConfig keeps the state in a folder of the test's own.
//
// Args:
//
//	t: the test.
//
// Returns:
//
//	The state file's path.
func ownConfig(t *testing.T) string {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("as root the state is /etc/ansibleforms/installer.json")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return filepath.Join(dir, "ansibleforms", "installer.json")
}

// TestPath checks that a user's state is under their own config folder.
func TestPath(t *testing.T) {
	want := ownConfig(t)
	if Path() != want {
		t.Errorf("Path = %s, want %s", Path(), want)
	}
}

// TestSaveLoadRemove checks a state written, read back as it was, readable by its owner
// only, and forgotten.
func TestSaveLoadRemove(t *testing.T) {
	ownConfig(t)
	if _, err := Load(); !errors.Is(err, ErrNone) {
		t.Fatalf("no state yet: %v", err)
	}
	if err := Remove(); err != nil {
		t.Errorf("removing no state: %v", err)
	}
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	s := &State{Mode: Compose, Role: "controller", Version: "7.3.0", InstalledAt: at, UpdatedAt: at, Runtime: "docker", Dir: "/srv/af", Name: "af", Port: 443, HTTPS: true}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(Path())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("the file: %v, %v", st, err)
	}
	got, err := Load()
	if err != nil || *got != *s {
		t.Errorf("read back %+v, %v", got, err)
	}
	if err := Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); !errors.Is(err, ErrNone) {
		t.Errorf("after Remove: %v", err)
	}
}

// TestLoadBroken checks that a file that is no state says where it is.
func TestLoadBroken(t *testing.T) {
	p := ownConfig(t)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || errors.Is(err, ErrNone) {
		t.Errorf("a broken file: %v", err)
	}
}

// TestPathRootAndNoConfig checks root's state under /etc, and a user without a config folder
// under ~/.config.
func TestPathRootAndNoConfig(t *testing.T) {
	was := geteuid
	defer func() { geteuid = was }()
	geteuid = func() int { return 0 }
	if Path() != "/etc/ansibleforms/installer.json" {
		t.Errorf("root: %s", Path())
	}
	geteuid = func() int { return 1000 }
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if Path() != filepath.Join(".config", "ansibleforms", "installer.json") {
		t.Errorf("no config folder: %s", Path())
	}
}

// TestUnreadableAndUnwritable checks a state that is a folder, and one whose folder is a file.
func TestUnreadableAndUnwritable(t *testing.T) {
	p := ownConfig(t)
	if err := os.MkdirAll(filepath.Join(p, "inside"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || errors.Is(err, ErrNone) {
		t.Errorf("a folder: %v", err)
	}
	if err := Remove(); err == nil {
		t.Error("a folder that is not empty removed")
	}
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "file"))
	_ = os.WriteFile(filepath.Join(dir, "file"), nil, 0o600)
	if err := (&State{}).Save(); err == nil {
		t.Error("saved under a file")
	}
}
