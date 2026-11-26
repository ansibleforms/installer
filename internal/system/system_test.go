package system

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ansibleforms/installer/internal/fakebin"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// as runs the test as root or as a user, the way IsRoot sees it.
//
// Args:
//
//	t: the test.
//	root: true for root.
func as(t *testing.T, root bool) {
	t.Helper()
	was := Geteuid
	Geteuid = func() int {
		if root {
			return 0
		}
		return 1000
	}
	t.Cleanup(func() { Geteuid = was })
}

// distribution writes the os-release the test runs on.
//
// Args:
//
//	t: the test.
//	content: the file's text ; "" : no file at all.
func distribution(t *testing.T, content string) {
	t.Helper()
	was := OSReleasePath
	OSReleasePath = filepath.Join(t.TempDir(), "os-release")
	if content != "" {
		if err := os.WriteFile(OSReleasePath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { OSReleasePath = was })
}

// quiet keeps the lines of the commands from the test's output, and collects them.
//
// Args:
//
//	t: the test.
//	verbose: --verbose.
//
// Returns:
//
//	The lines shown.
func quiet(t *testing.T, verbose bool) *[]string {
	t.Helper()
	var lines []string
	wasLine, wasVerbose := LogLine, Verbose
	LogLine = func(l string) { lines = append(lines, l) }
	Verbose = verbose
	t.Cleanup(func() { LogLine, Verbose = wasLine, wasVerbose })
	return &lines
}

// ─── running commands ─────────────────────────────────────────────────────────

// TestRun checks that a command's lines are shown as they come, the command itself with
// --verbose, and a failure names the command.
func TestRun(t *testing.T) {
	b := fakebin.New(t)
	b.Add(t, "tool", `echo one; echo two >&2; test "$1" = ok`)
	lines := quiet(t, true)
	if err := Run(context.Background(), t.TempDir(), "tool", "ok"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*lines, "|") != "$ tool ok|one|two" {
		t.Errorf("lines %q", *lines)
	}
	if err := Run(context.Background(), "", "tool", "bad"); err == nil || !strings.Contains(err.Error(), "tool bad") {
		t.Errorf("a failure: %v", err)
	}
}

// TestLogLine checks the default line goes to stderr, indented.
func TestLogLine(t *testing.T) {
	r, w, _ := os.Pipe()
	was := os.Stderr
	os.Stderr = w
	LogLine("hello")
	os.Stderr = was
	w.Close()
	buf := make([]byte, 64)
	n, _ := r.Read(buf)
	if string(buf[:n]) != "    hello\n" {
		t.Errorf("line %q", buf[:n])
	}
}

// TestOutput checks the output read and trimmed, and a failure with or without an error
// output.
func TestOutput(t *testing.T) {
	b := fakebin.New(t)
	b.Add(t, "tool", `case "$1" in ok) echo "  out  " ;; said) echo nope >&2; exit 1 ;; *) exit 2 ;; esac`)
	quiet(t, true)
	stderr := os.Stderr
	os.Stderr, _ = os.Open(os.DevNull)
	defer func() { os.Stderr = stderr }()
	if out, err := Output(context.Background(), "", "tool", "ok"); err != nil || out != "out" {
		t.Errorf("ok: %q, %v", out, err)
	}
	if _, err := Output(context.Background(), "", "tool", "said"); err == nil || !strings.HasSuffix(err.Error(), ": nope") {
		t.Errorf("said: %v", err)
	}
	if _, err := Output(context.Background(), "", "tool", "silent"); err == nil || !strings.Contains(err.Error(), "exit status 2") {
		t.Errorf("silent: %v", err)
	}
}

// TestCombined checks both outputs together, and a failure that still hands them over.
func TestCombined(t *testing.T) {
	b := fakebin.New(t)
	b.Add(t, "tool", `echo a; echo b >&2; test "$1" = ok`)
	quiet(t, true)
	stderr := os.Stderr
	os.Stderr, _ = os.Open(os.DevNull)
	defer func() { os.Stderr = stderr }()
	if out, err := Combined(context.Background(), "", "tool", "ok"); err != nil || out != "a\nb\n" {
		t.Errorf("ok: %q, %v", out, err)
	}
	if out, err := Combined(context.Background(), "", "tool", "bad"); err == nil || out != "a\nb\n" {
		t.Errorf("bad: %q, %v", out, err)
	}
}

// TestToFromFile checks a dump written to a file only its owner reads, removed when the
// command fails, and a restore fed from a file.
func TestToFromFile(t *testing.T) {
	b := fakebin.New(t)
	b.Add(t, "dump", `echo data; test "$1" = ok || { echo broken >&2; exit 1; }`)
	b.Add(t, "restore", `read line; test "$line" = data || { echo "got $line" >&2; exit 1; }`)
	dir := t.TempDir()
	file := filepath.Join(dir, "dump.sql")
	if err := ToFile(context.Background(), "", file, "dump", "ok"); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode())
	}
	if err := FromFile(context.Background(), "", file, "restore"); err != nil {
		t.Errorf("restore: %v", err)
	}
	if err := ToFile(context.Background(), "", file, "dump", "bad"); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Errorf("a failed dump: %v", err)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Error("the failed dump's file stayed")
	}
	if err := ToFile(context.Background(), "", filepath.Join(dir, "no", "such.sql"), "dump", "ok"); err == nil {
		t.Error("a file that cannot be made")
	}
	other := filepath.Join(dir, "other.sql")
	_ = os.WriteFile(other, []byte("other\n"), 0o600)
	if err := FromFile(context.Background(), "", other, "restore"); err == nil || !strings.Contains(err.Error(), "got other") {
		t.Errorf("a failed restore: %v", err)
	}
	if err := FromFile(context.Background(), "", filepath.Join(dir, "missing"), "restore"); err == nil {
		t.Error("a missing file")
	}
}

// TestInput checks a text fed on the standard input, and a failure with or without an error
// output, never showing the input.
func TestInput(t *testing.T) {
	b := fakebin.New(t)
	b.Add(t, "sql", `read line; case "$line" in ok) echo " done " ;; said) echo refused >&2; exit 1 ;; *) exit 4 ;; esac`)
	quiet(t, true)
	stderr := os.Stderr
	os.Stderr, _ = os.Open(os.DevNull)
	defer func() { os.Stderr = stderr }()
	if out, err := Input(context.Background(), "", "ok\n", "sql"); err != nil || out != "done" {
		t.Errorf("ok: %q, %v", out, err)
	}
	if _, err := Input(context.Background(), "", "said\n", "sql"); err == nil || !strings.HasSuffix(err.Error(), "refused") {
		t.Errorf("said: %v", err)
	}
	if _, err := Input(context.Background(), "", "secret\n", "sql"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("silent: %v", err)
	}
}

// TestHasIsRoot checks a program found on the PATH, and who runs the installer.
func TestHasIsRoot(t *testing.T) {
	b := fakebin.New(t)
	b.Add(t, "docker", "")
	if !Has("docker") || Has("podman") {
		t.Error("Has")
	}
	as(t, true)
	if !IsRoot() {
		t.Error("root")
	}
	as(t, false)
	if IsRoot() {
		t.Error("a user")
	}
}

// ─── container tools ──────────────────────────────────────────────────────────

// TestDetect checks each tool found only when it answers, and compose only on an engine that
// works.
func TestDetect(t *testing.T) {
	quiet(t, false)
	b := fakebin.New(t)
	if got := Detect(context.Background()); got != (Tools{}) {
		t.Errorf("nothing: %+v", got)
	}
	b.Add(t, "docker", "")
	b.Add(t, "podman", "")
	b.Add(t, "kubectl", "")
	b.Add(t, "helm", "")
	want := Tools{Docker: true, DockerCompose: true, Podman: true, PodmanCompose: true, Kubectl: true, Helm: true, Cluster: true}
	if got := Detect(context.Background()); got != want {
		t.Errorf("everything: %+v", got)
	}
	b.Add(t, "docker", `test "$1" = info`)
	b.Add(t, "podman", `test "$1" = version`)
	b.Add(t, "kubectl", "exit 1")
	want = Tools{Docker: true, Podman: true, Kubectl: true, Helm: true}
	if got := Detect(context.Background()); got != want {
		t.Errorf("no compose, no cluster: %+v", got)
	}
	b.Add(t, "docker", "exit 1")
	b.Add(t, "podman", "exit 1")
	if got := Detect(context.Background()); got.Docker || got.Podman {
		t.Errorf("engines down: %+v", got)
	}
}

// TestComposeReady checks each runtime needs its engine and its compose.
func TestComposeReady(t *testing.T) {
	tools := Tools{Docker: true, DockerCompose: true, Podman: true}
	if !tools.ComposeReady(Docker) || tools.ComposeReady(Podman) {
		t.Errorf("%+v", tools)
	}
}

// ─── installing a runtime ─────────────────────────────────────────────────────

// TestOSRelease checks the file read, quotes dropped, and the package manager of each family.
func TestOSRelease(t *testing.T) {
	distribution(t, "")
	if ReadOSRelease() != (OSRelease{}) {
		t.Error("no file")
	}
	distribution(t, "# comment\nID=rocky\nID_LIKE=\"rhel centos fedora\"\nPRETTY_NAME='Rocky Linux 9'\nVERSION_ID=9\n")
	r := ReadOSRelease()
	if r.ID != "rocky" || r.IDLike != "rhel centos fedora" || r.Name != "Rocky Linux 9" || r.Family() != "dnf" {
		t.Errorf("%+v", r)
	}
	for ids, want := range map[[2]string]string{
		{"ubuntu", "debian"}:      "apt",
		{"debian", ""}:            "apt",
		{"opensuse-leap", "suse"}: "zypper",
		{"fedora", ""}:            "dnf",
		{"alpine", ""}:            "",
	} {
		if got := (OSRelease{ID: ids[0], IDLike: ids[1]}).Family(); got != want {
			t.Errorf("%v: %q, want %q", ids, got, want)
		}
	}
}

// TestInstallDocker checks root is needed, each step that fails says so, and the service is
// started where systemd runs.
func TestInstallDocker(t *testing.T) {
	quiet(t, false)
	ctx := context.Background()
	as(t, false)
	if err := InstallDocker(ctx); !errors.Is(err, ErrNeedRoot) {
		t.Errorf("a user: %v", err)
	}
	as(t, true)
	b := fakebin.New(t)
	b.Add(t, "curl", "exit 1")
	if err := InstallDocker(ctx); err == nil || !strings.Contains(err.Error(), "downloading") {
		t.Errorf("curl: %v", err)
	}
	b.Add(t, "curl", "")
	b.Add(t, "sh", "exit 1")
	if err := InstallDocker(ctx); err == nil || !strings.Contains(err.Error(), "installing Docker") {
		t.Errorf("the script: %v", err)
	}
	b.Add(t, "sh", "")
	if err := InstallDocker(ctx); err != nil || b.Called("systemctl") {
		t.Errorf("without systemd: %v", err)
	}
	b.Add(t, "systemctl", "exit 1")
	if err := InstallDocker(ctx); err == nil || !strings.Contains(err.Error(), "starting Docker") {
		t.Errorf("systemctl: %v", err)
	}
	b.Add(t, "systemctl", "")
	if err := InstallDocker(ctx); err != nil || !b.Called("systemctl enable --now docker") {
		t.Errorf("with systemd: %v", err)
	}
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	if err := InstallDocker(ctx); err == nil {
		t.Error("no temporary folder")
	}
}

// TestInstallPodman checks each family's packages, EPEL enabled when podman-compose needs it,
// and an unknown distribution refused.
func TestInstallPodman(t *testing.T) {
	quiet(t, false)
	ctx := context.Background()
	as(t, false)
	if err := InstallPodman(ctx); !errors.Is(err, ErrNeedRoot) {
		t.Errorf("a user: %v", err)
	}
	as(t, true)
	b := fakebin.New(t)

	distribution(t, "ID=ubuntu\n")
	b.Add(t, "apt-get", `test "$1" = install`)
	if err := InstallPodman(ctx); err == nil {
		t.Error("apt-get update failed")
	}
	b.Add(t, "apt-get", "")
	if err := InstallPodman(ctx); err != nil || !b.Called("apt-get install -y podman podman-compose") {
		t.Errorf("apt: %v", err)
	}

	distribution(t, "ID=rhel\n")
	b.Add(t, "dnf", "exit 1")
	if err := InstallPodman(ctx); err == nil {
		t.Error("dnf failed")
	}
	b.Add(t, "dnf", "")
	if err := InstallPodman(ctx); err != nil || b.Called("dnf install -y epel-release") {
		t.Errorf("dnf: %v", err)
	}
	b.Add(t, "dnf", `case "$*" in "install -y podman-compose") exit 1 ;; "install -y epel-release") exit 1 ;; esac`)
	if err := InstallPodman(ctx); err == nil || !strings.Contains(err.Error(), "EPEL") {
		t.Errorf("no EPEL: %v", err)
	}
	b.Add(t, "dnf", `case "$*" in "install -y podman-compose") test -e "$FAKEBIN_LOG.epel" || { echo > "$FAKEBIN_LOG.epel"; exit 1; } ;; esac`)
	if err := InstallPodman(ctx); err != nil || !b.Called("dnf install -y epel-release") {
		t.Errorf("with EPEL: %v", err)
	}

	distribution(t, "ID=opensuse-tumbleweed\nID_LIKE=\"opensuse suse\"\n")
	b.Add(t, "zypper", "")
	if err := InstallPodman(ctx); err != nil {
		t.Errorf("zypper: %v", err)
	}
	distribution(t, "ID=alpine\nPRETTY_NAME=Alpine\n")
	if err := InstallPodman(ctx); err == nil || !strings.Contains(err.Error(), "Alpine") {
		t.Errorf("alpine: %v", err)
	}
}

// TestInstallPackages checks each family's command, nothing run when a family needs nothing,
// and an unknown distribution refused.
func TestInstallPackages(t *testing.T) {
	quiet(t, false)
	ctx := context.Background()
	p := Packages{Apt: []string{"a"}, Dnf: []string{"d"}, Zypper: []string{"z"}}
	as(t, false)
	if err := InstallPackages(ctx, p); !errors.Is(err, ErrNeedRoot) {
		t.Errorf("a user: %v", err)
	}
	as(t, true)
	b := fakebin.New(t)
	b.Add(t, "env", "")
	b.Add(t, "dnf", "")
	b.Add(t, "zypper", "")
	for _, c := range []struct{ os, call string }{
		{"ID=debian\n", "env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends a"},
		{"ID=fedora\n", "dnf install -y d"},
		{"ID=sles\nID_LIKE=suse\n", "zypper --non-interactive install --no-recommends z"},
	} {
		distribution(t, c.os)
		b.Add(t, "apt-get", "")
		if err := InstallPackages(ctx, p); err != nil || !b.Called(c.call) {
			t.Errorf("%s: %v, calls %q", c.os, err, b.Calls())
		}
		if err := InstallPackages(ctx, Packages{}); err != nil {
			t.Errorf("%s with nothing: %v", c.os, err)
		}
	}
	distribution(t, "ID=debian\n")
	b.Add(t, "apt-get", "exit 1")
	if err := InstallPackages(ctx, p); err == nil {
		t.Error("apt-get update failed")
	}
	distribution(t, "ID=arch\nPRETTY_NAME=Arch\n")
	if err := InstallPackages(ctx, p); err == nil || !strings.Contains(err.Error(), "Arch") {
		t.Errorf("arch: %v", err)
	}
}

// TestArch checks the two architectures the downloads have, and none for another.
func TestArch(t *testing.T) {
	was := GOARCH
	defer func() { GOARCH = was }()
	for arch, want := range map[string]string{"amd64": "amd64", "arm64": "arm64", "riscv64": ""} {
		GOARCH = arch
		if got := Arch(); got != want {
			t.Errorf("%s: %q", arch, got)
		}
	}
}
