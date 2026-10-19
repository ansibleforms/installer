package ui

import (
	"io"
	"os"
	"strings"
	"testing"
)

// the plain output writes a question's help before it, each line indented
func TestPlainHelp(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	was := os.Stderr
	os.Stderr = w
	plainHelp("Found:\n\n• one")
	plainHelp("")
	os.Stderr = was
	w.Close()
	out, _ := io.ReadAll(r)
	if string(out) != "\n  Found:\n\n  • one\n\n" {
		t.Errorf("wrote %q", out)
	}
	if !strings.Contains(string(out), "  • one") {
		t.Errorf("not indented: %q", out)
	}
}
