// Package tui is scurvy's screen: the crew by stack, sickest first. ↑↓
// picks a container or a whole stack, Enter updates it, r looks again,
// a shows the healthy ones too, u updates scurvy itself, q leaves.
package tui

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"sort"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/chriopter/scurvy/internal/scan"
	"github.com/chriopter/scurvy/internal/update"
)

// ItemEnv carries what to update from the screen to `scurvy fix`: one item
// or a list of them, as JSON.
const ItemEnv = "SCURVY_ITEM"

// row is a line one can pick: a stack, or a container.
type row struct {
	group string      // the stack's name; empty for a container on its own
	items []scan.Item // header: the stack's containers on screen; else one
	head  bool
	sub   bool // a container under its section's header
	total int  // header: all containers of the section, shown or not
}

// Model is the screen's state.
type Model struct {
	host, version string

	items    []scan.Item
	all      bool // show healthy ones too
	pick     int  // row
	top      int  // first body line shown
	scanning bool
	done     *atomic.Int64 // scan progress, packed done<<32|total
	err      error
	confirm  bool   // asking whether to update the picked row
	note     string // a line about what just happened
	width    int
	height   int

	newer    update.Release // a newer scurvy, once known
	checking bool           // looking for one now
	askSelf  bool           // asking whether to install it
	Restart  string         // after quitting: start this program again
}

type scannedMsg struct {
	items []scan.Item
	err   error
}
type tickMsg struct{}
type fixedMsg struct {
	name string
	err  error
}
type latestMsg struct {
	rel   update.Release
	err   error
	asked bool // the user pressed u
}
type installedMsg struct {
	path, tag string
	err       error
}

// Run shows the screen until the user leaves. It returns a program to
// start in its place when scurvy updated itself.
func Run(version string) (string, error) {
	host, _ := os.Hostname()
	m := &Model{host: host, version: version, done: new(atomic.Int64), scanning: true}
	_, err := tea.NewProgram(m).Run()
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
	done := m.done
	return func() tea.Msg {
		items, err := scan.Scan(context.Background(), func(d, t int) {
			done.Store(int64(d)<<32 | int64(t))
		})
		return scannedMsg{items, err}
	}
}

func (m *Model) lookForUpdate(asked bool) tea.Cmd {
	m.checking = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		rel, err := update.Latest(ctx)
		return latestMsg{rel, err, asked}
	}
}

func (m *Model) installSelf() tea.Cmd {
	tag := m.newer.Tag
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		path, err := update.Install(ctx, tag)
		return installedMsg{path, tag, err}
	}
}

func failed(it scan.Item) bool { return it.Err != "" && it.Level == scan.OK }

func needs(it scan.Item) bool { return it.Level > scan.OK || it.Err != "" }

// rows are the sections and their containers: one section per compose
// stack, the sickest first, and one for containers standing alone (on
// their own or the only one of their stack). Healthy ones show with a.
func (m *Model) rows() []row {
	type section struct {
		name  string // empty: standalone
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
		if key == "" || count[key] == 1 && it.Service == key {
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
			out = append(out, row{group: s.name, items: []scan.Item{it}, sub: true})
		}
	}
	return out
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
		m.pick = min(m.pick, max(0, len(m.rows())-1))
	case fixedMsg:
		if msg.err != nil {
			m.note = sDue.Render("✗ " + msg.name + " still has scurvy")
		} else {
			m.note = sOK.Render("✓ " + msg.name + " is shipshape again")
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
			if msg.asked {
				m.askSelf = true
			}
		case msg.asked:
			m.note = sDim.Render("scurvy " + m.version + " is the newest")
		}
	case installedMsg:
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
	yes := k == "y" || k == "enter" || k == "j"
	if m.askSelf {
		m.askSelf = false
		if yes {
			m.note = sDim.Render("installing scurvy " + m.newer.Tag + "…")
			return m.installSelf()
		}
		return nil
	}
	if m.confirm {
		m.confirm = false
		if yes && m.pick < len(rows) {
			return m.fix(rows[m.pick])
		}
		m.note = ""
		return nil
	}
	switch k {
	case "q", "ctrl+c", "esc":
		return tea.Quit
	case "up", "k":
		m.pick = max(0, m.pick-1)
	case "down", "j":
		m.pick = min(len(rows)-1, m.pick+1)
	case "home", "g":
		m.pick = 0
	case "end", "G":
		m.pick = max(0, len(rows)-1)
	case "a":
		m.all = !m.all
		m.pick, m.top = 0, 0
	case "r":
		if !m.scanning {
			m.note = ""
			return tea.Batch(m.scan(), tick())
		}
	case "u":
		if !update.Managed(m.version) {
			m.note = sDim.Render("this scurvy is a " + m.version + " build: it does not update itself")
			return nil
		}
		if m.newer.Tag != "" {
			m.askSelf = true
			return nil
		}
		if !m.checking {
			m.note = sDim.Render("looking for a newer scurvy…")
			return m.lookForUpdate(true)
		}
	case "enter", "space":
		if m.scanning || m.pick >= len(rows) {
			return nil
		}
		r := rows[m.pick]
		if len(targets(r)) == 0 {
			m.note = sDim.Render(name(r) + " has no compose file here: update it where it was started")
			return nil
		}
		m.confirm = true
	}
	return nil
}

// targets are what Enter updates on a row: a container, or the containers
// of a stack that need it (all of them, when none does).
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
	if r.head {
		if r.group == "" {
			return "the standalone containers"
		}
		return r.group
	}
	return r.items[0].Name()
}

// fix leaves the screen to update in the open terminal, so the docker
// output scrolls by, and comes back to look again.
func (m *Model) fix(r row) tea.Cmd {
	todo := targets(r)
	self, err := os.Executable()
	if err != nil {
		return func() tea.Msg { return fixedMsg{name(r), err} }
	}
	// What was found goes along, so fix need not look again.
	data, err := json.Marshal(todo)
	if err != nil {
		return func() tea.Msg { return fixedMsg{name(r), err} }
	}
	cmd := exec.Command(self, "fix")
	cmd.Env = append(os.Environ(), ItemEnv+"="+string(data))
	return tea.ExecProcess(cmd, func(err error) tea.Msg { return fixedMsg{name(r), err} })
}
