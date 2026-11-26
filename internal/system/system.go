// Package system is the installer's view of the server: who runs it, which container tools
// are there, running a command with its output shown, and installing Docker or Podman when
// neither is.
package system

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// ─── running commands ─────────────────────────────────────────────────────────

// Verbose shows every command before it runs (--verbose).
var Verbose bool

// LogLine shows a line of a command's output (the full screen's log panel ; main sets it to
// ui.Log). By default it is written to stderr, indented.
var LogLine = func(line string) { fmt.Fprintf(os.Stderr, "    %s\n", line) }

// Run runs a command, its output shown as it comes, indented under the installer's lines.
//
// Args:
//
//	ctx: the context, to cancel.
//	dir: the working directory ("" for the current one).
//	name: the program.
//	args: its arguments.
//
// Errors:
//
//	When the program cannot start or exits with an error.
func Run(ctx context.Context, dir, name string, args ...string) error {
	if Verbose {
		LogLine(fmt.Sprintf("$ %s %s", name, strings.Join(args, " ")))
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = nil
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			LogLine(sc.Text())
		}
		close(done)
	}()
	err := cmd.Run()
	pw.Close()
	<-done
	if err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// Output runs a command and returns what it printed, nothing shown.
//
// Args:
//
//	ctx: the context, to cancel.
//	dir: the working directory ("" for the current one).
//	name: the program.
//	args: its arguments.
//
// Returns:
//
//	Its standard output, trimmed.
//
// Errors:
//
//	When it fails, with its error output in the message.
func Output(ctx context.Context, dir, name string, args ...string) (string, error) {
	if Verbose {
		fmt.Fprintf(os.Stderr, "  $ %s %s\n", name, strings.Join(args, " "))
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// Combined runs a command and returns what it printed on its standard output and its error
// output together, nothing shown (a container's log, which a program writes to both).
//
// Args:
//
//	ctx: the context, to cancel.
//	dir: the working directory ("" for the current one).
//	name: the program.
//	args: its arguments.
//
// Returns:
//
//	Both outputs, interleaved as written.
//
// Errors:
//
//	When it fails.
func Combined(ctx context.Context, dir, name string, args ...string) (string, error) {
	if Verbose {
		fmt.Fprintf(os.Stderr, "  $ %s %s\n", name, strings.Join(args, " "))
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return string(out), nil
}

// ToFile runs a command with its standard output written to a file (a database dump).
//
// Args:
//
//	ctx: the context, to cancel.
//	dir: the working directory ("" for the current one).
//	path: the file to write.
//	name: the program.
//	args: its arguments.
//
// Errors:
//
//	When it fails; the file is removed then.
func ToFile(ctx context.Context, dir, path, name string, args ...string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = f
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	runErr := cmd.Run()
	closeErr := f.Close()
	if runErr != nil {
		os.Remove(path)
		return fmt.Errorf("%s: %s", name, strings.TrimSpace(errOut.String()+" "+runErr.Error()))
	}
	return closeErr
}

// FromFile runs a command with a file as its standard input (a database restore).
//
// Args:
//
//	ctx: the context, to cancel.
//	dir: the working directory ("" for the current one).
//	path: the file to read.
//	name: the program.
//	args: its arguments.
//
// Errors:
//
//	When it fails.
func FromFile(ctx context.Context, dir, path, name string, args ...string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = f
	var errOut bytes.Buffer
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %s", name, strings.TrimSpace(errOut.String()+" "+err.Error()))
	}
	return nil
}

// Input runs a command with a text as its standard input and returns what it printed, nothing
// shown (SQL for the database, which may hold passwords: on the standard input they never
// show on a command line, as ps would list it).
//
// Args:
//
//	ctx: the context, to cancel.
//	dir: the working directory ("" for the current one).
//	input: the text written to its standard input.
//	name: the program.
//	args: its arguments.
//
// Returns:
//
//	Its standard output, trimmed.
//
// Errors:
//
//	When it fails, with its error output in the message (never the input).
func Input(ctx context.Context, dir, input, name string, args ...string) (string, error) {
	if Verbose {
		fmt.Fprintf(os.Stderr, "  $ %s %s\n", name, strings.Join(args, " "))
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(input)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// Has tells whether a program is on the PATH.
//
// Args:
//
//	name: the program.
//
// Returns:
//
//	True when it is found.
func Has(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// IsRoot tells whether the installer runs as root.
//
// Returns:
//
//	True for uid 0.
func IsRoot() bool {
	return Geteuid() == 0
}

// Geteuid tells the user the installer runs as; the tests (this package's and others')
// pretend to be root or not.
var Geteuid = os.Geteuid

// ─── container tools ──────────────────────────────────────────────────────────

// Runtime is a container engine the compose layout runs on.
type Runtime string

const (
	Docker Runtime = "docker"
	Podman Runtime = "podman"
)

// Tools is what the server has, found once at the start.
type Tools struct {
	// Docker: the docker command answers and its daemon runs.
	Docker bool
	// DockerCompose: "docker compose" works.
	DockerCompose bool
	// Podman: the podman command works.
	Podman bool
	// PodmanCompose: "podman compose" finds a compose provider.
	PodmanCompose bool
	// Kubectl and Helm: both are needed for Kubernetes, with a cluster kubectl reaches.
	Kubectl bool
	Helm    bool
	Cluster bool
}

// Detect finds the container tools on the server.
//
// Args:
//
//	ctx: the context, to cancel.
//
// Returns:
//
//	What is there.
func Detect(ctx context.Context) Tools {
	var t Tools
	if Has("docker") {
		_, err := Output(ctx, "", "docker", "info", "--format", "{{.ServerVersion}}")
		t.Docker = err == nil
		if t.Docker {
			_, err = Output(ctx, "", "docker", "compose", "version")
			t.DockerCompose = err == nil
		}
	}
	if Has("podman") {
		_, err := Output(ctx, "", "podman", "version", "--format", "{{.Client.Version}}")
		t.Podman = err == nil
		if t.Podman {
			_, err = Output(ctx, "", "podman", "compose", "version")
			t.PodmanCompose = err == nil
		}
	}
	t.Kubectl = Has("kubectl")
	t.Helm = Has("helm")
	if t.Kubectl {
		_, err := Output(ctx, "", "kubectl", "version", "--request-timeout=5s")
		t.Cluster = err == nil
	}
	return t
}

// ComposeReady tells whether a runtime can run the compose layout.
//
// Args:
//
//	r: the runtime.
//
// Returns:
//
//	True when its engine and compose both work.
func (t Tools) ComposeReady(r Runtime) bool {
	if r == Podman {
		return t.Podman && t.PodmanCompose
	}
	return t.Docker && t.DockerCompose
}

// ─── installing a runtime ─────────────────────────────────────────────────────

// OSRelease is what /etc/os-release says about the distribution.
type OSRelease struct {
	ID     string
	IDLike string
	Name   string
}

// OSReleasePath is where the distribution describes itself; the tests write their own.
var OSReleasePath = "/etc/os-release"

// ReadOSRelease reads /etc/os-release.
//
// Returns:
//
//	The distribution; empty fields when the file is missing.
func ReadOSRelease() OSRelease {
	var r OSRelease
	data, err := os.ReadFile(OSReleasePath)
	if err != nil {
		return r
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"'`)
		switch k {
		case "ID":
			r.ID = v
		case "ID_LIKE":
			r.IDLike = v
		case "PRETTY_NAME":
			r.Name = v
		}
	}
	return r
}

// Family tells which package manager a distribution uses.
//
// Returns:
//
//	"apt", "dnf", "zypper" or "" when unknown.
func (r OSRelease) Family() string {
	ids := r.ID + " " + r.IDLike
	switch {
	case strings.Contains(ids, "debian") || strings.Contains(ids, "ubuntu"):
		return "apt"
	case strings.Contains(ids, "rhel") || strings.Contains(ids, "fedora") || strings.Contains(ids, "centos"):
		return "dnf"
	case strings.Contains(ids, "suse"):
		return "zypper"
	}
	return ""
}

// ErrNeedRoot says installing a runtime needs root.
var ErrNeedRoot = errors.New("installing a container runtime needs root: run the installer with sudo")

// InstallDocker installs Docker Engine and its compose plugin with Docker's own script
// (get.docker.com), then starts the service and enables it at boot.
//
// Args:
//
//	ctx: the context, to cancel.
//
// Errors:
//
//	When not root, or a step fails.
func InstallDocker(ctx context.Context) error {
	if !IsRoot() {
		return ErrNeedRoot
	}
	script, err := os.CreateTemp("", "get-docker-*.sh")
	if err != nil {
		return err
	}
	script.Close()
	defer os.Remove(script.Name())
	if err := Run(ctx, "", "curl", "-fsSL", "https://get.docker.com", "-o", script.Name()); err != nil {
		return fmt.Errorf("downloading Docker's install script: %w", err)
	}
	if err := Run(ctx, "", "sh", script.Name()); err != nil {
		return fmt.Errorf("installing Docker: %w", err)
	}
	if Has("systemctl") {
		if err := Run(ctx, "", "systemctl", "enable", "--now", "docker"); err != nil {
			return fmt.Errorf("starting Docker: %w", err)
		}
	}
	return nil
}

// InstallPodman installs Podman and a compose provider from the distribution's packages.
//
// Args:
//
//	ctx: the context, to cancel.
//
// Errors:
//
//	When not root, the distribution is not known, or a step fails.
func InstallPodman(ctx context.Context) error {
	if !IsRoot() {
		return ErrNeedRoot
	}
	osr := ReadOSRelease()
	switch osr.Family() {
	case "apt":
		if err := Run(ctx, "", "apt-get", "update"); err != nil {
			return err
		}
		return Run(ctx, "", "apt-get", "install", "-y", "podman", "podman-compose")
	case "dnf":
		if err := Run(ctx, "", "dnf", "install", "-y", "podman"); err != nil {
			return err
		}
		// podman-compose is in Fedora itself, and in EPEL on RHEL and its rebuilds
		if err := Run(ctx, "", "dnf", "install", "-y", "podman-compose"); err != nil {
			if err2 := Run(ctx, "", "dnf", "install", "-y", "epel-release"); err2 != nil {
				return fmt.Errorf("podman-compose is in EPEL, which could not be enabled: %w", err2)
			}
			return Run(ctx, "", "dnf", "install", "-y", "podman-compose")
		}
		return nil
	case "zypper":
		return Run(ctx, "", "zypper", "--non-interactive", "install", "podman", "podman-compose")
	}
	return fmt.Errorf("installing Podman on %s is not supported: install podman and podman-compose, then run the installer again", osr.Name)
}

// ─── packages and services ────────────────────────────────────────────────────

// Packages names a set of packages in each package manager's own words.
type Packages struct {
	Apt, Dnf, Zypper []string
}

// InstallPackages installs packages from the distribution's repositories.
//
// Args:
//
//	ctx: the context, to cancel.
//	p: the packages, per package manager.
//
// Errors:
//
//	When not root, the distribution is not known, or the package manager fails.
func InstallPackages(ctx context.Context, p Packages) error {
	if !IsRoot() {
		return ErrNeedRoot
	}
	osr := ReadOSRelease()
	switch osr.Family() {
	case "apt":
		if len(p.Apt) == 0 {
			return nil
		}
		if err := Run(ctx, "", "apt-get", "update"); err != nil {
			return err
		}
		return Run(ctx, "", "env", append([]string{"DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "--no-install-recommends"}, p.Apt...)...)
	case "dnf":
		if len(p.Dnf) == 0 {
			return nil
		}
		return Run(ctx, "", "dnf", append([]string{"install", "-y"}, p.Dnf...)...)
	case "zypper":
		if len(p.Zypper) == 0 {
			return nil
		}
		return Run(ctx, "", "zypper", append([]string{"--non-interactive", "install", "--no-recommends"}, p.Zypper...)...)
	}
	return fmt.Errorf("installing packages on %s is not supported", osr.Name)
}

// Arch is the server's architecture as the downloads name it.
//
// Returns:
//
//	"amd64", "arm64", or "" for another one.
func Arch() string {
	switch GOARCH {
	case "amd64", "arm64":
		return GOARCH
	}
	return ""
}

// GOARCH is the architecture the installer was built for; the tests try others.
var GOARCH = runtime.GOARCH
