package scan

import (
	"fmt"
	"os"
	"strings"
)

// Edit changes one line of one file. Old is the whole line as scurvy read
// it; the edit is only made while the line still says exactly that.
type Edit struct {
	File     string
	Line     int // counted from 1
	Old, New string
}

// imageEdit finds the one `image:` line among files whose value is old,
// and gives it new. More than one such line, or none, is no edit: which
// one compose uses is then not certain.
func imageEdit(files []string, old, new string) (Edit, error) {
	var found []Edit
	for _, f := range files {
		lines, err := readLines(f)
		if err != nil {
			return Edit{}, err
		}
		for i, l := range lines {
			key, val, ok := strings.Cut(strings.TrimSpace(l), ":")
			if !ok || strings.TrimSpace(key) != "image" {
				continue
			}
			val, _, _ = strings.Cut(val, " #") // a comment after the value
			if strings.Trim(strings.TrimSpace(val), `"'`) == old {
				found = append(found, Edit{f, i + 1, l, strings.Replace(l, old, new, 1)})
			}
		}
	}
	switch len(found) {
	case 0:
		return Edit{}, fmt.Errorf("no image: line says %s", old)
	case 1:
		return found[0], nil
	}
	return Edit{}, fmt.Errorf("%d image: lines say %s", len(found), old)
}

// fromEdits finds the FROM lines of a Dockerfile that start from old, or
// from a variant of it (old-builder), and moves them to new. A variant
// moves only if its new tag exists, else nothing does: the stages of a
// Dockerfile stay on one version.
func fromEdits(file, old, new string, exists func(ref string) bool) ([]Edit, error) {
	lines, err := readLines(file)
	if err != nil {
		return nil, err
	}
	var edits []Edit
	for i, l := range lines {
		f := strings.Fields(l)
		if len(f) < 2 || !strings.EqualFold(f[0], "FROM") {
			continue
		}
		k := 1
		for k < len(f) && strings.HasPrefix(f[k], "--") {
			k++
		}
		if k == len(f) {
			continue
		}
		ref := f[k]
		var to string
		switch {
		case ref == old:
			to = new
		case len(ref) > len(old)+1 && strings.HasPrefix(ref, old+"-") && isLetter(ref[len(old)+1]):
			to = new + ref[len(old):]
			if !exists(to) {
				return nil, fmt.Errorf("%s does not exist", to)
			}
		default:
			continue
		}
		edits = append(edits, Edit{file, i + 1, l, strings.Replace(l, ref, to, 1)})
	}
	if len(edits) == 0 {
		return nil, fmt.Errorf("no FROM line says %s", old)
	}
	return edits, nil
}

func readLines(file string) ([]string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return strings.Split(string(data), "\n"), nil
}

func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }
