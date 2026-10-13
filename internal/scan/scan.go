// Package scan finds the running containers and tells, for each, whether
// something newer is waiting: a new image behind its tag, a newer pinned
// version, a stale build, a base image past its end of life.
package scan

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chriopter/scurvy/internal/docker"
	"github.com/chriopter/scurvy/internal/registry"
)

// Kind is how a container's image comes about.
type Kind string

const (
	Pulled Kind = "pulled" // compose pulls it from a registry
	Built  Kind = "built"  // compose builds it from a Dockerfile
	Loose  Kind = "loose"  // not started by compose: shown, not updated
)

// Level is how badly a container needs its limes.
type Level int

const (
	OK      Level = iota // current: nothing newer
	Update               // an update is there, not for long
	Overdue              // an update is there for longer than its grace
	EOL                  // the base gets no fixes any more
)

// How long an update may wait before it is due, unless a container's
// label scurvy.due-after says otherwise (7d, 2w, 36h).
const (
	dueTag    = 14 * 24 * time.Hour // a new image behind a moving tag
	duePinned = 90 * 24 * time.Hour // a newer pinned version
	dueBuild  = 30 * 24 * time.Hour // a build, or a newer base under it
	eolSoon   = 60 * 24 * time.Hour // warn this long before end of life
)

// HookFile, in a stack's directory, updates the stack in place of pull,
// build and up; scurvy runs it as it is.
const HookFile = "docker-update.sh"

// Labels a compose service can set.
const (
	LabelIgnore   = "scurvy.ignore"    // "true": leave this container out
	LabelDueAfter = "scurvy.due-after" // grace for its updates
)

// Item is one running container and what scurvy found about it.
type Item struct {
	Container docker.Container
	Project   docker.Project
	Service   string
	Kind      Kind
	Image     string // as configured: the image, or what compose builds

	Level    Level
	Findings []Finding
	Current  string // what runs: a tag, a version, "built 3d ago"
	Latest   string // what is waiting, if it has a name
	Err      string // why something could not be checked

	Bump *Bump  // a pinned version to raise first, if any
	Pull string // a build context that is a git clone behind its upstream
	Hook string // the stack's own update script (docker-update.sh), if any
}

// Finding is one thing found, with its weight.
type Finding struct {
	Level Level
	Short string    // a few words for the table, empty for side notes
	Text  string    // the whole sentence
	Since time.Time // since when it is so, if known
}

// Bump raises a pinned version in a file: Old becomes New.
type Bump struct {
	File, Old, New string
}

// Name is how people call the container: its service, else its name.
func (it Item) Name() string {
	if it.Service != "" {
		return it.Service
	}
	return it.Container.Name
}

// Updatable reports whether scurvy can update it: compose knows how.
func (it Item) Updatable() bool { return it.Kind != Loose && it.Project.Name != "" }

func (it *Item) add(l Level, since time.Time, short, format string, a ...any) {
	it.Findings = append(it.Findings, Finding{l, short, fmt.Sprintf(format, a...), since})
	if l > it.Level {
		it.Level = l
	}
}

// Scanner holds what one scan has learned so far.
type Scanner struct {
	reg    *registry.Client
	images *docker.Images
	eol    *eolDates
	now    time.Time
}

// Scan checks every running container. progress, if set, hears how many
// of how many are done.
func Scan(ctx context.Context, progress func(done, total int)) ([]Item, error) {
	cs, err := docker.Running(ctx)
	if err != nil {
		return nil, err
	}
	s := &Scanner{reg: registry.New(), images: docker.NewImages(), eol: newEOLDates(), now: time.Now()}

	// Each compose project is resolved once.
	type resolved struct {
		svcs map[string]docker.Service
		err  error
	}
	projects := map[string]resolved{}
	var items []*Item
	for _, c := range cs {
		it := &Item{Container: c, Image: c.Image, Kind: Loose}
		if c.Project() != "" && docker.ProjectOf(c).Files == nil {
			// Labelled as compose, but no compose file: another tool
			// (an all-in-one installer, Portainer) made it. Checked, not updated.
			it.Project.Name = c.Project()
			it.Service = c.Service()
		} else if c.Project() != "" {
			it.Project = docker.ProjectOf(c)
			it.Service = c.Service()
			key := it.Project.Name + "\x00" + it.Project.Dir
			r, ok := projects[key]
			if !ok {
				r.svcs, r.err = docker.Services(ctx, it.Project)
				projects[key] = r
			}
			svc, found := r.svcs[it.Service]
			switch {
			case r.err != nil:
				it.Err = r.err.Error()
			case !found:
				it.Err = "service not in the compose files any more"
			case svc.Labels[LabelIgnore] == "true" || c.Labels[LabelIgnore] == "true":
				continue
			case svc.Build != nil:
				it.Kind = Built
				it.Image = svc.Image
				if it.Image == "" {
					it.Image = it.Project.Name + "-" + it.Service
				}
			default:
				it.Kind = Pulled
				if svc.Image != "" {
					it.Image = svc.Image
				}
			}
		} else if c.Labels[LabelIgnore] == "true" {
			continue
		}
		items = append(items, it)
	}

	var done atomic.Int32
	total := len(items)
	if progress != nil {
		progress(0, total)
	}
	for _, it := range items {
		if it.Updatable() && it.Project.Dir != "" {
			f := filepath.Join(it.Project.Dir, HookFile)
			if st, err := os.Stat(f); err == nil && st.Mode().IsRegular() {
				it.Hook = f
			}
		}
	}
	work := make(chan *Item)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range work {
				s.check(ctx, it, projects[it.Project.Name+"\x00"+it.Project.Dir].svcs)
				if progress != nil {
					progress(int(done.Add(1)), total)
				}
			}
		}()
	}
	for _, it := range items {
		work <- it
	}
	close(work)
	wg.Wait()

	out := make([]Item, len(items))
	for i, it := range items {
		sort.SliceStable(it.Findings, func(a, b int) bool { return it.Findings[a].Level > it.Findings[b].Level })
		out[i] = *it
	}
	Sort(out)
	return out, ctx.Err()
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

func (s *Scanner) check(ctx context.Context, it *Item, svcs map[string]docker.Service) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	switch it.Kind {
	case Built:
		s.built(ctx, it, svcs[it.Service])
	default:
		if it.Err == "" {
			s.pulled(ctx, it)
		}
	}
}

// pulled checks an image that comes from a registry.
func (s *Scanner) pulled(ctx context.Context, it *Item) {
	ref := registry.Parse(it.Image)
	it.Current = ref.Tag
	if ref.Digest != "" {
		it.Current = "@" + short(ref.Digest)
		it.add(OK, time.Time{}, "", "pinned by digest")
		s.endOfLife(ctx, it, ref)
		return
	}
	due := it.due(dueTag)
	if tagged := s.images.Get(ctx, it.Image); tagged != nil && tagged.ID != it.Container.ImageID {
		it.add(Update, time.Time{}, "pulled, not recreated", "newer image already pulled, container still on the old one")
	}
	running := s.images.Get(ctx, it.Container.ImageID)
	digest, err := s.reg.Digest(ctx, ref)
	switch {
	case err != nil:
		it.Err = err.Error()
	case running == nil:
		it.Err = "image of the container not found"
	case len(running.RepoDigests) == 0:
		it.add(OK, time.Time{}, "", "no registry digest on the local image")
	case !running.HasDigest(digest):
		created, _ := s.reg.Created(ctx, ref, digest)
		it.Latest = ref.Tag + "@" + short(digest)
		it.add(s.level(created, due), created, "new image", "new image for %s, %s", ref.Tag, s.ago(created, "built"))
	}

	if pinned(ref.Tag) {
		if tags, err := s.reg.Tags(ctx, ref); err == nil {
			minor, major := newerTags(tags, ref.Tag)
			if minor != "" {
				created, _ := s.reg.Created(ctx, ref, minor)
				it.Latest = minor
				it.add(s.level(created, it.due(duePinned)), created, "new version", "%s → %s, %s", ref.Tag, minor, s.ago(created, "released"))
				it.Bump = findBump(it.Project.Files, ref.Name+":"+ref.Tag, ref.Name+":"+minor)
				if it.Bump == nil && it.Updatable() {
					it.add(OK, time.Time{}, "", "set %s in the compose file by hand", minor)
				}
			}
			if major != "" {
				it.add(OK, time.Time{}, "", "new major version %s", major)
			}
		}
	}
	s.endOfLife(ctx, it, ref)
}

// built checks an image compose builds: how old the build is, whether its
// code or its base image moved since.
func (s *Scanner) built(ctx context.Context, it *Item, svc docker.Service) {
	img := s.images.Get(ctx, it.Container.ImageID)
	if img == nil {
		it.Err = "image of the container not found"
		return
	}
	if latest := s.images.Get(ctx, it.Image); latest != nil && latest.ID != it.Container.ImageID {
		it.add(Update, time.Time{}, "built, not recreated", "newer build ready, container still on the old one")
	}
	// The image's own date. (Its last tag time moves with every backup
	// tag, so it says nothing about the build.)
	built := img.Created
	due := it.due(dueBuild)
	it.Current = s.ago(built, "built")
	if !built.IsZero() && s.now.Sub(built) > due {
		it.add(Overdue, built, "old build", "build is %s", s.ago(built, ""))
	}

	dir := svc.Build.Context
	clone, moved := gitClone(ctx, dir)
	if moved {
		it.Pull = dir
		it.add(Update, time.Time{}, "upstream moved", "the build context is a git clone, and its upstream has new commits")
	}
	if changed := lastCommit(ctx, dir); !built.IsZero() && changed.After(built.Add(time.Minute)) {
		it.add(Update, changed, "code changed", "code %s, after the build", s.ago(changed, "changed"))
	}

	file := svc.Build.Dockerfile
	if file == "" {
		file = "Dockerfile"
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(dir, file)
	}
	b, ok := finalBase(file)
	if !ok {
		return
	}
	ref := registry.Parse(b.ref)
	if pinned(ref.Tag) {
		if tags, err := s.reg.Tags(ctx, ref); err == nil {
			minor, major := newerTags(tags, ref.Tag)
			if minor != "" {
				created, _ := s.reg.Created(ctx, ref, minor)
				it.Latest = minor
				it.add(s.level(created, it.due(duePinned)), created, "new base version", "base %s → %s, %s", ref.Name+":"+ref.Tag, minor, s.ago(created, "released"))
				switch {
				case clone: // not ours to edit: the next pull would clash
					it.add(OK, time.Time{}, "", "the Dockerfile belongs to the clone's upstream; raise it there")
				case !strings.Contains(b.raw, "$"):
					it.Bump = findBump([]string{file}, ref.Name+":"+ref.Tag, ref.Name+":"+minor)
				}
			}
			if major != "" {
				it.add(OK, time.Time{}, "", "base has a new major version %s", major)
			}
		}
	} else if ref.Digest == "" {
		if digest, err := s.reg.Digest(ctx, ref); err != nil {
			it.Err = err.Error()
		} else if created, err := s.reg.Created(ctx, ref, digest); err == nil &&
			!built.IsZero() && created.After(built) {
			it.add(s.level(created, due), created, "new base image", "base %s has a newer image, %s", b.ref, s.ago(created, "built"))
		}
	}
	s.endOfLife(ctx, it, ref)
}

// endOfLife warns about a release line that no longer gets fixes, or soon
// will not.
func (s *Scanner) endOfLife(ctx context.Context, it *Item, ref registry.Ref) {
	for _, rel := range releasesOf(ref) {
		end := s.eol.End(ctx, rel)
		switch {
		case end.IsZero():
		case end.Unix() == 0:
			it.add(EOL, time.Time{}, rel.label+" end of life", "%s is end of life", rel.label)
		case end.Before(s.now):
			it.add(EOL, time.Time{}, rel.label+" end of life", "%s is end of life since %s", rel.label, end.Format("2006-01-02"))
		case end.Before(s.now.Add(eolSoon)):
			it.add(Overdue, time.Time{}, rel.label+" end of life soon", "%s reaches end of life on %s", rel.label, end.Format("2006-01-02"))
		}
	}
}

// level weighs an update by how long it has been waiting.
func (s *Scanner) level(since time.Time, due time.Duration) Level {
	if !since.IsZero() && s.now.Sub(since) > due {
		return Overdue
	}
	return Update
}

func (s *Scanner) ago(t time.Time, verb string) string {
	if t.IsZero() {
		if verb == "" {
			return "of unknown age"
		}
		return verb + " at an unknown time"
	}
	d := Days(s.now.Sub(t))
	if verb == "" {
		if d == "today" {
			return "from today"
		}
		return d + " old"
	}
	if d == "today" {
		return verb + " today"
	}
	return verb + " " + d + " ago"
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

func short(digest string) string {
	d := strings.TrimPrefix(digest, "sha256:")
	if len(d) > 12 {
		d = d[:12]
	}
	return d
}

// lastCommit is when the build context last changed in git; zero outside
// a repository.
func lastCommit(ctx context.Context, dir string) time.Time {
	if dir == "" {
		return time.Time{}
	}
	out, err := exec.CommandContext(ctx, "git", "-c", "safe.directory=*", "-C", dir,
		"log", "-1", "--format=%ct", "--", ".").Output()
	if err != nil {
		return time.Time{}
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

// gitClone reports whether dir is a git clone of its own (not a folder in
// a bigger repository) with an upstream, and whether that upstream branch
// points elsewhere than HEAD. It asks the remote without fetching, so
// nothing changes here.
func gitClone(ctx context.Context, dir string) (clone, moved bool) {
	git := func(args ...string) string {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-c", "safe.directory=*", "-C", dir}, args...)...).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	top := git("rev-parse", "--show-toplevel")
	if top == "" || filepath.Clean(top) != filepath.Clean(dir) {
		return false, false
	}
	up := git("rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}") // origin/main
	remote, branch, ok := strings.Cut(up, "/")
	if !ok {
		return false, false
	}
	head := git("rev-parse", "HEAD")
	f := strings.Fields(git("ls-remote", remote, "refs/heads/"+branch))
	return true, head != "" && len(f) > 0 && f[0] != head
}

// findBump finds the file among files that names old, so it can say new.
func findBump(files []string, old, new string) *Bump {
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if containsRef(string(data), old) {
			return &Bump{File: f, Old: old, New: new}
		}
	}
	return nil
}

// containsRef finds ref as a whole reference: app:1.2.1 is not in
// app:1.2.11.
func containsRef(text, ref string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], ref)
		if j < 0 {
			return false
		}
		end := i + j + len(ref)
		if end == len(text) || !isRefChar(text[end]) {
			if start := i + j; start == 0 || !isRefChar(text[start-1]) || text[start-1] == '/' {
				return true
			}
		}
		i = end
	}
}

func isRefChar(c byte) bool {
	return c == '.' || c == '-' || c == '_' || c == '/' || c == ':' || c == '@' ||
		'0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// Source says where the image comes from: a registry with a moving tag or
// a pinned version, a build of one's own, or another tool's container.
func (it Item) Source() string {
	switch it.Kind {
	case Built:
		return "own build"
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

// Top is the finding that counts most: the first one that asks for
// something. ok is false when there is none.
func (it Item) Top() (Finding, bool) {
	if len(it.Findings) > 0 && it.Findings[0].Level > OK {
		return it.Findings[0], true
	}
	return Finding{}, false
}

// Word is a level's name: current, update, overdue, eol.
func (l Level) Word() string {
	return [...]string{"current", "update", "overdue", "eol"}[l]
}
