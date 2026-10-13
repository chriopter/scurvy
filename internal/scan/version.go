package scan

import (
	"regexp"
	"strconv"
	"strings"
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

// newerTags finds, among tags, the newest version of the same shape as cur
// (same number of parts, same v prefix) within cur's major version, and the
// newest beyond it.
func newerTags(tags []string, cur string) (minor, major string) {
	c := numbers(cur)
	v := strings.HasPrefix(cur, "v")
	var bestMinor, bestMajor []int
	for _, t := range tags {
		if !pinned(t) || strings.HasPrefix(t, "v") != v {
			continue
		}
		n := numbers(t)
		if len(n) != len(c) || !less(c, n) {
			continue
		}
		if n[0] == c[0] {
			if bestMinor == nil || less(bestMinor, n) {
				bestMinor, minor = n, t
			}
		} else if bestMajor == nil || less(bestMajor, n) {
			bestMajor, major = n, t
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
