// Package fix updates one container the way compose would by hand: keep
// the old image under a backup tag, pull or build the new one, recreate
// the container. It shows every step and the docker output as it comes.
package fix

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chriopter/scurvy/internal/docker"
	"github.com/chriopter/scurvy/internal/registry"
	"github.com/chriopter/scurvy/internal/scan"
)

// keep is how many backup tags stay per image. Scripts that tag
// bak-<time> themselves can use the same names.
const keep = 2

// Run updates the container of it. It returns the command that brings the
// old image back, for when the new one misbehaves.
func Run(ctx context.Context, it scan.Item, out io.Writer) (rollback string, err error) {
	if !it.Updatable() {
		return "", fmt.Errorf("%s was not started by compose; update it by hand", it.Name())
	}
	p := it.Project
	ref := registry.Parse(it.Image)
	if it.Kind == scan.Pulled && ref.Digest != "" && ref.Tag == "" {
		return "", fmt.Errorf("%s is pinned by digest; change the digest in the compose file", it.Name())
	}
	fmt.Fprintf(out, "🍋 prescribing limes to %s\n\n", it.Name())

	if it.Bump != nil {
		before, after, err := it.Bump.Apply()
		if err != nil {
			return "", err
		}
		fmt.Fprintf(out, "  %s\n", it.Bump.File)
		for i := range before {
			fmt.Fprintf(out, "  - %s\n  + %s\n", strings.TrimSpace(before[i]), strings.TrimSpace(after[i]))
		}
		fmt.Fprintln(out)
	}
	undo := func(err error) (string, error) {
		if it.Bump != nil {
			if uerr := it.Bump.Undo(); uerr == nil {
				fmt.Fprintf(out, "\n  put %s back in %s\n", it.Bump.Old, it.Bump.File)
			}
		}
		return "", err
	}

	backup := fmt.Sprintf("%s:bak-%s", ref.Name, time.Now().Format("20060102-150405"))
	if err := step(ctx, out, "", nil, "tag", it.Container.ImageID, backup); err != nil {
		return undo(err)
	}

	compose := func(sub ...string) error {
		return step(ctx, out, p.Dir, append([]string{"compose"}, sub...), p.Args(sub...)...)
	}
	if it.Kind == scan.Built {
		err = compose("build", "--pull", it.Service)
	} else {
		err = compose("pull", it.Service)
	}
	if err != nil {
		return undo(err)
	}
	if err := compose("up", "-d", it.Service); err != nil {
		return undo(err)
	}
	prune(ctx, ref.Name)

	tag := ref.Tag
	if tag == "" {
		tag = "latest"
	}
	rollback = fmt.Sprintf("docker tag %s %s:%s && docker %s", backup, ref.Name, tag,
		strings.Join(quote(p.Args("up", "-d", it.Service)), " "))
	if plain(p) {
		rollback = fmt.Sprintf("cd %s && docker tag %s %s:%s && docker compose up -d %s",
			quote([]string{p.Dir})[0], backup, ref.Name, tag, it.Service)
	}
	if it.Bump != nil {
		rollback = fmt.Sprintf("put %s back in %s, then %s", it.Bump.Old, it.Bump.File, rollback)
	}
	return rollback, nil
}

// step runs one docker command where the user can watch it, showing it
// the short way when show is set (compose in the project's directory).
func step(ctx context.Context, out io.Writer, dir string, show []string, args ...string) error {
	if show == nil {
		show = args
	}
	fmt.Fprintf(out, "\033[2m$ docker %s\033[0m\n", strings.Join(quote(show), " "))
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker %s failed: %w", args[0], err)
	}
	return nil
}

// plain reports whether `docker compose` in the project's directory finds
// the project by itself: default name, default file.
func plain(p docker.Project) bool {
	if len(p.Files) != 1 || filepath.Dir(p.Files[0]) != p.Dir || filepath.Base(p.Dir) != p.Name {
		return false
	}
	switch filepath.Base(p.Files[0]) {
	case "compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml":
		return true
	}
	return false
}

// prune removes all but the newest backup tags of an image.
func prune(ctx context.Context, name string) {
	out, err := exec.CommandContext(ctx, "docker", "images", name, "--format", "{{.Tag}}").Output()
	if err != nil {
		return
	}
	var baks []string
	for _, t := range strings.Fields(string(out)) {
		if strings.HasPrefix(t, "bak-") {
			baks = append(baks, t)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(baks)))
	for _, t := range baks[min(keep, len(baks)):] {
		_ = exec.CommandContext(ctx, "docker", "rmi", name+":"+t).Run()
	}
}

func quote(args []string) []string {
	q := make([]string, len(args))
	for i, a := range args {
		if a == "" || strings.ContainsAny(a, " \t'\"$") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		q[i] = a
	}
	return q
}
