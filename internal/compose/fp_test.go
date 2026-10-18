package compose

import (
	"testing"

	"github.com/ansibleforms/installer/internal/state"
)

// the app's fingerprint of the same token (rte/socket/protocol.js fingerprint)
func TestFingerprintMatchesTheApp(t *testing.T) {
	if got := Fingerprint("abc"); got != "ba78-16bf" {
		t.Fatalf("Fingerprint(abc) = %s, want ba78-16bf", got)
	}
}

// the compose project is the instance name, unless the installation taken over has its own
func TestProject(t *testing.T) {
	if got := Project(&state.State{Name: "af"}); got != "af" {
		t.Errorf("Project = %q, want af", got)
	}
	if got := Project(&state.State{Name: "af", Project: "docker"}); got != "docker" {
		t.Errorf("Project = %q, want docker", got)
	}
}
