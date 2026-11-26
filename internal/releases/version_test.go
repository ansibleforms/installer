package releases

import "testing"

// TestParseVersion checks the versions the app, the chart and a user write, and the ones the
// installer refuses (a pre-release, a missing part, a negative number).
func TestParseVersion(t *testing.T) {
	for in, want := range map[string]Version{
		"7.3.0":    {7, 3, 0},
		"v7.3.0":   {7, 3, 0},
		" 7.10.2 ": {7, 10, 2},
	} {
		got, err := ParseVersion(in)
		if err != nil || got != want {
			t.Errorf("ParseVersion(%q) = %v, %v ; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "7.3", "7.3.0.1", "7.3.0-rc.1", "7.x.0", "7.-1.0"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("ParseVersion(%q) took it", bad)
		}
	}
}

// TestVersionStringCompare checks the version as the app tags it, the order between two, and
// the zero version.
func TestVersionStringCompare(t *testing.T) {
	v := Version{7, 3, 1}
	if v.String() != "7.3.1" {
		t.Errorf("String = %s", v)
	}
	for _, c := range []struct {
		o    Version
		want int
	}{
		{Version{7, 3, 1}, 0},
		{Version{7, 3, 2}, -1},
		{Version{7, 2, 9}, 1},
		{Version{8, 0, 0}, -1},
		{Version{6, 9, 9}, 1},
	} {
		if got := v.Compare(c.o); got != c.want {
			t.Errorf("%s.Compare(%s) = %d, want %d", v, c.o, got, c.want)
		}
	}
	if !(Version{}).IsZero() || v.IsZero() {
		t.Error("IsZero")
	}
}
