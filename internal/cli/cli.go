// Package cli is what the installer does for each command: the questions it asks, in order,
// and the compose or Kubernetes work that follows.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/ansibleforms/installer/internal/i18n"
	"github.com/ansibleforms/installer/internal/releases"
	"github.com/ansibleforms/installer/internal/ui"
)

// Flags are the command line's answers; an empty one is asked (or defaulted unattended).
type Flags struct {
	InstallerVersion string

	Lang, Version, Role, Mode, Runtime string
	InstallRuntime                     bool
	Dir, Name                          string
	Port                               int
	TLS, Cert, Key                     string
	AdminPassword, JoinToken, Host     string
	RunnerMode, AppURL                 string
	Namespace, Release, Hostname       string
	IngressClass, StorageClass, Values string
	Purge, Yes                         bool
}

// Run runs a command.
//
// Args:
//
//	ctx: the context, to cancel (Ctrl+C).
//	command: install, upgrade, status, backup, release-notes, join-token, uninstall or version.
//	f: the flags.
//
// Errors:
//
//	What went wrong, for the person to read.
func Run(ctx context.Context, command string, f Flags) error {
	if command == "version" {
		fmt.Println(f.InstallerVersion)
		return nil
	}
	installerVersion = f.InstallerVersion
	applyLanguage()
	ui.Banner(i18n.T("app.title"), i18n.T("app.subtitle", "version", f.InstallerVersion))
	// install and upgrade ask it as their first step ; the others only when the locale names none
	wizard := command == "install" || command == "upgrade"
	if !wizard && !ui.Unattended && f.Lang == "" && i18n.FromEnv() == "" {
		if err := chooseLanguage(nil); err != nil {
			return err
		}
	}
	switch command {
	case "install":
		return install(ctx, f)
	case "upgrade":
		return upgrade(ctx, f)
	case "status":
		return status(ctx)
	case "backup":
		return backup(ctx)
	case "release-notes":
		return releaseNotes(ctx, f)
	case "uninstall":
		return uninstall(ctx, f)
	}
	return errors.New(i18n.T("app.unknownCommand", "command", command))
}

// installerVersion is this installer's version, for the header after a change of language.
var installerVersion string

// chooseLanguage lets the person pick the installer's language, the server's locale (or the
// one before) chosen first, each language named in itself.
//
// Args:
//
//	relabel: renames the wizard's steps in the new language (nil outside a wizard).
//
// Errors:
//
//	ErrAborted when stopped.
func chooseLanguage(relabel func()) error {
	opts := make([]ui.Option, len(i18n.Languages))
	for i, l := range i18n.Languages {
		opts[i] = ui.Option{Label: l.Label, Value: l.Code}
	}
	ui.Explain(i18n.T("language.help"))
	code, err := ui.Select(i18n.T("language.title"), opts, i18n.Current())
	if err != nil {
		return err
	}
	i18n.Set(code)
	applyLanguage()
	if relabel != nil {
		relabel()
	}
	for _, l := range i18n.Languages {
		if l.Code == code {
			ui.Answered(l.Label)
		}
	}
	return nil
}

// applyLanguage puts the full screen's header and key bar in the chosen language.
func applyLanguage() {
	words := map[string]string{}
	for _, w := range []string{"choose", "next", "answer", "scroll", "back", "quit", "stop", "close"} {
		words[w] = i18n.T("keys." + w)
	}
	words["go on"] = i18n.T("keys.goOn")
	ui.Language(i18n.T("app.header", "version", installerVersion), words)
}

// ─── versions ─────────────────────────────────────────────────────────────────

// targetVersion is the version to install or upgrade to: --version, else this installer's,
// else (a development build) the latest release.
//
// Args:
//
//	ctx: the context, to cancel.
//	f: the flags.
//
// Returns:
//
//	The version.
//
// Errors:
//
//	When none can be found.
func targetVersion(ctx context.Context, f Flags) (releases.Version, error) {
	if f.Version != "" {
		return releases.ParseVersion(f.Version)
	}
	if v, err := releases.ParseVersion(f.InstallerVersion); err == nil {
		return v, nil
	}
	list, err := releases.ListAppVersions(ctx)
	if err != nil {
		return releases.Version{}, err
	}
	if len(list) == 0 {
		return releases.Version{}, errors.New(i18n.T("app.noRelease"))
	}
	return list[0], nil
}

// choice asks to pick a value the cluster offers, or takes the flag's.
//
// Args:
//
//	flagValue: the flag.
//	title: the question.
//	values: the values offered ("" reads "the cluster's default").
//	def: the default when the cluster offers none.
//
// Returns:
//
//	The value.
//
// Errors:
//
//	ErrAborted when stopped.
func choice(flagValue, title string, values []string, def string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if len(values) == 0 {
		return text("", title, def, nil)
	}
	opts := make([]ui.Option, len(values))
	for i, v := range values {
		label := v
		if v == "" {
			label = i18n.T("kube.default")
		}
		opts[i] = ui.Option{Label: label, Value: v}
	}
	return ui.Select(title, opts, values[0])
}

// ─── the answers' checks ──────────────────────────────────────────────────────

// text takes a flag's value, or asks for a text.
//
// Args:
//
//	flagValue: the flag.
//	title: the question.
//	def: the default.
//	validate: the check, nil for none.
//
// Returns:
//
//	The text.
//
// Errors:
//
//	ErrAborted when stopped, or the check's error for a flag that fails it.
func text(flagValue, title, def string, validate func(string) error) (string, error) {
	if flagValue != "" {
		if validate != nil {
			if err := validate(flagValue); err != nil {
				return "", err
			}
		}
		return flagValue, nil
	}
	v, err := ui.Input(title, def, false, validate)
	if err == nil && validate != nil {
		err = validate(v)
	}
	return v, err
}

// number takes a flag's port, or asks for one.
//
// Args:
//
//	flagValue: the flag (0 when not given).
//	title: the question.
//	def: the default.
//
// Returns:
//
//	The port.
//
// Errors:
//
//	ErrAborted when stopped.
func number(flagValue int, title string, def int) (int, error) {
	if flagValue != 0 {
		return flagValue, nil
	}
	s, err := text("", title, strconv.Itoa(def), validPort)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(s)
}

func notEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New(i18n.T("check.required"))
	}
	return nil
}

func validName(s string) error {
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return errors.New(i18n.T("check.name"))
		}
	}
	return notEmpty(s)
}

func validURL(s string) error {
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return errors.New(i18n.T("check.url"))
	}
	return nil
}

func validPort(s string) error {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 1 || n > 65535 {
		return errors.New(i18n.T("check.port"))
	}
	return nil
}

func fileExists(s string) error {
	if _, err := os.Stat(s); err != nil {
		return errors.New(i18n.T("check.file", "file", s))
	}
	return nil
}
