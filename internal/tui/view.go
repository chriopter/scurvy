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

// Calm, as lazygit: grey text, one green for the active panel, colour
// only where something asks for action. Pink is scurvy's name alone.
var (
	cAccent  = lipgloss.Color("212") // scurvy's name
	cActive  = lipgloss.Color("108") // the active panel's frame
	cText    = lipgloss.Color("252")
	cDim     = lipgloss.Color("245")
	cFaint   = lipgloss.Color("240")
	cPickBg  = lipgloss.Color("237")
	cCurrent = lipgloss.Color("108") // green
	cUpdate  = lipgloss.Color("74")  // blue
	cOverdue = lipgloss.Color("179") // muted yellow
	cEOL     = lipgloss.Color("167") // muted red
	cUnk     = lipgloss.Color("242")
	cInk     = lipgloss.Color("16")

	sDim    = lipgloss.NewStyle().Foreground(cDim)
	sFaint  = lipgloss.NewStyle().Foreground(cFaint)
	sOK     = lipgloss.NewStyle().Foreground(lipgloss.Color("108"))
	sDue    = lipgloss.NewStyle().Foreground(cOverdue)
	sErr    = lipgloss.NewStyle().Foreground(cEOL)
	sAccent = lipgloss.NewStyle().Foreground(cActive)
	sBold   = lipgloss.NewStyle().Foreground(cText).Bold(true)
	sText   = lipgloss.NewStyle().Foreground(cText)
)

// state is how a container stands: its sign, word and colour.
type state struct {
	sign, word string // word: what the list says
	short      string // what the counts say
	color      color.Color
}

var (
	stEOL       = state{"✗", "end of life", "end of life", cEOL}
	stOverdue   = state{"!", "update overdue", "overdue", cOverdue}
	stUpdate    = state{"↑", "update available", "available", cUpdate}
	stUnchecked = state{"?", "not checked", "not checked", cUnk}
	stCurrent   = state{"✓", "up to date", "up to date", cCurrent}
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
		return "◇", "own build, from a Dockerfile"
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
		lw := min(max(w*50/100, 60), w-40)
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
		b = lipgloss.NewStyle().Foreground(cActive)
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

// fit makes a line exactly n cells wide; on, if given, styles the fill.
func fit(s string, n int, on ...func(lipgloss.Style) lipgloss.Style) string {
	if lipgloss.Width(s) > n {
		s = ansi.Truncate(s, n, "…")
	}
	fill := strings.Repeat(" ", max(0, n-lipgloss.Width(s)))
	if len(on) > 0 {
		fill = on[0](lipgloss.NewStyle()).Render(fill)
	}
	return s + fill
}

func (m *Model) titleLine(w int) string {
	left := lipgloss.NewStyle().Foreground(cInk).Background(cAccent).Bold(true).Padding(0, 1).Render("scurvy") +
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

// chips counts items by state, quietly: "1 overdue · 4 available".
func chips(items []scan.Item, _ bool) string {
	n := map[string]int{}
	for _, it := range items {
		n[stateOf(it).word]++
	}
	var out []string
	for _, st := range states {
		if c := n[st.word]; c > 0 {
			out = append(out, st.style().Bold(true).Render(fmt.Sprint(c))+" "+sDim.Render(st.short))
		}
	}
	return strings.Join(out, sFaint.Render("  ·  "))
}

// listPanel is the left panel: groups, each under its name and a line,
// and their containers with their state in a word.
func (m *Model) listPanel(w, h int) string {
	inner, room := w-4, h-2
	active := !m.updating
	title := sBold.Render("Containers")
	if m.scanning && len(m.items) == 0 {
		p := m.done.Load()
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		f := frames[int(time.Now().UnixMilli()/80)%len(frames)]
		return panel(title, []string{"", sDim.Render(f + fmt.Sprintf(" examining the crew… %d/%d", p>>32, p&0xffffffff))}, w, h, active)
	}
	if m.err != nil {
		return panel(title, []string{sErr.Render(m.err.Error())}, w, h, active)
	}
	rows := m.rows()
	c := m.columns(rows, inner)
	room-- // the column names stay on top
	var lines []string
	pickAt := 0
	for i, r := range rows {
		if r.head && i > 0 {
			lines = append(lines, "")
		}
		if i == m.pick {
			pickAt = len(lines)
		}
		lines = append(lines, m.line(r, i == m.pick, c, inner))
	}
	if len(rows) == 0 {
		lines = append(lines, "", sOK.Render("every container is up to date"))
	}
	hidden := 0
	for _, it := range m.items {
		if !m.all && !needs(it) {
			hidden++
		}
	}
	if hidden > 0 {
		lines = append(lines, "", sFaint.Render(fmt.Sprintf("%d up to date hidden · a shows them", hidden)))
	}
	// Scroll by lines, keeping the pick in view.
	if pickAt < m.top {
		m.top = pickAt
	}
	if pickAt >= m.top+room {
		m.top = pickAt - room + 1
	}
	m.top = max(0, min(m.top, max(0, len(lines)-room)))
	shown := append([]string{c.header(inner)}, lines[m.top:min(len(lines), m.top+room)]...)
	return panel(title, shown, w, h, active)
}

// cols are the widths of the list's columns.
type cols struct{ name, state, kind int }

const waitW = 4 // "WAIT", "120d"

func (m *Model) columns(rows []row, inner int) cols {
	c := cols{name: len("CONTAINER"), state: len("update available"), kind: len("external")}
	for _, r := range rows {
		if !r.head {
			c.name = max(c.name, lipgloss.Width(short(r.items[0], r.group)))
		}
	}
	c.name = min(c.name, max(8, inner-c.state-c.kind-waitW-6))
	return c
}

// header names the columns, as k9s and docker ps do.
func (c cols) header(inner int) string {
	h := fit("CONTAINER", c.name) + "  " + fit("STATE", c.state) + "  " + fit("TYPE", c.kind) + "  " + fmt.Sprintf("%*s", waitW, "WAIT")
	return lipgloss.NewStyle().Foreground(cDim).Bold(true).Render(fit(h, inner))
}

// kindWord says how the container's image is made, in one word.
func kindWord(it scan.Item) string {
	switch it.Source() {
	case "own build":
		return "build"
	case "pinned":
		return "version"
	case "registry":
		return "image"
	case "docker run":
		return "run"
	}
	return it.Source() // digest, external
}

// line draws one row: a group's name over a line, or a container with
// its state, type and how long its update has waited.
func (m *Model) line(r row, picked bool, c cols, inner int) string {
	on := func(s lipgloss.Style) lipgloss.Style {
		if picked {
			return s.Background(cPickBg).Bold(true)
		}
		return s
	}
	if r.head {
		title := r.group
		if title == "" {
			title = "standalone"
		}
		head := on(sDim).Render(title + " ")
		return head + on(sFaint).Render(strings.Repeat("─", max(0, inner-lipgloss.Width(head))))
	}
	it := r.items[0]
	st := stateOf(it)
	waiting := ""
	if f, ok := it.Top(); ok && !f.Since.IsZero() {
		waiting = fmt.Sprintf("%dd", int(time.Since(f.Since).Hours()/24))
	}
	gap := on(lipgloss.NewStyle()).Render("  ")
	line := cellOn(on(sText).Render(short(it, r.group)), c.name, on) + gap +
		cellOn(on(st.style()).Render(st.word), c.state, on) + gap +
		cellOn(on(sDim).Render(kindWord(it)), c.kind, on) + gap +
		on(st.style()).Render(fmt.Sprintf("%*s", waitW, waiting))
	return fit(line, inner, on)
}

// cellOn fits styled text into n cells, the fill styled too.
func cellOn(s string, n int, on func(lipgloss.Style) lipgloss.Style) string {
	return fit(s, n, on)
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
			lines = append(lines, sAccent.Render("↵")+" updates "+strings.Join(names, ", "), "")
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
		lines = append(lines, "", sAccent.Render("↵")+" "+plan(it))
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
		all = "hide up to date"
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
	m := &Model{host: host, items: items, width: w, height: h, pick: pick, all: true, done: new(atomic.Int64)}
	return m.render()
}
