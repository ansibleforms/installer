package ui

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/huh"

	"github.com/ansibleforms/installer/internal/tui"
)

// ─── helpers ──────────────────────────────────────────────────────────────────

// stderr collects what a function writes to stderr.
//
// Args:
//
//	t: the test.
//	fn: what writes.
//
// Returns:
//
//	What it wrote.
func stderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	was := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	fn()
	w.Close()
	os.Stderr = was
	return <-done
}

// reset puts the package back as it starts, for the test and after it.
func reset(t *testing.T) {
	clear := func() {
		Unattended, Fullscreen, steps, current, help, problem, doc, summary = false, false, nil, 0, "", "", nil, nil
	}
	clear()
	wasFits, wasStart, wasAsk, wasRun := fits, start, Asker, runForm
	t.Cleanup(func() {
		clear()
		fits, start, Asker, runForm = wasFits, wasStart, wasAsk, wasRun
	})
}

// keys makes the plain questions answer from typed keys, one string per form run.
//
// Args:
//
//	t: the test.
//	typed: the keys of each form, in order ("\r" Enter, "\x03" Ctrl+C).
func keys(t *testing.T, typed ...string) {
	runForm = func(f *huh.Form) error {
		k := "\r"
		if len(typed) > 0 {
			k, typed = typed[0], typed[1:]
		}
		return f.WithInput(strings.NewReader(k)).WithOutput(io.Discard).Run()
	}
}

// answers makes the full screen answer each question in turn.
func answers(t *testing.T, list ...tui.Answer) {
	Asker = func(q tui.Question) tui.Answer {
		if len(list) == 0 {
			t.Fatalf("no answer left for %q", q.Title)
		}
		a := list[0]
		list = list[1:]
		return a
	}
}

// ─── the full screen ──────────────────────────────────────────────────────────

// TestStartFullscreen checks the wizard opened only when the terminal fits it and someone
// answers.
func TestStartFullscreen(t *testing.T) {
	reset(t)
	started := false
	start = func(string, string) { started = true }
	fits = func() bool { return false }
	if StartFullscreen("t", "s") || started {
		t.Error("too small")
	}
	fits = func() bool { return true }
	Unattended = true
	if StartFullscreen("t", "s") {
		t.Error("unattended")
	}
	Unattended = false
	if !StartFullscreen("t", "s") || !started || !Fullscreen {
		t.Error("not opened")
	}
}

// TestStepsInFullscreen checks the steps kept with their answers through a change of the
// list, a relabel and a section, all sent to the wizard (here not running: nothing drawn).
func TestStepsInFullscreen(t *testing.T) {
	reset(t)
	Fullscreen = true
	Steps("A", "B", "C")
	Section("B")
	Answered("b")
	Steps("A", "B", "D")
	if current != 1 || steps[1].Answer != "b" {
		t.Errorf("steps %+v at %d", steps, current)
	}
	Relabel("a", "b")
	Language("Titel", map[string]string{"next": "Weiter"})
	if steps[0].Label != "a" || steps[2].Label != "D" {
		t.Errorf("relabelled %+v", steps)
	}
	current = 9
	Answered("ignored")
	Steps("X")
	for _, f := range []func(string){Step, Done, Warn, Fail, Heading, Line, Muted, Log} {
		f("text")
	}
	Blank()
	Banner("t", "s")
}

// TestCloseFullscreen checks the end of the wizard: its summary, a plain stop, an abort, and
// an error shown in it.
func TestCloseFullscreen(t *testing.T) {
	reset(t)
	Close(nil)
	for _, err := range []error{nil, ErrAborted, errors.New("broken")} {
		Fullscreen = true
		_ = stderr(t, func() { Close(err) })
	}
	Fullscreen = true
	Summary("Installed", []string{"url"})
	if out := stderr(t, func() { Close(nil) }); !strings.Contains(out, "Installed") {
		t.Errorf("summary %q", out)
	}
}

// TestQuestionsFullscreen checks each kind of question in the wizard: its answer, the step
// answered (a secret as dots), Esc back and Ctrl+C.
func TestQuestionsFullscreen(t *testing.T) {
	reset(t)
	Fullscreen = true
	Steps("One")
	Problem("taken")
	answers(t,
		tui.Answer{Value: "b"}, tui.Answer{Back: true},
		tui.Answer{Value: "secret"}, tui.Answer{Abort: true},
		tui.Answer{Bool: true}, tui.Answer{Bool: false}, tui.Answer{Back: true},
		tui.Answer{Value: "plain"}, tui.Answer{Value: "hidden"}, tui.Answer{Abort: true},
		tui.Answer{Bool: true},
	)
	opts := []Option{{Label: "A", Value: "a"}, {Label: "B", Value: "b"}}
	if v, err := Select("Pick", opts, "a"); err != nil || v != "b" || steps[0].Answer != "B" {
		t.Errorf("select %q, %v", v, err)
	}
	if _, err := Select("Pick", opts, "a"); !errors.Is(err, ErrBack) {
		t.Errorf("back %v", err)
	}
	if v, err := NewPassword("P", "Again", "differ", ""); err != nil || v != "secret" || steps[0].Answer != "••••••" {
		t.Errorf("password %q, %v", v, err)
	}
	if _, err := NewPassword("P", "Again", "differ", ""); !errors.Is(err, ErrAborted) {
		t.Errorf("abort %v", err)
	}
	for _, want := range []bool{true, false} {
		if v, err := Confirm("Sure?", "Yes", "No", true); err != nil || v != want {
			t.Errorf("confirm %v, %v", v, err)
		}
	}
	if _, err := Confirm("Sure?", "Yes", "No", true); !errors.Is(err, ErrBack) {
		t.Errorf("confirm back %v", err)
	}
	if v, err := Input("Name", "", false, nil); err != nil || v != "plain" {
		t.Errorf("input %q", v)
	}
	if v, _ := Input("Token", "", true, nil); v != "hidden" || steps[0].Answer != "••••••" {
		t.Errorf("secret input %q", v)
	}
	if _, err := Input("Name", "", false, nil); !errors.Is(err, ErrAborted) {
		t.Errorf("input abort %v", err)
	}
	if err := Document("Notes", func() { Heading("7.3.0"); Line("a"); Blank(); Muted("m") }); err != nil {
		t.Errorf("document %v", err)
	}
}

// ─── plain lines and questions ────────────────────────────────────────────────

// TestPlainLines checks each kind of line on stderr, a document written as it is built, and
// the colours.
func TestPlainLines(t *testing.T) {
	reset(t)
	out := stderr(t, func() {
		Banner("Title", "sub")
		Step("step")
		Done("done")
		Warn("warn")
		Fail("fail")
		Heading("heading")
		Line("line")
		Blank()
		Muted("muted")
		Log("log")
		Summary("Installed", []string{"url"})
		Explain("some help")
		_ = Document("Notes", func() { Line("in the document") })
	})
	for _, want := range []string{"Title", "step", "done", "warn", "fail", "heading", "line", "muted", "    log", "Installed", "some help", "in the document"} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in %q", want, out)
		}
	}
	for _, kind := range []string{"bad", "warn", "good", "accent", "other"} {
		if !strings.Contains(Colour(kind, "x"), "x") {
			t.Errorf("colour %s", kind)
		}
	}
	if !strings.Contains(Row("Name", "af", 10), "af") {
		t.Error("row")
	}
}

// TestPlainQuestions checks each question answered on the terminal (huh), unattended with its
// default, and Ctrl+C.
func TestPlainQuestions(t *testing.T) {
	reset(t)
	Unattended = true
	if v, _ := Select("Pick", nil, "d"); v != "d" {
		t.Error("unattended select")
	}
	if v, _ := NewPassword("P", "A", "M", "d"); v != "d" {
		t.Error("unattended password")
	}
	if v, _ := Confirm("C", "Y", "N", true); !v {
		t.Error("unattended confirm")
	}
	if v, _ := Input("I", "d", false, nil); v != "d" {
		t.Error("unattended input")
	}
	Unattended = false

	_ = stderr(t, func() {
		keys(t, "\r")
		if v, err := Select("Pick", []Option{{Label: "Off", Value: "off", Disabled: true}, {Label: "A", Value: "a"}}, "a"); err != nil || v != "a" {
			t.Errorf("select %q, %v", v, err)
		}
		keys(t, "\r", "\r")
		if v, err := NewPassword("P", "Again", "differ", ""); err != nil || v != "" {
			t.Errorf("no password %q, %v", v, err)
		}
		keys(t, "pw\r", "px\r", "pw\r", "pw\r")
		if v, err := NewPassword("P", "Again", "differ", ""); err != nil || v != "pw" {
			t.Errorf("password %q, %v", v, err)
		}
		keys(t, "\x03")
		if _, err := NewPassword("P", "Again", "differ", ""); !errors.Is(err, ErrAborted) {
			t.Errorf("password abort %v", err)
		}
		keys(t, "pw\r", "\x03")
		if _, err := NewPassword("P", "Again", "differ", ""); !errors.Is(err, ErrAborted) {
			t.Errorf("second abort %v", err)
		}
		keys(t, "\r")
		if v, err := Confirm("Sure?", "Yes", "No", true); err != nil || !v {
			t.Errorf("confirm %v, %v", v, err)
		}
		Problem("taken")
		keys(t, " name \r")
		if v, err := Input("Name", "", true, func(s string) error { return nil }); err != nil || v != "name" {
			t.Errorf("input %q, %v", v, err)
		}
	})
}

// TestRunForm checks the real form runner, fed from a reader.
func TestRunForm(t *testing.T) {
	v := ""
	f := huh.NewForm(huh.NewGroup(huh.NewInput().Value(&v))).WithInput(strings.NewReader("x\r")).WithOutput(io.Discard)
	if err := runForm(f); err != nil || v != "x" {
		t.Errorf("%q, %v", v, err)
	}
}
