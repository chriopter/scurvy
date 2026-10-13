package registry

import "testing"

func TestParse(t *testing.T) {
	for _, c := range []struct{ in, host, repo, tag, digest, name string }{
		{"python:3.12-slim", "registry-1.docker.io", "library/python", "3.12-slim", "", "python"},
		{"nginx", "registry-1.docker.io", "library/nginx", "latest", "", "nginx"},
		{"traefik/whoami:latest", "registry-1.docker.io", "traefik/whoami", "latest", "", "traefik/whoami"},
		{"docker.io/valkey/valkey:8-bookworm@sha256:abc", "registry-1.docker.io", "valkey/valkey", "8-bookworm", "sha256:abc", "docker.io/valkey/valkey"},
		{"quay.io/acme/api:latest", "quay.io", "acme/api", "latest", "", "quay.io/acme/api"},
		{"ghcr.io/acme/web:release", "ghcr.io", "acme/web", "release", "", "ghcr.io/acme/web"},
		{"localhost:5000/app", "localhost:5000", "app", "latest", "", "localhost:5000/app"},
		{"registry.example:5000/team/app:1.2", "registry.example:5000", "team/app", "1.2", "", "registry.example:5000/team/app"},
		{"alpine@sha256:def", "registry-1.docker.io", "library/alpine", "", "sha256:def", "alpine"},
	} {
		r := Parse(c.in)
		if r.Host != c.host || r.Repo != c.repo || r.Tag != c.tag || r.Digest != c.digest || r.Name != c.name {
			t.Errorf("Parse(%q) = %+v", c.in, r)
		}
	}
	if n, ok := Parse("python:3.12").Official(); !ok || n != "python" {
		t.Error("python is official")
	}
	if _, ok := Parse("ghcr.io/x/python").Official(); ok {
		t.Error("ghcr image is not official")
	}
}

func TestNextPage(t *testing.T) {
	got := nextPage(`</v2/library/alpine/tags/list?last=3.9&n=1000>; rel="next"`, "library/alpine")
	if got != "/tags/list?last=3.9&n=1000" {
		t.Errorf("nextPage = %q", got)
	}
	if nextPage("", "x") != "" {
		t.Error("no link, no page")
	}
}

func TestParseTime(t *testing.T) {
	if ParseTime("1970-01-01T00:00:00Z").IsZero() == false {
		t.Error("1970 is no date")
	}
	if ParseTime("2026-10-07T08:12:33.123456789Z").Year() != 2026 {
		t.Error("nanoseconds")
	}
}
