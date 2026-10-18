package compose

import "testing"

// a join token as Connections > Runners shows it ; anything else is refused
func TestParseJoin(t *testing.T) {
	if got, err := ParseJoin("  afj_abc123  "); err != nil || got != "afj_abc123" {
		t.Fatalf("ParseJoin(afj_abc123) = %q, %v", got, err)
	}
	for _, s := range []string{"", "afj_", "abc", "afjoin2.eyJ1IjoiaHR0cHM6Ly94IiwidCI6ImFmal94In0"} {
		if _, err := ParseJoin(s); err == nil {
			t.Errorf("ParseJoin(%q) accepted", s)
		}
	}
}
