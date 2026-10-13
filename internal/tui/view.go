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
)

// state is how a container stands: its sign, word and colour.
type state struct {
	sign, word string
	color      color.Color
}

func stateOf(l scan.Level, unchecked bool) state {
	switch {
	case l == scan.EOL:
		return state{"✗", "eol", cEOL}
	case l == scan.Overdue:
		return state{"!", "overdue", cOverdue}
	case l == scan.Update:
		return state{"↑", "update", cUpdate}
	case unchecked:
		return state{"?", "unchecked", cUnk}
	}
	return state{"✓", "current", cCurrent}
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
		return "↗", "started by " + it.Project.Name + ", not from a compose file"
	case "docker run":
		return "▸", "started with docker run"
	}
	return "↓", "pulled from a registry"
}

func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.WindowTitle = "scurvy · " + m.host
	return v
}

// cols are the widths of a container line, inside the frame.
type cols struct{ name, version, what int }

const (
	gap  = 2
	ageW = 5 // "120d"
)

func (m *Model) columns(inner int) cols {
	c := cols{name: 10, version: 7}
	for _, r := range m.rows() {
		if r.head {
			continue
		}
		it := r.items[0]
		c.name = max(c.name, lipgloss.Width(short(it, r.group)))
		cur, next := version(it)
		w := lipgloss.Width(cur)
		if next != "" {
			w += 3 + lipgloss.Width(next)
		}
		c.version = max(c.version, w)
	}
	c.name = min(c.name, 28)
	c.version = min(c.version, 26)
	fixed := 2 + 2 + gap + 1 + gap + gap + ageW + gap // cursor, sign, source, age
	c.what = inner - fixed - c.name - c.version
	if c.what < 20 {
		c.name, c.version = min(c.name, 18), min(c.version, 16)
		c.what = inner - fixed - c.name - c.version
	}
	c.what = max(c.what, 6)
	return c
}

// version is what runs and, for a raised pin, what comes.
func version(it scan.Item) (cur, next string) {
	if it.Bump != nil {
		return tagOf(it.Bump.Old), tagOf(it.Bump.New)
	}
	if it.Kind == scan.Built {
		return strings.Replace(strings.Replace(it.Current, " days", "d", 1), " day", "d", 1), ""
	}
	return it.Current, ""
}

func tagOf(ref string) string {
	if i := strings.LastIndex(ref, ":"); i >= 0 && i > strings.LastIndex(ref, "/") {
		return ref[i+1:]
	}
	return ref
}

func (m *Model) render() string {
	w, h := m.width, m.height
	if w <= 0 {
		w = 100
	}
	if h <= 0 {
		h = 30
	}
	detailH := 7
	frame := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cFaint).Padding(0, 1)
	tableH := max(6, h-4-detailH)
	inner := w - frame.GetHorizontalFrameSize()
	table := frame.Width(w).Height(tableH).Render(m.table(inner, tableH-frame.GetVerticalFrameSize()))

	detail := strings.Repeat("\n", detailH-1)
	if rows := m.rows(); m.pick < len(rows) && len(m.items) > 0 {
		detail = m.detail(rows[m.pick], w, detailH)
	}
	return lipgloss.JoinVertical(lipgloss.Left, m.titleLine(w), m.pills(), table, detail, m.status(w), m.help(w))
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

// pills count the containers by state.
func (m *Model) pills() string {
	if len(m.items) == 0 {
		return ""
	}
	n := map[string]int{}
	for _, it := range m.items {
		n[stateOf(it.Level, failed(it)).word]++
	}
	levels := map[string]scan.Level{"eol": scan.EOL, "overdue": scan.Overdue, "update": scan.Update}
	var out []string
	for _, word := range []string{"eol", "overdue", "update", "unchecked", "current"} {
		if n[word] == 0 {
			continue
		}
		st := stateOf(levels[word], word == "unchecked")
		label := word
		if word == "update" && n[word] > 1 {
			label = "updates"
		}
		out = append(out, st.style().Bold(true).Render(st.sign+" "+fmt.Sprint(n[word]))+" "+sDim.Render(label))
	}
	return " " + strings.Join(out, "    ")
}

// table is the inside of the frame: sections and their containers,
// scrolled so the pick stays in view.
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
	head := sFaint.Render("    " + pad("CONTAINER", c.name) + sp() + " " + sp() + pad("VERSION", c.version) + sp() +
		padLeft("WAITS", ageW) + sp() + "WHAT")
	lines := []string{head}
	rows := m.rows()
	room := height - len(lines)
	hidden := 0
	if !m.all {
		for _, it := range m.items {
			if !needs(it) {
				hidden++
			}
		}
	}
	if len(rows) == 0 {
		msg := sOK.Render("✓ Every container is current.") + sDim.Render(" Not a single loose tooth.")
		if hidden > 0 {
			msg += "\n\n" + sFaint.Render("a shows all "+plural(hidden, "container"))
		}
		return head + "\n" + lipgloss.Place(inner, room, lipgloss.Center, lipgloss.Center, msg)
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
	end := min(len(rows), m.top+room)
	for i := m.top; i < end; i++ {
		lines = append(lines, m.line(rows, i, c, inner))
	}
	if m.top > 0 {
		lines[1] = sFaint.Render(fmt.Sprintf("    ↑ %d more", m.top))
	}
	if end < len(rows) {
		lines[len(lines)-1] = sFaint.Render(fmt.Sprintf("    ↓ %d more", len(rows)-end))
	}
	if hidden > 0 {
		lines = append(lines, sFaint.Render(rule("  ", plural(hidden, "more container")+" current · a shows them", inner)))
	}
	return strings.Join(lines, "\n")
}

// rule is a quiet section line: ── text ───────.
func rule(left, text string, width int) string {
	head := left + "── " + text + " "
	return head + strings.Repeat("─", max(2, width-lipgloss.Width(head)))
}

// line draws one row: a section header, or a container.
func (m *Model) line(rows []row, i int, c cols, inner int) string {
	r := rows[i]
	picked := i == m.pick
	on := func(s lipgloss.Style) lipgloss.Style {
		if picked {
			return s.Background(cPickBg)
		}
		return s
	}
	cursor := on(lipgloss.NewStyle()).Render("  ")
	if picked {
		cursor = on(sAccent.Bold(true)).Render("▌ ")
	}
	if r.head {
		return cursor + m.header(r, inner-2, on)
	}

	it := r.items[0]
	st := stateOf(it.Level, failed(it))
	sign, _ := source(it)
	blank := on(lipgloss.NewStyle())
	nameStyle := on(lipgloss.NewStyle().Foreground(cText))
	if it.Kind == scan.Loose {
		nameStyle = on(sDim) // not ours to update
	}
	if picked {
		nameStyle = nameStyle.Bold(true)
	}
	srcStyle := on(sDim)
	if it.Kind == scan.Built {
		srcStyle = on(sAccent)
	}

	cur, next := version(it)
	ver := on(sDim).Render(cur)
	used := lipgloss.Width(cur)
	if next != "" {
		ver += on(sFaint).Render(" → ") + on(st.style().Bold(true)).Render(next)
		used += 3 + lipgloss.Width(next)
	}
	if used > c.version {
		ver = ansi.Truncate(ver, c.version, "…")
		used = c.version
	}
	ver += blank.Render(strings.Repeat(" ", c.version-used))

	age, what := "", ""
	if f, ok := it.Top(); ok {
		what = f.Short
		if !f.Since.IsZero() {
			age = days(time.Since(f.Since))
		}
	} else if it.Err != "" {
		what = brief(it.Err)
	}
	g := blank.Render(sp())
	return cursor + on(st.style().Bold(true)).Render(st.sign+" ") + nameStyle.Render(pad(short(it, r.group), c.name)) + g +
		srcStyle.Render(sign) + g + ver + g + on(st.style()).Render(padLeft(age, ageW)) + g +
		on(lipgloss.NewStyle().Foreground(cText)).Render(pad(what, c.what))
}

// header is a section's line: its name and what its containers need.
func (m *Model) header(r row, width int, on func(lipgloss.Style) lipgloss.Style) string {
	title := r.group
	if title == "" {
		title = "standalone"
	}
	n := map[scan.Level]int{}
	worst, external, unchecked := scan.OK, false, 0
	for _, it := range r.items {
		n[it.Level]++
		worst = max(worst, it.Level)
		external = external || it.Kind == scan.Loose && it.Project.Name != ""
		if failed(it) {
			unchecked++
		}
	}
	var parts []string
	for _, l := range []scan.Level{scan.EOL, scan.Overdue, scan.Update} {
		if n[l] > 0 {
			word := l.Word()
			if l == scan.Update && n[l] > 1 {
				word = "updates"
			}
			parts = append(parts, fmt.Sprintf("%d %s", n[l], word))
		}
	}
	if unchecked > 0 {
		parts = append(parts, fmt.Sprintf("%d unchecked", unchecked))
	}
	if current := r.total - n[scan.EOL] - n[scan.Overdue] - n[scan.Update] - unchecked; current > 0 {
		parts = append(parts, fmt.Sprintf("%d current", current))
	}
	if external {
		parts = append(parts, "↗ external")
	}
	nameStyle := on(lipgloss.NewStyle().Foreground(cText).Bold(true))
	if r.group == "" {
		nameStyle = on(sDim.Bold(true))
	}
	head := on(sFaint).Render("── ") + nameStyle.Render(title) + on(lipgloss.NewStyle()).Render("  ") +
		on(stateOf(worst, worst == scan.OK && unchecked > 0).style()).Render(strings.Join(parts, " · ")) +
		on(lipgloss.NewStyle()).Render(" ")
	return head + on(sFaint).Render(strings.Repeat("─", max(2, width-lipgloss.Width(head))))
}

// detail is the panel under the table: everything about the picked row.
func (m *Model) detail(r row, w, h int) string {
	var title string
	var lines []string
	if r.head {
		t := r.group
		if t == "" {
			t = "standalone"
		}
		title = sBold.Render(t) + sFaint.Render("  "+plural(r.total, "container"))
		todo := targets(r)
		if len(todo) == 0 {
			lines = append(lines, sDim.Render("↗ These come from another tool, not from a compose file. Update them where they were started."))
		} else {
			var names []string
			for _, it := range todo {
				names = append(names, it.Name())
			}
			lines = append(lines, sAccent.Render("⏎")+" updates "+strings.Join(names, ", "))
		}
		for _, it := range r.items {
			if f, ok := it.Top(); ok {
				st := stateOf(f.Level, false)
				lines = append(lines, st.style().Render(st.sign)+" "+sDim.Render(it.Name()+":")+" "+f.Text)
			}
		}
	} else {
		it := r.items[0]
		st := stateOf(it.Level, failed(it))
		sign, words := source(it)
		title = st.style().Bold(true).Render(st.sign+" "+st.word) + "  " + sBold.Render(it.Name()) + sFaint.Render("  "+it.Image)
		lines = append(lines, sDim.Render(sign+" "+words))
		for _, f := range it.Findings {
			fs := stateOf(f.Level, false)
			mark := fs.style().Render(fs.sign)
			if f.Level == scan.OK {
				mark = sFaint.Render("·")
			}
			lines = append(lines, mark+" "+f.Text)
		}
		if it.Err != "" {
			lines = append(lines, sDim.Render("? "+brief(it.Err)+": "+it.Err))
		}
		switch {
		case !it.Updatable():
		case it.Hook != "":
			lines = append(lines, sAccent.Render("⏎")+" runs "+scan.HookFile+" of "+it.Project.Name)
		case it.Bump != nil:
			file := it.Bump.File
			if rel, err := filepath.Rel(it.Project.Dir, file); err == nil && !strings.HasPrefix(rel, "..") {
				file = rel
			}
			lines = append(lines, sAccent.Render("⏎")+" writes "+tagOf(it.Bump.New)+" into "+file+", then "+verb(it)+" and recreates")
		case it.Level > scan.OK:
			pull := ""
			if it.Pull != "" {
				pull = "git pull, then "
			}
			lines = append(lines, sAccent.Render("⏎")+" "+pull+verb(it)+" and recreates")
		}
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cFaint).Padding(0, 1).Width(w).Height(h)
	room := h - box.GetVerticalFrameSize() - 1
	if len(lines) > room {
		lines = append(lines[:room-1], sFaint.Render(fmt.Sprintf("… %d more", len(lines)-room+1)))
	}
	iw := w - box.GetHorizontalFrameSize()
	body := []string{ansi.Truncate(title, iw, "…")}
	for _, l := range lines {
		body = append(body, ansi.Truncate(l, iw, "…"))
	}
	return box.Render(strings.Join(body, "\n"))
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
		return "private image: needs docker login"
	case strings.Contains(err, "no such host"), strings.Contains(err, "timeout"), strings.Contains(err, "deadline"):
		return "registry not reachable"
	}
	return "not checked"
}

func verb(it scan.Item) string {
	if it.Kind == scan.Built {
		return "rebuilds"
	}
	return "pulls"
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

// help is the key line: keys bright, words dim, dots between.
func (m *Model) help(w int) string {
	k := lipgloss.NewStyle().Foreground(cDim).Bold(true)
	d := sFaint
	self := "check scurvy"
	if m.newer.Tag != "" {
		self = "update scurvy"
	}
	all := "show all"
	if m.all {
		all = "hide current"
	}
	parts := []string{
		k.Render("↑/↓") + " " + d.Render("move"),
		k.Render("enter") + " " + d.Render("update"),
		k.Render("a") + " " + d.Render(all),
		k.Render("r") + " " + d.Render("look again"),
		k.Render("u") + " " + d.Render(self),
		k.Render("q") + " " + d.Render("quit"),
	}
	return ansi.Truncate(" "+strings.Join(parts, d.Render(" • ")), w, "…")
}

func confirmText(r row) string {
	todo := targets(r)
	if r.head {
		return fmt.Sprintf("Update %s of %s?", plural(len(todo), "container"), name(r))
	}
	it := todo[0]
	v := "Pull"
	if it.Kind == scan.Built {
		v = "Rebuild"
	}
	if it.Bump != nil {
		return fmt.Sprintf("Raise %s to %s, %s and recreate it?", it.Name(), tagOf(it.Bump.New), strings.ToLower(v))
	}
	if it.Level == scan.OK {
		return fmt.Sprintf("%s is current. %s and recreate it anyway?", it.Name(), v)
	}
	return fmt.Sprintf("%s %s and recreate it?", v, it.Name())
}

// days says how long, the short way: 0d, 7d, 120d.
func days(d time.Duration) string { return fmt.Sprintf("%dd", int(d.Hours()/24)) }

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func sp() string { return strings.Repeat(" ", gap) }

func pad(s string, n int) string {
	w := lipgloss.Width(s)
	if w > n {
		return ansi.Truncate(s, n, "…")
	}
	return s + strings.Repeat(" ", n-w)
}

func padLeft(s string, n int) string { return strings.Repeat(" ", max(0, n-lipgloss.Width(s))) + s }
