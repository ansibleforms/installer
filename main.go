// Command ansibleforms-installer installs, upgrades and looks after AnsibleForms on a
// server: Docker Compose on Docker or Podman, or the Helm chart on Kubernetes; a controller
// (the whole application) or an RTE runner for a controller elsewhere.
//
//	curl -fsSL https://ansibleforms.com/install | sh
//	ansibleforms-installer [command] [flags]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ansibleforms/installer/internal/cli"
	"github.com/ansibleforms/installer/internal/compose"
	"github.com/ansibleforms/installer/internal/i18n"
	"github.com/ansibleforms/installer/internal/system"
	"github.com/ansibleforms/installer/internal/ui"
)

// version is set at build time (goreleaser: -X main.version=7.3.0); it is the AnsibleForms
// version the installer installs by default.
var version = "dev"

// usage lists the commands and flags (--help).
func usage() {
	fmt.Fprintf(os.Stderr, `%s

  ansibleforms-installer [command] [flags]

%s
  install        %s
  upgrade        %s
  status         %s
  backup         %s
  release-notes  %s
  uninstall      %s
  version        %s

%s
`, i18n.T("usage.title"), i18n.T("usage.commands"),
		i18n.T("usage.install"), i18n.T("usage.upgrade"), i18n.T("usage.status"), i18n.T("usage.backup"),
		i18n.T("usage.releaseNotes"), i18n.T("usage.uninstall"), i18n.T("usage.version"),
		i18n.T("usage.flags"))
	printFlags()
}

// printFlags lists the flags with two hyphens (--version), as they are written everywhere
// else ; Go's own list shows one.
func printFlags() {
	flag.VisitAll(func(f *flag.Flag) {
		kind, text := flag.UnquoteUsage(f)
		line := "  --" + f.Name
		if kind != "" {
			line += " " + kind
		}
		fmt.Fprintf(os.Stderr, "%s\n        %s\n", line, text)
	})
}

func main() {
	// the language first, so the help is in it too
	if code := i18n.FromEnv(); code != "" {
		i18n.Set(code)
	}
	var o cli.Flags
	flag.StringVar(&o.Lang, "lang", "", "language: ca de en es fr it ja nl pl pt zh")
	flag.StringVar(&o.Version, "version", "", "AnsibleForms version to install or upgrade to (default: this installer's)")
	flag.StringVar(&o.Role, "role", "", "controller or runner")
	flag.StringVar(&o.Mode, "mode", "", "compose or kubernetes")
	flag.StringVar(&o.Runtime, "runtime", "", "docker or podman (compose)")
	flag.BoolVar(&o.InstallRuntime, "install-runtime", false, "install Docker or Podman when missing, without asking")
	flag.StringVar(&o.Dir, "dir", "", "installation folder (compose)")
	flag.StringVar(&o.Name, "name", "", "instance name, its containers' prefix (compose, default af)")
	flag.IntVar(&o.Port, "port", 0, "port on the host (controller: web, runner: RTE)")
	flag.StringVar(&o.TLS, "tls", "", "self, own or off (compose controller)")
	flag.StringVar(&o.Cert, "cert", "", "certificate file (--tls own)")
	flag.StringVar(&o.Key, "key", "", "key file (--tls own)")
	flag.StringVar(&o.AdminPassword, "admin-password", "", "first admin password (default: generated)")
	flag.StringVar(&o.JoinToken, "join", "", "join string or join token from Connections > Runners > Join tokens (runner)")
	flag.StringVar(&o.AppURL, "app-url", "", "the app's address, https://forms.example.com (runner)")
	flag.StringVar(&o.RunnerMode, "runner-mode", "", "outbound (the RTE dials the app) or inbound (the app dials the RTE) (runner)")
	flag.StringVar(&o.Host, "host", "", "this server's address the app dials (inbound runner)")
	flag.StringVar(&o.Namespace, "namespace", "", "Kubernetes namespace (default ansibleforms)")
	flag.StringVar(&o.Release, "release", "", "Helm release (default ansibleforms)")
	flag.StringVar(&o.Hostname, "hostname", "", "ingress hostname (kubernetes)")
	flag.StringVar(&o.IngressClass, "ingress-class", "", "ingress class (kubernetes)")
	flag.StringVar(&o.StorageClass, "storage-class", "", "storage class (kubernetes)")
	flag.StringVar(&o.Values, "values", "", "extra Helm values file (kubernetes)")
	flag.BoolVar(&o.Purge, "purge", false, "uninstall: also delete the data")
	flag.StringVar(&compose.Source, "source", "", "a local copy of the ansibleforms/docker repository to install from (air-gapped)")
	flag.BoolVar(&o.Yes, "yes", false, "answer every question with its default (unattended)")
	flag.BoolVar(&system.Verbose, "verbose", false, "show every command run")
	flag.Usage = usage

	// the command, before or after the flags (ansibleforms-installer upgrade --yes)
	args := os.Args[1:]
	command := "install"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		command, args = args[0], args[1:]
	}
	if err := flag.CommandLine.Parse(args); err != nil {
		os.Exit(2)
	}
	if rest := flag.Args(); len(rest) > 0 {
		command = rest[0]
	}
	if o.Lang != "" {
		i18n.Set(o.Lang)
	}
	o.InstallerVersion = version
	// no terminal (a pipe without /dev/tty, CI) : unattended
	ui.Unattended = o.Yes || !isTerminal()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// install and upgrade in the full-screen wizard when the terminal fits it ; the other
	// commands, a small terminal and --yes write plain lines
	system.LogLine = ui.Log
	if command == "install" || command == "upgrade" || command == "" {
		ui.StartFullscreen(i18n.T("app.header", "version", version), "")
	}
	err := cli.Run(ctx, command, o)
	ui.Close(err)
	switch {
	case err == nil:
	case errors.Is(err, ui.ErrAborted) || errors.Is(err, context.Canceled):
		ui.Warn(i18n.T("app.aborted"))
		os.Exit(130)
	default:
		ui.Fail(err.Error())
		os.Exit(1)
	}
}

// isTerminal tells whether the installer can ask questions: its input is a terminal.
//
// Returns:
//
//	True for a terminal.
func isTerminal() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
