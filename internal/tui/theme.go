package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// theme is every colour and box the roster draws with.
//
// It is built once per terminal rather than referenced from package globals so
// that the light/dark answer the terminal gives us can change it - and so that a
// test can build one without a terminal at all.
type theme struct {
	brass    color.Color // headings, the unit's own colours
	steel    color.Color // chrome: rules, borders, labels
	parch    color.Color // body text
	dim      color.Color // secondary text
	live     color.Color // deployed, healthy
	held     color.Color // committed to the repository
	alarm    color.Color // refusals and failures
	selected color.Color // the row under the cursor

	title    lipgloss.Style
	subtitle lipgloss.Style
	label    lipgloss.Style
	body     lipgloss.Style
	faint    lipgloss.Style
	cursor   lipgloss.Style
	badge    lipgloss.Style
	panel    lipgloss.Style
	modal    lipgloss.Style
	fail     lipgloss.Style
	ok       lipgloss.Style
}

func newTheme(dark bool) theme {
	pick := lipgloss.LightDark(dark)
	t := theme{
		brass:    pick(lipgloss.Color("#8a6a1f"), lipgloss.Color("#d9a441")),
		steel:    pick(lipgloss.Color("#6b7684"), lipgloss.Color("#7c8b9c")),
		parch:    pick(lipgloss.Color("#2b2b2b"), lipgloss.Color("#e6ddc9")),
		dim:      pick(lipgloss.Color("#7a7466"), lipgloss.Color("#8d8677")),
		live:     pick(lipgloss.Color("#2f7a45"), lipgloss.Color("#6fbf87")),
		held:     pick(lipgloss.Color("#1f5f8a"), lipgloss.Color("#63b0e0")),
		alarm:    pick(lipgloss.Color("#a32b2b"), lipgloss.Color("#e06c6c")),
		selected: pick(lipgloss.Color("#5b3d0c"), lipgloss.Color("#f0c674")),
	}
	t.title = lipgloss.NewStyle().Foreground(t.brass).Bold(true)
	t.subtitle = lipgloss.NewStyle().Foreground(t.steel)
	t.label = lipgloss.NewStyle().Foreground(t.steel)
	t.body = lipgloss.NewStyle().Foreground(t.parch)
	t.faint = lipgloss.NewStyle().Foreground(t.dim)
	t.cursor = lipgloss.NewStyle().Foreground(t.selected).Bold(true)
	t.badge = lipgloss.NewStyle().Foreground(t.live)
	t.fail = lipgloss.NewStyle().Foreground(t.alarm)
	t.ok = lipgloss.NewStyle().Foreground(t.live)
	t.panel = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(t.steel).
		Padding(0, 1)
	// The margin is what keeps the screen behind a modal from butting straight
	// against its border: the compositor draws those spaces, so the card reads
	// as a card rather than a hole cut in the roster.
	t.modal = lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(t.brass).
		Padding(1, 3).
		Margin(0, 1)
	return t
}
