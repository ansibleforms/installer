// Package tui is the installer's full-screen interface : a header bar, the steps of the
// install at the left (done, current, still to come, with the answers given), the question at
// the right, and a bar of keys at the bottom ; then the install itself as a checklist with its
// live log, and its end. The installer's flow runs in its own goroutine and talks to it through
// Ask, Task, Log and Finish (package ui wraps them) : this package only draws and reads keys.
package tui

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// ─── colours ──────────────────────────────────────────────────────────────────
// AnsibleForms' own red (its logo, #ED1C24) for the two bars, the chosen button's border, and as a
// small mark (the current step's dot, the choice's arrow) ; titles and the choice are white on a dark grey,
// green and amber as the app's pills. Errors stay red, always with their ✗ and their words.
// The terminal's own background is kept.

var (
	brand     = lipgloss.Color("#ED1C24")
	brandDark = lipgloss.Color("#B3141B")
	green     = lipgloss.Color("#43a047")
	amber     = lipgloss.Color("#f9a825")
	red       = lipgloss.Color("#e53935")
	grey      = lipgloss.Color("#8a8f98")
	white     = lipgloss.Color("#ffffff")
	panel     = lipgloss.Color("#2f3540")
	headerBar = lipgloss.NewStyle().Background(brand).Foreground(white).Bold(true).Padding(0, 1)
	headerSub = lipgloss.NewStyle().Background(brand).Foreground(lipgloss.Color("#FDE2E3")).Padding(0, 1)
	footerBar = lipgloss.NewStyle().Background(brandDark).Foreground(lipgloss.Color("#FDE2E3")).Padding(0, 1)
	keyStyle  = lipgloss.NewStyle().Background(brandDark).Foreground(white).Bold(true)
	titleSt   = lipgloss.NewStyle().Foreground(white).Bold(true)
	helpSt    = lipgloss.NewStyle().Foreground(grey)
	doneSt    = lipgloss.NewStyle().Foreground(green)
	currentSt = lipgloss.NewStyle().Foreground(white).Bold(true)
	markSt    = lipgloss.NewStyle().Foreground(brand).Bold(true)
	todoSt    = lipgloss.NewStyle().Foreground(grey)
	answerSt  = lipgloss.NewStyle().Foreground(grey).Italic(true)
	warnSt    = lipgloss.NewStyle().Foreground(amber).Bold(true)
	errSt     = lipgloss.NewStyle().Foreground(red).Bold(true)
	chosenSt  = lipgloss.NewStyle().Background(panel).Foreground(white).Bold(true)
	buttonSt  = lipgloss.NewStyle().Padding(0, 2).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#3a4250")).Foreground(grey)
	buttonOn  = lipgloss.NewStyle().Padding(0, 2).Border(lipgloss.RoundedBorder()).BorderForeground(brand).Foreground(white).Bold(true)
	sidebarSt = lipgloss.NewStyle().BorderStyle(lipgloss.NormalBorder()).BorderRight(true).BorderForeground(lipgloss.Color("#3a4250")).PaddingRight(1)
	logBox    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#3a4250")).Padding(0, 1)
)

// MinWidth and MinHeight are the smallest terminal the full screen fits ; smaller, the
// installer writes plain lines instead.
const (
	MinWidth  = 80
	MinHeight = 22
)

// Fits tells whether the terminal is big enough for the full screen.
//
// Returns:
//
//	True when it is a terminal of at least MinWidth by MinHeight.
func Fits() bool {
	w, h, err := term.GetSize(os.Stdout.Fd())
	return err == nil && w >= MinWidth && h >= MinHeight
}

// ─── what the flow asks ───────────────────────────────────────────────────────

// Kind is what a question is.
type Kind int

const (
	// Select picks one of options.
	Select Kind = iota
	// Input types a text.
	Input
	// Confirm answers yes or no.
	Confirm
	// Document is a text to read (the release notes), Enter to go on.
	Document
)

// Option is one answer of a Select.
type Option struct {
	Label string
	Value string
	// Hint explains it, under the list while it is the one chosen.
	Hint string
	// Disabled shows it greyed out, not to be chosen (what this server does not have).
	Disabled bool
}

// Question is what the flow asks.
type Question struct {
	Kind  Kind
	Title string
	// Help is a few words under the title.
	Help    string
	Options []Option
	// Default is the value chosen at first (a Select, an Input).
	Default string
	// Secret hides what is typed (a password).
	Secret bool
	// Validate checks an Input ; its error shows under the field.
	Validate func(string) error
	// Repeat asks an Input a second time under the first, its label (a new password) ;
	// Mismatch is the error when the two differ. Left empty, the first is answered alone.
	Repeat, Mismatch string
	// Yes, No and DefaultBool are a Confirm's.
	Yes, No     string
	DefaultBool bool
	// Body is a Document's text, styled already.
	Body string
	// First : no step before it, Esc does nothing.
	First bool
}

// Answer is what the person answered.
type Answer struct {
	Value string
	Bool  bool
	// Back : Esc, to the step before.
	Back bool
	// Abort : Ctrl+C.
	Abort bool
}

// Step is one line of the steps at the left.
type Step struct {
	Label  string
	Answer string
}

// TaskState is how a line of the install checklist is going.
type TaskState int

const (
	TaskRunning TaskState = iota
	TaskDone
	TaskWarn
	TaskFailed
	TaskNote
)

type task struct {
	state TaskState
	text  string
}

// ─── messages to the program ──────────────────────────────────────────────────

type askMsg struct {
	q     Question
	reply chan Answer
}

// languageMsg puts the header and the key bar in another language.
type languageMsg struct {
	title string
	words map[string]string
}

type stepsMsg struct {
	steps   []Step
	current int
}
type taskMsg task
type logMsg string
type finishMsg struct {
	title  string
	lines  []string
	failed bool
	done   chan struct{}
}

// ─── the model ────────────────────────────────────────────────────────────────

type phase int

const (
	asking phase = iota
	working
	finished
)

type model struct {
	title, sub string
	// words are the key bar's words in the person's language (SetLanguage), English without
	words         map[string]string
	width, height int
	steps         []Step
	current       int
	phase         phase

	q      *Question
	reply  chan Answer
	cursor int
	input  textinput.Model
	// input2 is the Input's repeat (Question.Repeat), onSecond when it has the focus
	input2   textinput.Model
	onSecond bool
	yes      bool
	errMsg   string
	doc      viewport.Model

	tasks   []task
	logs    []string
	spin    spinner.Model
	finish  *finishMsg
	aborted bool
}

func newModel(title, sub string) model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(brand)
	return model{title: title, sub: sub, spin: sp, width: 100, height: 30}
}

func (m model) Init() tea.Cmd { return m.spin.Tick }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.q != nil && m.q.Kind == Document {
			m.doc.Width, m.doc.Height = m.panelWidth(), m.docHeight(*m.q)
		}
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case languageMsg:
		m.title, m.words = msg.title, msg.words
		return m, nil
	case stepsMsg:
		m.steps, m.current = msg.steps, msg.current
		return m, nil
	case askMsg:
		return m.ask(msg), textinput.Blink
	case taskMsg:
		m.phase = working
		// a new line ends the one running before it (it went well, or it said otherwise)
		if len(m.tasks) > 0 && m.tasks[len(m.tasks)-1].state == TaskRunning && msg.state == TaskRunning {
			m.tasks[len(m.tasks)-1].state = TaskDone
		}
		if msg.state != TaskRunning && msg.state != TaskNote && len(m.tasks) > 0 && m.tasks[len(m.tasks)-1].state == TaskRunning {
			m.tasks[len(m.tasks)-1] = task(msg)
		} else {
			m.tasks = append(m.tasks, task(msg))
		}
		return m, nil
	case logMsg:
		m.logs = append(m.logs, string(msg))
		if len(m.logs) > 500 {
			m.logs = m.logs[len(m.logs)-500:]
		}
		return m, nil
	case finishMsg:
		m.phase = finished
		if len(m.tasks) > 0 && m.tasks[len(m.tasks)-1].state == TaskRunning {
			if msg.failed {
				m.tasks[len(m.tasks)-1].state = TaskFailed
			} else {
				m.tasks[len(m.tasks)-1].state = TaskDone
			}
		}
		m.finish = &msg
		return m, nil
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

// move gives the option a step up (-1) or down (1) from the cursor, over the greyed ones ;
// the cursor itself when there is none that way.
func (m model) move(step int) int {
	for i := m.cursor + step; i >= 0 && i < len(m.q.Options); i += step {
		if !m.q.Options[i].Disabled {
			return i
		}
	}
	return m.cursor
}

// repeatKey handles a key of an Input asked twice : Enter or Tab on the first moves to the
// second (an empty first is answered as it is), Enter on the second answers when both match,
// Shift+Tab or Up goes back to the first.
func (m model) repeatKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	q := m.q
	focus := func(second bool) {
		m.onSecond = second
		if second {
			m.input.Blur()
			m.input2.Focus()
		} else {
			m.input2.Blur()
			m.input.Focus()
		}
	}
	first := strings.TrimSpace(m.input.Value())
	switch {
	case !m.onSecond && (k.Type == tea.KeyEnter || k.Type == tea.KeyTab || k.Type == tea.KeyDown):
		if first == "" && k.Type == tea.KeyEnter {
			m.answer(Answer{Value: ""})
			return m, nil
		}
		if first != "" {
			focus(true)
		}
		m.errMsg = ""
		return m, nil
	case m.onSecond && (k.Type == tea.KeyShiftTab || k.Type == tea.KeyUp):
		focus(false)
		m.errMsg = ""
		return m, nil
	case m.onSecond && k.Type == tea.KeyEnter:
		if strings.TrimSpace(m.input2.Value()) != first {
			m.errMsg = q.Mismatch
			return m, nil
		}
		m.answer(Answer{Value: first})
		return m, nil
	}
	var cmd tea.Cmd
	if m.onSecond {
		m.input2, cmd = m.input2.Update(k)
	} else {
		m.input, cmd = m.input.Update(k)
	}
	m.errMsg = ""
	return m, cmd
}

// ask shows a question.
func (m model) ask(msg askMsg) model {
	q := msg.q
	m.phase = asking
	m.q, m.reply, m.errMsg = &q, msg.reply, ""
	switch q.Kind {
	case Select:
		m.cursor = -1
		for i, o := range q.Options {
			if o.Value == q.Default && !o.Disabled {
				m.cursor = i
			}
		}
		// no default, or a greyed one : the first that can be chosen
		for i := 0; m.cursor < 0 && i < len(q.Options); i++ {
			if !q.Options[i].Disabled {
				m.cursor = i
			}
		}
		m.cursor = max(m.cursor, 0)
	case Input:
		ti := textinput.New()
		ti.Prompt = "› "
		ti.PromptStyle = currentSt
		ti.SetValue(q.Default)
		ti.CursorEnd()
		ti.Width = m.panelWidth() - 6
		if q.Secret {
			ti.EchoMode = textinput.EchoPassword
			ti.EchoCharacter = '•'
		}
		ti.Focus()
		m.input = ti
		m.onSecond = false
		if q.Repeat != "" {
			t2 := textinput.New()
			t2.Prompt = "› "
			t2.PromptStyle = currentSt
			t2.Width = ti.Width
			t2.EchoMode, t2.EchoCharacter = ti.EchoMode, ti.EchoCharacter
			if q.Default != "" {
				t2.SetValue(q.Default)
				t2.CursorEnd()
			}
			m.input2 = t2
		}
	case Confirm:
		m.yes = q.DefaultBool
	case Document:
		m.doc = viewport.New(m.panelWidth(), m.docHeight(q))
		m.doc.SetContent(hangWrap(q.Body, m.panelWidth()-1))
	}
	return m
}

// answer sends the reply and clears the question.
func (m *model) answer(a Answer) {
	if m.reply != nil {
		m.reply <- a
	}
	// the question stays on screen until the next one or a line of work replaces it, so the
	// panel does not flash empty between two questions
	m.reply = nil
}

func (m model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.Type == tea.KeyCtrlC {
		m.aborted = true
		m.answer(Answer{Abort: true})
		if m.finish != nil && m.finish.done != nil {
			close(m.finish.done)
			m.finish.done = nil
		}
		return m, tea.Quit
	}
	if m.phase == finished && m.finish != nil {
		if k.Type == tea.KeyEnter || k.String() == "q" || k.Type == tea.KeyEsc {
			if m.finish.done != nil {
				close(m.finish.done)
				m.finish.done = nil
			}
			return m, tea.Quit
		}
		return m, nil
	}
	// no question, or one answered already and still shown
	if m.q == nil || m.reply == nil {
		return m, nil
	}
	q := m.q
	if k.Type == tea.KeyEsc && !q.First {
		m.answer(Answer{Back: true})
		return m, nil
	}
	switch q.Kind {
	case Select:
		switch k.String() {
		case "up", "k":
			m.cursor = m.move(-1)
		case "down", "j":
			m.cursor = m.move(1)
		case "enter":
			m.answer(Answer{Value: q.Options[m.cursor].Value})
		}
	case Input:
		if q.Repeat != "" {
			return m.repeatKey(k)
		}
		if k.Type == tea.KeyEnter {
			v := strings.TrimSpace(m.input.Value())
			if q.Validate != nil {
				if err := q.Validate(v); err != nil {
					m.errMsg = err.Error()
					return m, nil
				}
			}
			m.answer(Answer{Value: v})
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(k)
		m.errMsg = ""
		return m, cmd
	case Confirm:
		switch k.String() {
		case "left", "right", "tab", "h", "l":
			m.yes = !m.yes
		case "y":
			m.answer(Answer{Bool: true})
		case "n":
			m.answer(Answer{Bool: false})
		case "enter":
			m.answer(Answer{Bool: m.yes})
		}
	case Document:
		if k.Type == tea.KeyEnter {
			m.answer(Answer{Bool: true})
			return m, nil
		}
		var cmd tea.Cmd
		m.doc, cmd = m.doc.Update(k)
		return m, cmd
	}
	return m, nil
}

// ─── drawing ──────────────────────────────────────────────────────────────────

// sidebarWidth is the steps' column : wider on a wide terminal, so the answers fit under them
func (m model) sidebarWidth() int {
	if m.width >= 130 {
		return 40
	}
	return 26
}

func (m model) bodyHeight() int { return max(8, m.height-2) }

// docHeight is the room a document has : the body, less its title and the blank line under
// it, its help, the blank line under them and the scroll percentage under it.
func (m model) docHeight(q Question) int {
	help := 0
	if q.Help != "" {
		help = lipgloss.Height(helpSt.Width(m.panelWidth()).Render(q.Help))
	}
	return max(3, m.bodyHeight()-3-help-1-2)
}
func (m model) panelWidth() int { return max(30, m.width-m.sidebarWidth()-6) }

func (m model) View() string {
	if m.width == 0 {
		return ""
	}
	header := m.header()
	footer := m.footer()
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		sidebarSt.Width(m.sidebarWidth()).Height(m.bodyHeight()).Render(m.sidebar()),
		lipgloss.NewStyle().PaddingLeft(2).Width(m.panelWidth()+2).Height(m.bodyHeight()).Render(m.panel()),
	)
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m model) header() string {
	left := headerBar.Render(m.title)
	right := ""
	if m.sub != "" {
		right = headerSub.Render(m.sub)
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	return left + lipgloss.NewStyle().Background(brand).Render(strings.Repeat(" ", max(0, gap))) + right
}

// word gives a key bar's word in the person's language.
func (m model) word(w string) string {
	if t, ok := m.words[w]; ok && t != "" {
		return t
	}
	return w
}

func (m model) footer() string {
	var keys [][2]string
	switch {
	case m.phase == finished:
		keys = [][2]string{{"enter", m.word("close")}}
	case m.phase == working:
		keys = [][2]string{{"ctrl+c", m.word("stop")}}
	case m.q == nil:
		keys = [][2]string{{"ctrl+c", m.word("quit")}}
	case m.q.Kind == Select:
		keys = [][2]string{{"↑↓", m.word("choose")}, {"enter", m.word("next")}}
	case m.q.Kind == Confirm:
		keys = [][2]string{{"←→", m.word("choose")}, {"enter", m.word("answer")}}
	case m.q.Kind == Document:
		keys = [][2]string{{"↑↓ pgup pgdn", m.word("scroll")}, {"enter", m.word("go on")}}
	default:
		keys = [][2]string{{"enter", m.word("next")}}
	}
	if m.q != nil && !m.q.First && m.phase == asking {
		keys = append(keys, [2]string{"esc", m.word("back")})
	}
	if m.phase != finished {
		keys = append(keys, [2]string{"ctrl+c", m.word("quit")})
	}
	var parts []string
	seen := map[string]bool{}
	for _, k := range keys {
		if seen[k[0]] {
			continue
		}
		seen[k[0]] = true
		parts = append(parts, keyStyle.Render(k[0])+footerBar.UnsetPadding().Render(" "+k[1]))
	}
	line := footerBar.Render(strings.Join(parts, footerBar.UnsetPadding().Render("   ")))
	gap := m.width - lipgloss.Width(line)
	return line + lipgloss.NewStyle().Background(brandDark).Render(strings.Repeat(" ", max(0, gap)))
}

func (m model) sidebar() string {
	var b strings.Builder
	b.WriteString("\n")
	for i, s := range m.steps {
		var icon, label string
		switch {
		case i < m.current || (m.phase == finished && i == m.current):
			icon, label = doneSt.Render("✓"), s.Label
		case i == m.current:
			icon, label = markSt.Render("●"), currentSt.Render(s.Label)
		default:
			icon, label = todoSt.Render("○"), todoSt.Render(s.Label)
		}
		b.WriteString(" " + icon + " " + label + "\n")
		if s.Answer != "" && i < m.current {
			b.WriteString("   " + answerSt.Render(truncate(s.Answer, m.sidebarWidth()-4)) + "\n")
		}
	}
	return b.String()
}

func (m model) panel() string {
	if m.phase == finished && m.finish != nil {
		return m.finished()
	}
	if m.phase == working || m.q == nil {
		return m.work()
	}
	q := m.q
	var b strings.Builder
	b.WriteString("\n" + titleSt.Render(q.Title) + "\n\n")
	if q.Help != "" {
		b.WriteString(helpSt.Width(m.panelWidth()).Render(q.Help) + "\n")
	}
	b.WriteString("\n")
	switch q.Kind {
	case Select:
		for i, o := range q.Options {
			line := "  " + o.Label
			if o.Disabled {
				line = todoSt.Faint(true).Render(line)
			}
			if i == m.cursor {
				line = markSt.Background(panel).Render("› ") + chosenSt.Width(m.panelWidth()-2).Render(o.Label)
			}
			b.WriteString(line + "\n")
		}
		if hint := q.Options[m.cursor].Hint; hint != "" {
			b.WriteString("\n" + helpSt.Width(m.panelWidth()).Render(hint) + "\n")
		}
	case Input:
		b.WriteString(m.input.View() + "\n")
		if q.Repeat != "" {
			label := helpSt.Render(q.Repeat)
			if m.onSecond {
				label = titleSt.Render(q.Repeat)
			}
			b.WriteString("\n" + label + "\n\n" + m.input2.View() + "\n")
		}
		if m.errMsg != "" {
			b.WriteString("\n" + errSt.Render("✗ "+m.errMsg) + "\n")
		}
	case Confirm:
		yes, no := q.Yes, q.No
		yb, nb := buttonSt.Render(yes), buttonSt.Render(no)
		if m.yes {
			yb = buttonOn.Render(yes)
		} else {
			nb = buttonOn.Render(no)
		}
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, yb, "  ", nb) + "\n")
	case Document:
		b.WriteString(m.doc.View() + "\n")
		pct := int(m.doc.ScrollPercent() * 100)
		b.WriteString(helpSt.Render(fmt.Sprintf("%d%%", pct)))
	}
	return b.String()
}

// checklist draws the install's lines : done, running, warned, failed.
func (m model) checklist() string {
	var b strings.Builder
	for i, t := range m.tasks {
		var icon string
		switch t.state {
		case TaskRunning:
			icon = m.spin.View()
			if i != len(m.tasks)-1 {
				icon = doneSt.Render("✓")
			}
		case TaskDone:
			icon = doneSt.Render("✓")
		case TaskWarn:
			icon = warnSt.Render("!")
		case TaskFailed:
			icon = errSt.Render("✗")
		case TaskNote:
			icon = " "
		}
		text := t.text
		if t.state == TaskNote {
			text = helpSt.Render(text)
		}
		b.WriteString(icon + " " + lipgloss.NewStyle().Width(m.panelWidth()-2).Render(text) + "\n")
	}
	return b.String()
}

// work draws the install : its checklist, then its log, the newest lines that fit.
func (m model) work() string {
	var b strings.Builder
	b.WriteString("\n" + m.checklist())
	used := lipgloss.Height(b.String())
	room := m.bodyHeight() - used - 3
	if room >= 3 && len(m.logs) > 0 {
		lines := m.logs
		if len(lines) > room {
			lines = lines[len(lines)-room:]
		}
		w := m.panelWidth() - 4
		for i, l := range lines {
			lines[i] = truncate(l, w)
		}
		b.WriteString(logBox.Width(m.panelWidth() - 2).Foreground(grey).Render(strings.Join(lines, "\n")))
	}
	return b.String()
}

func (m model) finished() string {
	f := m.finish
	border := green
	title := doneSt.Bold(true).Render("✓ " + f.title)
	if f.failed {
		border = red
		title = errSt.Render("✗ " + f.title)
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(1, 2).Width(m.panelWidth() - 2)
	content := title + "\n\n" + strings.Join(f.lines, "\n")
	return "\n" + m.checklist() + "\n" + box.Render(content) + "\n\n" + helpSt.Render("enter: "+m.word("close"))
}

// hangWrap wraps a text's long lines to a width, each continuation under the start of its
// line's text (after its indent and its bullet), so a long release note stays readable.
//
// Args:
//
//	s: the text, styled already.
//	w: the width.
//
// Returns:
//
//	The text wrapped.
func hangWrap(s string, w int) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if ansi.StringWidth(line) <= w {
			out = append(out, line)
			continue
		}
		plain := ansi.Strip(line)
		indent := len(plain) - len(strings.TrimLeft(plain, " "))
		if strings.HasPrefix(strings.TrimLeft(plain, " "), "• ") {
			indent += 2
		}
		wrapped := strings.Split(ansi.Wordwrap(line, w, ""), "\n")
		for i, part := range wrapped {
			if i > 0 {
				part = strings.Repeat(" ", indent) + strings.TrimLeft(part, " ")
			}
			out = append(out, part)
		}
	}
	return strings.Join(out, "\n")
}

func truncate(s string, w int) string {
	if w <= 1 || lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r)) > w-1 {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// ─── the program ──────────────────────────────────────────────────────────────

var (
	mu      sync.Mutex
	program *tea.Program
	stopped chan struct{}
)

// Start opens the full screen.
//
// Args:
//
//	title: the header's title.
//	sub: the header's right side (the version).
func Start(title, sub string) {
	mu.Lock()
	defer mu.Unlock()
	program = tea.NewProgram(newModel(title, sub), tea.WithAltScreen())
	stopped = make(chan struct{})
	go func() {
		_, _ = program.Run()
		close(stopped)
	}()
}

// Running tells whether the full screen is open.
//
// Returns:
//
//	True while it is.
func Running() bool {
	mu.Lock()
	defer mu.Unlock()
	if program == nil {
		return false
	}
	select {
	case <-stopped:
		return false
	default:
		return true
	}
}

// send hands a message to the program ; nothing once it stopped.
func send(msg tea.Msg) {
	if Running() {
		program.Send(msg)
	}
}

// Ask asks a question and waits for the answer.
//
// Args:
//
//	q: the question.
//
// Returns:
//
//	The answer ; Abort when the screen closed meanwhile.
func Ask(q Question) Answer {
	if !Running() {
		return Answer{Abort: true}
	}
	reply := make(chan Answer, 1)
	program.Send(askMsg{q: q, reply: reply})
	select {
	case a := <-reply:
		return a
	case <-stopped:
		return Answer{Abort: true}
	}
}

// SetSteps sets the steps at the left and the current one.
//
// Args:
//
//	steps: the steps, their answers so far.
//	current: the current one's index.
func SetSteps(steps []Step, current int) {
	cp := append([]Step(nil), steps...)
	send(stepsMsg{steps: cp, current: current})
}

// SetLanguage puts the header and the key bar in the language just chosen.
//
// Args:
//
//	title: the header's title.
//	words: the key bar's words by their English one (choose, next, back, quit...).
func SetLanguage(title string, words map[string]string) {
	send(languageMsg{title: title, words: words})
}

// Task adds a line to the install's checklist (a running one ends the one before).
//
// Args:
//
//	state: how it goes.
//	text: the line.
func Task(state TaskState, text string) { send(taskMsg{state: state, text: text}) }

// Log adds a line of a command's output.
//
// Args:
//
//	line: the line.
func Log(line string) { send(logMsg(line)) }

// Finish shows the end, waits for Enter, and closes the full screen.
//
// Args:
//
//	title: the end's title.
//	lines: what to tell (where it answers, how to sign in).
//	failed: it ended in an error.
func Finish(title string, lines []string, failed bool) {
	if !Running() {
		return
	}
	done := make(chan struct{})
	program.Send(finishMsg{title: title, lines: lines, failed: failed, done: done})
	select {
	case <-done:
	case <-stopped:
	}
	Stop()
}

// Stop closes the full screen, the terminal as it was.
func Stop() {
	if !Running() {
		return
	}
	program.Quit()
	<-stopped
}
