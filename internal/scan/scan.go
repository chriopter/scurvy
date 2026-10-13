// Package scan finds the running containers and tells, for each, whether
// something newer is waiting: a new image behind its tag, a newer pinned
// version, a stale build, a base image past its end of life. It changes
// nothing; what an update would change, it describes as exact edits.
package scan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chriopter/scurvy/internal/docker"
	"github.com/chriopter/scurvy/internal/registry"
)

// How long an update may wait before it is overdue, unless a container's
// label scurvy.due-after says otherwise.
const (
	dueTag    = 14 * 24 * time.Hour // a new image behind a moving tag
	duePinned = 90 * 24 * time.Hour // a newer pinned version
	dueBuild  = 30 * 24 * time.Hour // a build, or a newer base under it
	eolSoon   = 60 * 24 * time.Hour // warn this long before end of life
)

type scanner struct {
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
	s := &scanner{reg: registry.New(), images: docker.NewImages(), eol: newEOLDates(), now: time.Now()}

	type resolved struct {
		svcs map[string]docker.Service
		err  error
	}
	projects := map[string]resolved{} // each compose project is resolved once
	var items []*Item
	var svcs []docker.Service
	for _, c := range cs {
		if c.Labels[LabelIgnore] == "true" {
			continue
		}
		it := &Item{Container: c, Image: c.Image, Running: c.Image, Kind: Loose, Project: docker.ProjectOf(c), Service: c.Service()}
		var svc docker.Service
		switch {
		case it.Project.Name == "":
		case c.OneOff():
			it.add(OK, time.Time{}, "", "started by docker compose run: up would not make it again")
		case len(it.Project.Files) == 0:
			// Labelled as compose, but without a compose file: another
			// tool (an all-in-one installer, Portainer) made it.
		default:
			r, ok := projects[it.Project.Key()]
			if !ok {
				r.svcs, r.err = docker.Services(ctx, it.Project)
				projects[it.Project.Key()] = r
			}
			found := false
			svc, found = r.svcs[it.Service]
			switch {
			case r.err != nil:
				it.fail("compose", r.err)
			case !found:
				it.fail("compose", errors.New("the service is not in the compose files any more"))
			case svc.Labels[LabelIgnore] == "true":
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
		}
		if it.Updatable() && it.Project.Dir != "" {
			if st, err := os.Lstat(filepath.Join(it.Project.Dir, HookFile)); err == nil && st.Mode().IsRegular() {
				it.Hook = filepath.Join(it.Project.Dir, HookFile)
			}
		}
		items = append(items, it)
		svcs = append(svcs, svc)
	}

	// Nextcloud All-in-One starts its containers itself; with its
	// mastercontainer here, scurvy can ask it to update them.
	for _, it := range items {
		if it.Container.Name == Mastercontainer {
			for _, x := range items {
				if x.Kind == Loose && x.Project.Name == "nextcloud-aio" {
					x.Kind = AIO
				}
			}
			break
		}
	}

	var done atomic.Int32
	if progress != nil {
		progress(0, len(items))
	}
	var wg sync.WaitGroup
	work := make(chan int)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				s.check(ctx, items[i], svcs[i])
				if progress != nil {
					progress(int(done.Add(1)), len(items))
				}
			}
		}()
	}
	for i := range items {
		work <- i
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

func (s *scanner) check(ctx context.Context, it *Item, svc docker.Service) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	switch {
	case it.Kind == Built:
		s.built(ctx, it, svc)
	case len(it.Errs) == 0:
		s.pulled(ctx, it)
	}
}

// pulled checks an image that comes from a registry.
func (s *scanner) pulled(ctx context.Context, it *Item) {
	running := registry.Parse(it.Running)
	target := registry.Parse(it.Image)
	it.Current = running.Tag
	if running.Digest != "" {
		it.Current = "@" + short(running.Digest)
	}
	if it.Kind == Pulled && it.Running != it.Image {
		it.add(Update, time.Time{}, "compose file changed", "the compose file says %s, the container still runs %s", it.Image, it.Running)
	}
	s.endOfLife(ctx, it, running)
	if target.Digest != "" {
		it.add(OK, time.Time{}, "", "pinned by digest: change the digest in the compose file to update")
		return
	}

	if tagged := s.images.Get(ctx, it.Image); tagged != nil && tagged.ID != it.Container.ImageID {
		it.add(Update, time.Time{}, "pulled, not recreated", "a newer image is already pulled, the container still runs the old one")
	}
	img := s.images.Get(ctx, it.Container.ImageID)
	digest, err := s.reg.Digest(ctx, target)
	switch {
	case err != nil:
		it.fail("image", err)
	case img == nil:
		it.fail("image", errors.New("the container's image is gone"))
	case len(img.RepoDigests) == 0:
		it.fail("image", errors.New("the local image has no registry digest to compare"))
	case !img.HasDigest(digest):
		published, _ := s.reg.Created(ctx, target, digest)
		it.add(s.level(published, it.due(dueTag)), published, "new image", "new image for %s, %s", target.Tag, s.ago(published, "published"))
	}

	if !pinned(target.Tag) {
		return
	}
	tags, err := s.reg.Tags(ctx, target, majorOf(target.Tag))
	if err != nil {
		it.fail("versions", err)
		return
	}
	newer := newerTag(tags, target.Tag)
	if newer == "" {
		return
	}
	released, _ := s.reg.Created(ctx, target, newer)
	it.add(s.level(released, it.due(duePinned)), released, "new version", "%s → %s, %s", target.Tag, newer, s.ago(released, "released"))
	if !it.Updatable() {
		return
	}
	e, err := imageEdit(it.Project.Files, it.Service, it.Image, target.Name+":"+newer)
	if err != nil {
		it.add(OK, time.Time{}, "", "set %s by hand: %v", newer, err)
		return
	}
	it.Edits = []Edit{e}
}

// built checks an image compose builds: how old the build is, whether its
// code or its base image moved since.
func (s *scanner) built(ctx context.Context, it *Item, svc docker.Service) {
	img := s.images.Get(ctx, it.Container.ImageID)
	if img == nil {
		it.fail("image", errors.New("the container's image is gone"))
		return
	}
	if latest := s.images.Get(ctx, it.Image); latest != nil && latest.ID != it.Container.ImageID {
		it.add(Update, time.Time{}, "built, not recreated", "a newer build is ready, the container still runs the old one")
	}
	// The image's own date; its last tag time moves with every backup tag.
	built := img.Created
	it.Current = s.ago(built, "built")
	if !built.IsZero() && s.now.Sub(built) > it.due(dueBuild) {
		it.add(Overdue, built, "old build", "the build is %s", s.ago(built, ""))
	}

	dir := svc.Build.Context
	if svc.Build.Inline != "" || !filepath.IsAbs(dir) {
		it.fail("base", errors.New("an inline Dockerfile or a remote build context is not checked"))
		return
	}
	clone := false
	switch top, err := Trusted(ctx, dir); {
	case err != nil:
		it.add(OK, time.Time{}, "", "git not asked: %v", err)
	case top != "":
		var moved bool
		if clone, moved, err = upstream(ctx, dir, top); err != nil {
			it.fail("git", err)
		} else if moved {
			it.Pull = dir
			it.add(Update, time.Time{}, "upstream moved", "the build context is a git clone, and its upstream has new commits")
		}
		if changed := lastCommit(ctx, dir); !built.IsZero() && changed.After(built.Add(time.Minute)) {
			it.add(Update, changed, "code changed", "code %s, after the build", s.ago(changed, "changed"))
		}
	}

	file := svc.Build.Dockerfile
	if file == "" {
		file = "Dockerfile"
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(dir, file)
	}
	b, err := baseOf(file, svc.Build.Args, svc.Build.Target)
	switch {
	case err != nil:
		it.fail("base", err)
		return
	case b.ref == "": // FROM scratch: no base to check
		return
	}
	ref := registry.Parse(b.ref)
	s.endOfLife(ctx, it, ref)
	switch {
	case ref.Digest != "":
		it.add(OK, time.Time{}, "", "the base is pinned by digest")
	case pinned(ref.Tag):
		s.baseVersion(ctx, it, file, b, ref, clone)
	default:
		// The base's tag moves: compare dates, the best that can be told
		// without the digest the build used.
		digest, err := s.reg.Digest(ctx, ref)
		if err != nil {
			it.fail("base", err)
			return
		}
		published, err := s.reg.Created(ctx, ref, digest)
		if err != nil {
			it.fail("base", err)
		} else if !built.IsZero() && published.After(built) {
			it.add(s.level(published, it.due(dueBuild)), published, "new base image", "base %s has an image published after the build, %s", b.ref, s.ago(published, "published"))
		}
	}
}

// baseVersion looks for a newer version of a pinned base, and how to raise
// it: the FROM lines, and an image tag named after the base's version
// (image: caddy-custom:2.11.4 goes along to 2.11.7).
func (s *scanner) baseVersion(ctx context.Context, it *Item, file string, b base, ref registry.Ref, clone bool) {
	tags, err := s.reg.Tags(ctx, ref, majorOf(ref.Tag))
	if err != nil {
		it.fail("versions", err)
		return
	}
	newer := newerTag(tags, ref.Tag)
	if newer == "" {
		return
	}
	released, _ := s.reg.Created(ctx, ref, newer)
	it.add(s.level(released, it.due(duePinned)), released, "new base version", "base %s:%s → %s, %s", ref.Name, ref.Tag, newer, s.ago(released, "released"))
	switch {
	case clone:
		it.add(OK, time.Time{}, "", "the Dockerfile belongs to the clone's upstream: raise it there")
		return
	case b.raw != b.ref:
		it.add(OK, time.Time{}, "", "the base comes from a build ARG: raise it by hand")
		return
	}
	have := map[string]bool{}
	for _, t := range tags {
		have[t] = true
	}
	edits, err := fromEdits(file, b.raw, ref.Name+":"+newer, func(to string) bool { return have[registry.Parse(to).Tag] })
	if err != nil {
		it.add(OK, time.Time{}, "", "raise the base by hand: %v", err)
		return
	}
	if own := registry.Parse(it.Image); own.Tag == ref.Tag {
		e, err := imageEdit(it.Project.Files, it.Service, it.Image, own.Name+":"+newer)
		if err != nil {
			it.add(OK, time.Time{}, "", "raise the base by hand: %v", err)
			return
		}
		edits = append(edits, e)
	}
	it.Edits = edits
}

// endOfLife warns about a release line that no longer gets fixes, or soon
// will not.
func (s *scanner) endOfLife(ctx context.Context, it *Item, ref registry.Ref) {
	for _, rel := range releasesOf(ref) {
		end, err := s.eol.End(ctx, rel)
		switch {
		case err != nil:
			it.fail("end of life", err)
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
func (s *scanner) level(since time.Time, due time.Duration) Level {
	if !since.IsZero() && s.now.Sub(since) > due {
		return Overdue
	}
	return Update
}

func (s *scanner) ago(t time.Time, verb string) string {
	switch d := Days(s.now.Sub(t)); {
	case t.IsZero() && verb == "":
		return "of unknown age"
	case t.IsZero():
		return verb + " at an unknown time"
	case verb == "" && d == "today":
		return "from today"
	case verb == "":
		return d + " old"
	case d == "today":
		return verb + " today"
	default:
		return verb + " " + d + " ago"
	}
}

func short(digest string) string {
	d := strings.TrimPrefix(digest, "sha256:")
	return d[:min(12, len(d))]
}
