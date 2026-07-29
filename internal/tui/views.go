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
		// Compositor, not Canvas.Compose: a Layer's own Draw ignores its x and
		// y, and it is the compositor that flattens the hierarchy and applies
		// the offsets. Composing the layers straight onto a canvas silently
		// paints both at the origin.
		v.SetContent(lipgloss.NewCompositor(
			lipgloss.NewLayer(screenContent).Z(0),
			lipgloss.NewLayer(overlay).
				X(maxInt(0, (m.w-lipgloss.Width(overlay))/2)).
				Y(maxInt(0, (m.h-lipgloss.Height(overlay))/2)).
				Z(1),
		).Render())
		return v
	}
	v.SetContent(screenContent)
	return v
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
	line := fmt.Sprintf(" %s %s   %s",
		m.th.label.Render("ground"),
		m.th.body.Render(where),
		m.th.faint.Render(fmt.Sprintf("%d mustered · %d standing here", len(m.st.Units), deployed)))

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
		fmt.Fprintf(&b, "     %s\n", m.th.faint.Render(fmt.Sprintf("pinned %s · %d skills", shortCommit(eq.Commit), len(eq.Skills))))
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
			m.th.body.Render(fmt.Sprintf("committed to this repository · %d skills", u.Committed.SkillCount())))
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
	return fmt.Sprintf("%s · %d skills · %s", l.Target, len(l.Links), l.Kind)
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
	case screenOutcome:
		return m.outcomeModal()
	case screenHelp:
		return m.helpModal()
	}
	return ""
}

func (m *model) confirmModal() string {
	u, ok := m.selected()
	if !ok {
		return ""
	}
	var b strings.Builder
	fmt.Fprintln(&b, m.th.title.Render(strings.ToUpper(m.pending.verb())+" ORDER"))
	fmt.Fprintln(&b)
	switch m.pending {
	case orderDeploy:
		fmt.Fprintf(&b, "%s\n", m.th.body.Render(fmt.Sprintf("Send %s into %s?", u.Loadout.Name, filepath.Base(m.st.Root))))
		fmt.Fprintf(&b, "%s\n", m.th.faint.Render(fmt.Sprintf("%d sources · %d skills · %s", len(u.Loadout.Equipment), u.SkillCount(), targetLabel(u.Loadout))))
		fmt.Fprintf(&b, "%s\n", m.th.faint.Render("Symlinks from the shared store. git status stays clean."))
	case orderRecall:
		fmt.Fprintf(&b, "%s\n", m.th.body.Render(fmt.Sprintf("Stand %s down from %s?", u.Loadout.Name, filepath.Base(m.st.Root))))
		fmt.Fprintf(&b, "%s\n", m.th.faint.Render(fmt.Sprintf("%d live %s here.", len(u.Here), plural(len(u.Here), "spawn", "spawns"))))
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, m.th.faint.Render("y confirm   n stand down"))
	return m.th.modal.Render(b.String())
}

func (m *model) workingModal() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n\n", m.sp.View(), m.th.title.Render("MOVING OUT"))
	fmt.Fprintln(&b, m.th.faint.Render("forming up..."))
	return m.th.modal.Width(64).Render(b.String())
}

func (m *model) outcomeModal() string {
	var b strings.Builder
	if m.result.Err != nil {
		fmt.Fprintln(&b, m.th.fail.Render("REFUSED"))
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, m.th.body.Render(wrap(m.result.Err.Error(), 58)))
	} else {
		fmt.Fprintln(&b, m.th.ok.Render(strings.ToUpper(m.result.Title)))
		fmt.Fprintln(&b)
		for _, line := range m.result.Lines {
			fmt.Fprintln(&b, m.th.body.Render(truncate(line, 58)))
		}
	}
	for _, n := range m.result.Notices {
		fmt.Fprintln(&b, m.th.fail.Render("! "+truncate(n, 58)))
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, m.th.faint.Render("any key to return to the roster"))
	return m.th.modal.Width(64).Render(b.String())
}

func (m *model) helpModal() string {
	var b strings.Builder
	fmt.Fprintln(&b, m.th.title.Render("ORDERS"))
	fmt.Fprintln(&b)
	fmt.Fprint(&b, m.help.FullHelpView(m.keys.FullHelp()))
	fmt.Fprintln(&b)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, m.th.faint.Render("any key to return"))
	return m.th.modal.Render(b.String())
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
