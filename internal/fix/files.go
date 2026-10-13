package fix

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/chriopter/scurvy/internal/scan"
)

// written is a file as it was before scurvy changed it, and as scurvy left
// it, so it can be put back exactly, and only if nobody changed it since.
type written struct {
	path          string
	before, after []byte
}

// edit makes the edits, file by file, and returns what to put back. Every
// line must still say what scurvy read; a symlink or anything but a plain
// file is left alone. Nothing is written until all edits check out.
func edit(edits []scan.Edit, write bool) ([]written, error) {
	byFile := map[string][]scan.Edit{}
	var order []string
	for _, e := range edits {
		if _, ok := byFile[e.File]; !ok {
			order = append(order, e.File)
		}
		byFile[e.File] = append(byFile[e.File], e)
	}
	var plan []written
	for _, path := range order {
		st, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !st.Mode().IsRegular() {
			return nil, fmt.Errorf("%s is not a plain file; not changing it", path)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(string(before), "\n")
		for _, e := range byFile[path] {
			if e.Line < 1 || e.Line > len(lines) || lines[e.Line-1] != e.Old {
				return nil, fmt.Errorf("%s changed since it was read (line %d)", path, e.Line)
			}
			lines[e.Line-1] = e.New
		}
		plan = append(plan, written{path, before, []byte(strings.Join(lines, "\n"))})
	}
	if !write {
		return plan, nil
	}
	for i, w := range plan {
		if err := replace(w.path, w.after); err != nil {
			restore(plan[:i])
			return nil, err
		}
	}
	return plan, nil
}

// restore puts files back as they were, where they are still as scurvy
// left them. It returns what it could not put back.
func restore(files []written) error {
	var errs []error
	for i := len(files) - 1; i >= 0; i-- {
		w := files[i]
		now, err := os.ReadFile(w.path)
		switch {
		case err != nil:
			errs = append(errs, err)
		case !bytes.Equal(now, w.after):
			errs = append(errs, fmt.Errorf("%s changed meanwhile; put it back by hand", w.path))
		default:
			if err := replace(w.path, w.before); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// replace writes data to path by renaming a new file over it: never half
// written. The new file is made beside it with an unguessable name, and
// gets the old one's mode and owner before it takes its place.
func replace(path string, data []byte) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("%s is not a plain file; not changing it", path)
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".scurvy-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(st.Mode().Perm()); err != nil {
		f.Close()
		return err
	}
	if sys, has := st.Sys().(*syscall.Stat_t); has {
		if err := f.Chown(int(sys.Uid), int(sys.Gid)); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
}
