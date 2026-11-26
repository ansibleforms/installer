package fakebin

import (
	"os"
	"os/exec"
	"testing"
)

// TestBin checks that a fake program is the only one found, runs its body, and logs its
// calls in order.
func TestBin(t *testing.T) {
	b := New(t)
	if b.Calls() != nil {
		t.Error("calls before any")
	}
	b.Add(t, "docker", `case "$1" in info) echo 27.0 ;; *) exit 3 ;; esac`)
	if os.Getenv("PATH") != b.Dir {
		t.Errorf("PATH is %s", os.Getenv("PATH"))
	}
	if _, err := exec.LookPath("kubectl"); err == nil {
		t.Error("a real program was found")
	}
	out, err := exec.Command("docker", "info").Output()
	if err != nil || string(out) != "27.0\n" {
		t.Errorf("docker info: %q, %v", out, err)
	}
	if err := exec.Command("docker", "ps").Run(); err == nil {
		t.Error("docker ps should exit 3")
	}
	if got := b.Calls(); len(got) != 2 || got[0] != "docker info" || got[1] != "docker ps" {
		t.Errorf("calls %q", got)
	}
	if !b.Called("docker p") || b.Called("podman") {
		t.Error("Called")
	}
}

// recorder is a test that records a failure instead of stopping.
type recorder struct {
	testing.TB
	failed bool
}

// Fatal records the failure.
func (r *recorder) Fatal(...any) { r.failed = true }

// TestAddFails checks that a program that cannot be written fails the test.
func TestAddFails(t *testing.T) {
	r := &recorder{TB: t}
	b := &Bin{Dir: "/nonexistent/fakebin"}
	b.Add(r, "docker", "")
	if !r.failed {
		t.Error("a program that cannot be written did not fail the test")
	}
}
