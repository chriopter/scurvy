package tui

import (
	"fmt"
	"image/color"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/chriopter/scurvy/internal/scan"
)

// The look follows gum's: one pink accent, quiet greys, coloured badges
// for what needs attention.
var (
	cAccent = lipgloss.Color("212") // gum's pink
	cText   = lipgloss.Color("252")
	cDim    = lipgloss.Color("245")
	cFaint  = lipgloss.Color("240")
	cPickBg = lipgloss.Color("236")
	cOK     = lipgloss.Color("78")
	cFresh  = lipgloss.Color("220")
	cDue    = lipgloss.Color("209")
	cSick   = lipgloss.Color("197")
	cUnk    = lipgloss.Color("141")
	cInk    = lipgloss.Color("16")

	sDim    = lipgloss.NewStyle().Foreground(cDim)
	sFaint  = lipgloss.NewStyle().Foreground(cFaint)
	sOK     = lipgloss.NewStyle().Foreground(cOK)
	sDue    = lipgloss.NewStyle().Foreground(cDue)
	sErr    = lipgloss.NewStyle().Foreground(cUnk)
	sAccent = lipgloss.NewStyle().Foreground(cAccent)
	sBold   = lipgloss.NewStyle().Foreground(cText).Bold(true)
)

// badge is a level's label on its colour.
func badge(l scan.Level, unchecked bool) (string, lipgloss.Style) {
	b := lipgloss.NewStyle().Foreground(cInk).Bold(true)
	switch {
	case l == scan.Sick:
		return "EOL", b.Background(cSick)
	case l == scan.Due:
		return "OVERDUE", b.Background(cDue)
	case l == scan.Fresh:
		return "FRESH", b.Background(cFresh)
	case unchecked:
		return "UNKNOWN", b.Background(cUnk)
	}
	return "OK", lipgloss.NewStyle().Foreground(cOK)
}

func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "scurvy · " + m.host
	return v
}

// cols are the table's column widths, inside its border.
type cols struct{ name, source, running, status, finding int }

const gap = 2

func (m *Model) columns(inner int) cols {
	c := cols{name: len("CONTAINER"), source: len("docker run"), running: len("RUNNING"), status: len(" OVERDUE ")}
	for _, it := range m.items {
		c.name = max(c.name, len([]rune(it.Name()))+2, len([]rune(it.Project.Name)))
		c.running = max(c.running, len([]rune(it.Current)))
	}
	c.name = min(c.name, 30)
	c.running = min(c.running, 18)
	// cursor, then four gaps between five columns
	c.finding = inner - 2 - c.name - c.source - c.running - c.status - 4*gap
	if c.finding < 24 { // narrow: give the findings room first
		c.running = min(c.running, 10)
		c.name = min(c.name, 20)
		c.finding = inner - 2 - c.name - c.source - c.running - c.status - 4*gap
	}
	c.finding = max(c.finding, 8)
	return c
}

func (m *Model) render() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 100
	}
	if h <= 0 {
		h = 30
	}

	head := m.titleLine(w)
	pills := m.pills()
	foot := []string{m.status(w), m.help(w)}
	detailH := 6 // border, title and four lines
	tableH := max(5, h-2-len(foot)-detailH)

	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cFaint).Padding(0, 1)
	inner := w - box.GetHorizontalFrameSize()
	table := box.Width(w).Height(tableH).Render(m.table(inner, tableH-2))

	rows := m.rows()
	var detail string
	if !m.scanning || len(m.items) > 0 {
		if m.pick < len(rows) {
			detail = m.detail(rows[m.pick], w, detailH)
		}
	}
	if detail == "" {
		detail = strings.Repeat("\n", detailH-1)
	}
	return lipgloss.JoinVertical(lipgloss.Left, head, pills, table, detail, foot[0], foot[1])
}

func (m *Model) titleLine(w int) string {
	name := lipgloss.NewStyle().Foreground(cInk).Background(cAccent).Bold(true).Padding(0, 1).Render("🍋 scurvy")
	what := sDim.Render("  " + m.host)
	if n := len(m.items); n > 0 {
		what += sFaint.Render(fmt.Sprintf("  ·  %d containers", n))
	}
	left := name + what
	if m.newer.Tag == "" {
		return left
	}
	news := lipgloss.NewStyle().Foreground(cInk).Background(cFresh).Padding(0, 1).
		Render("scurvy " + m.newer.Tag + " is out · press u")
	return left + strings.Repeat(" ", max(1, w-lipgloss.Width(left)-lipgloss.Width(news))) + news
}

// pills count what was found, the gum way: a coloured dot and a number.
func (m *Model) pills() string {
	if len(m.items) == 0 {
		return ""
	}
	var sick, due, fresh, unk, ok int
	for _, it := range m.items {
		switch {
		case it.Level == scan.Sick:
			sick++
		case it.Level == scan.Due:
			due++
		case it.Level == scan.Fresh:
			fresh++
		case it.Err != "":
			unk++
		default:
			ok++
		}
	}
	var out []string
	add := func(n int, c color.Color, word string) {
		if n > 0 {
			out = append(out, lipgloss.NewStyle().Foreground(c).Render("●")+" "+sBold.Render(fmt.Sprint(n))+" "+sDim.Render(word))
		}
	}
	add(sick, cSick, "end of life")
	add(due, cDue, "overdue")
	add(fresh, cFresh, "fresh")
	add(unk, cUnk, "not checked")
	add(ok, cOK, "healthy")
	return " " + strings.Join(out, "    ")
}

// table is the inside of the bordered table: its head and as many rows as
// fit, scrolled so the pick stays in view.
func (m *Model) table(inner, height int) string {
	if m.scanning && len(m.items) == 0 {
		p := m.done.Load()
		d, t := p>>32, p&0xffffffff
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		f := frames[int(time.Now().UnixMilli()/80)%len(frames)]
		msg := sAccent.Render(f) + " " + sDim.Render(fmt.Sprintf("examining the crew… %d/%d", d, t))
		return lipgloss.Place(inner, height, lipgloss.Center, lipgloss.Center, msg)
	}
	if m.err != nil {
		return sErr.Render(m.err.Error())
	}
	c := m.columns(inner)
	headStyle := lipgloss.NewStyle().Foreground(cDim).Bold(true)
	lines := []string{
		"  " + cell("CONTAINER", c.name, headStyle) + sp() + cell("SOURCE", c.source, headStyle) + sp() +
			cell("RUNNING", c.running, headStyle) + sp() + cell("STATUS", c.status, headStyle) + sp() +
			cell("FINDING", c.finding, headStyle),
		sFaint.Render(strings.Repeat("─", inner)),
	}
	rows := m.rows()
	room := height - len(lines)
	if len(rows) == 0 {
		msg := sOK.Render("⚓ All hands healthy.") + sDim.Render(" Not a single loose tooth.")
		return strings.Join(lines, "\n") + "\n" + lipgloss.Place(inner, room, lipgloss.Center, lipgloss.Center, msg)
	}
	hidden := 0
	if !m.all {
		for _, it := range m.items {
			if !needs(it) {
				hidden++
			}
		}
	}
	if hidden > 0 {
		room-- // a line saying how many healthy ones are hidden
	}
	if m.pick < m.top {
		m.top = m.pick
	}
	if m.pick >= m.top+room {
		m.top = m.pick - room + 1
	}
	m.top = max(0, min(m.top, max(0, len(rows)-room)))
	for i := m.top; i < len(rows) && i < m.top+room; i++ {
		lines = append(lines, m.row(rows, i, c, inner))
	}
	if hidden > 0 {
		lines = append(lines, sFaint.Render(fmt.Sprintf("  + %d healthy · a shows them", hidden)))
	}
	if m.top > 0 {
		lines[2] = sFaint.Render(fmt.Sprintf("  ↑ %d more", m.top))
	}
	if end := m.top + room; end < len(rows) {
		lines[len(lines)-1] = sFaint.Render(fmt.Sprintf("  ↓ %d more", len(rows)-end))
	}
	return strings.Join(lines, "\n")
}

// row draws one table row; the picked one is lit across the table.
func (m *Model) row(rows []row, i int, c cols, inner int) string {
	r := rows[i]
	picked := i == m.pick
	bg := func(s lipgloss.Style) lipgloss.Style {
		if picked {
			return s.Background(cPickBg)
		}
		return s
	}
	cursor := bg(lipgloss.NewStyle()).Render("  ")
	if picked {
		cursor = bg(sAccent.Bold(true)).Render("▌ ")
	}
	gapS := bg(lipgloss.NewStyle()).Render(strings.Repeat(" ", gap))
	nameStyle := bg(lipgloss.NewStyle().Foreground(cText))
	if picked {
		nameStyle = nameStyle.Foreground(cAccent).Bold(true)
	}

	if r.head {
		worst, unk, hurt, total := scan.OK, false, 0, 0
		for _, it := range r.items {
			worst = max(worst, it.Level)
			unk = unk || failed(it)
			if it.Level > scan.OK {
				hurt++
			}
		}
		for _, it := range m.items {
			if it.Project.Name == r.group {
				total++
			}
		}
		what := plural(total, "container")
		if hurt > 0 {
			what += fmt.Sprintf(" · %d to update", hurt)
		}
		label, st := badge(worst, unk)
		return cursor + cell(r.group, c.name, nameStyle.Bold(true)) + gapS +
			cell("stack", c.source, bg(sFaint)) + gapS + cell("", c.running, bg(sFaint)) + gapS +
			badgeCell(label, c.status, bg(st), bg(lipgloss.NewStyle())) + gapS +
			cell(what, c.finding, bg(sDim))
	}

	it := r.items[0]
	n := it.Name()
	if r.sub {
		branch := "├ "
		if i+1 >= len(rows) || !rows[i+1].sub || rows[i+1].group != r.group {
			branch = "└ "
		}
		n = branch + n
	} else if it.Project.Name != "" && it.Project.Name != it.Service {
		n = it.Project.Name + "/" + n
	}
	label, st := badge(it.Level, failed(it))
	src := bg(sDim)
	if it.Kind == scan.Built {
		src = bg(lipgloss.NewStyle().Foreground(cAccent))
	}
	return cursor + cell(n, c.name, nameStyle) + gapS + cell(it.Source(), c.source, src) + gapS +
		cell(it.Current, c.running, bg(sDim)) + gapS + badgeCell(label, c.status, bg(st), bg(lipgloss.NewStyle())) + gapS +
		cell(finding(it), c.finding, bg(lipgloss.NewStyle().Foreground(cText)))
}

// detail is the panel under the table: everything about the picked row.
func (m *Model) detail(r row, w, h int) string {
	var title string
	var lines []string
	if r.head {
		title = r.group
		todo := targets(r)
		if len(todo) == 0 {
			lines = append(lines, "These containers come from another tool, not from a compose file.", "Update them where they were started.")
		} else {
			var names []string
			for _, it := range todo {
				names = append(names, it.Name())
			}
			lines = append(lines, "Enter updates "+strings.Join(names, ", ")+".")
		}
		for _, it := range r.items {
			if it.Level > scan.OK && len(it.Findings) > 0 {
				lines = append(lines, sDim.Render(it.Name()+": ")+it.Findings[0].Text)
			}
		}
	} else {
		it := r.items[0]
		title = it.Name() + sFaint.Render("  "+it.Image)
		for _, f := range it.Findings {
			lines = append(lines, dot(f.Level)+" "+f.Text)
		}
		if it.Err != "" {
			lines = append(lines, sErr.Render("●")+" could not check: "+it.Err)
		}
		if it.Bump != nil {
			file := it.Bump.File
			if rel, err := filepath.Rel(it.Project.Dir, file); err == nil && !strings.HasPrefix(rel, "..") {
				file = rel
			}
			lines = append(lines, sAccent.Render("→")+" Enter writes "+it.Bump.New+" into "+file+".")
		}
		if len(lines) == 0 {
			lines = append(lines, sOK.Render("●")+" Nothing newer found.")
		}
	}
	inner := h - 2 - 1
	if len(lines) > inner {
		lines = append(lines[:inner-1], sFaint.Render(fmt.Sprintf("… %d more", len(lines)-inner+1)))
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cAccent).Padding(0, 1).Width(w).Height(h)
	iw := w - box.GetHorizontalFrameSize()
	body := []string{ansi.Truncate(sBold.Render(title), iw, "…")}
	for _, l := range lines {
		body = append(body, ansi.Truncate(l, iw, "…"))
	}
	return box.Render(strings.Join(body, "\n"))
}

func dot(l scan.Level) string {
	switch l {
	case scan.Sick:
		return lipgloss.NewStyle().Foreground(cSick).Render("●")
	case scan.Due:
		return lipgloss.NewStyle().Foreground(cDue).Render("●")
	case scan.Fresh:
		return lipgloss.NewStyle().Foreground(cFresh).Render("●")
	}
	return sFaint.Render("●")
}

// status is the line above the help: a question, or what just happened.
func (m *Model) status(w int) string {
	rows := m.rows()
	q := func(s string) string {
		return " " + sAccent.Bold(true).Render("?") + " " + sBold.Render(s) + "  " + sDim.Render("(Y/n)")
	}
	var s string
	switch {
	case m.askSelf:
		s = q("Update scurvy " + m.version + " to " + m.newer.Tag + "?")
		if len(m.newer.Points) > 0 {
			s += sFaint.Render("   " + strings.Join(m.newer.Points, " · "))
		}
	case m.confirm && m.pick < len(rows):
		s = q(confirmText(rows[m.pick]))
	case m.scanning && len(m.items) > 0:
		s = " " + sDim.Render("looking again…")
	case m.note != "":
		s = " " + m.note
	}
	return ansi.Truncate(s, w, "…")
}

// help is gum's help line: keys bright, words dim, dots between.
func (m *Model) help(w int) string {
	k := lipgloss.NewStyle().Foreground(cDim).Bold(true)
	d := sFaint
	sep := d.Render(" • ")
	self := "check scurvy"
	if m.newer.Tag != "" {
		self = "update scurvy"
	}
	parts := []string{
		k.Render("↑/↓") + " " + d.Render("move"),
		k.Render("enter") + " " + d.Render("update"),
		k.Render("a") + " " + d.Render(map[bool]string{true: "hide healthy", false: "show all"}[m.all]),
		k.Render("r") + " " + d.Render("look again"),
		k.Render("u") + " " + d.Render(self),
		k.Render("q") + " " + d.Render("quit"),
	}
	return ansi.Truncate(" "+strings.Join(parts, sep), w, "…")
}

// finding is the one line about an item: its worst finding, or why it
// could not be checked.
func finding(it scan.Item) string {
	n := 0
	for _, f := range it.Findings {
		if f.Level > scan.OK {
			n++
		}
	}
	if n > 0 {
		s := it.Findings[0].Text
		if n > 1 {
			s += fmt.Sprintf("  +%d", n-1)
		}
		return s
	}
	if it.Err != "" {
		return "could not check: " + it.Err
	}
	return ""
}

func confirmText(r row) string {
	todo := targets(r)
	if r.head {
		return fmt.Sprintf("Update %s of %s?", plural(len(todo), "container"), r.group)
	}
	it := todo[0]
	verb := "Pull"
	if it.Kind == scan.Built {
		verb = "Rebuild"
	}
	if it.Bump != nil {
		return fmt.Sprintf("Raise %s to %s, %s and recreate it?", it.Name(), it.Latest, strings.ToLower(verb))
	}
	if it.Level == scan.OK {
		return fmt.Sprintf("%s looks healthy. %s and recreate it anyway?", it.Name(), verb)
	}
	return fmt.Sprintf("%s %s and recreate it?", verb, it.Name())
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func sp() string { return strings.Repeat(" ", gap) }

// cell fits text into n cells in a style.
func cell(text string, n int, st lipgloss.Style) string {
	if lipgloss.Width(text) > n {
		text = ansi.Truncate(text, n, "…")
	}
	return st.Render(text + strings.Repeat(" ", max(0, n-lipgloss.Width(text))))
}

// badgeCell puts a badge at the start of a cell.
func badgeCell(label string, n int, st, rest lipgloss.Style) string {
	b := st.Render(" " + label + " ")
	return b + rest.Render(strings.Repeat(" ", max(0, n-lipgloss.Width(b))))
}
