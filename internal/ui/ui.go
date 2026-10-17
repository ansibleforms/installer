// Package ui is how the installer talks to the person running it : its questions, steps,
// warnings and results. In a terminal big enough it draws the full-screen wizard (package
// tui) ; otherwise - too small, no terminal, --yes - it writes plain coloured lines and asks
// with huh, the questions answered by their defaults when it runs unattended.
package ui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/ansibleforms/installer/internal/tui"
)

// Unattended answers every question with its default (--yes, or no terminal).
var Unattended bool

// Fullscreen : the wizard is drawn (StartFullscreen).
var Fullscreen bool

// ─── colours ──────────────────────────────────────────────────────────────────
// AnsibleForms' red (its logo) for what leads the eye, green, amber and red as the app's pills,
// readable on a dark and a light terminal

var (
	accent  = lipgloss.NewStyle().Bold(true)
	good    = lipgloss.NewStyle().Foreground(lipgloss.Color("#43a047")).Bold(true)
	warn    = lipgloss.NewStyle().Foreground(lipgloss.Color("#f9a825")).Bold(true)
	bad     = lipgloss.NewStyle().Foreground(lipgloss.Color("#e53935")).Bold(true)
	muted   = lipgloss.NewStyle().Foreground(lipgloss.Color("#8a8f98"))
	heading = lipgloss.NewStyle().Bold(true)
	box     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#ED1C24")).Padding(0, 2)
)

// ErrAborted says the person stopped the installer (Ctrl+C).
var ErrAborted = errors.New("aborted")

// ErrBack says the person went back a step (Esc in the full screen).
var ErrBack = errors.New("back")

// ─── the full screen ──────────────────────────────────────────────────────────

// StartFullscreen opens the wizard when the terminal fits it and the installer asks questions.
//
// Args:
//
//	title: the header's title.
//	sub: the header's right side ("" for none).
//
// Returns:
//
//	True when it opened.
func StartFullscreen(title, sub string) bool {
	if Unattended || !tui.Fits() {
		return false
	}
	tui.Start(title, sub)
	Fullscreen = true
	return true
}

// the steps at the left, the current one, and the help of the next question
var (
	steps   []tui.Step
	current int
	help    string
	// a document being written (release notes) : its lines go there, not to the screen
	doc *strings.Builder
)

// Steps sets the steps of the flow, shown at the left of the full screen.
//
// Args:
//
//	labels: the steps' names, in order.
func Steps(labels ...string) {
	// the answers given so far stay with their steps (the list changes as the answers do : a
	// runner has other steps than a controller)
	known := map[string]string{}
	for _, st := range steps {
		known[st.Label] = st.Answer
	}
	var at string
	if current < len(steps) {
		at = steps[current].Label
	}
	steps = make([]tui.Step, len(labels))
	current = 0
	for i, l := range labels {
		steps[i] = tui.Step{Label: l, Answer: known[l]}
		if l == at {
			current = i
		}
	}
	if Fullscreen {
		tui.SetSteps(steps, current)
	}
}

// Relabel renames the steps after a change of language : the same steps in the same order,
// their answers and the current one kept.
//
// Args:
//
//	labels: the steps' names in the new language, in order.
func Relabel(labels ...string) {
	for i := range steps {
		if i < len(labels) {
			steps[i].Label = labels[i]
		}
	}
	if Fullscreen {
		tui.SetSteps(steps, current)
	}
}

// Language puts the full screen's header and key bar in the language just chosen.
//
// Args:
//
//	title: the header's title.
//	words: the key bar's words by their English one.
func Language(title string, words map[string]string) {
	if Fullscreen {
		tui.SetLanguage(title, words)
	}
}

// Section makes a step the current one (by its name).
//
// Args:
//
//	label: the step's name.
func Section(label string) {
	for i, s := range steps {
		if s.Label == label {
			current = i
		}
	}
	if Fullscreen {
		tui.SetSteps(steps, current)
	}
}

// Answered notes the answer of the current step, shown under it once done.
//
// Args:
//
//	text: the answer as shown (a password : dots).
func Answered(text string) {
	if current < len(steps) {
		steps[current].Answer = text
	}
	if Fullscreen {
		tui.SetSteps(steps, current)
	}
}

// Explain sets a few words shown under the next question's title.
//
// Args:
//
//	text: the help.
func Explain(text string) {
	help = text
}

// takeHelp gives the help set for this question, and clears it.
func takeHelp() string {
	h := help
	help = ""
	return h
}

// Close ends the run : in the full screen its end shown until Enter, then the terminal as it
// was, with the summary (or the error) printed there too so it stays in the scrollback.
//
// Args:
//
//	err: the run's error, nil when it went well.
func Close(err error) {
	if !Fullscreen {
		return
	}
	Fullscreen = false
	switch {
	case err == nil && summary != nil:
		tui.Finish(summary.title, summary.lines, false)
		printSummary(summary.title, summary.lines)
	case err == nil:
		tui.Stop()
	case errors.Is(err, ErrAborted):
		tui.Stop()
	default:
		tui.Finish(err.Error(), nil, true)
	}
}

// ─── lines ────────────────────────────────────────────────────────────────────

// Banner shows the installer's name and version at the start (plain output only : the full
// screen has its header).
//
// Args:
//
//	title: the first line.
//	sub: the second line.
func Banner(title, sub string) {
	if Fullscreen {
		return
	}
	fmt.Fprintln(os.Stderr, box.Render(accent.Render(title)+"\n"+muted.Render(sub)))
}

// Step says what the installer does now.
//
// Args:
//
//	msg: the step.
func Step(msg string) {
	if Fullscreen {
		tui.Task(tui.TaskRunning, msg)
		return
	}
	fmt.Fprintln(os.Stderr, accent.Render("→")+" "+msg)
}

// Done says a step went well.
//
// Args:
//
//	msg: what was done.
func Done(msg string) {
	if Fullscreen {
		tui.Task(tui.TaskDone, msg)
		return
	}
	fmt.Fprintln(os.Stderr, good.Render("✓")+" "+msg)
}

// Warn says something worth knowing that does not stop the installer.
//
// Args:
//
//	msg: the warning.
func Warn(msg string) {
	if Fullscreen {
		tui.Task(tui.TaskWarn, msg)
		return
	}
	fmt.Fprintln(os.Stderr, warn.Render("!")+" "+msg)
}

// Fail says what went wrong.
//
// Args:
//
//	msg: the error.
func Fail(msg string) {
	if Fullscreen {
		tui.Task(tui.TaskFailed, msg)
		return
	}
	fmt.Fprintln(os.Stderr, bad.Render("✗")+" "+msg)
}

// Heading starts a part of a text (a version's release notes).
//
// Args:
//
//	msg: the heading.
func Heading(msg string) {
	if doc != nil {
		doc.WriteString("\n" + heading.Render(msg) + "\n\n")
		return
	}
	if Fullscreen {
		tui.Task(tui.TaskNote, msg)
		return
	}
	fmt.Fprintln(os.Stderr, "\n"+heading.Render(msg)+"\n")
}

// Line writes a plain line, indented.
//
// Args:
//
//	msg: the line.
func Line(msg string) {
	if doc != nil {
		doc.WriteString("  " + msg + "\n")
		return
	}
	if Fullscreen {
		tui.Task(tui.TaskNote, msg)
		return
	}
	fmt.Fprintln(os.Stderr, "  "+msg)
}

// Blank writes an empty line, the space under a title (a document's, or the plain output's).
func Blank() {
	if doc != nil {
		doc.WriteString("\n")
		return
	}
	if Fullscreen {
		return
	}
	fmt.Fprintln(os.Stderr)
}

// Muted writes a line in grey, indented.
//
// Args:
//
//	msg: the line.
func Muted(msg string) {
	if doc != nil {
		doc.WriteString("  " + muted.Render(msg) + "\n")
		return
	}
	if Fullscreen {
		tui.Task(tui.TaskNote, msg)
		return
	}
	fmt.Fprintln(os.Stderr, "  "+muted.Render(msg))
}

// Log writes a line of a command's output : the full screen's log panel, else indented.
//
// Args:
//
//	line: the line.
func Log(line string) {
	if Fullscreen {
		tui.Log(line)
		return
	}
	fmt.Fprintf(os.Stderr, "    %s\n", line)
}

// Row is a line of a summary : its label in white bold with a colon, padded to a width (in terminal columns, so
// Japanese and Chinese line up too), then its value in grey.
//
// Args:
//
//	label: the label.
//	value: the value.
//	width: the labels' column.
//
// Returns:
//
//	The line, styled.
func Row(label, value string, width int) string {
	label += ":"
	pad := strings.Repeat(" ", max(1, width-lipgloss.Width(label)))
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ffffff")).Render(label) + pad + muted.Render(value)
}

// Colour paints a text for a kind of note: "bad", "warn", "good" or "accent".
//
// Args:
//
//	kind: the colour's name.
//	s: the text.
//
// Returns:
//
//	The painted text.
func Colour(kind, s string) string {
	switch kind {
	case "bad":
		return bad.Render(s)
	case "warn":
		return warn.Render(s)
	case "good":
		return good.Render(s)
	case "accent":
		return accent.Render(s)
	}
	return s
}

// the result to show at the end (Summary), kept for Close in the full screen
var summary *struct {
	title string
	lines []string
}

// Summary shows the result (where AnsibleForms answers, how to sign in) : in a box, or at the
// full screen's end (Close).
//
// Args:
//
//	title: the box's first line.
//	lines: its other lines.
func Summary(title string, lines []string) {
	if Fullscreen {
		summary = &struct {
			title string
			lines []string
		}{title, lines}
		return
	}
	printSummary(title, lines)
}

// printSummary prints the result in a box.
func printSummary(title string, lines []string) {
	fmt.Fprintln(os.Stderr, "\n"+box.Render(good.Render(title)+"\n\n"+strings.Join(lines, "\n")))
}

// Document shows a text to read before going on (the release notes of an upgrade) : written by
// build with Heading, Line and Muted ; in the full screen a page to scroll, Enter to go on.
//
// Args:
//
//	title: the page's title.
//	build: writes the text.
//
// Errors:
//
//	ErrBack or ErrAborted in the full screen.
func Document(title string, build func()) error {
	if !Fullscreen {
		build()
		return nil
	}
	doc = &strings.Builder{}
	build()
	body := doc.String()
	doc = nil
	return answerErr(tui.Ask(tui.Question{Kind: tui.Document, Title: title, Help: takeHelp(), Body: strings.TrimLeft(body, "\n"), First: current == 0}))
}

// ─── questions ────────────────────────────────────────────────────────────────

// Option is one answer of a choice.
type Option struct {
	Label string
	Value string
	// Hint explains it (the full screen shows it under the list while it is chosen).
	Hint string
	// Disabled shows it greyed out, not to be chosen (left out of the plain list).
	Disabled bool
}

// answerErr turns the full screen's back and abort into errors.
func answerErr(a tui.Answer) error {
	if a.Abort {
		return ErrAborted
	}
	if a.Back {
		return ErrBack
	}
	return nil
}

// run asks a question with huh, turning huh's own abort into ErrAborted.
//
// Args:
//
//	field: the question.
//
// Errors:
//
//	ErrAborted, or huh's error.
func run(field huh.Field) error {
	err := huh.NewForm(huh.NewGroup(field)).WithTheme(huh.ThemeBase16()).Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return ErrAborted
	}
	return err
}

// Select asks to pick one answer.
//
// Args:
//
//	title: the question.
//	options: the answers.
//	def: the value picked by default (and unattended).
//
// Returns:
//
//	The value picked.
//
// Errors:
//
//	ErrAborted when stopped, ErrBack when the person went back.
func Select(title string, options []Option, def string) (string, error) {
	h := takeHelp()
	if Unattended {
		return def, nil
	}
	if Fullscreen {
		opts := make([]tui.Option, len(options))
		for i, o := range options {
			opts[i] = tui.Option{Label: o.Label, Value: o.Value, Hint: o.Hint, Disabled: o.Disabled}
		}
		a := tui.Ask(tui.Question{Kind: tui.Select, Title: title, Help: h, Options: opts, Default: def, First: current == 0})
		if err := answerErr(a); err != nil {
			return "", err
		}
		for _, o := range options {
			if o.Value == a.Value {
				Answered(o.Label)
			}
		}
		return a.Value, nil
	}
	value := def
	var opts []huh.Option[string]
	for _, o := range options {
		if !o.Disabled {
			opts = append(opts, huh.NewOption(o.Label, o.Value))
		}
	}
	err := run(huh.NewSelect[string]().Title(title).Options(opts...).Value(&value))
	return value, err
}

// NewPassword asks a password typed twice, the second field under the first ; empty, it is
// answered at once (one is generated).
//
// Args:
//
//	title: the first field's question.
//	again: the second field's label.
//	mismatch: the error when the two differ.
//	def: the answer of before (Esc), in both fields.
//
// Returns:
//
//	The password, or "".
//
// Errors:
//
//	ErrBack or ErrAborted in the full screen.
func NewPassword(title, again, mismatch, def string) (string, error) {
	h := takeHelp()
	if Unattended {
		return def, nil
	}
	if Fullscreen {
		a := tui.Ask(tui.Question{Kind: tui.Input, Title: title, Help: h, Default: def, Secret: true, Repeat: again, Mismatch: mismatch, First: current == 0})
		if err := answerErr(a); err != nil {
			return "", err
		}
		Answered(strings.Repeat("•", min(8, len(a.Value))))
		return a.Value, nil
	}
	for {
		first, second := def, ""
		if err := run(huh.NewInput().Title(title).EchoMode(huh.EchoModePassword).Value(&first)); err != nil {
			return "", err
		}
		first = strings.TrimSpace(first)
		if first == "" {
			return "", nil
		}
		if err := run(huh.NewInput().Title(again).EchoMode(huh.EchoModePassword).Value(&second)); err != nil {
			return "", err
		}
		if strings.TrimSpace(second) == first {
			return first, nil
		}
		Fail(mismatch)
	}
}

// Confirm asks yes or no.
//
// Args:
//
//	title: the question.
//	yes: the "yes" button's text.
//	no: the "no" button's text.
//	def: the answer by default (and unattended).
//
// Returns:
//
//	The answer.
//
// Errors:
//
//	ErrAborted when stopped, ErrBack when the person went back.
func Confirm(title, yes, no string, def bool) (bool, error) {
	h := takeHelp()
	if Unattended {
		return def, nil
	}
	if Fullscreen {
		a := tui.Ask(tui.Question{Kind: tui.Confirm, Title: title, Help: h, Yes: yes, No: no, DefaultBool: def, First: current == 0})
		if err := answerErr(a); err != nil {
			return false, err
		}
		if a.Bool {
			Answered(yes)
		} else {
			Answered(no)
		}
		return a.Bool, nil
	}
	value := def
	err := run(huh.NewConfirm().Title(title).Affirmative(yes).Negative(no).Value(&value))
	return value, err
}

// Input asks for a text.
//
// Args:
//
//	title: the question.
//	def: the text by default (and unattended).
//	secret: hide what is typed (a password).
//	validate: checks the answer, nil to take any; its error is shown under the field.
//
// Returns:
//
//	The text, trimmed.
//
// Errors:
//
//	ErrAborted when stopped, ErrBack when the person went back.
func Input(title, def string, secret bool, validate func(string) error) (string, error) {
	h := takeHelp()
	if Unattended {
		return def, nil
	}
	if Fullscreen {
		a := tui.Ask(tui.Question{Kind: tui.Input, Title: title, Help: h, Default: def, Secret: secret, Validate: validate, First: current == 0})
		if err := answerErr(a); err != nil {
			return "", err
		}
		shown := a.Value
		if secret {
			shown = strings.Repeat("•", min(8, len(a.Value)))
		}
		Answered(shown)
		return a.Value, nil
	}
	value := def
	in := huh.NewInput().Title(title).Value(&value)
	if secret {
		in = in.EchoMode(huh.EchoModePassword)
	}
	if validate != nil {
		in = in.Validate(validate)
	}
	err := run(in)
	return strings.TrimSpace(value), err
}
