package scan

import (
	"fmt"
	"os"
	"strings"
	"syscall"
)

// Apply writes the new version into the file, in every place that names
// the old one as a whole reference. It returns the changed lines, as
// before and after, for showing.
func (b Bump) Apply() (before, after []string, err error) {
	info, err := os.Stat(b.File)
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(b.File)
	if err != nil {
		return nil, nil, err
	}
	lines := strings.SplitAfter(string(data), "\n")
	for i, l := range lines {
		if !containsRef(l, b.Old) {
			continue
		}
		n := replaceRef(l, b.Old, b.New)
		before = append(before, strings.TrimRight(l, "\n"))
		after = append(after, strings.TrimRight(n, "\n"))
		lines[i] = n
	}
	if len(before) == 0 {
		return nil, nil, fmt.Errorf("%s no longer names %s", b.File, b.Old)
	}
	tmp := b.File + ".scurvy"
	if err := os.WriteFile(tmp, []byte(strings.Join(lines, "")), info.Mode().Perm()); err != nil {
		return nil, nil, err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok { // root writes it; its owner keeps it
		_ = os.Chown(tmp, int(st.Uid), int(st.Gid))
	}
	return before, after, os.Rename(tmp, b.File)
}

// Undo puts the old version back.
func (b Bump) Undo() error {
	_, _, err := Bump{File: b.File, Old: b.New, New: b.Old}.Apply()
	return err
}

func replaceRef(line, old, new string) string {
	var out strings.Builder
	for {
		i := strings.Index(line, old)
		if i < 0 {
			out.WriteString(line)
			return out.String()
		}
		end := i + len(old)
		whole := (end == len(line) || !isRefChar(line[end])) &&
			(i == 0 || !isRefChar(line[i-1]) || line[i-1] == '/')
		out.WriteString(line[:i])
		if whole {
			out.WriteString(new)
		} else {
			out.WriteString(old)
		}
		line = line[end:]
	}
}
