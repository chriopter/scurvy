package scan

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Git runs as root here, and a repository's config can make git run
// commands (core.sshCommand, core.fsmonitor). So scurvy uses git only in
// repositories whose .git and config belong to the user it runs as and
// nobody else may write.

// errUntrusted says why a repository was left alone.
var errUntrusted = errors.New("not owned by this user alone; git left alone")

// trusted reports whether git may run in the repository around dir, and
// its top directory.
func trusted(ctx context.Context, dir string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--show-toplevel", "--git-dir").Output()
	if err != nil {
		return "", nil // no repository, or one git itself does not trust
	}
	f := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(f) != 2 {
		return "", nil
	}
	top, gitDir := f[0], f[1]
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(dir, gitDir)
	}
	for _, p := range []string{gitDir, filepath.Join(gitDir, "config")} {
		st, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok || int(sys.Uid) != os.Geteuid() || st.Mode().Perm()&0o022 != 0 || st.Mode()&os.ModeSymlink != 0 {
			return "", errUntrusted
		}
	}
	return top, nil
}

// git runs git in dir and returns its output, trimmed. It never asks for
// a password.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// lastCommit is when dir last changed in its repository; zero outside one.
func lastCommit(ctx context.Context, dir string) time.Time {
	out, err := git(ctx, dir, "log", "-1", "--format=%ct", "--", ".")
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
	up, err := git(ctx, dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}") // origin/main
	remote, branch, ok := strings.Cut(up, "/")
	if err != nil || !ok {
		return false, false, nil
	}
	head, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return true, false, err
	}
	out, err := git(ctx, dir, "ls-remote", remote, "refs/heads/"+branch)
	if err != nil {
		return true, false, errors.New("could not ask " + remote)
	}
	f := strings.Fields(out)
	return true, len(f) > 0 && f[0] != head, nil
}
