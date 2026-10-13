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

// less compares two versions; missing parts count as 0 (12.2 is 12.2.0).
func less(a, b []int) bool {
	for i := 0; i < max(len(a), len(b)); i++ {
		x, y := 0, 0
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// versions are the plain version tags among tags, with cur's v prefix.
func versions(tags []string, cur string) []string {
	v := strings.HasPrefix(cur, "v")
	var out []string
	for _, t := range tags {
		if pinned(t) && strings.HasPrefix(t, "v") == v {
			out = append(out, t)
		}
	}
	return out
}

// newerTag finds the newest version newer than cur within cur's major
// version, and the newest of a later major: that one is a decision of its
// own, offered but never taken by itself.
func newerTag(tags []string, cur string) (minor, major string) {
	c := numbers(cur)
	for _, t := range versions(tags, cur) {
		n := numbers(t)
		switch {
		case !less(c, n):
		case n[0] == c[0] && (minor == "" || less(numbers(minor), n)):
			minor = t
		case n[0] > c[0] && (major == "" || less(numbers(major), n)):
			major = t
		}
	}
	return minor, major
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

// Versions lists the versions an image could be pinned to, newest first:
// those of its own major, and the newest ones of every other.
func Versions(ctx context.Context, image string) ([]string, error) {
	ref := registry.Parse(image)
	if !pinned(ref.Tag) {
		return nil, fmt.Errorf("%s is not pinned to a version", image)
	}
	reg := registry.New()
	same, err := reg.Tags(ctx, ref, majorOf(ref.Tag))
	if err != nil {
		return nil, err
	}
	recent, err := reg.RecentTags(ctx, ref)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range versions(append(same, recent...), ref.Tag) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Slice(out, func(a, b int) bool { return less(numbers(out[b]), numbers(out[a])) })
	return out, nil
}
