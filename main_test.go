package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"testing"

	"github.com/ansibleforms/installer/internal/cli"
	"github.com/ansibleforms/installer/internal/i18n"
	"github.com/ansibleforms/installer/internal/ui"
)

// exited is how a test sees the installer end with a code.
type exited int

// launch runs main with arguments, its command replaced, and tells how it ended.
//
// Args:
//
//	t: the test.
//	run: what the command does.
//	args: the command line, without the program.
//
// Returns:
//
//	The flags the command got, and the exit code (-1 : main returned).
func launch(t *testing.T, run func(cli.Flags) error, args ...string) (got cli.Flags, code int) {
	t.Helper()
	wasArgs, wasExit, wasRun, wasFlags := os.Args, exit, runCommand, flag.CommandLine
	t.Cleanup(func() { os.Args, exit, runCommand, flag.CommandLine = wasArgs, wasExit, wasRun, wasFlags })
	os.Args = append([]string{"ansibleforms-installer"}, args...)
	flag.CommandLine = flag.NewFlagSet("ansibleforms-installer", flag.ContinueOnError)
	flag.CommandLine.SetOutput(new(nopWriter))
	exit = func(c int) { panic(exited(c)) }
	runCommand = func(_ context.Context, _ string, f cli.Flags) error { got = f; return run(f) }
	code = -1
	defer func() {
		if r := recover(); r != nil {
			code = int(r.(exited))
		}
		ui.Fullscreen, ui.Unattended = false, false
	}()
	main()
	return got, code
}

// nopWriter swallows the flags' own messages.
type nopWriter struct{}

// Write writes nothing.
func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// TestMainCommand checks the command and its flags read before or after the command, the
// language from the environment or a flag, and the exit code of each outcome.
func TestMainCommand(t *testing.T) {
	ok := func(cli.Flags) error { return nil }
	t.Setenv("LANG", "de_DE.UTF-8")
	f, code := launch(t, ok, "status", "--yes", "--port", "8443")
	if code != -1 || !f.Yes || f.Port != 8443 || i18n.Current() != "de" {
		t.Errorf("status: %+v, %d, %s", f, code, i18n.Current())
	}
	t.Setenv("LANG", "")
	if f, _ = launch(t, ok, "--lang", "fr", "--yes", "backup"); f.Lang != "fr" {
		t.Errorf("command after the flags: %+v", f)
	}
	if _, code = launch(t, func(cli.Flags) error { return ui.ErrAborted }, "install", "--yes"); code != 130 {
		t.Errorf("aborted: %d", code)
	}
	if _, code = launch(t, func(cli.Flags) error { return errors.New("broken") }, "upgrade", "--yes"); code != 1 {
		t.Errorf("failed: %d", code)
	}
	if _, code = launch(t, ok, "--no-such-flag"); code != 2 {
		t.Errorf("a flag that is none: %d", code)
	}
	i18n.Set("en")
}

// TestUsage checks the help: every command and every flag with two hyphens.
func TestUsage(t *testing.T) {
	wasFlags := flag.CommandLine
	defer func() { flag.CommandLine = wasFlags }()
	flag.CommandLine = flag.NewFlagSet("x", flag.ContinueOnError)
	flag.String("dir", "", "installation folder")
	flag.Bool("yes", false, "answer every question")
	r, w, _ := os.Pipe()
	was := os.Stderr
	os.Stderr = w
	usage()
	w.Close()
	os.Stderr = was
	buf := make([]byte, 64*1024)
	n, _ := r.Read(buf)
	if out := string(buf[:n]); len(out) == 0 {
		t.Error("no help")
	}
}

// TestIsTerminal checks that no input but a terminal lets the installer ask: /dev/null (cron,
// systemd, docker exec without -i), a pipe, a file and a closed input are none.
func TestIsTerminal(t *testing.T) {
	was := os.Stdin
	defer func() { os.Stdin = was }()
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	file, err := os.CreateTemp(t.TempDir(), "input")
	if err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string]*os.File{"/dev/null": null, "a pipe": r, "a file": file} {
		os.Stdin = in
		if isTerminal() {
			t.Errorf("%s was taken for a terminal", name)
		}
		in.Close()
		if isTerminal() {
			t.Errorf("%s, closed, was taken for a terminal", name)
		}
	}
}
