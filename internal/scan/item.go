package scan

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/chriopter/scurvy/internal/docker"
	"github.com/chriopter/scurvy/internal/registry"
)

// Kind is how a container's image comes about.
type Kind string

const (
	Pulled Kind = "pulled" // compose pulls it from a registry
	Built  Kind = "built"  // compose builds it from a Dockerfile
	Loose  Kind = "loose"  // not up to compose here: shown, not updated
	AIO    Kind = "aio"    // started by Nextcloud AIO, which updates them itself
)

// Mastercontainer is the container of Nextcloud All-in-One that starts
// and updates the others.
const Mastercontainer = "nextcloud-aio-mastercontainer"

// Level is how much a container asks for an update.
type Level int

const (
	OK      Level = iota // current: nothing newer
	Update               // an update is there, not for long
	Overdue              // an update is there for longer than its grace
	EOL                  // the base gets no fixes any more
)

// Word is a level's name: current, update, overdue, eol.
func (l Level) Word() string {
	return [...]string{"current", "update", "overdue", "eol"}[l]
}

// Labels a compose service can set.
const (
	LabelIgnore   = "scurvy.ignore"    // "true": leave this container out
	LabelDueAfter = "scurvy.due-after" // grace for its updates: 7d, 2w, 36h
)

// HookFile, in a stack's directory, updates the stack in place of pull,
// build and up; scurvy runs it as it is.
const HookFile = "docker-update.sh"

// Item is one running container and what scurvy found about it.
type Item struct {
	Container docker.Container
	Project   docker.Project
	Service   string
	Kind      Kind
	Image     string // the update's target: the image compose would use now
	Running   string // the image the container was started from

	Level    Level
	Findings []Finding
	Current  string   // what runs: a tag, a version, "built 3 days ago"
	Errs     []string // checks that could not be done, and why

	Edits []Edit // exact lines to change before updating: a raised version
	Pull  string // a build context that is a git clone behind its upstream
	Hook  string // the stack's own update script, if any
}

// Finding is one thing found, with its weight.
type Finding struct {
	Level Level
	Short string    // a few words for the table, empty for side notes
	Text  string    // the whole sentence
	Since time.Time // since when it is so, if known
}

// Name is how people call the container: its service, else its name.
func (it Item) Name() string {
	if it.Service != "" {
		return it.Service
	}
	return it.Container.Name
}

// Updatable reports whether Enter can update it: compose knows how, and no
// digest pins it.
func (it Item) Updatable() bool {
	return it.Kind != Loose && it.Project.Name != "" && registry.Parse(it.Image).Digest == ""
}

// Unchecked reports a container nothing could be found about, because a
// check failed.
func (it Item) Unchecked() bool { return it.Level == OK && len(it.Errs) > 0 }

// Source says where the image comes from: a registry with a moving tag or
// a pinned version or digest, a build of one's own, or another tool.
func (it Item) Source() string {
	switch it.Kind {
	case Built:
		return "own build"
	case AIO:
		return "aio"
	case Loose:
		if it.Project.Name != "" {
			return "external"
		}
		return "docker run"
	}
	ref := registry.Parse(it.Image)
	switch {
	case ref.Digest != "":
		return "digest"
	case pinned(ref.Tag):
		return "pinned"
	}
	return "registry"
}

// Top is the finding that counts most. ok is false when none asks for
// anything.
func (it Item) Top() (Finding, bool) {
	if len(it.Findings) > 0 && it.Findings[0].Level > OK {
		return it.Findings[0], true
	}
	return Finding{}, false
}

func (it *Item) add(l Level, since time.Time, short, format string, a ...any) {
	it.Findings = append(it.Findings, Finding{l, short, fmt.Sprintf(format, a...), since})
	it.Level = max(it.Level, l)
}

func (it *Item) fail(check string, err error) {
	it.Errs = append(it.Errs, check+": "+err.Error())
}

// due is how long an update may wait: the label scurvy.due-after, else def.
func (it Item) due(def time.Duration) time.Duration {
	v := strings.TrimSpace(it.Container.Labels[LabelDueAfter])
	if v == "" {
		return def
	}
	unit := map[byte]time.Duration{'h': time.Hour, 'd': 24 * time.Hour, 'w': 7 * 24 * time.Hour}[v[len(v)-1]]
	n, err := strconv.Atoi(v[:len(v)-1])
	if unit == 0 || err != nil || n < 0 {
		return def
	}
	return time.Duration(n) * unit
}

// Sort puts the sickest first, then by project and name.
func Sort(items []Item) {
	sort.SliceStable(items, func(a, b int) bool {
		x, y := items[a], items[b]
		if x.Level != y.Level {
			return x.Level > y.Level
		}
		if x.Project.Name != y.Project.Name {
			return x.Project.Name < y.Project.Name
		}
		return x.Name() < y.Name()
	})
}

// Days says a duration the short way: today, 1 day, 12 days.
func Days(d time.Duration) string {
	n := int(d.Hours() / 24)
	switch {
	case n < 1:
		return "today"
	case n == 1:
		return "1 day"
	}
	return strconv.Itoa(n) + " days"
}
