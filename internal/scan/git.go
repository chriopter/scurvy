package scan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// scurvy runs as root, and a repository's configuration can make git run
// commands. So git runs only in repositories whose .git and every config
// file git reads (includes too) belong to the user scurvy runs as and
// nobody else may change, never asks for a password, and never runs hooks.

var errUntrusted = errors.New("its git files are not owned by this user alone; git left alone")

// Trusted reports whether git may run in the repository around dir, and
// its top directory; "" when dir is in no repository.
func Trusted(ctx context.Context, dir string) (string, error) {
	out, err := Git(ctx, dir, "rev-parse", "--show-toplevel", "--absolute-git-dir")
	if err != nil {
		return "", nil // no repository, or one git itself does not trust
	}
	f := strings.Split(out, "\n")
	if len(f) != 2 {
		return "", nil
	}
	top, gitDir := f[0], f[1]
	paths := []string{gitDir, filepath.Join(gitDir, "config")}
	origins, err := Git(ctx, dir, "config", "--list", "--show-origin", "--includes")
	if err != nil {
		return "", fmt.Errorf("git config: %w", err)
	}
	for _, l := range strings.Split(origins, "\n") {
		if p, ok := strings.CutPrefix(l, "file:"); ok {
			p, _, _ = strings.Cut(p, "\t")
			paths = append(paths, p)
		}
	}
	for _, p := range paths {
		if !ownedAlone(p) {
			return "", errUntrusted
		}
	}
	return top, nil
}

// ownedAlone: p and the directory it is in belong to this user, are no
// symlink, and nobody else may write them.
func ownedAlone(p string) bool {
	for _, x := range []string{p, filepath.Dir(p)} {
		st, err := os.Lstat(x)
		if err != nil {
			return false
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok || int(sys.Uid) != os.Geteuid() || st.Mode()&os.ModeSymlink != 0 ||
			st.Mode().Perm()&0o022 != 0 && st.Mode()&os.ModeSticky == 0 {
			return false
		}
	}
	return true
}

// Git runs git in dir, without hooks, prompts or a terminal, for at most
// five minutes, and returns its output trimmed.
func Git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	out, err := cmd.Output()
	if ee := (*exec.ExitError)(nil); errors.As(err, &ee) && len(ee.Stderr) > 0 {
		err = fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(ee.Stderr)))
	}
	return strings.TrimSpace(string(out)), err
}

// lastCommit is when dir last changed in its repository; zero outside one.
func lastCommit(ctx context.Context, dir string) time.Time {
	out, err := Git(ctx, dir, "log", "-1", "--format=%ct", "--", ".")
	if err != nil {
		return time.Time{}
	}
	sec, err := strconv.ParseInt(out, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

// upstream reports whether dir is a git clone of its own (not a folder of
// a bigger repository) with an upstream branch, and whether that branch
// now points elsewhere than HEAD. It asks the remote with ls-remote and
// fetches nothing.
func upstream(ctx context.Context, dir, top string) (clone, moved bool, err error) {
	if filepath.Clean(top) != filepath.Clean(dir) {
		return false, false, nil
	}
	up, err := Git(ctx, dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}") // origin/main
	remote, branch, ok := strings.Cut(up, "/")
	if err != nil || !ok {
		return false, false, nil
	}
	head, err := Git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return true, false, err
	}
	out, err := Git(ctx, dir, "ls-remote", remote, "refs/heads/"+branch)
	if err != nil {
		return true, false, errors.New("could not ask " + remote)
	}
	f := strings.Fields(out)
	return true, len(f) > 0 && f[0] != head, nil
}
