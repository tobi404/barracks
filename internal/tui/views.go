package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/tobi404/barracks/internal/lease"
)

const (
	headerHeight = 3
	footerHeight = 2
	rosterWidth  = 46

	// The roster's fixed columns. The unit's name takes whatever is left, so
	// the row can never be wider than the pane and wrap - a wrapped row in a
	// fixed-height pane pushes the last unit off the bottom of the screen.
	colMarker  = 2
	colSources = 4
	colSkills  = 5
	colPosture = 13

	// legendRows is what the posture key costs the pane: a blank line, the
	// heading, and one line per glyph.
	legendRows = 6

	// cardWidthMax is how wide a card in front of the roster is drawn when the
	// terminal can hold it, and cardChrome is what one costs in rows before any
	// of its own: two border rows, and the padding above and below.
	cardWidthMax = 64
	cardChrome   = 4

	// cardProse is the columns a card's own sentences are written to fit: what
	// cardText comes to on the narrowest terminal the roster claims to work on
	// (60 columns). Every fixed line a card prints is written inside it, so the
	// prose that explains an order is never cut off mid-sentence - a truncated
	// sentence reads as a fault rather than as a description of what is about
	// to happen, and unlike a list it cannot be counted instead of read.
	cardProse = 48
)

func (m *model) View() tea.View {
	var v tea.View
	v.AltScreen = true
	v.WindowTitle = "barracks"
	if !m.ready {
		v.SetContent("")
		return v
	}

	body := lipgloss.JoinHorizontal(lipgloss.Top, m.rosterPane(), m.dossierPane())
	screenContent := lipgloss.JoinVertical(lipgloss.Left, m.header(), body, m.footer())

	// A modal is a layer over the roster rather than a different screen, so the
	// unit being ordered about stays visible behind the order.
	if overlay := m.overlay(); overlay != "" {
		oh := lipgloss.Height(overlay)
		y := maxInt(0, (m.h-oh)/2)
		// Compositor, not Canvas.Compose: a Layer's own Draw ignores its x and
		// y, and it is the compositor that flattens the hierarchy and applies
		// the offsets. Composing the layers straight onto a canvas silently
		// paints both at the origin.
		return m.fit(&v, lipgloss.NewCompositor(
			lipgloss.NewLayer(screenContent).Z(0),
			lipgloss.NewLayer(curtain(m.w, oh)).X(0).Y(y).Z(1),
			lipgloss.NewLayer(overlay).
				X(maxInt(0, (m.w-lipgloss.Width(overlay))/2)).
				Y(y).
				Z(2),
		).Render())
	}
	return m.fit(&v, screenContent)
}

// fit is the one place the frame is bounded to the terminal it is drawn on.
//
// Everything above budgets for its own size, and this is what makes that a
// guarantee rather than an intention. The alternate screen does not scroll a
// frame larger than itself, it clips it, and what falls off is the bottom and
// the right: the status line, the help bar, and the last thing a card says.
// An overlay is the way this is most easily broken, because the compositor's
// bounds are the union of its layers, so a card taller than the screen takes
// the whole frame with it.
func (m *model) fit(v *tea.View, content string) tea.View {
	v.SetContent(lipgloss.NewStyle().
		MaxWidth(maxInt(1, m.w)).
		MaxHeight(maxInt(1, m.h)).
		Render(content))
	return *v
}

// curtain is the blank band a card is drawn on, the full width of the terminal
// and exactly as tall as the card.
//
// The roster behind a card is context, but only whole rows of it are: a card is
// narrower than the screen, so without this the compositor leaves whatever the
// dossier had on those rows sticking out past the card's border - the tail of a
// truncated word, an orphaned "reaped" floating beside a REFUSED card. That
// reads as a rendering fault rather than as context, and no amount of shortening
// the strings behind it would fix it, because the line that overflows is
// whichever one happens to be long. Clearing the band is what makes the card's
// edge clean for a line of any length. The rows a card does not cover are left
// exactly as they were, which is the whole point of drawing the order over the
// unit rather than instead of it.
func curtain(w, h int) string {
	row := strings.Repeat(" ", maxInt(1, w))
	rows := make([]string, maxInt(1, h))
	for i := range rows {
		rows[i] = row
	}
	return strings.Join(rows, "\n")
}

func (m *model) header() string {
	crest := m.th.title.Render("⚔  B A R R A C K S")
	// The version line barracks prints for --version carries the commit and the
	// build date too. In a header that is a banner, not a report, so only the
	// first field of it is shown.
	build := m.th.faint.Render(strings.Fields(m.cfg.Version + " dev")[0])
	gap := maxInt(1, m.w-lipgloss.Width(crest)-lipgloss.Width(build)-2)
	top := " " + crest + strings.Repeat(" ", gap) + build + " "

	where := "no repository here - deployments are unavailable"
	if m.st.Root != "" {
		where = abbreviate(m.st.Root)
	}
	deployed := 0
	for _, u := range m.st.Units {
		if len(u.Here) > 0 || u.Committed != nil {
			deployed++
		}
	}
	// The counts are what the line is for, and a deep repository path is what
	// would otherwise push them off the right of the screen, so the path is the
	// half that gives way.
	counts := fmt.Sprintf("%d mustered · %d standing here", len(m.st.Units), deployed)
	line := fmt.Sprintf(" %s %s   %s",
		m.th.label.Render("ground"),
		m.th.body.Render(truncate(where, maxInt(8, m.w-len([]rune(counts))-11))),
		m.th.faint.Render(counts))

	rule := m.th.subtitle.Render(strings.Repeat("─", maxInt(1, m.w)))
	return lipgloss.JoinVertical(lipgloss.Left, top, rule, line)
}

func (m *model) paneWidths() (int, int) {
	left := rosterWidth
	if m.w < 96 {
		left = maxInt(24, m.w/2)
	}
	right := maxInt(20, m.w-left)
	return right, left
}

func (m *model) bodyHeight() int {
	return maxInt(3, m.h-headerHeight-footerHeight)
}

func (m *model) rosterPane() string {
	_, w := m.paneWidths()
	h := m.bodyHeight()
	inner := maxInt(8, w-4)
	nameW := maxInt(4, inner-colMarker-colSources-colSkills-colPosture)

	// Every row that is not a unit is paid for before the window is sized. The
	// pane pads to the height it declares but never truncates, so a pane whose
	// rows outnumber that height grows the frame past the terminal, and the
	// terminal clips what falls off the bottom: first the status line - the
	// roster's only channel for a refusal - and then the help bar.
	content := maxInt(1, h-3) // the pane's rows, less its border and its title
	reserved := 1             // the column header
	if len(m.st.Units) == 0 {
		reserved++ // the line that says how to fill an empty roster
	}
	reserved += len(m.st.Problems)
	showLegend := content-reserved-legendRows >= 1
	if showLegend {
		reserved += legendRows
	}
	budget := content - reserved
	if len(m.st.Units) > budget {
		budget-- // the count a windowed roster carries
	}

	var rows []string
	rows = append(rows, m.th.label.Render(
		pad("", colMarker)+pad("UNIT", nameW)+pad("SRC", colSources)+pad("SKL", colSkills)+"POSTURE"))
	if len(m.st.Units) == 0 {
		rows = append(rows, m.th.faint.Render(truncate("(no units trained - barracks train <name>)", inner)))
	}

	// The pane is a fixed height, so only a window of the roster is drawn and
	// the cursor is kept inside it. A roster taller than the terminal that
	// silently stopped at the bottom would be a roster that hides units.
	first, last := m.window(budget)
	for i := first; i < last; i++ {
		u := m.st.Units[i]
		style := m.th.body
		marker := pad("", colMarker)
		if i == m.cursor {
			style = m.th.cursor
			marker = m.th.cursor.Render(pad("▸", colMarker))
		}
		rows = append(rows, marker+
			style.Render(pad(u.Loadout.Name, nameW))+
			m.th.faint.Render(pad(fmt.Sprintf("%d", len(u.Loadout.Equipment)), colSources))+
			m.th.faint.Render(pad(fmt.Sprintf("%d", u.SkillCount()), colSkills))+
			m.postureBadge(u))
	}
	if last < len(m.st.Units) || first > 0 {
		rows = append(rows, m.th.faint.Render(fmt.Sprintf("  %d-%d of %d", first+1, last, len(m.st.Units))))
	}
	for _, p := range m.st.Problems {
		rows = append(rows, m.th.fail.Render("! "+truncate(p, inner-2)))
	}
	// The legend is the key to the posture column, and it is sized off the same
	// inner width as the rows so it can never be the thing that wraps. It is the
	// first thing a pane too short for everything gives up: the units, the count
	// and the unreadable records all outrank a key to a column.
	if showLegend {
		legend := func(style lipgloss.Style, glyph, word, gloss string) string {
			return style.Render(pad(glyph+" "+word, 12)) + m.th.faint.Render(truncate(gloss, inner-12))
		}
		rows = append(rows, "", m.th.label.Render("POSTURE"),
			legend(m.th.badge, "●", "spawned", "symlinked, lease-held"),
			legend(lipgloss.NewStyle().Foreground(m.th.held), "▣", "held", "committed here"),
			legend(m.th.faint, "○", "afield", "standing in another repo"),
			legend(m.th.faint, "·", "reserve", "deployed nowhere"))
	}
	// The last guard, for a terminal too short to hold even what was reserved.
	// Rows the pane cannot draw are better dropped here than drawn and left for
	// the terminal to cut off the bottom of the screen instead.
	if len(rows) > content {
		rows = rows[:content]
	}

	return m.th.panel.
		Width(w).
		Height(h).
		Render(titled("ROSTER", m.th) + "\n" + strings.Join(rows, "\n"))
}

// window is the slice of the roster that fits in n rows, with the cursor in it.
func (m *model) window(n int) (int, int) {
	if n < 1 {
		n = 1
	}
	if len(m.st.Units) <= n {
		return 0, len(m.st.Units)
	}
	first := m.cursor - n/2
	if first < 0 {
		first = 0
	}
	if first+n > len(m.st.Units) {
		first = len(m.st.Units) - n
	}
	return first, first + n
}

func (m *model) postureBadge(u unit) string {
	s := u.Status()
	switch {
	case u.Committed != nil:
		return lipgloss.NewStyle().Foreground(m.th.held).Render("▣ " + s)
	case len(u.Here) > 0:
		return m.th.badge.Render("● " + s)
	case u.Away > 0:
		return m.th.faint.Render("○ " + s)
	default:
		return m.th.faint.Render("· " + s)
	}
}

func (m *model) dossierPane() string {
	w, _ := m.paneWidths()
	h := m.bodyHeight()
	return m.th.panel.
		Width(w).
		Height(h).
		Render(titled("DOSSIER", m.th) + "\n" + m.vp.View())
}

// dossier is the detail body for one unit. It is a plain string builder rather
// than anything stateful so that a test can assert on exactly what the pane
// would show.
func (m *model) dossier(u unit, w int) string {
	var b strings.Builder
	l := u.Loadout

	fmt.Fprintln(&b, m.th.title.Render(l.Name)+"  "+m.postureBadge(u))
	if l.Description != "" {
		fmt.Fprintln(&b, m.th.body.Render(l.Description))
	}
	fmt.Fprintln(&b, m.th.faint.Render(fmt.Sprintf("id %s · trained %s", l.ID, l.CreatedAt.Format("2006-01-02"))))
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, m.th.label.Render("ORDERS TO")+"  "+m.th.body.Render(targetLabel(l)))
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, m.th.label.Render("EQUIPMENT"))
	if len(l.Equipment) == 0 {
		fmt.Fprintln(&b, m.th.faint.Render("  nothing issued - barracks equip "+l.Name+" <source>"))
	}
	for _, eq := range l.Equipment {
		fmt.Fprintf(&b, "  %s %s\n", m.th.body.Render("▪"), m.th.body.Render(truncate(eq.Ident(), w-4)))
		fmt.Fprintf(&b, "     %s\n", m.th.faint.Render(fmt.Sprintf("pinned %s · %d %s",
			shortCommit(eq.Commit), len(eq.Skills), plural(len(eq.Skills), "skill", "skills"))))
		for _, s := range eq.Skills {
			fmt.Fprintf(&b, "       %s\n", m.th.faint.Render(truncate(s, w-8)))
		}
	}
	fmt.Fprintln(&b)

	fmt.Fprintln(&b, m.th.label.Render("DEPLOYMENTS"))
	if !u.Deployed() {
		fmt.Fprintln(&b, m.th.faint.Render("  in reserve"))
	}
	if u.Committed != nil {
		fmt.Fprintf(&b, "  %s %s\n",
			lipgloss.NewStyle().Foreground(m.th.held).Render("▣"),
			m.th.body.Render(fmt.Sprintf("committed to this repository · %d %s",
				u.Committed.SkillCount(), plural(u.Committed.SkillCount(), "skill", "skills"))))
		fmt.Fprintf(&b, "     %s\n", m.th.faint.Render("barracks.lock · no lease, never reaped"))
	}
	for _, ls := range u.Here {
		fmt.Fprintf(&b, "  %s %s\n", m.th.badge.Render("●"), m.th.body.Render(describeLease(ls)))
		fmt.Fprintf(&b, "     %s\n", m.th.faint.Render(truncate(relativeTo(m.st.Root, ls.Dir), w-5)))
	}
	if u.Away > 0 {
		fmt.Fprintf(&b, "  %s\n", m.th.faint.Render(fmt.Sprintf("○ %d %s elsewhere on this machine", u.Away, plural(u.Away, "spawn", "spawns"))))
	}
	return b.String()
}

// relativeTo shortens a path that sits inside the repository the roster is
// standing in. The absolute path is what the record holds, but a dossier that
// spends two thirds of a line repeating the directory the header already names
// is a dossier nobody reads.
func relativeTo(root, path string) string {
	if root == "" {
		return path
	}
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return "./" + rel
	}
	return path
}

func describeLease(l *lease.Lease) string {
	return fmt.Sprintf("%s · %d %s · %s", l.Target, len(l.Links), plural(len(l.Links), "skill", "skills"), l.Kind)
}

func (m *model) footer() string {
	help := m.th.faint.Render(m.help.ShortHelpView(m.keys.ShortHelp()))
	status := " "
	if m.status != "" {
		status = " " + m.th.subtitle.Render("▸ "+m.status)
	}
	return lipgloss.JoinVertical(lipgloss.Left, " "+help, status)
}

// overlay is the modal in front of the roster, or "" when there is none.
func (m *model) overlay() string {
	switch m.scr {
	case screenConfirm:
		return m.confirmModal()
	case screenWorking:
		return m.workingModal()
	case screenPreview:
		return m.previewModal()
	case screenOutcome:
		return m.outcomeModal()
	case screenHelp:
		return m.helpModal()
	}
	return ""
}

// cardWidth is how wide a card is drawn: its natural width, or whatever the
// terminal can hold when that is less.
func (m *model) cardWidth() int { return minInt(cardWidthMax, maxInt(24, m.w-4)) }

// cardText is the columns a card's own text has, inside its border and padding.
// Every line a card draws is cut to this rather than left to wrap, because a
// line that wraps is a row the card did not budget for.
func (m *model) cardText() int { return maxInt(8, m.cardWidth()-8) }

// cardRows is how many rows of that text this terminal leaves inside a card.
func (m *model) cardRows() int { return maxInt(1, m.h-cardChrome) }

// card renders rows as the card in front of the roster. Every overlay goes
// through here: bounding one of them is not the rule, the rule is that nothing
// barracks draws over the roster may outgrow the screen it is drawn on.
func (m *model) card(rows ...string) string { return m.cardOf(m.cardWidth(), rows) }

// fittedCard is a card that takes the width of what it holds, for the one
// overlay whose content is laid out in columns something else has already
// sized. Cutting that to the standard width would wrap the columns into each
// other rather than shorten them.
func (m *model) fittedCard(rows []string) string { return m.cardOf(0, rows) }

func (m *model) cardOf(width int, rows []string) string {
	s := m.th.modal.MaxHeight(maxInt(1, m.h))
	if width > 0 {
		s = s.Width(width)
	}
	return s.Render(strings.Join(rows, "\n"))
}

// elide keeps at most n of rows, cutting the middle out and saying how many
// went with it.
//
// Nothing is ever dropped in silence: a card that quietly showed eight of a
// spawn's thirty skills would read exactly like a spawn that installed eight.
func (m *model) elide(rows []string, n int) []string {
	if n >= len(rows) {
		return rows
	}
	if n <= 0 {
		return nil
	}
	marker := func(k int) string { return m.th.faint.Render(fmt.Sprintf("… %d more", k)) }
	if n == 1 {
		return []string{marker(len(rows))}
	}
	front := n / 2
	back := n - front - 1
	out := append([]string{}, rows[:front]...)
	out = append(out, marker(len(rows)-front-back))
	return append(out, rows[len(rows)-back:]...)
}

func (m *model) confirmModal() string {
	u, ok := m.selected()
	if !ok {
		return ""
	}
	text := m.cardText()
	sources := fmt.Sprintf("%d %s · %d %s",
		len(u.Loadout.Equipment), plural(len(u.Loadout.Equipment), "source", "sources"),
		u.SkillCount(), plural(u.SkillCount(), "skill", "skills"))

	head := []string{m.th.title.Render(strings.ToUpper(m.pending.verb()) + " ORDER"), ""}
	var body []string
	line := func(s string) { body = append(body, m.th.body.Render(truncate(s, text))) }
	dim := func(s string) { body = append(body, m.th.faint.Render(truncate(s, text))) }

	// Every line below is written to fit cardProse columns, which is what a
	// card has on the narrowest terminal the roster claims to work on. A card's
	// prose is a sentence, and half a sentence ending in an ellipsis reads as a
	// fault rather than as a description of what is about to happen.
	switch m.pending {
	case orderDeploy:
		line(fmt.Sprintf("Send %s into %s?", u.Loadout.Name, filepath.Base(m.st.Root)))
		dim(sources + " · " + targetLabel(u.Loadout))
		dim("Symlinks from the shared store.")
		dim("git status stays clean.")
	case orderRecall:
		line(fmt.Sprintf("Stand %s down from %s?", u.Loadout.Name, filepath.Base(m.st.Root)))
		dim(fmt.Sprintf("%d live %s here.", len(u.Here), plural(len(u.Here), "spawn", "spawns")))
		// The asymmetry is deliberate and is said out loud rather than left for
		// somebody to discover: a recall from the roster is the personal tier
		// only, because removing tracked files from a checkout is not something
		// a single key press should do.
		dim("Spawns only - a garrison stays where it is.")
		dim("Remove that with: barracks recall " + u.Loadout.Name)
	case orderGarrison:
		line(fmt.Sprintf("Commit %s into %s?", u.Loadout.Name, filepath.Base(m.st.Root)))
		dim(sources)
		dim("Real files plus barracks.lock, tracked by git.")
		dim("Everyone who clones this repository gets them.")
		dim("Review the diff and commit it.")
	case orderLaunch:
		line(fmt.Sprintf("Run an agent with %s in %s?", u.Loadout.Name, filepath.Base(m.st.Root)))
		dim(sources)
		dim("Spawned for the session, recalled when it exits.")
	}

	foot := []string{"", m.th.faint.Render(m.confirmHint(text))}
	if m.note != "" {
		foot = append([]string{"", m.th.fail.Render(truncate(m.note, text))}, foot...)
	}

	// What gives way when the card cannot hold all of it. The picker is a
	// choice, so it is never cut below the row the cursor is on - a choice you
	// cannot see is a choice you cannot make - and the prose that explains the
	// order is what shrinks first.
	rows := m.cardRows()
	choices := m.pickerRows(rows - len(head) - len(foot) - 1)
	body = m.elide(body, rows-len(head)-len(foot)-len(choices))

	out := append([]string{}, head...)
	out = append(out, body...)
	out = append(out, choices...)
	out = append(out, foot...)
	return m.card(out...)
}

// confirmHint is the line that says which keys this card answers to.
//
// It names the picker's key only when there is something to pick, because a
// card that advertises a key it ignores is the same failure as a key that does
// nothing. And it says as much as the card is wide enough to hold on one line:
// this is the only thing on the card that says how to leave it, so it may never
// be the line that wraps into a row the card did not budget for, and it may
// never be truncated into "n stand…" either. The last form always fits, because
// no card is drawn narrower than it.
func (m *model) confirmHint(width int) string {
	forms := []string{"y confirm   n stand down"}
	if len(m.pick.options) > 0 {
		forms = []string{
			"space choose   ↑/↓ move   y confirm   n stand down",
			"space choose · y confirm · n stand down",
			"space · y · n stand down",
		}
	}
	for _, f := range forms {
		if len([]rune(f)) <= width {
			return f
		}
	}
	return forms[len(forms)-1]
}

// pickerRows is the picker's own band of the card, in at most n rows, or none
// when there is nothing to choose.
//
// The heading and the "showing this many" line are paid for out of n before a
// single option is drawn, for the reason every producer budgets for itself: the
// compositor's bounds are the union of its layers, so a card that overran here
// would take the whole frame off the screen with it.
func (m *model) pickerRows(n int) []string {
	if len(m.pick.options) == 0 || n < 3 {
		return nil
	}
	text := m.cardText()
	title := "TARGETS"
	if !m.pick.multi {
		title = "AGENT"
	}
	rows := []string{"", m.th.label.Render(title)}
	budget := n - len(rows)
	first, last := m.pick.window(budget)
	if last-first < len(m.pick.options) {
		budget--
		first, last = m.pick.window(budget)
	}
	for i := first; i < last; i++ {
		o := m.pick.options[i]
		box := "[ ]"
		if m.pick.on[i] {
			box = "[x]"
		}
		cursor := "  "
		style := m.th.body
		if i == m.pick.cursor {
			cursor, style = "▸ ", m.th.cursor
		}
		row := cursor + box + " " + o.Label
		if o.Note != "" {
			row += "  (" + o.Note + ")"
		}
		rows = append(rows, style.Render(truncate(row, text)))
	}
	if last-first < len(m.pick.options) {
		rows = append(rows, m.th.faint.Render(fmt.Sprintf("  %d-%d of %d", first+1, last, len(m.pick.options))))
	}
	return rows
}

func (m *model) workingModal() string {
	return m.card(
		m.sp.View()+" "+m.th.title.Render(m.working.working()),
		"",
		m.th.faint.Render("forming up..."))
}

// previewModal is the plan an upgrade would carry out, shown before any of it
// is carried out.
//
// The body is whatever the upgrade reported for itself, so the roster and
// `barracks upgrade --dry-run` describe a run in the same words - there is only
// one plan and only one renderer of it.
func (m *model) previewModal() string {
	text := m.cardText()
	head := []string{m.th.title.Render(strings.ToUpper(m.result.Title)), ""}
	foot := []string{"", m.th.faint.Render("y carry it out   n stand down")}
	return m.card(m.reportCard(head, foot, text)...)
}

func (m *model) outcomeModal() string {
	text := m.cardText()
	var head []string
	if m.result.Err != nil {
		head = append(head, m.th.fail.Render("REFUSED"), "")
	} else {
		head = append(head, m.th.ok.Render(strings.ToUpper(m.result.Title)), "")
	}
	foot := []string{"", m.th.faint.Render("any key to return to the roster")}
	return m.card(m.reportCard(head, foot, text)...)
}

// reportCard lays out what an order reported - its body and its notices -
// between a head and a foot neither of which may ever be cut.
//
// It is shared by the outcome card and the plan card because the two hold the
// same three things and have to give way in the same order. What gives way when
// the card cannot hold all of it: a notice is a path barracks declined to touch
// and is the last thing that may go; the foot is the only thing that says how
// to leave the card at all. The body is a list of what moved, and a list can be
// counted instead of read, so it is what is cut - never those two. The one row
// held back for it is the difference between a card that says twenty-two skills
// are not shown and a card on which they were never mentioned.
func (m *model) reportCard(head, foot []string, text int) []string {
	var body []string
	if m.result.Err != nil {
		// A barracks error names a path, so it is wrapped rather than cut.
		for _, line := range strings.Split(wrap(m.result.Err.Error(), text), "\n") {
			body = append(body, m.th.body.Render(line))
		}
	}
	for _, line := range m.result.Lines {
		body = append(body, m.th.body.Render(truncate(line, text)))
	}

	// A notice is wrapped rather than cut, for the reason wrap exists: it names
	// the path barracks declined to touch, and half a path is not one.
	var notices []string
	for _, n := range m.result.Notices {
		for _, line := range strings.Split(wrap("! "+n, text), "\n") {
			notices = append(notices, m.th.fail.Render(line))
		}
	}

	rows := m.cardRows()
	keep := minInt(1, len(body))
	notices = m.elide(notices, rows-len(head)-len(foot)-keep)
	body = m.elide(body, rows-len(head)-len(notices)-len(foot))

	out := append([]string{}, head...)
	out = append(out, body...)
	out = append(out, notices...)
	out = append(out, foot...)
	return out
}

func (m *model) helpModal() string {
	// A copy, sized to what a card can hold on this terminal: the footer's bar
	// is still the width of the screen, and this one is inside a border, six
	// columns of padding and a margin.
	h := m.help
	h.SetWidth(maxInt(8, m.w-10))

	rows := []string{m.th.title.Render("ORDERS"), ""}
	rows = append(rows, strings.Split(h.FullHelpView(m.keys.FullHelp()), "\n")...)
	rows = append(rows, "", m.th.faint.Render("any key to return"))
	return m.fittedCard(rows)
}

func titled(s string, th theme) string {
	return th.title.Render(s)
}

// pad left-aligns s in a field of n columns, truncating rather than overflowing.
func pad(s string, n int) string {
	if n <= 0 {
		return ""
	}
	s = truncate(s, n)
	return s + strings.Repeat(" ", n-len([]rune(s)))
}

// abbreviate shortens a path under the user's home to ~, the way a prompt does.
func abbreviate(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.Join("~", rel)
	}
	return p
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// wrap breaks text to n columns, hard-breaking a word too long to fit.
//
// The hard break is the point: the text this wraps is an error message, and a
// barracks error names a path. A wrapper that only broke on spaces would hand a
// 90-column path to a 58-column modal and let the terminal decide.
func wrap(s string, n int) string {
	if n < 8 {
		n = 8
	}
	var lines []string
	cur := ""
	flush := func() {
		if cur != "" {
			lines = append(lines, cur)
			cur = ""
		}
	}
	for _, word := range strings.Fields(s) {
		for len([]rune(word)) > n {
			flush()
			r := []rune(word)
			lines = append(lines, string(r[:n]))
			word = string(r[n:])
		}
		switch {
		case cur == "":
			cur = word
		case len([]rune(cur))+1+len([]rune(word)) > n:
			flush()
			cur = word
		default:
			cur += " " + word
		}
	}
	flush()
	return strings.Join(lines, "\n")
}
