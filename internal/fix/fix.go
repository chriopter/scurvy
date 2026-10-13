// Package fix is scurvy's core: it carries out the update of one compose
// project, and nothing else. In order: it checks what must hold, keeps the
// running images under backup tags, pulls git clones, makes the exact file
// edits it was handed, pulls or builds, and starts the planned containers
// again, waiting until they are healthy. If a step fails, or the update is
// interrupted, it puts back what it changed: images, git clones, files,
// and the containers, if it had touched them. It shows every change it
// makes; with DryRun it makes none.
package fix

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/chriopter/scurvy/internal/docker"
	"github.com/chriopter/scurvy/internal/registry"
	"github.com/chriopter/scurvy/internal/scan"
)

// DryRun shows every change and makes none.
var DryRun bool

const (
	keep       = 2      // backup tags kept per service
	buildCache = "10GB" // build cache left after a build
	waitHealth = "180"  // seconds up --wait gives the containers
	stampTime  = "20060102-150405"
)

// lockFile is shared by every scurvy on the machine.
var lockFile = "/run/lock/scurvy.lock"

// Plan is the update of one compose project.
type Plan struct {
	Project docker.Project
	Items   []scan.Item // one per service
	Hook    string      // the project's own update script: it does pull, build and up
	Edits   []scan.Edit
	Pulls   []string // build contexts that are git clones, pulled first
	NoCache bool     // build from scratch
}

// Plans groups what to update by compose project. all is everything scan
// found: a project with its own update script updates as a whole, so all
// its containers go into the plan, to be backed up. Containers scurvy
// cannot update come back as refused, with the reason.
func Plans(todo, all []scan.Item, noCache bool) (plans []Plan, refused []string) {
	byKey := map[string]*Plan{}
	var order []string
	seen := map[string]bool{}
	add := func(p *Plan, it scan.Item) {
		if seen[it.Project.Key()+"\x00"+it.Service] {
			return // another replica of a service already in the plan
		}
		seen[it.Project.Key()+"\x00"+it.Service] = true
		p.Items = append(p.Items, it)
		for _, e := range it.Edits {
			if !hasEdit(p.Edits, e) {
				p.Edits = append(p.Edits, e)
			}
		}
		if it.Pull != "" && !contains(p.Pulls, it.Pull) {
			p.Pulls = append(p.Pulls, it.Pull)
		}
	}
	for _, it := range todo {
		if !it.Updatable() {
			refused = append(refused, it.Name()+": "+whyNot(it))
			continue
		}
		key := it.Project.Key()
		p := byKey[key]
		if p == nil {
			p = &Plan{Project: it.Project, Hook: it.Hook, NoCache: noCache}
			byKey[key] = p
			order = append(order, key)
		}
		add(p, it)
	}
	for _, k := range order {
		p := byKey[k]
		if p.Hook != "" { // the script is in charge, of the whole project
			for _, it := range all {
				if it.Project.Key() == k && it.Updatable() {
					add(p, it)
				}
			}
			p.Edits, p.Pulls = nil, nil
		}
		plans = append(plans, *p)
	}
	return plans, refused
}

func whyNot(it scan.Item) string {
	switch {
	case it.Kind == scan.Loose && it.Project.Name != "":
		return "started by " + it.Project.Name + ", not from a compose file here"
	case it.Kind == scan.Loose:
		return "not started by compose"
	}
	return "pinned by digest: change the digest in the compose file"
}

func hasEdit(edits []scan.Edit, e scan.Edit) bool {
	for _, x := range edits {
		if x.File == e.File && x.Line == e.Line {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (p Plan) services(kind scan.Kind) []string {
	var out []string
	for _, it := range p.Items {
		if kind == "" || it.Kind == kind {
			out = append(out, it.Service)
		}
	}
	return out
}

// Name is the project's name.
func (p Plan) Name() string { return p.Project.Name }

// backup is a service's running image kept under a tag, and the
// references to give back to it: the one it ran as, and the one the
// compose file names now, so compose starts the old image either way.
type backup struct {
	service, tag string
	refs         []string
}

// clone is a git clone scurvy pulled: where it was, and where it left it.
type clone struct{ dir, before, after string }

// tx is one update under way, and what it changed so far.
type tx struct {
	r       *runner
	p       Plan
	backups []backup
	clones  []clone
	files   []written
	started bool // containers were touched: up, or the project's script
}

// Run carries out the plan. When it fails or is interrupted it has put
// back what it changed, and says so; the error tells what failed.
func Run(ctx context.Context, p Plan, out io.Writer) error {
	return run(&runner{ctx: ctx, out: out, exec: execute}, p)
}

func run(r *runner, p Plan) error {
	if DryRun {
		r.say("\033[33mdry run: changes shown, none made\033[0m")
	} else {
		unlock, err := lock()
		if err != nil {
			return err
		}
		defer unlock()
	}
	r.say(fmt.Sprintf("updating %s: %s", p.Name(), strings.Join(p.services(""), ", ")))
	t := &tx{r: r, p: p}
	for _, step := range []func() error{t.check, t.backup, t.pull, t.edit, t.renew, t.start} {
		if err := step(); err != nil {
			return t.fail(err)
		}
	}
	t.tidy()
	if DryRun {
		r.say("\033[33m· dry run: nothing was changed\033[0m")
		return nil
	}
	r.say("\033[32m✓ " + p.Name() + " is shipshape again\033[0m")
	r.say("\033[2mto go back later: " + t.wayBack() + "\033[0m")
	return nil
}

// check makes sure, before anything changes, that the git clones may be
// pulled: trusted, and without local changes.
func (t *tx) check() error {
	for _, dir := range t.p.Pulls {
		if _, err := scan.Trusted(t.r.ctx, dir); err != nil {
			return fmt.Errorf("%s: %w", dir, err)
		}
		status, err := scan.Git(t.r.ctx, dir, "status", "--porcelain")
		if err != nil {
			return err
		}
		if status != "" {
			return fmt.Errorf("%s has local changes; not pulling", dir)
		}
	}
	return nil
}

// backup keeps every planned service's running image under a tag of its
// own: two services on one image keep two backups.
func (t *tx) backup() error {
	stamp := time.Now().Format(stampTime)
	for _, it := range t.p.Items {
		running := registry.Parse(it.Running)
		b := backup{service: tagSafe(it.Service), tag: running.Name + ":bak-" + stamp + "-" + tagSafe(it.Service)}
		for _, ref := range []registry.Ref{running, registry.Parse(it.Image)} {
			if ref.Tag != "" && ref.Digest == "" && !contains(b.refs, ref.Name+":"+ref.Tag) {
				b.refs = append(b.refs, ref.Name+":"+ref.Tag)
			}
		}
		if err := t.r.run("", "docker", "tag", it.Container.ImageID, b.tag); err != nil {
			return err
		}
		t.backups = append(t.backups, b)
	}
	return nil
}

// pull brings each git clone to its upstream, noting where it was first.
func (t *tx) pull() error {
	for _, dir := range t.p.Pulls {
		before, err := scan.Git(t.r.ctx, dir, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		t.clones = append(t.clones, clone{dir: dir, before: before}) // put back even if the pull fails halfway
		if err := t.r.run(dir, "git", "-c", "core.hooksPath=/dev/null", "pull", "--ff-only", "--no-recurse-submodules"); err != nil {
			return err
		}
		if !DryRun {
			after, err := scan.Git(t.r.ctx, dir, "rev-parse", "HEAD")
			if err != nil {
				return err
			}
			t.clones[len(t.clones)-1].after = after
		}
	}
	return nil
}

// edit makes the file edits, each only on the line scurvy read.
func (t *tx) edit() error {
	if len(t.p.Edits) == 0 {
		return nil
	}
	files, err := edit(t.p.Edits, !DryRun)
	if !DryRun {
		t.files = files // what was written, even when a later file failed
	}
	for _, w := range files {
		t.r.diff(w)
	}
	return err
}

// renew pulls or builds the new images, or runs the project's script,
// which starts the containers too.
func (t *tx) renew() error {
	if t.p.Hook != "" {
		t.started = true
		return t.r.run(t.p.Project.Dir, "bash", scan.HookFile)
	}
	if svcs := t.p.services(scan.Pulled); len(svcs) > 0 {
		if err := t.r.compose(t.p.Project, append([]string{"pull"}, svcs...)...); err != nil {
			return err
		}
	}
	if svcs := t.p.services(scan.Built); len(svcs) > 0 {
		build := []string{"build", "--pull"}
		if t.p.NoCache {
			build = append(build, "--no-cache")
		}
		if err := t.r.compose(t.p.Project, append(build, svcs...)...); err != nil {
			return err
		}
	}
	return nil
}

// start recreates exactly the planned containers on the images just made,
// and waits until they run and their health checks pass.
func (t *tx) start() error {
	t.started = true
	if err := t.r.compose(t.p.Project, t.up()...); err != nil {
		return fmt.Errorf("%s did not come up healthy", strings.Join(t.p.services(""), ", "))
	}
	return nil
}

// up starts the planned services and nothing else, as they are now: no
// pull, no build, no dependency touched.
func (t *tx) up() []string {
	return append([]string{"up", "-d", "--no-deps", "--pull", "never", "--no-build", "--wait", "--wait-timeout", waitHealth}, t.p.services("")...)
}

// fail puts back what the update changed, even when it was interrupted:
// the way back has time of its own.
func (t *tx) fail(err error) error {
	t.r.say("\033[31m✗ " + err.Error() + "\033[0m")
	if DryRun {
		return err
	}
	t.r.say("putting back what changed")
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.r.ctx), 10*time.Minute)
	defer cancel()
	back := &runner{ctx: ctx, out: t.r.out, exec: t.r.exec}

	errs := []error{restore(t.files)}
	for _, w := range t.files {
		back.say("↺ " + w.path + " as it was")
	}
	for _, c := range t.clones {
		errs = append(errs, back.resetClone(c))
	}
	for _, b := range t.backups {
		for _, ref := range b.refs {
			errs = append(errs, back.run("", "docker", "tag", b.tag, ref))
		}
	}
	if t.started {
		errs = append(errs, back.compose(t.p.Project, t.up()...))
	}
	if rerr := errors.Join(errs...); rerr != nil {
		back.say("\033[31m✗ not everything could be put back: " + rerr.Error() + "\033[0m")
		return fmt.Errorf("%w; putting it back failed too: %v", err, rerr)
	}
	if t.p.Hook != "" {
		back.say("\033[33m↺ the images are back; what " + scan.HookFile + " changed itself stays\033[0m")
	} else {
		back.say("\033[33m↺ " + t.p.Name() + " is as before\033[0m")
	}
	return err
}

// resetClone puts a clone back where it was, but only if nobody changed it
// since scurvy pulled it.
func (r *runner) resetClone(c clone) error {
	head, err := scan.Git(r.ctx, c.dir, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	status, err := scan.Git(r.ctx, c.dir, "status", "--porcelain")
	if err != nil {
		return err
	}
	switch {
	case head == c.before:
		return nil // the pull changed nothing
	case head != c.after || status != "":
		return fmt.Errorf("%s changed since the pull; reset it by hand to %s", c.dir, c.before)
	}
	return r.run(c.dir, "git", "-c", "core.hooksPath=/dev/null", "reset", "--hard", c.before)
}

// tidy removes old backups and what the update left unused. A failure here
// is told; the update stands.
func (t *tx) tidy() {
	for _, b := range t.backups {
		t.r.prune(registry.Parse(b.tag).Name, b.service)
	}
	t.r.warn(t.r.run("", "docker", "image", "prune", "-f"))
	if len(t.p.services(scan.Built)) > 0 {
		t.r.warn(t.r.run("", "docker", "builder", "prune", "-f", "--max-used-space", buildCache))
	}
}

// wayBack is how to return to the old images later, by hand.
func (t *tx) wayBack() string {
	var cmds []string
	for _, w := range t.files {
		cmds = append(cmds, "restore "+quote(w.path))
	}
	for _, c := range t.clones {
		cmds = append(cmds, shell("git", "-C", c.dir, "reset", "--hard", c.before))
	}
	for _, b := range t.backups {
		for _, ref := range b.refs {
			cmds = append(cmds, shell("docker", "tag", b.tag, ref))
		}
	}
	cmds = append(cmds, shell(append([]string{"docker"}, t.p.Project.Args(t.up()...)...)...))
	return strings.Join(cmds, " && ")
}

// prune removes all but the newest backup tags of a service's image.
func (r *runner) prune(name, service string) {
	if DryRun {
		return
	}
	out, err := exec.CommandContext(r.ctx, "docker", "images", name, "--format", "{{.Tag}}").Output()
	if err != nil {
		return
	}
	var baks []string
	for _, t := range strings.Fields(string(out)) {
		if strings.HasPrefix(t, "bak-") && strings.HasSuffix(t, "-"+service) {
			baks = append(baks, t)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(baks)))
	for _, t := range baks[min(keep, len(baks)):] {
		r.warn(r.run("", "docker", "image", "rm", name+":"+t))
	}
}

// lock makes sure only one scurvy updates at a time, all users alike.
func lock() (func(), error) {
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("cannot take the update lock %s: %w", lockFile, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another scurvy is updating right now")
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// runner runs commands where the user can watch them.
type runner struct {
	ctx  context.Context
	out  io.Writer
	exec func(ctx context.Context, out io.Writer, dir, name string, args ...string) error
}

func (r *runner) say(s string) { fmt.Fprintln(r.out, s) }

func (r *runner) warn(err error) {
	if err != nil {
		r.say("\033[33m! " + err.Error() + " (the update stands)\033[0m")
	}
}

func (r *runner) diff(w written) {
	r.say("  " + w.path)
	before := strings.Split(string(w.before), "\n")
	after := strings.Split(string(w.after), "\n")
	for i := range before {
		if i < len(after) && before[i] != after[i] {
			r.say("  \033[31m- " + strings.TrimSpace(before[i]) + "\033[0m")
			r.say("  \033[32m+ " + strings.TrimSpace(after[i]) + "\033[0m")
		}
	}
}

func (r *runner) compose(p docker.Project, sub ...string) error {
	if err := r.run(p.Dir, "docker", p.Args(sub...)...); err != nil {
		return fmt.Errorf("docker compose %s failed", sub[0])
	}
	return nil
}

// run shows a command that changes something exactly as it runs, then
// runs it, unless DryRun.
func (r *runner) run(dir, name string, args ...string) error {
	line := shell(append([]string{name}, args...)...)
	if dir != "" {
		line = "cd " + quote(dir) + " && " + line
	}
	r.say("\033[2m$ " + line + "\033[0m")
	if DryRun {
		return nil
	}
	if err := r.exec(r.ctx, r.out, dir, name, args...); err != nil {
		if r.ctx.Err() != nil {
			return errors.New("interrupted")
		}
		return fmt.Errorf("%s failed: %w", shell(append([]string{name}, args[:min(2, len(args))]...)...), err)
	}
	return nil
}

// execute runs a command in a process group of its own, so that when the
// update is interrupted, a script's children stop with it.
func execute(ctx context.Context, out io.Writer, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	return cmd.Run()
}

var notInTag = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// tagSafe makes s fit into an image tag.
func tagSafe(s string) string {
	s = notInTag.ReplaceAllString(s, "-")
	return s[:min(len(s), 60)]
}

var plainArg = regexp.MustCompile(`^[A-Za-z0-9_./:@=+,-]+$`)

// quote makes one shell word of s.
func quote(s string) string {
	if plainArg.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func shell(args ...string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = quote(a)
	}
	return strings.Join(q, " ")
}
