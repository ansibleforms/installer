package compose

import "testing"

// the app's fingerprint of the same token (rte/socket/protocol.js fingerprint)
func TestFingerprintMatchesTheApp(t *testing.T) {
	if got := Fingerprint("abc"); got != "ba78-16bf" {
		t.Fatalf("Fingerprint(abc) = %s, want ba78-16bf", got)
	}
}
