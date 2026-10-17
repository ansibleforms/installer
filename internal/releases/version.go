// Package releases knows the AnsibleForms versions: the releases published on GitHub, the
// order between two versions, and the release notes (CHANGELOG.md) of every version an
// upgrade skips.
package releases

import (
	"fmt"
	"strconv"
	"strings"
)

// ─── a version ────────────────────────────────────────────────────────────────

// Version is a released AnsibleForms version: major, minor and patch, as the app tags them
// (7.3.0, without a "v").
type Version struct {
	Major int
	Minor int
	Patch int
}

// ParseVersion reads a version as the app, the chart or a user writes it.
//
// Args:
//
//	s: the version, with or without a leading "v" (7.3.0, v7.3.0).
//
// Returns:
//
//	The version.
//
// Errors:
//
//	When s is not three dot-separated numbers (a pre-release such as 7.3.0-rc.1 included:
//	the installer only installs releases).
func ParseVersion(s string) (Version, error) {
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("%q is not a version like 7.3.0", s)
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("%q is not a version like 7.3.0", s)
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2]}, nil
}

// String writes the version as the app tags it (7.3.0).
//
// Returns:
//
//	The version, without a leading "v".
func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Compare orders two versions.
//
// Args:
//
//	o: the other version.
//
// Returns:
//
//	-1 when v is older than o, 0 when they are the same, 1 when v is newer.
func (v Version) Compare(o Version) int {
	for _, d := range []int{v.Major - o.Major, v.Minor - o.Minor, v.Patch - o.Patch} {
		if d < 0 {
			return -1
		}
		if d > 0 {
			return 1
		}
	}
	return 0
}

// IsZero tells whether the version was never set.
//
// Returns:
//
//	True for 0.0.0.
func (v Version) IsZero() bool {
	return v == Version{}
}
