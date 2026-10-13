// Package fix is scurvy's core: it carries out the update of one compose
// project, and nothing else. It keeps the running images under backup
// tags, makes the exact file edits it was handed, pulls or builds, starts
// the containers again and waits until they are healthy. If any step
// fails, it puts everything back: files, images, containers. It shows
// every command it runs, and with DryRun it runs none.
package fix

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/chriopter/scurvy/internal/docker"
	"github.com/chriopter/scurvy/internal/registry"
	"github.com/chriopter/scurvy/internal/scan"
)

// DryRun shows every step and runs none.
var DryRun bool

const (
	keep       = 2      // backup tags kept per image
	buildCache = "10GB" // build cache left after a build
	waitHealth = "180"  // seconds up --wait gives the containers
	stampTime  = "20060102-150405"
)

// Plan is the update of one compose project.
type Plan struct {
	Project docker.Project
	Items   []scan.Item // one per service
	Hook    string      // the project's own update script: it does pull, build and up
	Edits   []scan.Edit
	Pulls   []string // build contexts that are git clones, pulled first
}

// Plans groups what to update by compose project. Containers it cannot
// update come back as refused, with the reason.
func Plans(items []scan.Item) (plans []Plan, refused []string) {
	byKey := map[string]*Plan{}
	var order []string
	seen := map[string]bool{}
	for _, it := range items {
		if !it.Updatable() {
			refused = append(refused, it.Name()+": "+whyNot(it))
			continue
		}
		key := it.Project.Key()
		if seen[key+"\x00"+it.Service] {
			continue // another replica of a service already in the plan
		}
		seen[key+"\x00"+it.Service] = true
		p := byKey[key]
		if p == nil {
			p = &Plan{Project: it.Project, Hook: it.Hook}
			byKey[key] = p
			order = append(order, key)
		}
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
	for _, k := range order {
		p := byKey[k]
		if p.Hook != "" { // the script is in charge: no edits or pulls of ours
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

// backup is a running image kept under a tag, and the reference it ran as.
type backup struct{ tag, ref, service string }

// Run carries out the plan. On failure it has put everything back, and
// says so; the error tells what failed.
func Run(ctx context.Context, p Plan, out io.Writer) error {
	r := &runner{ctx: ctx, out: out}
	if DryRun {
		r.say("\033[33mdry run: every step shown, none run\033[0m")
	} else {
		unlock, err := lock()
		if err != nil {
			return err
		}
		defer unlock()
	}
	r.say(fmt.Sprintf("updating %s: %s", p.Name(), strings.Join(p.services(""), ", ")))

	// Keep what runs, so it can come back.
	stamp := time.Now().Format(stampTime)
	var backups []backup
	for _, it := range p.Items {
		ref := registry.Parse(it.Running)
		// The service is in the tag: two services on one image keep two backups.
		b := backup{tag: ref.Name + ":bak-" + stamp + "-" + tagSafe(it.Service), ref: ref.Name + ":" + ref.Tag, service: tagSafe(it.Service)}
		if err := r.run("", "docker", "tag", it.Container.ImageID, b.tag); err != nil {
			return err
		}
		backups = append(backups, b)
	}

	var files []written
	heads := map[string]string{}
	failed := func(err error) error {
		r.say("\033[31m✗ " + err.Error() + "\033[0m")
		r.say("putting everything back")
		if rerr := r.rollBack(p, files, heads, backups); rerr != nil {
			return fmt.Errorf("%w; putting it back failed too: %v", err, rerr)
		}
		r.say("\033[33m↺ " + p.Name() + " runs as before\033[0m")
		return err
	}

	if len(p.Edits) > 0 {
		var err error
		if files, err = edit(p.Edits, !DryRun); err != nil {
			return failed(err)
		}
		for _, w := range files {
			r.diff(w)
		}
		if DryRun {
			files = nil // nothing was written, nothing to put back
		}
	}
	for _, dir := range p.Pulls {
		head, err := r.gitPull(dir)
		if err != nil {
			return failed(err)
		}
		heads[dir] = head
	}

	if p.Hook != "" {
		if err := r.run(p.Project.Dir, "bash", scan.HookFile); err != nil {
			return failed(err)
		}
	} else {
		if svcs := p.services(scan.Pulled); len(svcs) > 0 {
			if err := r.compose(p.Project, append([]string{"pull"}, svcs...)...); err != nil {
				return failed(err)
			}
		}
		if svcs := p.services(scan.Built); len(svcs) > 0 {
			if err := r.compose(p.Project, append([]string{"build", "--pull"}, svcs...)...); err != nil {
				return failed(err)
			}
		}
	}
	// Up, and wait until the containers run and their health checks pass.
	// After a hook this only waits.
	up := []string{"up", "-d", "--wait", "--wait-timeout", waitHealth}
	if p.Hook != "" {
		up = []string{"up", "-d", "--pull", "never", "--no-build", "--wait", "--wait-timeout", waitHealth}
	}
	if err := r.compose(p.Project, append(up, p.services("")...)...); err != nil {
		return failed(fmt.Errorf("%s did not come up healthy", strings.Join(p.services(""), ", ")))
	}

	// Tidy up. A failure here is told; the update stands.
	for _, b := range backups {
		r.prune(registry.Parse(b.tag).Name, b.service)
	}
	r.warn(r.run("", "docker", "image", "prune", "-f"))
	if len(p.services(scan.Built)) > 0 {
		r.warn(r.run("", "docker", "builder", "prune", "-f", "--max-used-space", buildCache))
	}
	if DryRun {
		r.say("\033[33m· dry run: nothing was changed\033[0m")
		return nil
	}
	r.say("\033[32m✓ " + p.Name() + " is shipshape again\033[0m")
	r.say("\033[2mto go back later: " + rollbackLine(p, backups, files) + "\033[0m")
	return nil
}

// rollBack puts back what Run changed: files, git clones, images, and the
// containers on them.
func (r *runner) rollBack(p Plan, files []written, heads map[string]string, backups []backup) error {
	if DryRun {
		return nil
	}
	errs := []error{restore(files)}
	for _, w := range files {
		r.say("↺ " + w.path + " as it was")
	}
	for dir, head := range heads {
		errs = append(errs, r.run(dir, "git", "reset", "--hard", head))
	}
	for _, b := range backups {
		errs = append(errs, r.run("", "docker", "tag", b.tag, b.ref))
	}
	up := []string{"up", "-d", "--pull", "never", "--no-build", "--wait", "--wait-timeout", waitHealth}
	errs = append(errs, r.compose(p.Project, append(up, p.services("")...)...))
	return errors.Join(errs...)
}

// gitPull brings a clean clone to its upstream and returns where it was.
func (r *runner) gitPull(dir string) (string, error) {
	if DryRun {
		return "", r.run(dir, "git", "pull", "--ff-only")
	}
	status, err := exec.CommandContext(r.ctx, "git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return "", fmt.Errorf("git status in %s: %w", dir, err)
	}
	if len(strings.TrimSpace(string(status))) > 0 {
		return "", fmt.Errorf("%s has local changes; not pulling", dir)
	}
	head, err := exec.CommandContext(r.ctx, "git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(head)), r.run(dir, "git", "pull", "--ff-only")
}

// prune removes all but the newest backup tags of a service's image.
func (r *runner) prune(name, service string) {
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

func rollbackLine(p Plan, backups []backup, files []written) string {
	var cmds []string
	if len(files) > 0 {
		var names []string
		for _, w := range files {
			names = append(names, w.path)
		}
		cmds = append(cmds, "restore "+strings.Join(names, ", ")+" by hand")
	}
	for _, b := range backups {
		cmds = append(cmds, shell("docker", "tag", b.tag, b.ref))
	}
	up := append([]string{"up", "-d", "--pull", "never", "--no-build"}, p.services("")...)
	cmds = append(cmds, shell(append([]string{"docker"}, p.Project.Args(up...)...)...))
	return "cd " + quote(p.Project.Dir) + " && " + strings.Join(cmds, " && ")
}

// lock makes sure only one scurvy updates at a time.
func lock() (func(), error) {
	f, err := os.OpenFile("/run/lock/scurvy.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil { // not root: a lock of this user's own
		f, err = os.OpenFile(filepath.Join(os.TempDir(), fmt.Sprintf("scurvy-%d.lock", os.Getuid())), os.O_CREATE|os.O_RDWR, 0o600)
	}
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another scurvy is updating right now")
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// runner runs commands where the user can watch them.
type runner struct {
	ctx context.Context
	out io.Writer
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
	return r.run(p.Dir, "docker", p.Args(sub...)...)
}

// run shows the command exactly as it runs, then runs it, unless DryRun.
func (r *runner) run(dir, name string, args ...string) error {
	line := shell(append([]string{name}, args...)...)
	if dir != "" {
		line = "cd " + quote(dir) + " && " + line
	}
	r.say("\033[2m$ " + line + "\033[0m")
	if DryRun {
		return nil
	}
	cmd := exec.CommandContext(r.ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = r.out, r.out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", shell(append([]string{name}, args[:min(2, len(args))]...)...), err)
	}
	return nil
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
