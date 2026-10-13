package tui

import (
	"fmt"
	"image/color"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/chriopter/scurvy/internal/scan"
)

// One pink accent for scurvy itself; every other colour says a state.
var (
	cAccent  = lipgloss.Color("212")
	cText    = lipgloss.Color("252")
	cDim     = lipgloss.Color("245")
	cFaint   = lipgloss.Color("240")
	cPickBg  = lipgloss.Color("236")
	cCurrent = lipgloss.Color("78")  // green
	cUpdate  = lipgloss.Color("75")  // blue
	cOverdue = lipgloss.Color("214") // orange
	cEOL     = lipgloss.Color("203") // red
	cUnk     = lipgloss.Color("244") // grey
	cInk     = lipgloss.Color("16")

	sDim    = lipgloss.NewStyle().Foreground(cDim)
	sFaint  = lipgloss.NewStyle().Foreground(cFaint)
	sOK     = lipgloss.NewStyle().Foreground(cCurrent)
	sDue    = lipgloss.NewStyle().Foreground(cOverdue)
	sErr    = lipgloss.NewStyle().Foreground(cEOL)
	sAccent = lipgloss.NewStyle().Foreground(cAccent)
	sBold   = lipgloss.NewStyle().Foreground(cText).Bold(true)
	sText   = lipgloss.NewStyle().Foreground(cText)
)

// state is how a container stands: its sign, word and colour.
type state struct {
	sign, word string
	color      color.Color
}

var (
	stEOL       = state{"✗", "eol", cEOL}
	stOverdue   = state{"!", "overdue", cOverdue}
	stUpdate    = state{"↑", "update", cUpdate}
	stUnchecked = state{"?", "unchecked", cUnk}
	stCurrent   = state{"✓", "current", cCurrent}
	states      = []state{stEOL, stOverdue, stUpdate, stUnchecked, stCurrent}
)

func stateOf(it scan.Item) state {
	switch {
	case it.Unchecked():
		return stUnchecked
	case it.Level == scan.EOL:
		return stEOL
	case it.Level == scan.Overdue:
		return stOverdue
	case it.Level == scan.Update:
		return stUpdate
	}
	return stCurrent
}

func levelState(l scan.Level) state {
	return stateOf(scan.Item{Level: l})
}

func (st state) style() lipgloss.Style { return lipgloss.NewStyle().Foreground(st.color) }

// source is a one-cell sign for where an image comes from, and its words.
func source(it scan.Item) (string, string) {
	switch it.Source() {
	case "own build":
		return "⚒", "own build, from a Dockerfile"
	case "pinned":
		return "#", "pinned version from a registry"
	case "digest":
		return "@", "pinned by digest"
	case "external":
		return "↗", "started by " + it.Project.Name + ", not from a compose file here"
	case "docker run":
		return "▸", "not started by compose"
	}
	return "↓", "pulled from a registry, moving tag"
}

func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "scurvy · " + m.host
	return v
}

func (m *Model) render() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 100
	}
	if h <= 0 {
		h = 30
	}
	area := max(8, h-4) // title, counts above; status, keys below
	var panels string
	if w >= 100 {
		lw := min(max(w*55/100, 56), w-44)
		panels = lipgloss.JoinHorizontal(lipgloss.Top,
			m.listPanel(lw, area), m.rightPanel(w-lw, area))
	} else {
		rh := min(10, area/2)
		panels = lipgloss.JoinVertical(lipgloss.Left,
			m.listPanel(w, area-rh), m.rightPanel(w, rh))
	}
	return strings.Join([]string{m.titleLine(w), m.counts(), panels, m.status(w), m.help(w)}, "\n")
}

// panel draws a framed box with its title in the top edge, as lazygit
// does; the active one is pink.
func panel(title string, lines []string, w, h int, active bool) string {
	b := sFaint
	if active {
		b = sAccent
	}
	inner := w - 4
	top := b.Render("╭─ ") + title + b.Render(" "+strings.Repeat("─", max(0, w-5-lipgloss.Width(title)))+"╮")
	out := []string{ansi.Truncate(top, w, "")}
	for i := 0; i < h-2; i++ {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		out = append(out, b.Render("│ ")+fit(l, inner)+b.Render(" │"))
	}
	out = append(out, b.Render("╰"+strings.Repeat("─", w-2)+"╯"))
	return strings.Join(out, "\n")
}

// fit makes a line exactly n cells wide.
func fit(s string, n int) string {
	if lipgloss.Width(s) > n {
		s = ansi.Truncate(s, n, "…")
	}
	return s + strings.Repeat(" ", max(0, n-lipgloss.Width(s)))
}

func (m *Model) titleLine(w int) string {
	left := lipgloss.NewStyle().Foreground(cInk).Background(cAccent).Bold(true).Padding(0, 1).Render("🍋 scurvy") +
		sDim.Render("  "+m.host)
	if n := len(m.items); n > 0 {
		left += sFaint.Render("  ·  " + plural(n, "container"))
	}
	if m.dryRun {
		left += "  " + lipgloss.NewStyle().Foreground(cInk).Background(cOverdue).Bold(true).Padding(0, 1).Render("DRY RUN")
	}
	if m.newer.Tag == "" {
		return left
	}
	news := lipgloss.NewStyle().Foreground(cInk).Background(cUpdate).Padding(0, 1).
		Render("scurvy " + m.newer.Tag + " is out · press u")
	return left + strings.Repeat(" ", max(1, w-lipgloss.Width(left)-lipgloss.Width(news))) + news
}

// counts says how many containers are in each state, in their colours.
func (m *Model) counts() string {
	if len(m.items) == 0 {
		return ""
	}
	return " " + chips(m.items, true)
}

// chips counts items by state: "↑ 2 updates  ✓ 1 current", or with words
// false just "↑2 ✓1".
func chips(items []scan.Item, words bool) string {
	n := map[string]int{}
	for _, it := range items {
		n[stateOf(it).word]++
	}
	var out []string
	for _, st := range states {
		c := n[st.word]
		if c == 0 {
			continue
		}
		if !words {
			out = append(out, st.style().Render(fmt.Sprintf("%s%d", st.sign, c)))
			continue
		}
		label := st.word
		if st == stUpdate && c > 1 {
			label = "updates"
		}
		out = append(out, st.style().Bold(true).Render(fmt.Sprintf("%s %d", st.sign, c))+" "+sDim.Render(label))
	}
	sep := " "
	if words {
		sep = "    "
	}
	return strings.Join(out, sep)
}

// listPanel is the left panel: sections and their containers.
func (m *Model) listPanel(w, h int) string {
	inner, room := w-4, h-2
	active := !m.updating
	if m.scanning && len(m.items) == 0 {
		p := m.done.Load()
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		f := frames[int(time.Now().UnixMilli()/80)%len(frames)]
		return panel(sBold.Render("Containers"), []string{"", sAccent.Render(f) + " " +
			sDim.Render(fmt.Sprintf("examining the crew… %d/%d", p>>32, p&0xffffffff))}, w, h, active)
	}
	if m.err != nil {
		return panel(sBold.Render("Containers"), []string{sErr.Render(m.err.Error())}, w, h, active)
	}
	rows := m.rows()
	hidden := 0
	for _, it := range m.items {
		if !m.all && !needs(it) {
			hidden++
		}
	}
	var lines []string
	if len(rows) == 0 {
		lines = append(lines, "", sOK.Render("✓ every container is current"), sDim.Render("  not a single loose tooth"))
	}
	if hidden > 0 {
		room--
	}
	if m.pick < m.top {
		m.top = m.pick
	}
	if m.pick >= m.top+room {
		m.top = m.pick - room + 1
	}
	m.top = max(0, min(m.top, max(0, len(rows)-room)))
	c := m.columns(rows, inner)
	for i := m.top; i < len(rows) && i < m.top+room; i++ {
		lines = append(lines, m.line(rows[i], i == m.pick, c, inner))
	}
	if m.top > 0 {
		lines[0] = sFaint.Render(fmt.Sprintf("  ↑ %d more", m.top))
	}
	if end := m.top + room; end < len(rows) {
		lines[len(lines)-1] = sFaint.Render(fmt.Sprintf("  ↓ %d more", len(rows)-end))
	}
	if hidden > 0 {
		lines = append(lines, sFaint.Render(fmt.Sprintf("  + %d more current · a shows them", hidden)))
	}
	return panel(sBold.Render("Containers"), lines, w, h, active)
}

// cols are the widths in a container's line.
type cols struct{ name, version, what int }

const ageW = 4 // "120d"

func (m *Model) columns(rows []row, inner int) cols {
	c := cols{name: 8, version: 6}
	for _, r := range rows {
		if r.head {
			continue
		}
		c.name = max(c.name, lipgloss.Width(short(r.items[0], r.group)))
		cur, next := version(r.items[0])
		c.version = max(c.version, lipgloss.Width(cur)+lipgloss.Width(arrow(next)))
	}
	c.name, c.version = min(c.name, 26), min(c.version, 22)
	fixed := 2 + 2 + 2 + 1 + 2 + 2 + ageW // cursor, sign, gap, source, gap, gap, age
	c.what = inner - fixed - c.name - c.version - 2
	if c.what < 10 { // a narrow panel drops the words, the table says them
		c.what = 0
		c.name = min(c.name, inner-fixed-c.version)
	}
	return c
}

func arrow(next string) string {
	if next == "" {
		return ""
	}
	return " → " + next
}

// line draws one row: a section header, or a container.
func (m *Model) line(r row, picked bool, c cols, inner int) string {
	on := func(s lipgloss.Style) lipgloss.Style {
		if picked {
			return s.Background(cPickBg)
		}
		return s
	}
	pad := func(s string, n int) string { return on(lipgloss.NewStyle()).Render(strings.Repeat(" ", max(0, n))) }
	cursor := pad("", 2)
	if picked {
		cursor = on(sAccent.Bold(true)).Render("▌ ")
	}
	var line string
	if r.head {
		title := r.group
		style := on(sBold)
		if title == "" {
			title, style = "standalone", on(sDim.Bold(true))
		}
		line = cursor + style.Render(title) + pad("", 2) + chipsOn(r.items, on)
		if hidden := r.total - len(r.items); hidden > 0 {
			line += on(sFaint).Render(fmt.Sprintf("  +%d", hidden))
		}
	} else {
		it := r.items[0]
		st := stateOf(it)
		sign, _ := source(it)
		name := on(sText)
		if it.Kind == scan.Loose {
			name = on(sDim) // not ours to update
		}
		if picked {
			name = name.Bold(true)
		}
		src := on(sDim)
		if it.Kind == scan.Built {
			src = on(sAccent)
		}
		cur, next := version(it)
		ver := on(sDim).Render(cur)
		if next != "" {
			ver += on(sFaint).Render(" → ") + on(st.style().Bold(true)).Render(next)
		}
		ver = cell(ver, c.version, on)
		age, what := "", ""
		if f, ok := it.Top(); ok {
			what = f.Short
			if !f.Since.IsZero() {
				age = fmt.Sprintf("%dd", int(time.Since(f.Since).Hours()/24))
			}
		} else if len(it.Errs) > 0 {
			what = brief(it.Errs[0])
		}
		line = cursor + on(st.style().Bold(true)).Render(st.sign) + pad("", 1) +
			cell(name.Render(short(it, r.group)), c.name, on) + pad("", 1) + src.Render(sign) + pad("", 1) +
			ver + pad("", 1) + on(st.style()).Render(fmt.Sprintf("%*s", ageW, age))
		if c.what > 0 {
			line += pad("", 2) + cell(on(sText).Render(what), c.what, on)
		}
	}
	if picked {
		line += pad("", inner-lipgloss.Width(line))
	}
	return line
}

func chipsOn(items []scan.Item, on func(lipgloss.Style) lipgloss.Style) string {
	n := map[string]int{}
	for _, it := range items {
		n[stateOf(it).word]++
	}
	var out []string
	for _, st := range states {
		if c := n[st.word]; c > 0 {
			out = append(out, on(st.style()).Render(fmt.Sprintf("%s%d", st.sign, c)))
		}
	}
	return strings.Join(out, on(lipgloss.NewStyle()).Render(" "))
}

// cell fits styled text into n cells.
func cell(s string, n int, on func(lipgloss.Style) lipgloss.Style) string {
	if lipgloss.Width(s) > n {
		s = ansi.Truncate(s, n, "…")
	}
	return s + on(lipgloss.NewStyle()).Render(strings.Repeat(" ", max(0, n-lipgloss.Width(s))))
}

// version is what runs, and with a raised version, what comes.
func version(it scan.Item) (cur, next string) {
	if it.Kind == scan.Built {
		cur = strings.Replace(strings.Replace(it.Current, " days", "d", 1), " day", "d", 1)
	} else {
		cur = it.Current
	}
	// The edit that shows the version: a pulled image's line, or the last
	// FROM, the stage that runs (an earlier one is a -builder variant).
	var show *scan.Edit
	for i, e := range it.Edits {
		if it.Kind == scan.Pulled || strings.HasPrefix(strings.ToUpper(strings.TrimSpace(e.Old)), "FROM") {
			show = &it.Edits[i]
		}
	}
	if show == nil {
		return cur, ""
	}
	return tagIn(show.Old), tagIn(show.New)
}

// tagIn finds the tag in a line that names an image.
func tagIn(line string) string {
	f := strings.Fields(line)
	for _, w := range f {
		if i := strings.LastIndex(w, ":"); i > 0 && i > strings.LastIndex(w, "/") && !strings.HasSuffix(w, ":") {
			return strings.Trim(w[i+1:], `"'`)
		}
	}
	return ""
}

// rightPanel shows the update running, or the last one, else what the
// picked row is about.
func (m *Model) rightPanel(w, h int) string {
	inner, room := w-4, h-2
	if m.log != nil || m.updating {
		title := sBold.Render("Update · " + m.logName)
		if m.updating {
			frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
			title += " " + sAccent.Render(frames[int(time.Now().UnixMilli()/80)%len(frames)])
		}
		lines := m.log
		if m.outcome != "" {
			lines = append(append([]string{}, lines...), "", m.outcome, sFaint.Render("esc: back to the details"))
		}
		if len(lines) > room {
			lines = lines[len(lines)-room:]
		}
		for i, l := range lines {
			lines[i] = ansi.Truncate(l, inner, "…")
		}
		return panel(title, lines, w, h, m.updating)
	}
	r, ok := m.picked()
	if !ok || len(m.items) == 0 {
		return panel(sBold.Render("Details"), nil, w, h, false)
	}
	title, lines := details(r)
	return panel(title, wrap(lines, inner, room), w, h, false)
}

// wrap breaks lines to the width, and keeps as many as fit.
func wrap(lines []string, width, room int) []string {
	var out []string
	for _, l := range lines {
		out = append(out, strings.Split(ansi.Wordwrap(l, width, " "), "\n")...)
	}
	if len(out) > room {
		out = append(out[:room-1], sFaint.Render("…"))
	}
	return out
}

// details are the right panel's title and lines for a row.
func details(r row) (string, []string) {
	if r.head {
		title := sBold.Render(name(r)) + sFaint.Render("  "+plural(r.total, "container"))
		var lines []string
		if todo := targets(r); len(todo) == 0 {
			lines = append(lines, sDim.Render("↗ these come from another tool, not from a compose file here: update them where they were started"))
		} else {
			var names []string
			for _, it := range todo {
				names = append(names, it.Name())
			}
			lines = append(lines, sAccent.Render("⏎")+" updates "+strings.Join(names, ", "), "")
		}
		for _, it := range r.items {
			st := stateOf(it)
			what := "current"
			if f, ok := it.Top(); ok {
				what = f.Text
			} else if len(it.Errs) > 0 {
				what = it.Errs[0]
			}
			lines = append(lines, st.style().Render(st.sign)+" "+sBold.Render(it.Name())+sDim.Render("  "+what))
		}
		return title, lines
	}
	it := r.items[0]
	st := stateOf(it)
	sign, words := source(it)
	title := st.style().Bold(true).Render(st.sign+" "+st.word) + "  " + sBold.Render(it.Name())
	lines := []string{sDim.Render(it.Image), sDim.Render(sign + " " + words), ""}
	for _, f := range it.Findings {
		mark := levelState(f.Level).style().Render(levelState(f.Level).sign)
		if f.Level == scan.OK {
			mark = sFaint.Render("·")
		}
		lines = append(lines, mark+" "+f.Text)
	}
	for _, e := range it.Errs {
		lines = append(lines, sDim.Render("? "+e))
	}
	if len(it.Findings) == 0 && len(it.Errs) == 0 {
		lines = append(lines, sOK.Render("✓")+" nothing newer found")
	}
	if it.Updatable() {
		lines = append(lines, "", sAccent.Render("⏎")+" "+plan(it))
		for _, e := range it.Edits {
			lines = append(lines, sFaint.Render("  "+filepath.Base(e.File)+":")+" "+sErr.Render(strings.TrimSpace(e.Old))+
				sFaint.Render(" → ")+sOK.Render(strings.TrimSpace(e.New)))
		}
	}
	return title, lines
}

// plan says in words what Enter will do with a container.
func plan(it scan.Item) string {
	switch {
	case it.Hook != "":
		return "backs up, runs " + scan.HookFile + ", waits until healthy"
	case it.Kind == scan.Built && it.Pull != "":
		return "backs up, git pull, rebuilds, waits until healthy"
	case it.Kind == scan.Built && len(it.Edits) > 0:
		return "backs up, raises the base, rebuilds, waits until healthy"
	case it.Kind == scan.Built:
		return "backs up, rebuilds, waits until healthy"
	case len(it.Edits) > 0:
		return "backs up, raises the version, pulls, waits until healthy"
	}
	return "backs up, pulls, waits until healthy; puts it all back if anything fails"
}

// short is a container's name in its section: nextcloud-aio-apache is
// apache under nextcloud-aio.
func short(it scan.Item, section string) string {
	n := it.Name()
	for _, sep := range []string{"-", "_"} {
		if rest, ok := strings.CutPrefix(n, section+sep); ok && section != "" && rest != "" {
			return rest
		}
	}
	return n
}

// brief says why something could not be checked, in a few words.
func brief(err string) string {
	switch {
	case strings.Contains(err, "rate limited"):
		return "rate limited, try later"
	case strings.Contains(err, "docker login"):
		return "private: needs docker login"
	case strings.Contains(err, "no such host"), strings.Contains(err, "timeout"), strings.Contains(err, "deadline"):
		return "registry not reachable"
	}
	return "not checked"
}

// status is the line above the keys: a question, or what just happened.
func (m *Model) status(w int) string {
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
	case m.confirm:
		if r, ok := m.picked(); ok {
			s = q(confirmText(r, m.dryRun))
		}
	case m.scanning && len(m.items) > 0:
		s = " " + sDim.Render("looking again…")
	case m.note != "":
		s = " " + m.note
	}
	return ansi.Truncate(s, w, "…")
}

// help is the key line: keys bright, words dim, dots between.
func (m *Model) help(w int) string {
	k := lipgloss.NewStyle().Foreground(cDim).Bold(true)
	self, all := "check scurvy", "show all"
	if m.newer.Tag != "" {
		self = "update scurvy"
	}
	if m.all {
		all = "hide current"
	}
	parts := []string{
		k.Render("↑/↓") + " " + sFaint.Render("move"),
		k.Render("enter") + " " + sFaint.Render("update"),
		k.Render("a") + " " + sFaint.Render(all),
		k.Render("r") + " " + sFaint.Render("look again"),
		k.Render("u") + " " + sFaint.Render(self),
		k.Render("q") + " " + sFaint.Render("quit"),
	}
	return ansi.Truncate(" "+strings.Join(parts, sFaint.Render(" • ")), w, "…")
}

func confirmText(r row, dry bool) string {
	todo := targets(r)
	s := fmt.Sprintf("Update %s?", todo[0].Name())
	if r.head || len(todo) > 1 {
		s = fmt.Sprintf("Update %s of %s?", plural(len(todo), "container"), name(r))
	}
	if dry {
		s += " (dry run: only shown)"
	}
	return s
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Snapshot draws the screen for the given containers once, as it would
// look in a terminal of w×h with row pick chosen. It serves the README's
// picture (tools/shot).
func Snapshot(host string, items []scan.Item, w, h, pick int) string {
	m := &Model{host: host, items: items, width: w, height: h, pick: pick, done: new(atomic.Int64)}
	return m.render()
}
