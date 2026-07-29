package tui

import (
	"bytes"
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
//	@work      run every command the model has returned so far, holding their
//	           results, which is what makes the in-flight screen capturable
//	@pump      the same, and deliver the held results too
func Frame(cfg Config, w, h int, script ...string) string {
	frame, _ := FrameAndTerminal(cfg, w, h, script...)
	return frame
}

// FrameAndTerminal is Frame plus everything an order wrote to the terminal
// while the roster had handed it back.
//
// An order that fetches runs with the screen given up - see terminalJob - so
// what it reported is not on the frame at all, and a harness that could not see
// it could not tell a talkative order from a silent one. The handover itself is
// run in process here, because it is the one part of a session that needs a
// program loop; everything either side of it is the model a real run drives.
func FrameAndTerminal(cfg Config, w, h int, script ...string) (string, string) {
	m := newModel(cfg)

	var released bytes.Buffer
	m.exec = func(c tea.ExecCommand, fn tea.ExecCallback) tea.Cmd {
		return func() tea.Msg {
			c.SetStdin(strings.NewReader(""))
			c.SetStdout(&released)
			c.SetStderr(&released)
			err := c.Run()
			if fn == nil {
				return nil
			}
			return fn(err)
		}
	}

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
	return m.View().Content, released.String()
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
	case "space":
		// The name a binding is written in and the key the terminal sends are
		// not the same string for this one, and a harness that took the name
		// literally would press "s" instead - which is the deploy key.
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
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
