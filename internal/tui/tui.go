// Package tui is scurvy's screen, two panels side by side as in lazygit:
// the containers by stack on the left, on the right what the picked one is
// about, or the update running now, line by line. ↑↓ picks a container or
// a stack, Enter updates it, r looks again, a shows the current ones too,
// u updates scurvy itself, q leaves.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/chriopter/scurvy/internal/fix"
	"github.com/chriopter/scurvy/internal/registry"
	"github.com/chriopter/scurvy/internal/scan"
	"github.com/chriopter/scurvy/internal/update"
)

// row is a line one can pick: a section, or a container.
type row struct {
	group string      // the section's stack; empty: standalone
	items []scan.Item // header: the section's containers on screen; else one
	head  bool
	total int // header: all containers of the section, shown or not
}

// Model is the screen's state.
type Model struct {
	ctx     context.Context
	host    string
	version string
	dryRun  bool // Enter shows what it would run, and runs nothing

	items    []scan.Item
	all      bool            // show the up-to-date ones too: the start
	marked   map[string]bool // container IDs marked with space
	pick     int             // row
	top      int             // first row shown
	scanning bool
	done     *atomic.Int64 // scan progress, packed done<<32|total
	err      error
	confirm  bool   // asking whether to update the picked row
	noCache  bool   // and to build from scratch (R)
	note     string // a line about what just happened
	width    int
	height   int

	// The update running, or the last one: its output, line by line.
	updating bool
	logName  string
	log      []string
	lines    chan string
	result   chan error
	outcome  string // after it: how it went

	pin *pinning // choosing a version to pin to (e)

	newer      update.Release // a newer scurvy, once known
	checking   bool           // looking for one now
	askSelf    bool           // asking whether to install it
	installing bool
	Restart    string // after quitting: start this program again
}

// pinning is the version chooser of e: the container, the versions its
// image could be pinned to, the one under the cursor, and, once chosen,
// the edit to confirm.
type pinning struct {
	it       scan.Item
	versions []string
	pick     int
	loading  bool
	err      string
	edit     *scan.Edit
}

type versionsMsg struct {
	versions []string
	err      error
}

type scannedMsg struct {
	items []scan.Item
	err   error
}
type tickMsg struct{}
type lineMsg string
type fixedMsg struct{ err error }
type latestMsg struct {
	rel   update.Release
	err   error
	asked bool // the user pressed u
}
type installedMsg struct {
	path string
	err  error
}

// Run shows the screen until the user leaves. It returns a program to
// start in its place when scurvy updated itself.
func Run(ctx context.Context, host, version string, dryRun bool) (string, error) {
	m := &Model{ctx: ctx, host: host, version: version, dryRun: dryRun, all: true, done: new(atomic.Int64)}
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	if m.updating { // stopped from outside mid-update: let the core put things back
		fmt.Println("an update was interrupted; scurvy puts back what it changed…")
		for l := range m.lines {
			fmt.Println(l)
		}
		<-m.result
	}
	return m.Restart, err
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.scan(), tick()}
	if update.Managed(m.version) {
		cmds = append(cmds, m.lookForUpdate(false))
	}
	return tea.Batch(cmds...)
}

func tick() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m *Model) scan() tea.Cmd {
	m.scanning = true
	m.done.Store(0)
	done, ctx := m.done, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		items, err := scan.Scan(ctx, func(d, t int) { done.Store(int64(d)<<32 | int64(t)) })
		return scannedMsg{items, err}
	}
}

func (m *Model) lookForUpdate(asked bool) tea.Cmd {
	m.checking = true
	ctx := m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		rel, err := update.Latest(ctx)
		return latestMsg{rel, err, asked}
	}
}

func (m *Model) installSelf() tea.Cmd {
	m.installing = true
	tag, ctx := m.newer.Tag, m.ctx
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		path, err := update.Install(ctx, tag)
		return installedMsg{path, err}
	}
}

func needs(it scan.Item) bool { return it.Level > scan.OK || it.Unchecked() }

// rows are the sections and their containers: one section per compose
// stack, the sickest first, and one for containers standing alone (on
// their own, or the only one of their stack). Current ones show with a.
func (m *Model) rows() []row {
	type section struct {
		name  string
		items []scan.Item
		worst scan.Level
	}
	count := map[string]int{}
	for _, it := range m.items {
		count[it.Project.Name]++
	}
	byName := map[string]*section{}
	var secs []*section
	for _, it := range m.items {
		key := it.Project.Name
		if count[key] == 1 && it.Service == key {
			key = ""
		}
		s := byName[key]
		if s == nil {
			s = &section{name: key}
			byName[key] = s
			secs = append(secs, s)
		}
		s.items = append(s.items, it)
		s.worst = max(s.worst, it.Level)
	}
	sort.SliceStable(secs, func(a, b int) bool {
		x, y := secs[a], secs[b]
		if (x.name == "") != (y.name == "") {
			return y.name == "" // standalone last
		}
		if x.worst != y.worst {
			return x.worst > y.worst
		}
		return x.name < y.name
	})
	var out []row
	for _, s := range secs {
		var shown []scan.Item
		for _, it := range s.items {
			if m.all || needs(it) {
				shown = append(shown, it)
			}
		}
		if len(shown) == 0 {
			continue
		}
		scan.Sort(shown)
		out = append(out, row{group: s.name, items: shown, head: true, total: len(s.items)})
		for _, it := range shown {
			out = append(out, row{group: s.name, items: []scan.Item{it}})
		}
	}
	return out
}

// picked is the row under the cursor; ok is false when there is none.
func (m *Model) picked() (row, bool) {
	rows := m.rows()
	if m.pick < 0 || m.pick >= len(rows) {
		return row{}, false
	}
	return rows[m.pick], true
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tickMsg:
		if m.scanning {
			return m, tick()
		}
	case scannedMsg:
		m.scanning = false
		m.items, m.err = msg.items, msg.err
		m.pick = max(0, min(m.pick, len(m.rows())-1))
	case versionsMsg:
		if m.pin != nil {
			m.pin.loading = false
			m.pin.versions = msg.versions
			if msg.err != nil {
				m.pin.err = msg.err.Error()
			}
			for i, v := range msg.versions {
				if v == registry.Parse(m.pin.it.Image).Tag {
					m.pin.pick = i
				}
			}
		}
	case lineMsg:
		m.log = append(m.log, string(msg))
		if len(m.log) > 2000 {
			m.log = m.log[len(m.log)-2000:]
		}
		return m, m.nextLine()
	case fixedMsg:
		m.updating = false
		switch {
		case msg.err != nil:
			m.outcome = sErr.Render("✗ " + m.logName + ": " + msg.err.Error())
		case m.dryRun:
			m.outcome = sDim.Render("dry run: nothing was changed")
		default:
			m.outcome = sOK.Render("✓ " + m.logName + " is shipshape again")
		}
		return m, tea.Batch(m.scan(), tick())
	case latestMsg:
		m.checking = false
		switch {
		case msg.err != nil:
			if msg.asked {
				m.note = sErr.Render("could not look for a newer scurvy: " + msg.err.Error())
			}
		case update.Newer(msg.rel.Tag, m.version):
			m.newer = msg.rel
			m.askSelf = msg.asked
		case msg.asked:
			m.note = sDim.Render("scurvy " + m.version + " is the newest")
		}
	case installedMsg:
		m.installing = false
		if msg.err != nil {
			m.note = sErr.Render("✗ " + msg.err.Error())
			return m, nil
		}
		m.Restart = msg.path
		return m, tea.Quit
	case tea.KeyPressMsg:
		return m, m.key(msg.String())
	}
	return m, nil
}

func (m *Model) key(k string) tea.Cmd {
	rows := m.rows()
	yes := k == "y" || k == "Y" || k == "enter"
	if m.pin != nil {
		return m.pinKey(k, yes)
	}
	switch {
	case m.askSelf:
		m.askSelf = false
		if yes {
			m.note = sDim.Render("installing scurvy " + m.newer.Tag + "…")
			return m.installSelf()
		}
		return nil
	case m.confirm:
		m.confirm = false
		noCache := m.noCache
		m.noCache = false
		if todo, what := m.todo(); yes && len(todo) > 0 {
			return m.startFix(todo, what, noCache)
		}
		return nil
	}
	switch k {
	case "q", "ctrl+c":
		if m.updating {
			m.note = sDue.Render("an update is running; q again once it is done")
			return nil
		}
		return tea.Quit
	case "esc":
		if !m.updating {
			m.log, m.outcome = nil, "" // back to the details
		}
	case "up", "k":
		m.pick = max(0, m.pick-1)
	case "down", "j":
		m.pick = max(0, min(len(rows)-1, m.pick+1))
	case "home", "g":
		m.pick = 0
	case "end", "G":
		m.pick = max(0, len(rows)-1)
	case "a":
		m.all = !m.all
		m.pick, m.top = 0, 0
	case "r":
		if !m.scanning && !m.updating {
			m.note = ""
			return tea.Batch(m.scan(), tick())
		}
	case "u":
		switch {
		case m.dryRun:
			m.note = sDim.Render("dry run: scurvy does not update itself either")
		case !update.Managed(m.version):
			m.note = sDim.Render("this scurvy is a " + m.version + " build: it does not update itself")
		case m.installing, m.checking:
		case m.updating:
			m.note = sDim.Render("scurvy updates itself once the containers are done")
		case m.newer.Tag != "":
			m.askSelf = true
		default:
			m.note = sDim.Render("looking for a newer scurvy…")
			return m.lookForUpdate(true)
		}
	case "e":
		r, ok := m.picked()
		switch {
		case !ok || r.head || m.updating:
		case r.items[0].Source() != "pinned":
			m.note = sDim.Render(r.items[0].Name() + " is not pinned to a version; e changes a version in image:")
		default:
			m.pin = &pinning{it: r.items[0], loading: true}
			image, ctx := r.items[0].Image, m.ctx
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(ctx, time.Minute)
				defer cancel()
				v, err := scan.Versions(ctx, image)
				return versionsMsg{v, err}
			}
		}
	case "space":
		if r, ok := m.picked(); ok && !m.updating {
			m.toggle(r)
		}
	case "enter", "R":
		todo, _ := m.todo()
		// A new major is a decision: Enter on it opens the version choice.
		if len(todo) == 1 && len(m.marked) == 0 && todo[0].Major != "" && len(todo[0].Edits) == 0 && k == "enter" {
			return m.key("e")
		}
		switch {
		case m.scanning:
		case m.updating, m.installing:
			m.note = sDim.Render("one update at a time")
		case len(todo) == 0:
			m.note = sDim.Render(whyNot(m.picked()))
		default:
			m.note = ""
			m.confirm, m.noCache = true, k == "R"
		}
	}
	return nil
}

// toggle marks a container, or all of a group's that scurvy can update;
// again, it unmarks them.
func (m *Model) toggle(r row) {
	can := targets(r)
	if r.head {
		can = nil
		for _, it := range r.items {
			if it.Updatable() {
				can = append(can, it)
			}
		}
	}
	if len(can) == 0 {
		m.note = sDim.Render(name(r) + ": nothing scurvy can update here")
		return
	}
	if m.marked == nil {
		m.marked = map[string]bool{}
	}
	all := true
	for _, it := range can {
		all = all && m.marked[it.Container.ID]
	}
	for _, it := range can {
		if all {
			delete(m.marked, it.Container.ID)
		} else {
			m.marked[it.Container.ID] = true
		}
	}
}

// todo is what Enter updates: the marked containers, else the picked row's;
// and what to call it.
func (m *Model) todo() ([]scan.Item, string) {
	if len(m.marked) > 0 {
		var out []scan.Item
		for _, it := range m.items {
			if m.marked[it.Container.ID] {
				out = append(out, it)
			}
		}
		return out, plural(len(out), "marked container")
	}
	if r, ok := m.picked(); ok {
		return targets(r), name(r)
	}
	return nil, ""
}

// targets are what Enter updates on a row: a container, or the containers
// of a section that need it (all of them, when none does).
func targets(r row) []scan.Item {
	var sick, all []scan.Item
	for _, it := range r.items {
		if !it.Updatable() {
			continue
		}
		all = append(all, it)
		if it.Level > scan.OK {
			sick = append(sick, it)
		}
	}
	if len(sick) > 0 {
		return sick
	}
	return all
}

func name(r row) string {
	switch {
	case r.head && r.group == "":
		return "standalone"
	case r.head:
		return r.group
	}
	return r.items[0].Name()
}

// startFix runs the update in the background and shows its output on the
// right, line by line, while the list stays.
func (m *Model) startFix(todo []scan.Item, what string, noCache bool) tea.Cmd {
	plans, _ := fix.Plans(todo, m.items, noCache)
	m.updating, m.logName, m.log, m.outcome = true, what, nil, ""
	m.marked = nil
	m.lines, m.result = make(chan string, 256), make(chan error, 1)
	lines, result, ctx := m.lines, m.result, m.ctx
	go func() {
		w := &lineWriter{out: lines}
		var err error
		for _, p := range plans {
			if err = fix.Run(ctx, p, w); err != nil {
				break
			}
		}
		w.flush()
		result <- err
		close(lines)
	}()
	return m.nextLine()
}

// nextLine waits for the update's next line, or its end.
func (m *Model) nextLine() tea.Cmd {
	lines, result := m.lines, m.result
	return func() tea.Msg {
		l, ok := <-lines
		if !ok {
			return fixedMsg{<-result}
		}
		return lineMsg(l)
	}
}

// lineWriter turns output into lines, also at carriage returns.
type lineWriter struct {
	out     chan<- string
	partial strings.Builder
}

func (w *lineWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		switch b {
		case '\n', '\r':
			w.flush()
		default:
			w.partial.WriteByte(b)
		}
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	if w.partial.Len() > 0 {
		w.out <- w.partial.String()
		w.partial.Reset()
	}
}

// whyNot says why Enter does nothing on a row.
func whyNot(r row, ok bool) string {
	if !ok || len(r.items) == 0 {
		return "nothing to update here"
	}
	it := r.items[0]
	if !it.Up() {
		return name(r) + ": it is " + it.Container.State + "; scurvy does not start it"
	}
	switch it.Source() {
	case "external":
		return name(r) + ": started by " + it.Project.Name + ", not from a compose file here; update it where it was started"
	case "docker run":
		return name(r) + ": started with docker run; update it by hand"
	case "digest":
		return name(r) + ": pinned by digest; change the digest in the compose file"
	}
	return name(r) + ": nothing scurvy can update here"
}

// pinKey handles keys while choosing a version, then while confirming it.
func (m *Model) pinKey(k string, yes bool) tea.Cmd {
	p := m.pin
	if p.edit != nil {
		if yes {
			it := p.it
			it.Edits = []scan.Edit{*p.edit}
			m.pin = nil
			return m.startFix([]scan.Item{it}, it.Name(), false)
		}
		p.edit = nil // back to the versions
		return nil
	}
	switch k {
	case "esc", "q", "e":
		m.pin = nil
	case "up", "k":
		p.pick = max(0, p.pick-1)
	case "down", "j":
		p.pick = max(0, min(len(p.versions)-1, p.pick+1))
	case "enter":
		if p.loading || p.pick >= len(p.versions) {
			return nil
		}
		v := p.versions[p.pick]
		if v == registry.Parse(p.it.Image).Tag {
			p.err = p.it.Name() + " is pinned to " + v + " already"
			return nil
		}
		e, err := scan.PinEdit(p.it, v)
		if err != nil {
			p.err = err.Error()
			return nil
		}
		p.err, p.edit = "", &e
	}
	return nil
}
