package scan

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chriopter/scurvy/internal/registry"
)

// endoflife.date products, by official image name.
var eolProducts = map[string]string{
	"alpine":   "alpine-linux",
	"debian":   "debian",
	"ubuntu":   "ubuntu",
	"python":   "python",
	"node":     "nodejs",
	"golang":   "go",
	"postgres": "postgresql",
	"redis":    "redis",
	"mariadb":  "mariadb",
	"mysql":    "mysql",
	"php":      "php",
	"ruby":     "ruby",
	"nginx":    "nginx",
}

var (
	debianNames = []string{"trixie", "bookworm", "bullseye", "buster", "stretch"}
	ubuntuNames = []string{"noble", "jammy", "focal", "bionic", "plucky", "questing", "oracular"}
	alpineIn    = regexp.MustCompile(`alpine(\d+\.\d+)`)
	leading     = regexp.MustCompile(`^v?(\d+)(?:\.(\d+))?`)
)

// release names a product line an image is built on: python 3.12,
// debian bookworm, alpine 3.19.
type release struct {
	product string // endoflife.date product
	cycle   string // version or codename, as endoflife.date lists it
	label   string // for people: "alpine 3.19"
}

// releasesOf finds the lines an image is built on: the official image
// itself (python:3.12-slim → python 3.12) and the distribution named in its
// tag (…-bookworm → debian bookworm, …-alpine3.19 → alpine 3.19).
func releasesOf(r registry.Ref) []release {
	var out []release
	tag := strings.ToLower(r.Tag)
	if name, ok := r.Official(); ok {
		if product, ok := eolProducts[name]; ok {
			if m := leading.FindStringSubmatch(tag); m != nil {
				cycle := m[1]
				if m[2] != "" && name != "postgres" && name != "debian" && name != "mariadb" {
					cycle += "." + m[2]
				}
				out = append(out, release{product, cycle, name + " " + cycle})
			}
		}
	}
	for _, word := range strings.FieldsFunc(tag, func(r rune) bool { return r == '-' || r == '_' }) {
		for _, d := range debianNames {
			if word == d {
				out = append(out, release{"debian", d, "debian " + d})
			}
		}
		for _, u := range ubuntuNames {
			if word == u {
				out = append(out, release{"ubuntu", u, "ubuntu " + u})
			}
		}
		if m := alpineIn.FindStringSubmatch(word); m != nil && !strings.HasPrefix(tag, m[1]) {
			out = append(out, release{"alpine-linux", m[1], "alpine " + m[1]})
		}
	}
	// debian:bookworm and ubuntu:noble name themselves.
	if name, ok := r.Official(); ok && (name == "debian" || name == "ubuntu") && len(out) > 1 {
		out = out[1:]
	}
	return out
}

type cycle struct {
	Cycle    string `json:"cycle"`
	Codename string `json:"codename"`
	EOL      any    `json:"eol"` // a date, or true/false
}

// eolDates looks up end-of-life dates on endoflife.date, once per product.
type eolDates struct {
	http *http.Client
	mu   sync.Mutex
	data map[string][]cycle
}

func newEOLDates() *eolDates {
	return &eolDates{http: &http.Client{Timeout: 15 * time.Second}, data: map[string][]cycle{}}
}

func (e *eolDates) cycles(ctx context.Context, product string) ([]cycle, error) {
	e.mu.Lock()
	defer e.mu.Unlock() // one product at a time; the answers are small
	if c, ok := e.data[product]; ok {
		return c, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://endoflife.date/api/"+product+".json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("endoflife.date: %s", resp.Status)
	}
	var c []cycle
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&c); err != nil {
		return nil, err
	}
	e.data[product] = c
	return c, nil
}

// End is when a release stops getting fixes; zero when unknown or not set.
func (e *eolDates) End(ctx context.Context, rel release) time.Time {
	cs, err := e.cycles(ctx, rel.product)
	if err != nil {
		return time.Time{}
	}
	for _, c := range cs {
		if c.Cycle != rel.cycle && !strings.EqualFold(firstWord(c.Codename), rel.cycle) {
			continue
		}
		switch v := c.EOL.(type) {
		case string:
			t, err := time.Parse("2006-01-02", v)
			if err == nil {
				return t
			}
		case bool:
			if v {
				return time.Unix(0, 0) // over, date unknown
			}
		}
		return time.Time{}
	}
	return time.Time{}
}

// firstWord: Ubuntu's codenames are "Noble Numbat"; tags say "noble".
func firstWord(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}
