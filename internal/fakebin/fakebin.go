// Package fakebin stands in for the programs the installer runs (docker, podman, kubectl,
// helm, systemctl, the package managers...) in its tests: each one a small shell script in a
// folder of the test's own, that folder the whole PATH, so a test never reaches a real
// program of the machine it runs on, whatever is installed there. Every call is written to a
// log the test reads back.
//
// The scripts run with /bin/sh and use its builtins only (echo, printf, case, read, exit):
// with nothing else on the PATH, nothing else is found.
package fakebin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Bin is a folder of fake programs, the PATH of the test.
type Bin struct {
	// Dir is the folder.
	Dir string
	// log is the file every call is written to, one line each: the program and its arguments.
	log string
}

// New makes an empty folder of fake programs and puts it alone on the PATH.
//
// Args:
//
//	t: the test (the folder goes with it, and PATH comes back after it).
//
// Returns:
//
//	The folder.
func New(t testing.TB) *Bin {
	t.Helper()
	b := &Bin{Dir: t.TempDir()}
	b.log = filepath.Join(b.Dir, ".calls")
	t.Setenv("PATH", b.Dir)
	t.Setenv("FAKEBIN_LOG", b.log)
	return b
}

// Add makes a fake program: its calls are logged, then its body runs.
//
// Args:
//
//	t: the test.
//	name: the program (docker).
//	body: the shell its call runs, with $1... its arguments ; "" : it does nothing and
//	  exits 0. Builtins only: echo, printf, case, read, test, exit.
func (b *Bin) Add(t testing.TB, name, body string) {
	t.Helper()
	script := "#!/bin/sh\necho \"" + name + " $*\" >> \"$FAKEBIN_LOG\"\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(b.Dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// Calls reads the calls made so far.
//
// Returns:
//
//	One line per call, the program then its arguments, in order.
func (b *Bin) Calls() []string {
	data, err := os.ReadFile(b.log)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// Called tells whether a call starting with a prefix was made.
//
// Args:
//
//	prefix: the start of the line (docker compose up).
//
// Returns:
//
//	True when one was.
func (b *Bin) Called(prefix string) bool {
	for _, c := range b.Calls() {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}
