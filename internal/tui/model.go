package tui

import (
	"context"
	"fmt"

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
	screenPreview
	screenOutcome
	screenHelp
)

// order is what a confirmed modal is about to do.
type order int

const (
	orderNone order = iota
	orderDeploy
	orderRecall
	orderGarrison
	orderUpgrade
	orderLaunch
)

func (o order) verb() string {
	switch o {
	case orderDeploy:
		return "Deploy"
	case orderRecall:
		return "Recall"
	case orderGarrison:
		return "Garrison"
	case orderUpgrade:
		return "Upgrade"
	case orderLaunch:
		return "Launch"
	default:
		return ""
	}
}

// working is the headline the in-flight card carries. Each order says what it
// is doing rather than every one of them claiming to be moving out: the card is
// on screen for as long as a fetch takes, and "forming up" over a five-minute
// upgrade tells the user nothing about what barracks is busy with.
func (o order) working() string {
	switch o {
	case orderGarrison:
		return "DIGGING IN"
	case orderUpgrade:
		return "SCOUTING AHEAD"
	case orderRecall:
		return "STANDING DOWN"
	default:
		return "MOVING OUT"
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
	// working is the order the in-flight card is showing, which is not always
	// pending: an upgrade being applied was ordered from the plan card and has
	// no pending order behind it any more.
	working order
	// pick is the picker the pending order's card is offering, empty for the
	// orders that have nothing to choose.
	pick picker
	// note is what the card in front has to say for itself - a refusal raised
	// by the card's own keys, which belongs on the card rather than in a status
	// line the card may well be covering.
	note   string
	result Outcome
	// apply carries out the plan the preview card is showing. Non-nil is
	// exactly what puts that card in front of the user, so a plan can never be
	// shown as an order the roster cannot then carry out.
	apply  func(context.Context, Session) Outcome
	status string

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
	Up       key.Binding
	Down     key.Binding
	Deploy   key.Binding
	Recall   key.Binding
	Garrison key.Binding
	Upgrade  key.Binding
	Launch   key.Binding
	Refresh  key.Binding
	Help     key.Binding
	Quit     key.Binding
	Confirm  key.Binding
	Cancel   key.Binding
	Choose   key.Binding
}

func defaultKeys() keymap {
	return keymap{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up the line")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down the line")),
		Deploy:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "spawn")),
		Recall:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "recall")),
		Garrison: key.NewBinding(key.WithKeys("g"), key.WithHelp("g", "garrison")),
		Upgrade:  key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "upgrade")),
		Launch:   key.NewBinding(key.WithKeys("L"), key.WithHelp("L", "launch an agent")),
		Refresh:  key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "muster again")),
		Help:     key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "orders")),
		Quit:     key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "dismissed")),
		Confirm:  key.NewBinding(key.WithKeys("y", "enter"), key.WithHelp("y", "confirm")),
		Cancel:   key.NewBinding(key.WithKeys("n", "esc"), key.WithHelp("n/esc", "stand down")),
		Choose:   key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "choose")),
	}
}

// The loadout-editing verbs - train, equip, strip, rename - deliberately have
// no binding at all. A key that answers "not in this build" still advertises
// itself in the help and still has to be explained; a key that is not there is
// the honest shape of a surface that does not do the thing. Those verbs remain
// commands until somebody asks twice.

// ShortHelp is the footer bar, and its order is the whole point of it.
//
// The help elides from the end when the bar is wider than the terminal, so the
// order decides what a narrow screen stops advertising. The two keys that get a
// user out - `q`, and `?`, which is where every other key is written down - come
// first and are never the ones that go; the movement keys come last, because
// arrows and a cursor are the one thing a roster does not have to explain. It
// puts `q dismissed` at the left edge, which is not where a footer conventionally
// ends: that is the trade, and it is deliberate.
func (k keymap) ShortHelp() []key.Binding {
	return []key.Binding{k.Quit, k.Help, k.Deploy, k.Recall, k.Garrison, k.Upgrade, k.Launch, k.Refresh, k.Up, k.Down}
}

func (k keymap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Refresh},
		{k.Deploy, k.Recall, k.Garrison},
		{k.Upgrade, k.Launch},
		{k.Choose, k.Confirm, k.Cancel},
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
	// The dossier scrolls vertically and only vertically. The viewport's own
	// keymap also binds l/h (and the arrows) to a horizontal scroll, which the
	// roster advertises nowhere and which cuts the first columns off every line
	// of the pane - a unit called "backend" reading as "end" looks like a
	// rendering fault, and nothing on screen says which key undoes it. A step
	// of zero is how the widget itself turns that axis off, so the pane keeps
	// the vertical scrolling a long dossier needs.
	m.vp.SetHorizontalStep(0)
	m.st = gather(cfg.Records)
	return m
}

// refreshedMsg carries a re-read of every record back to the model.
type refreshedMsg struct{ st state }

// doneMsg is an action's result. It carries a Preview rather than an Outcome
// because the two arrive the same way and differ only in whether there is
// something left to confirm.
type doneMsg struct{ p Preview }

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
		m.result = msg.p.Outcome
		m.apply = msg.p.Apply
		if m.apply != nil {
			// A plan, not a result. Nothing has been written, so there is
			// nothing to re-read: the roster behind the card is still true.
			m.scr = screenPreview
			return m, nil
		}
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
			// An order with nothing chosen is refused on the card that offered
			// the choice, not in the status line the card is standing on: the
			// user is looking at the picker, and a key that appears to do
			// nothing is the failure this whole surface is held to. Which band
			// is empty is named, because "choose at least one" over a card
			// holding both targets and skills does not say which.
			if g, empty := m.pick.emptyBand(); empty {
				m.note = fmt.Sprintf("Choose at least one %s, or stand the order down.", g.Noun)
				return nil
			}
			return m.start(m.pending)
		case key.Matches(msg, m.keys.Cancel), key.Matches(msg, m.keys.Quit):
			m.stand("Order withdrawn.")
		case key.Matches(msg, m.keys.Choose):
			m.pick.toggle()
			m.note = ""
		case key.Matches(msg, m.keys.Up):
			m.pick.move(-1)
			m.note = ""
		case key.Matches(msg, m.keys.Down):
			m.pick.move(1)
			m.note = ""
		}
		return nil

	case screenWorking:
		// Nothing interrupts work already underway. A half-applied spawn is the
		// one state this tier must not be left in, and the engine's own rollback
		// is what guarantees that - not a key press.
		return nil

	case screenPreview:
		switch {
		case key.Matches(msg, m.keys.Confirm):
			return m.applyPlan()
		case key.Matches(msg, m.keys.Cancel), key.Matches(msg, m.keys.Quit):
			// The plan was read and declined. Nothing was written to decline,
			// which is the whole reason the plan is shown first.
			m.apply = nil
			m.stand("Plan read, nothing applied.")
		}
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
	case key.Matches(msg, m.keys.Garrison):
		return m.propose(orderGarrison)
	case key.Matches(msg, m.keys.Upgrade):
		return m.propose(orderUpgrade)
	case key.Matches(msg, m.keys.Launch):
		return m.propose(orderLaunch)
	default:
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return cmd
	}
	return nil
}

// stand puts the roster back in front with nothing pending.
func (m *model) stand(status string) {
	m.scr, m.pending = screenRoster, orderNone
	m.pick = picker{}
	m.note = ""
	m.status = status
}

// propose puts an order in front of the user rather than carrying it out. Every
// state-changing action goes through here: a roster you can deploy from by
// brushing a key is a roster nobody will trust.
//
// An upgrade is the one order that does not stop here, and for the reason the
// gate exists at all: what `u` starts writes nothing. It resolves refs and
// fetches into the shared, content-addressed store, and then shows the plan -
// and that plan card is where an upgrade is confirmed.
func (m *model) propose(o order) tea.Cmd {
	u, ok := m.selected()
	if !ok {
		m.status = "No unit selected."
		return nil
	}
	if reason := m.refuse(o, u); reason != "" {
		m.status = reason
		return nil
	}
	pick, err := m.pickerFor(o, u)
	if err != nil {
		// Where a deploy would go is part of the loadout's own definition, so a
		// definition barracks cannot read is a refusal and not a choice to be
		// made. It goes to the outcome panel rather than the status line for the
		// reason every barracks refusal names a thing: the sentence names the
		// target it does not know, and a status line would cut it.
		m.refused(err)
		return nil
	}
	m.pick = pick
	m.note = ""
	if o == orderUpgrade {
		return m.start(o)
	}
	m.pending = o
	m.scr = screenConfirm
	m.status = ""
	return nil
}

// refuse is why this order cannot be given for this unit, or "".
//
// Every gate is here rather than at the key that raises it, so what an order
// needs is stated once. They are not the same for every verb: an upgrade
// re-resolves sources for every spawn on the machine and needs no repository at
// all, while the three that put files somewhere need one.
func (m *model) refuse(o order, u unit) string {
	if len(u.Loadout.Equipment) == 0 {
		switch o {
		case orderDeploy, orderGarrison, orderUpgrade, orderLaunch:
			return fmt.Sprintf("%s carries nothing - equip it first.", u.Loadout.Name)
		}
	}
	switch o {
	case orderRecall:
		if len(u.Here) == 0 {
			return fmt.Sprintf("%s is not deployed here.", u.Loadout.Name)
		}
	case orderLaunch:
		if len(m.cfg.Launchers) == 0 {
			return "No agent barracks knows is on the PATH here."
		}
	}
	if m.st.Root == "" && o != orderUpgrade {
		return "Not inside a repository - there is nowhere here to deploy to."
	}
	return ""
}

// pickerFor is the choice an order's card offers, if it offers one.
//
// It refuses rather than returning a picker whenever the order cannot honestly
// be offered - which today is a deploy whose loadout barracks cannot work out a
// destination for at all.
//
// The skills band is built from the loadout definition the roster is already
// holding, resolved here every time the card opens and kept nowhere. A
// per-loadout memo of this is exactly what was deleted from this screen once
// already: a tea.Cmd cleared it while Update read it, and holding a key down
// killed the process with the terminal still in the alternate screen.
func (m *model) pickerFor(o order, u unit) (picker, error) {
	switch o {
	case orderDeploy:
		var targets []choice
		for _, t := range m.cfg.Targets {
			c := choice{Key: t.ID, Label: t.Display}
			if t.Present {
				c.Note = "present here"
			}
			targets = append(targets, c)
		}
		var ids []string
		if m.cfg.Selection != nil {
			got, _, err := m.cfg.Selection(u.Loadout)
			if err != nil {
				return picker{}, err
			}
			ids = got
		}
		// Every skill opens ticked, because deploying the whole unit is what a
		// deploy is. Untouched then reports nil, which reaches the engine as no
		// narrowing at all rather than as a hand-written list of everything -
		// the same distinction the targets band draws, for the same reason.
		var skills []choice
		names := skillNames(u.Loadout)
		for _, n := range names {
			skills = append(skills, choice{Key: n, Label: n})
		}
		return newPicker(
			band{ID: groupTargets, Title: "TARGETS", Noun: "target", Multi: true, Options: targets, Chosen: ids},
			band{ID: groupSkills, Title: "SKILLS", Noun: "skill", Multi: true, Options: skills, Chosen: names},
		), nil
	case orderLaunch:
		var options []choice
		for _, l := range m.cfg.Launchers {
			options = append(options, choice{Key: l.Command, Label: l.Display, Note: l.Command})
		}
		var first []string
		if len(options) > 0 {
			first = []string{options[0].Key}
		}
		return newPicker(band{ID: groupAgent, Title: "AGENT", Noun: "agent", Options: options, Chosen: first}), nil
	}
	return picker{}, nil
}

// refused puts a refusal the roster raised itself in front of the user, in the
// same panel an order's own refusal lands in. Nothing was written, so unlike a
// finished order there is nothing to re-read.
func (m *model) refused(err error) {
	m.pending, m.working = orderNone, orderNone
	m.pick = picker{}
	m.note, m.status = "", ""
	m.apply = nil
	m.result = Outcome{Err: err}
	m.scr = screenOutcome
}

// start carries out an order.
func (m *model) start(o order) tea.Cmd {
	u, ok := m.selected()
	if !ok {
		m.stand("")
		return nil
	}
	// Each band is asked for by name. Nil from either is "the user left this
	// alone", which is not the same instruction as the same list chosen by hand.
	chosen := m.pick.chosen(groupTargets)
	skills := m.pick.chosen(groupSkills)
	// Only a launch has a program, and only the launch picker's keys are
	// programs. Asking for one on the way through every other order is what
	// would let a deploy's ticked target be read as a choice of agent.
	var program Launcher
	if o == orderLaunch {
		program = m.launcher()
	}
	m.pending = orderNone
	m.pick = picker{}
	m.note = ""
	m.working = o
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
		return m.handover(func(s Session) Outcome { return cfg.Deploy(context.Background(), l, chosen, skills, s) })
	case orderGarrison:
		// The committed tier fetches too, and writes real files into somebody's
		// checkout, so it goes the same way for the same reason.
		return m.handover(func(s Session) Outcome { return cfg.Garrison(context.Background(), l, s) })
	case orderUpgrade:
		// An upgrade refetches every source. What comes back is a plan rather
		// than a result, and the roster puts it in front of the user before a
		// single link is moved.
		job := &terminalJob{run: func(s Session) Preview { return cfg.Upgrade(context.Background(), l, s) }}
		return m.exec(job, job.done)
	case orderLaunch:
		// The agent owns the terminal outright for the whole session, which is
		// the strongest form of the same handover: it reads the keyboard.
		return m.handover(func(s Session) Outcome { return cfg.Launch(context.Background(), l, program, s) })
	case orderRecall:
		// A recall reads records and removes symlinks. It starts no child, so
		// it keeps the screen and the roster keeps drawing.
		return func() tea.Msg { return doneMsg{Preview{Outcome: cfg.Recall(context.Background(), l)}} }
	}
	return func() tea.Msg { return doneMsg{Preview{Outcome: Outcome{Title: "nothing to do"}}} }
}

// applyPlan carries out the plan the preview card is showing.
func (m *model) applyPlan() tea.Cmd {
	apply := m.apply
	if apply == nil {
		m.stand("")
		return nil
	}
	m.apply = nil
	m.working = orderUpgrade
	m.scr = screenWorking
	m.status = ""
	// The committed half of an upgrade rewrites vendored files, and the
	// personal half relinks live spawns, so this owns the terminal exactly as
	// the planning half did.
	return m.handover(func(s Session) Outcome { return apply(context.Background(), s) })
}

// handover runs an order with the terminal given back to it.
func (m *model) handover(run func(Session) Outcome) tea.Cmd {
	job := &terminalJob{run: func(s Session) Preview { return Preview{Outcome: run(s)} }}
	return m.exec(job, job.done)
}

// launcher is the program a launch order would start.
//
// The choice is looked up by the key the picker reports, never by the row it
// sits on. The launch picker happens to be built one row per launcher today, so
// the two agree by coincidence rather than by construction - and a coincidence
// that decides which program gets started is one filtering change away from
// starting the wrong agent, silently, with somebody's skills already in place.
func (m *model) launcher() Launcher {
	for _, key := range m.pick.keys(groupAgent) {
		for _, l := range m.cfg.Launchers {
			if l.Command == key {
				return l
			}
		}
	}
	return Launcher{}
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
	// The help bar is the widest thing the footer draws, and left unbounded it
	// is what makes the frame wider than the terminal - which costs the user
	// the end of it, where `q dismissed` is.
	m.help.SetWidth(maxInt(1, m.w-2))
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
