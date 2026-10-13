package scan

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/chriopter/scurvy/internal/registry"
)

// A pinned tag is a plain version: 10.11.11, v2.3, 3.19. Tags with a word
// in them (3.12-slim, 8-bookworm, latest, release) move with their line,
// so a new digest is their update, not a new tag.
var pinnedTag = regexp.MustCompile(`^v?\d+(\.\d+)+$`)

func pinned(tag string) bool { return pinnedTag.MatchString(tag) }

func numbers(tag string) []int {
	parts := strings.Split(strings.TrimPrefix(tag, "v"), ".")
	n := make([]int, len(parts))
	for i, p := range parts {
		n[i], _ = strconv.Atoi(p)
	}
	return n
}

// less compares two versions of the same shape.
func less(a, b []int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// newerTag finds, among tags, the newest version of the same shape as
// cur (same number of parts, same v prefix) within cur's major version.
// A new major is a decision of its own, not an update.
func newerTag(tags []string, cur string) string {
	c := numbers(cur)
	v := strings.HasPrefix(cur, "v")
	var best []int
	newest := ""
	for _, t := range tags {
		if !pinned(t) || strings.HasPrefix(t, "v") != v {
			continue
		}
		n := numbers(t)
		if len(n) == len(c) && n[0] == c[0] && less(c, n) && (best == nil || less(best, n)) {
			best, newest = n, t
		}
	}
	return newest
}

// majorOf is the start every newer version of the same major shares:
// "10." for 10.11.11, "v2." for v2.3.
func majorOf(tag string) string {
	v := strings.HasPrefix(tag, "v")
	n := numbers(tag)
	if len(n) == 0 {
		return ""
	}
	p := strconv.Itoa(n[0]) + "."
	if v {
		p = "v" + p
	}
	return p
}

// Versions lists the versions an image could be pinned to: the tags of
// the same shape as its own (10.11.11: three numbers), newest first.
func Versions(ctx context.Context, image string) ([]string, error) {
	ref := registry.Parse(image)
	if !pinned(ref.Tag) {
		return nil, fmt.Errorf("%s is not pinned to a version", image)
	}
	tags, err := registry.New().Tags(ctx, ref, "")
	if err != nil {
		return nil, err
	}
	c, v := numbers(ref.Tag), strings.HasPrefix(ref.Tag, "v")
	var out []string
	for _, t := range tags {
		if pinned(t) && strings.HasPrefix(t, "v") == v && len(numbers(t)) == len(c) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(a, b int) bool { return less(numbers(out[b]), numbers(out[a])) })
	return out, nil
}
