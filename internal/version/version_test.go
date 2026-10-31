package version

import (
	"regexp"
	"testing"

	"github.com/ansibleforms/installer/internal/releases"
)

// TestAppIsARelease checks App is a release version (x.y.z, no pre-release): the release
// workflow names the release v<App> and refuses anything else, and the installer has to be
// able to install it.
func TestAppIsARelease(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(App) {
		t.Fatalf("App is %q, not a release version (x.y.z)", App)
	}
	if _, err := releases.ParseVersion(App); err != nil {
		t.Fatalf("App %q does not parse as a release: %v", App, err)
	}
}
