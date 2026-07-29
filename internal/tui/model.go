package tui

import (
	"context"
	"fmt"
	"io"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

// screen is which of the roster's faces is in front.
type screen int

const (
	screenRoster screen = iota
	screenConfirm
	screenWorking
	screenOutcome
	screenHelp
)

// order is what a confirmed modal is about to do.
type order int

const (
	orderNone order = iota
	orderDeploy
	orderRecall
)

func (o order) verb() string {
	switch o {
	case orderDeploy:
		return "Deploy"
	case orderRecall:
		return "Recall"
	default:
		return ""
	}
}

// model is the whole screen. Every field is data the view is a pure function
// of, which is what makes the screen assertable without a terminal.
type model struct {
	cfg Config
	th  theme

	st     state
	cursor int
	w, h   int
	ready  bool

	scr     screen
	pending order
	result  Outcome
	status  string

	sp   spinner.Model
	vp   viewport.Model
	help help.Model
	keys keymap

	// exec runs an order that has to own the terminal, and is Bubble Tea's own
	// handover - see terminalJob. It is a field only so the capture harness can
	// run the same job in process: the message tea.Exec produces is unexported,
	// so a test can neither build one nor look inside one.
	exec func(tea.ExecCommand, tea.ExecCallback) tea.Cmd
}

type keymap struct {
	Up      key.Binding
	Down    key.Binding
	Deploy  key.Binding
	Recall  key.Binding
	Refresh key.Binding
	Help    key.Binding
	Quit    key.Binding
	Confirm key.Binding
	Cancel  key.Binding
}

func defaultKeys() keymap {
	return keymap{
		Up:      key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up the line")),
		Down:    key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down the line")),
		Deploy:  key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "spawn")),
		Recall:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "recall")),
		Refresh: key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "muster again")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "orders")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "dismissed")),
		Confirm: key.NewBinding(key.WithKeys("y", "enter"), key.WithHelp("y", "confirm")),
		Cancel:  key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n/esc", "stand down")),
	}
}

// The verbs the roster does not drive - train, equip, garrison, upgrade, strip,
// run - deliberately have no binding at all. A key that answers "not in this
// build" still advertises itself in the help and still has to be explained; a
// key that is not there is the honest shape of a surface that does not do the
// thing yet. Those verbs remain commands until the roster genuinely runs them.

func (k keymap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Deploy, k.Recall, k.Refresh, k.Help, k.Quit}
}

func (k keymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Refresh},
		{k.Deploy, k.Recall},
		{k.Confirm, k.Cancel},
		{k.Help, k.Quit},
	}
}

func newModel(cfg Config) *model {
	dark := true
	if cfg.Dark != nil {
		dark = *cfg.Dark
	}
	m := &model{
		cfg:  cfg,
		th:   newTheme(dark),
		sp:   spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		vp:   viewport.New(),
		help: help.New(),
		keys: defaultKeys(),
		exec: tea.Exec,
	}
	m.st = gather(cfg.Records)
	return m
}

// refreshedMsg carries a re-read of every record back to the model.
type refreshedMsg struct{ st state }

// doneMsg is an action's result.
type doneMsg struct{ out Outcome }

func (m *model) Init() tea.Cmd {
	return tea.Batch(m.sp.Tick, tea.RequestBackgroundColor)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.ready = true
		m.layout()
		return m, nil

	case tea.BackgroundColorMsg:
		// The palette is settled by the terminal rather than guessed, so the
		// roster is readable on a light background without a flag.
		if m.cfg.Dark == nil {
			m.th = newTheme(msg.IsDark())
		}
		return m, nil

	case refreshedMsg:
		m.st = msg.st
		if m.cursor >= len(m.st.Units) {
			m.cursor = maxInt(0, len(m.st.Units)-1)
		}
		m.layout()
		return m, nil

	case doneMsg:
		m.result = msg.out
		m.scr = screenOutcome
		return m, m.refresh()

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.sp, cmd = m.sp.Update(msg)
		return m, cmd

	case tea.KeyPressMsg:
		return m, m.onKey(msg)
	}

	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	return m, cmd
}

func (m *model) onKey(msg tea.KeyPressMsg) tea.Cmd {
	switch m.scr {
	case screenConfirm:
		switch {
		case key.Matches(msg, m.keys.Confirm):
			return m.start()
		case key.Matches(msg, m.keys.Cancel), key.Matches(msg, m.keys.Quit):
			m.scr, m.pending = screenRoster, orderNone
			m.status = "Order withdrawn."
		}
		return nil

	case screenWorking:
		// Nothing interrupts work already underway. A half-applied spawn is the
		// one state this tier must not be left in, and the engine's own rollback
		// is what guarantees that - not a key press.
		return nil

	case screenOutcome:
		m.scr = screenRoster
		return nil

	case screenHelp:
		m.scr = screenRoster
		return nil
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return tea.Quit
	case key.Matches(msg, m.keys.Up):
		m.move(-1)
	case key.Matches(msg, m.keys.Down):
		m.move(1)
	case key.Matches(msg, m.keys.Help):
		m.scr = screenHelp
	case key.Matches(msg, m.keys.Refresh):
		m.status = "Mustering."
		return m.refresh()
	case key.Matches(msg, m.keys.Deploy):
		return m.propose(orderDeploy)
	case key.Matches(msg, m.keys.Recall):
		return m.propose(orderRecall)
	default:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return cmd
	}
	return nil
}

// propose puts an order in front of the user rather than carrying it out. Every
// state-changing action goes through here: a roster you can deploy from by
// brushing a key is a roster nobody will trust.
func (m *model) propose(o order) tea.Cmd {
	u, ok := m.selected()
	if !ok {
		m.status = "No unit selected."
		return nil
	}
	switch {
	case o == orderDeploy && len(u.Loadout.Equipment) == 0:
		m.status = fmt.Sprintf("%s carries nothing - equip it first.", u.Loadout.Name)
		return nil
	case o == orderRecall && len(u.Here) == 0:
		m.status = fmt.Sprintf("%s is not deployed here.", u.Loadout.Name)
		return nil
	case m.st.Root == "":
		m.status = "Not inside a repository - there is nowhere here to deploy to."
		return nil
	}
	m.pending = o
	m.scr = screenConfirm
	m.status = ""
	return nil
}

// start carries out the pending order.
func (m *model) start() tea.Cmd {
	u, ok := m.selected()
	if !ok {
		m.scr, m.pending = screenRoster, orderNone
		return nil
	}
	o := m.pending
	m.pending = orderNone
	m.scr = screenWorking
	m.status = ""

	l := u.Loadout
	cfg := m.cfg
	switch o {
	case orderDeploy:
		// A deploy fetches, so it is run with the terminal handed back to it -
		// see terminalJob for what that is guarding against. Everything it
		// reports goes to the terminal it now owns, which is also where any
		// prompt a child of it raises will be, and answerable.
		job := &terminalJob{run: func(w io.Writer) Outcome {
			return cfg.Deploy(context.Background(), l, func(line string) {
				fmt.Fprintln(w, line)
			})
		}}
		return m.exec(job, job.done)
	case orderRecall:
		// A recall reads records and removes symlinks. It starts no child, so
		// it keeps the screen and the roster keeps drawing.
		return func() tea.Msg { return doneMsg{cfg.Recall(context.Background(), l)} }
	}
	return func() tea.Msg { return doneMsg{Outcome{Title: "nothing to do"}} }
}

func (m *model) refresh() tea.Cmd {
	records := m.cfg.Records
	return func() tea.Msg { return refreshedMsg{gather(records)} }
}

func (m *model) move(d int) {
	if len(m.st.Units) == 0 {
		return
	}
	m.cursor = (m.cursor + d + len(m.st.Units)) % len(m.st.Units)
	m.status = ""
	m.layout()
}

func (m *model) selected() (unit, bool) {
	if m.cursor < 0 || m.cursor >= len(m.st.Units) {
		return unit{}, false
	}
	return m.st.Units[m.cursor], true
}

// layout re-sizes the panes and refills the dossier. It runs on every change
// that could alter either, so the viewport never holds a previous unit's text.
func (m *model) layout() {
	if !m.ready {
		return
	}
	w, _ := m.paneWidths()
	body := m.bodyHeight()
	// The pane is a border (2 rows), a title line, and the viewport. Getting
	// this wrong by one makes the two panes end on different rows, which is the
	// first thing the eye catches.
	m.vp.SetWidth(maxInt(1, w-4))
	m.vp.SetHeight(maxInt(1, body-3))
	if u, ok := m.selected(); ok {
		m.vp.SetContent(m.dossier(u, maxInt(1, w-4)))
	} else {
		m.vp.SetContent(m.th.faint.Render("No units on the roster.\n\nTrain one with:  barracks train <name>"))
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
