package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The prompt card answers to two keys of its own and hands every other key to
// the field. They are not the card keys the rest of the roster uses: `n` and
// `y` are letters somebody may be typing into a loadout's name, so a prompt is
// sent with enter and withdrawn with esc, and nothing else.
var (
	promptSubmit   = key.NewBinding(key.WithKeys("enter"))
	promptWithdraw = key.NewBinding(key.WithKeys("esc", "ctrl+c"))
)

// promptedMsg is what a train or equip order left behind. It is its own message
// rather than a doneMsg because a refusal here does not end on a card: it goes
// back to the prompt, with what was typed still in the field, so the one thing
// that was wrong is corrected in place.
type promptedMsg struct {
	order order
	name  string
	out   Outcome
}

// ask puts the one-field prompt for a train or equip order in front of the
// user. Nothing is written until the field is sent, and esc leaves everything
// as it was.
func (m *model) ask(o order) tea.Cmd {
	switch o {
	case orderTrain:
		if m.cfg.Train == nil {
			return nil
		}
		m.input.Placeholder = ""
	case orderEquip:
		if m.cfg.Equip == nil {
			return nil
		}
		if _, ok := m.selected(); !ok {
			m.status = m.nothingSelected()
			return nil
		}
		m.input.Placeholder = "gh:owner/repo#ref:subpath"
	default:
		return nil
	}
	m.pending = o
	m.scr = screenPrompt
	m.note, m.status = "", ""
	m.input.Reset()
	m.styleInput()
	m.sizeInput()
	return m.input.Focus()
}

// onPromptKey is the prompt card's half of the keyboard.
func (m *model) onPromptKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case key.Matches(msg, promptWithdraw):
		m.input.Blur()
		m.stand("Order withdrawn.")
		return nil
	case key.Matches(msg, promptSubmit):
		return m.submit()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	// The refusal on the card was about what was typed, and what was typed has
	// just changed.
	m.note = ""
	return cmd
}

// submit carries out the prompt's order with what was typed.
//
// Each order runs exactly what its command runs. A train writes one definition
// and fetches nothing, so it keeps the screen, the way a recall does. An equip
// fetches, so it is handed the terminal the way a deploy is - after its source
// has been checked for a spelling `barracks equip` would refuse, because that
// refusal needs no fetch and belongs on the card the typo is still on.
func (m *model) submit() tea.Cmd {
	raw := strings.TrimSpace(m.input.Value())
	if raw == "" {
		m.note = fmt.Sprintf("Type a %s, or esc to stand down.", m.promptNoun())
		return nil
	}
	cfg := m.cfg
	switch m.pending {
	case orderTrain:
		m.work(orderTrain)
		return func() tea.Msg {
			return promptedMsg{order: orderTrain, name: raw, out: cfg.Train(context.Background(), raw)}
		}
	case orderEquip:
		u, ok := m.selected()
		if !ok {
			m.stand("")
			return nil
		}
		if cfg.CheckSource != nil {
			if err := cfg.CheckSource(raw); err != nil {
				m.note = err.Error()
				return nil
			}
		}
		m.work(orderEquip)
		l := u.Loadout
		job := &terminalJob{run: func(s Session) Preview {
			return Preview{Outcome: cfg.Equip(context.Background(), l, raw, s)}
		}}
		return m.exec(job, func(err error) tea.Msg {
			// The handover's own verdict first - a terminal that could not be
			// released is a refusal, one that came back imperfectly a notice -
			// and then the prompt's rule for what a refusal does.
			done, _ := job.done(err).(doneMsg)
			return promptedMsg{order: orderEquip, name: l.Name, out: done.p.Outcome}
		})
	}
	m.stand("")
	return nil
}

// work puts the in-flight card up for a prompt's order. The field keeps its
// text, which is what a refusal comes back to.
func (m *model) work(o order) {
	m.input.Blur()
	m.note = ""
	m.working = o
	m.scr = screenWorking
}

// prompted is where a train or equip order lands.
//
// A refusal goes back to the prompt it came from, still holding what was typed,
// with barracks' own words for what was wrong: that is the whole difference
// between a prompt and an order card, and it is what keeps a mistyped name or
// an unknown ref from being a trip out to the roster and back. What worked is
// re-read from disk, because only the records say what the roster now holds.
func (m *model) prompted(msg promptedMsg) tea.Cmd {
	m.working = orderNone
	if msg.out.Err != nil {
		m.pending = msg.order
		m.scr = screenPrompt
		m.note = strings.Join(append([]string{msg.out.Err.Error()}, prefixed("! ", msg.out.Notices)...), "\n")
		return m.input.Focus()
	}
	switch msg.order {
	case orderTrain:
		// The new unit is where the cursor goes, because the next thing a
		// loadout with nothing in it needs is the order that fills it.
		m.stand(fmt.Sprintf("Trained %s. Press e to equip it.", msg.name))
		m.follow = msg.name
	default:
		m.pending = orderNone
		m.result = msg.out
		m.scr = screenOutcome
	}
	return m.refresh(false)
}

// land puts the cursor on the unit of that name, if the roster has one.
func (m *model) land(name string) {
	if name == "" {
		return
	}
	for i, u := range m.st.Units {
		if u.Loadout.Name == name {
			m.cursor = i
			return
		}
	}
}

// nothingSelected is what an order says when there is no unit to give it to.
// On an empty roster that is not a question of the cursor at all, so it names
// the key that puts a unit there.
func (m *model) nothingSelected() string {
	if len(m.st.Units) == 0 {
		return "No units yet - press n to train one."
	}
	return "No unit selected."
}

// promptNoun is what the prompt's field holds.
func (m *model) promptNoun() string {
	if m.pending == orderEquip {
		return "source"
	}
	return "name"
}

// styleInput dresses the field in the roster's own palette. It is re-applied
// whenever the palette is, so a prompt opened before the terminal answered the
// background question is not left in the other one.
func (m *model) styleInput() {
	st := textinput.DefaultStyles(true)
	for _, s := range []*textinput.StyleState{&st.Focused, &st.Blurred} {
		s.Prompt = m.th.label
		s.Text = m.th.body
		s.Placeholder = m.th.faint
	}
	st.Cursor.Color = m.th.selected
	// A static cursor, not a blinking one. A blink is a timer, and a timer is
	// a command that re-arms itself for as long as the prompt is open.
	st.Cursor.Blink = false
	m.input.SetStyles(st)
	m.input.Prompt = "> "
}

// promptModal is the prompt card: what is being asked, the field, and how to
// send or leave it.
//
// It gives way in the same order every card does. The head says what the
// order is and to what, the field is the thing being answered and the foot is
// the only thing that says how to leave, so none of those three is ever cut.
// A refusal is barracks' own sentence and names what was wrong, so it is
// wrapped rather than truncated and is the last thing to go; the hints under
// the field go first, with one row held back to count them.
func (m *model) promptModal() string {
	text := m.cardText()
	head := []string{m.th.title.Render(strings.ToUpper(m.pending.verb()) + " ORDER"), ""}
	var hints []string
	line := func(s string) { head = append(head, m.th.body.Render(truncate(s, text))) }
	dim := func(s string) { hints = append(hints, m.th.faint.Render(truncate(s, text))) }

	// Written to fit cardProse, like every other card's prose.
	verb := "train"
	switch m.pending {
	case orderTrain:
		line("Name the new loadout.")
		dim("Letters, digits, dot, dash or underscore.")
		dim("It carries nothing until you equip it.")
	case orderEquip:
		verb = "equip"
		name := ""
		if u, ok := m.selected(); ok {
			name = u.Loadout.Name
		}
		line(fmt.Sprintf("Equip %s with which source?", name))
		dim("gh:owner/repo, a git URL, or a path on disk.")
		dim("Add #ref to pin it, #ref:subpath to narrow it.")
	}

	field := []string{"", m.input.View()}

	var note []string
	if m.note != "" {
		note = append(note, "")
		for _, para := range strings.Split(m.note, "\n") {
			for _, l := range strings.Split(wrap(para, text), "\n") {
				note = append(note, m.th.fail.Render(l))
			}
		}
	}
	foot := []string{"", m.th.faint.Render(fmt.Sprintf("enter %s   esc stand down", verb))}

	avail := m.cardRows() - len(head) - len(field) - len(foot)
	keep := minInt(1, len(hints))
	note = m.elide(note, avail-keep)
	hints = m.elide(hints, avail-len(note))

	out := append([]string{}, head...)
	out = append(out, hints...)
	out = append(out, field...)
	out = append(out, note...)
	out = append(out, foot...)
	return m.card(out...)
}

// prefixed is every line with p in front of it.
func prefixed(p string, lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, p+l)
	}
	return out
}

// sizeInput fits the field to the card it sits on: the prompt, the text, and
// the one column the cursor takes past the end of it. Longer text scrolls
// inside the field rather than wrapping into a row the card did not budget for.
func (m *model) sizeInput() {
	m.input.SetWidth(maxInt(1, m.cardText()-lipgloss.Width(m.input.Prompt)-1))
}
