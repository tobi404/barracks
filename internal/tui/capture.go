package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Frame renders one frame of the roster at the given size, after running a
// short script of key presses.
//
// It exists because the whole screen is a pure function of the model, and that
// is only worth anything if something actually exercises it that way: Frame
// drives the same Update and the same View a real session drives, with no
// terminal, no goroutine and no timing. It is the roster's test harness, and
// the reason a layout regression is something the suite can catch rather than
// something a person has to notice.
//
// A script entry is either a key name ("j", "enter", "esc") or one of two
// directives:
//
//	@size:WxH  resize the terminal mid-script
//	@work      run every command the model has returned so far and deliver only
//	           the progress they reported while running, holding their results
//	@pump      the same, and deliver the held results too
//
// Without an @pump a command the model returned is left un-run, which is what
// makes the in-flight screen capturable at all; @work is the frame in between,
// where the work has reported something but has not finished.
func Frame(cfg Config, w, h int, script ...string) string {
	m := newModel(cfg)

	var produced []tea.Msg
	m.out.bind(func(msg tea.Msg) { produced = append(produced, msg) })

	var pending []tea.Cmd
	var held []tea.Msg
	deliver := func(msg tea.Msg) {
		_, cmd := m.Update(msg)
		if cmd != nil {
			pending = append(pending, cmd)
		}
	}
	drain := func(results bool) {
		for len(pending) > 0 {
			cmd := pending[0]
			pending = pending[1:]
			if msg := cmd(); msg != nil {
				held = append(held, msg)
			}
		}
		// Anything the command reported while it ran arrives first, in the
		// order it was reported, exactly as the program's own loop would have
		// taken it.
		for _, p := range produced {
			deliver(p)
		}
		produced = nil
		if !results {
			return
		}
		for _, msg := range held {
			deliver(msg)
		}
		held = nil
	}

	deliver(tea.WindowSizeMsg{Width: w, Height: h})
	for _, step := range script {
		switch {
		case step == "@work":
			drain(false)
		case step == "@pump":
			for drain(true); len(pending) > 0; drain(true) {
			}
		case strings.HasPrefix(step, "@size:"):
			var nw, nh int
			if _, err := fmt.Sscanf(step[6:], "%dx%d", &nw, &nh); err == nil {
				deliver(tea.WindowSizeMsg{Width: nw, Height: nh})
			}
		default:
			deliver(keyPress(step))
		}
	}
	return m.View().Content
}

// keyPress turns a key name into the message the terminal would have sent.
func keyPress(name string) tea.KeyPressMsg {
	switch name {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	}
	r := []rune(name)
	k := tea.KeyPressMsg{Code: r[0], Text: name}
	if len(r) == 1 && r[0] >= 'A' && r[0] <= 'Z' {
		// A capital arrives as the shifted lower-case key; Bubble Tea reports
		// the base key plus the text it produced.
		k.Code = r[0] + ('a' - 'A')
		k.ShiftedCode = r[0]
		k.Mod = tea.ModShift
	}
	return k
}
