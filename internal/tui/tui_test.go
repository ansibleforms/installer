package tui

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// ─── the model, driven ────────────────────────────────────────────────────────

// press sends keys to a model, one after the other.
//
// Args:
//
//	m: the model.
//	keys: the keys (enter, up, a rune...).
//
// Returns:
//
//	The model after them.
func press(m model, keys ...tea.KeyMsg) model {
	for _, k := range keys {
		next, _ := m.Update(k)
		m = next.(model)
	}
	return m
}

// key is a key by its kind, or a rune.
func key(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

// runes is a typed text.
func runes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// asked gives a model a question, and the channel its answer comes on.
//
// Args:
//
//	m: the model.
//	q: the question.
//
// Returns:
//
//	The model, and the answer's channel.
func asked(m model, q Question) (model, chan Answer) {
	reply := make(chan Answer, 1)
	next, _ := m.Update(askMsg{q: q, reply: reply})
	return next.(model), reply
}

// got reads an answer given, or none.
func got(t *testing.T, reply chan Answer) (Answer, bool) {
	t.Helper()
	select {
	case a := <-reply:
		return a, true
	default:
		return Answer{}, false
	}
}

// TestSelect checks the cursor on the default (or the first option it may take), moves that
// skip the disabled ones, a hint shown, Esc back unless first, and Enter's answer.
func TestSelect(t *testing.T) {
	m := newModel("Install", "7.3.0")
	m.words = map[string]string{"next": "Weiter", "choose": ""}
	opts := []Option{{Label: "A", Value: "a", Disabled: true}, {Label: "B", Value: "b", Hint: "the b"}, {Label: "C", Value: "c", Disabled: true}, {Label: "D", Value: "d"}}
	m, reply := asked(m, Question{Kind: Select, Title: "Pick", Help: "some help", Options: opts, Default: "x"})
	if m.cursor != 1 {
		t.Errorf("cursor on the first enabled when the default is none: %d", m.cursor)
	}
	m, _ = asked(m, Question{Kind: Select, Title: "Pick", Options: opts, Default: "b"})
	m.reply = reply
	if v := m.View(); !strings.Contains(v, "the b") || !strings.Contains(v, "Weiter") || !strings.Contains(v, "esc") {
		t.Errorf("view %s", v)
	}
	m = press(m, key(tea.KeyDown), key(tea.KeyDown), runes("k"), runes("j"), key(tea.KeyUp))
	if m.cursor != 1 {
		t.Errorf("cursor %d", m.cursor)
	}
	m = press(m, key(tea.KeyEnter))
	if a, ok := got(t, reply); !ok || a.Value != "b" {
		t.Errorf("answer %+v", a)
	}
	m = press(m, key(tea.KeyEnter))
	m, reply = asked(m, Question{Kind: Select, Options: []Option{{Label: "only", Disabled: true}}})
	if m.cursor != 0 {
		t.Errorf("all disabled: %d", m.cursor)
	}
	m = press(m, key(tea.KeyEsc))
	if a, _ := got(t, reply); !a.Back {
		t.Error("esc is back")
	}
	m, reply = asked(m, Question{Kind: Select, First: true, Options: opts[1:2]})
	m = press(m, key(tea.KeyEsc))
	if _, ok := got(t, reply); ok {
		t.Error("esc on the first question")
	}
}

// TestInput checks a plain input, a secret one, a check that refuses, an error shown at once,
// and the typed value trimmed.
func TestInput(t *testing.T) {
	m := newModel("Install", "")
	m, reply := asked(m, Question{Kind: Input, Title: "Name", Default: "af", Secret: true, Error: "taken",
		Validate: func(v string) error {
			if v == "afx" {
				return errors.New("no x")
			}
			return nil
		}})
	if v := m.View(); !strings.Contains(v, "taken") {
		t.Errorf("view %s", v)
	}
	m = press(m, runes("x"), key(tea.KeyEnter))
	if m.errMsg != "no x" {
		t.Errorf("err %q", m.errMsg)
	}
	m = press(m, key(tea.KeyBackspace), runes("y "), key(tea.KeyEnter))
	if a, ok := got(t, reply); !ok || a.Value != "afy" {
		t.Errorf("answer %+v", a)
	}
}

// TestRepeat checks a password asked twice: an empty first answer taken as none, the second
// field reached and left, a mismatch said, and the same twice taken.
func TestRepeat(t *testing.T) {
	m := newModel("Install", "")
	m, reply := asked(m, Question{Kind: Input, Title: "Password", Secret: true, Repeat: "Again", Mismatch: "differ"})
	m = press(m, key(tea.KeyEnter))
	if a, ok := got(t, reply); !ok || a.Value != "" {
		t.Errorf("none: %+v", a)
	}
	m, reply = asked(m, Question{Kind: Input, Title: "Password", Secret: true, Default: "pw", Repeat: "Again", Mismatch: "differ"})
	m = press(m, key(tea.KeyTab))
	if !m.onSecond || !strings.Contains(m.View(), "Again") {
		t.Fatal("not on the second field")
	}
	m = press(m, key(tea.KeyUp), key(tea.KeyDown), key(tea.KeyBackspace), runes("x"), key(tea.KeyEnter))
	if m.errMsg != "differ" || !strings.Contains(m.View(), "differ") {
		t.Errorf("mismatch %q", m.errMsg)
	}
	m = press(m, key(tea.KeyShiftTab), runes("z"), key(tea.KeyTab), key(tea.KeyBackspace), key(tea.KeyBackspace), runes("pwz"), key(tea.KeyEnter))
	if a, ok := got(t, reply); !ok || a.Value != "pwz" {
		t.Errorf("answer %+v", a)
	}
	m, reply = asked(m, Question{Kind: Input, Repeat: "Again"})
	m = press(m, key(tea.KeyDown))
	if m.onSecond {
		t.Error("an empty first answer moved on")
	}
	_ = reply
}

// TestConfirm checks the default, the toggles, y and n, and Enter.
func TestConfirm(t *testing.T) {
	m := newModel("Install", "")
	for _, c := range []struct {
		keys []tea.KeyMsg
		want bool
	}{
		{[]tea.KeyMsg{runes("y")}, true},
		{[]tea.KeyMsg{runes("n")}, false},
		{[]tea.KeyMsg{key(tea.KeyLeft), runes("h"), key(tea.KeyRight), key(tea.KeyEnter)}, false},
	} {
		var reply chan Answer
		m, reply = asked(m, Question{Kind: Confirm, Title: "Sure?", Yes: "Yes", No: "No", DefaultBool: true})
		_ = m.View()
		m = press(m, c.keys...)
		if a, ok := got(t, reply); !ok || a.Bool != c.want {
			t.Errorf("%v: %+v", c.keys, a)
		}
	}
	m, _ = asked(m, Question{Kind: Confirm, Yes: "Yes", No: "No"})
	_ = m.View()
}

// TestDocument checks a document scrolled and acknowledged, its size following the window.
func TestDocument(t *testing.T) {
	m := newModel("Notes", "")
	body := strings.Repeat("• a long line of release notes that wraps across the panel's width, with more words\n", 40)
	m, reply := asked(m, Question{Kind: Document, Title: "Release notes", Help: "read them", Body: body})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = next.(model)
	if v := m.View(); !strings.Contains(v, "%") || !strings.Contains(v, "pgup") {
		t.Errorf("view %s", v)
	}
	m = press(m, key(tea.KeyPgDown), key(tea.KeyEnter))
	if a, ok := got(t, reply); !ok || !a.Bool {
		t.Errorf("answer %+v", a)
	}
}

// TestWorkAndFinish checks the checklist as tasks come (a running one done when the next
// starts, replaced by its outcome), the log panel and its trimming, the finish box, failed or
// not, and the keys that close it.
func TestWorkAndFinish(t *testing.T) {
	m := newModel("Install", "")
	for _, msg := range []tea.Msg{
		languageMsg{title: "Installieren", words: map[string]string{"stop": "Stopp"}},
		stepsMsg{steps: []Step{{Label: "One", Answer: "a very long answer that does not fit in the sidebar at all"}, {Label: "Two"}, {Label: "Three"}}, current: 1},
		taskMsg{state: TaskRunning, text: "first"},
		taskMsg{state: TaskRunning, text: "second"},
		taskMsg{state: TaskWarn, text: "second warned"},
		taskMsg{state: TaskNote, text: "a note"},
		taskMsg{state: TaskRunning, text: "third"},
		taskMsg{state: TaskFailed, text: "fourth"},
		taskMsg{state: TaskRunning, text: "fifth"},
		taskMsg{state: TaskRunning, text: "sixth"},
		taskMsg{state: TaskNote, text: "a note after a running task"},
	} {
		next, _ := m.Update(msg)
		m = next.(model)
	}
	for i := 0; i < 510; i++ {
		next, _ := m.Update(logMsg(strings.Repeat("x", 200)))
		m = next.(model)
	}
	if len(m.logs) != 500 {
		t.Errorf("logs %d", len(m.logs))
	}
	if v := m.View(); !strings.Contains(v, "Stopp") || !strings.Contains(v, "Installieren") || !strings.Contains(v, "…") {
		t.Errorf("working view %s", v)
	}
	next, cmd := m.Update(spinner.TickMsg{})
	m = next.(model)
	_ = cmd
	m = press(m, key(tea.KeyEnter))

	done := make(chan struct{})
	next, _ = m.Update(finishMsg{title: "Done", lines: []string{"url"}, failed: true, done: done})
	m = next.(model)
	if v := m.View(); !strings.Contains(v, "Done") || !strings.Contains(v, "close") {
		t.Errorf("finished view %s", v)
	}
	m = press(m, runes("x"))
	m = press(m, runes("q"))
	select {
	case <-done:
	default:
		t.Error("q did not close")
	}
	m = press(m, key(tea.KeyEsc))

	m = newModel("Install", "")
	next, _ = m.Update(taskMsg{state: TaskRunning, text: "only"})
	m = next.(model)
	next, _ = m.Update(finishMsg{title: "Done"})
	m = next.(model)
	if m.tasks[0].state != TaskDone {
		t.Error("the running task not done")
	}
	next, _ = m.Update(taskMsg{state: TaskRunning, text: "again"})
	next, _ = next.(model).Update(finishMsg{title: "Failed", failed: true})
	if ts := next.(model).tasks; ts[len(ts)-1].state != TaskFailed {
		t.Error("the running task not failed")
	}
}

// TestCtrlC checks an abort: the question answered as aborted, a finish waited for released.
func TestCtrlC(t *testing.T) {
	m := newModel("Install", "")
	m, reply := asked(m, Question{Kind: Select, Options: []Option{{Label: "A", Value: "a"}}})
	done := make(chan struct{})
	m.finish = &finishMsg{done: done}
	_, cmd := m.Update(key(tea.KeyCtrlC))
	if a, _ := got(t, reply); !a.Abort || cmd == nil {
		t.Errorf("abort %+v", a)
	}
	select {
	case <-done:
	default:
		t.Error("the finish not released")
	}
	m = newModel("Install", "")
	m = press(m, key(tea.KeyCtrlC), runes("x"))
	_, _ = m.Update("an unknown message")
}

// TestLayout checks a narrow and a wide window, nothing drawn without a size, the footer of
// each phase, and the helpers that fit texts.
func TestLayout(t *testing.T) {
	m := newModel("Install", "sub")
	m.width = 0
	if m.View() != "" {
		t.Error("drawn without a size")
	}
	m.width = 150
	if m.sidebarWidth() != 40 || !strings.Contains(m.View(), "quit") {
		t.Error("wide")
	}
	m.phase = finished
	m.steps, m.current = []Step{{Label: "a"}}, 0
	_ = m.sidebar()
	if truncate("abc", 1) != "abc" || truncate("abcdef", 4) != "abc…" {
		t.Error("truncate")
	}
	wrapped := hangWrap("  • "+strings.Repeat("word ", 20)+"\nshort", 30)
	if !strings.Contains(wrapped, "\n    word") || !strings.HasSuffix(wrapped, "short") {
		t.Errorf("hangWrap %q", wrapped)
	}
	m.q = &Question{Kind: Document}
	m.phase = asking
	next, _ := m.Update(tea.WindowSizeMsg{Width: 90, Height: 25})
	if next.(model).width != 90 {
		t.Error("size")
	}
}

// ─── the program ──────────────────────────────────────────────────────────────

// headless runs the wizard without a terminal for the test.
func headless(t *testing.T) {
	was := ProgramOptions
	ProgramOptions = []tea.ProgramOption{tea.WithInput(nil), tea.WithOutput(io.Discard)}
	t.Cleanup(func() { ProgramOptions = was; Stop() })
}

// answer keeps pressing a key until the question in flight is answered.
//
// Args:
//
//	q: the question.
//	k: the key.
//
// Returns:
//
//	The answer.
func answer(q Question, k tea.KeyMsg) Answer {
	result := make(chan Answer, 1)
	go func() { result <- Ask(q) }()
	for {
		select {
		case a := <-result:
			return a
		case <-time.After(10 * time.Millisecond):
			p, _ := current()
			p.Send(k)
		}
	}
}

// TestProgram checks the wizard run for real: a question asked and answered, steps, the
// language, tasks and logs sent, a finish closed by a key, and nothing sent once it stopped.
func TestProgram(t *testing.T) {
	headless(t)
	if Running() || Ask(Question{}).Abort != true {
		t.Error("not started: Ask aborts")
	}
	Start("Install", "7.3.0")
	if !Running() {
		t.Fatal("not running")
	}
	SetSteps([]Step{{Label: "One"}}, 0)
	SetLanguage("Install", map[string]string{})
	Task(TaskRunning, "working")
	Log("a line")
	if a := answer(Question{Kind: Select, Options: []Option{{Label: "A", Value: "a"}}}, key(tea.KeyEnter)); a.Value != "a" {
		t.Errorf("answer %+v", a)
	}
	p, gone := current()
	go func() {
		for {
			select {
			case <-gone:
				return
			case <-time.After(10 * time.Millisecond):
				p.Send(key(tea.KeyEnter))
			}
		}
	}()
	Finish("Done", []string{"x"}, false)
	if Running() {
		t.Error("still running after Finish")
	}
	Finish("again", nil, false)
	Stop()
	Task(TaskDone, "ignored")

	// stopped while a question waits : it is aborted
	Start("Install", "")
	go func() {
		time.Sleep(30 * time.Millisecond)
		Stop()
	}()
	if a := Ask(Question{Kind: Confirm}); !a.Abort {
		t.Errorf("stopped: %+v", a)
	}
	// stopped while a finish waits, and a finish ended by Ctrl+C
	Start("Install", "")
	p, _ = current()
	go func() {
		time.Sleep(30 * time.Millisecond)
		p.Quit()
	}()
	Finish("Done", nil, true)
	Start("Install", "")
	p, _ = current()
	go func() {
		time.Sleep(30 * time.Millisecond)
		p.Send(key(tea.KeyCtrlC))
	}()
	Finish("Done", nil, true)
}

// TestFits checks the terminal's size against the wizard's least.
func TestFits(t *testing.T) {
	was := termSize
	defer func() { termSize = was }()
	termSize = func() (int, int, error) { return 120, 40, nil }
	if !Fits() {
		t.Error("120x40")
	}
	termSize = func() (int, int, error) { return 70, 40, nil }
	if Fits() {
		t.Error("70 wide")
	}
	termSize = func() (int, int, error) { return 0, 0, errors.New("no terminal") }
	if Fits() {
		t.Error("no terminal")
	}
	termSize = was
	_, _, _ = termSize()
}
