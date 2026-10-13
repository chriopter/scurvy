package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chriopter/scurvy/internal/docker"
	"github.com/chriopter/scurvy/internal/registry"
)

func TestNewerTags(t *testing.T) {
	tags := []string{"10.11.11", "10.11.12", "10.12.0", "11.0.0", "10.12.0-rc1", "latest", "2025111705", "v10.13.0", "10.11"}
	minor, major := newerTags(tags, "10.11.11")
	if minor != "10.12.0" || major != "11.0.0" {
		t.Errorf("got %q %q", minor, major)
	}
	if m, _ := newerTags(tags, "10.12.0"); m != "" {
		t.Errorf("nothing newer in 10.x, got %q", m)
	}
	if m, _ := newerTags([]string{"3.19", "3.24", "3.9", "3.24.1"}, "3.19"); m != "3.24" {
		t.Errorf("alpine: got %q", m)
	}
}

func TestPinned(t *testing.T) {
	for tag, want := range map[string]bool{
		"10.11.11": true, "v2.3": true, "3.19": true,
		"latest": false, "3.12-slim": false, "8-bookworm": false, "8": false, "release": false,
	} {
		if pinned(tag) != want {
			t.Errorf("pinned(%q) != %v", tag, want)
		}
	}
}

func TestFinalBase(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, "Dockerfile")
		os.WriteFile(p, []byte(s), 0o644)
		return p
	}
	b, ok := finalBase(write("FROM rust:latest AS tunnel\nRUN cargo build \\\n  --release\nFROM python:3.12-slim\nCOPY --from=tunnel /x /x\n"))
	if !ok || b.ref != "python:3.12-slim" {
		t.Errorf("multi-stage: %+v", b)
	}
	b, _ = finalBase(write("ARG PY=3.13\nFROM --platform=linux/amd64 python:${PY}-alpine AS base\nFROM base AS final\n"))
	if b.ref != "python:3.13-alpine" || b.raw != "python:${PY}-alpine" {
		t.Errorf("args and stages: %+v", b)
	}
	if _, ok := finalBase(write("FROM golang AS b\nFROM scratch\n")); ok {
		t.Error("scratch has no base")
	}
}

func TestReleasesOf(t *testing.T) {
	labels := func(ref string) []string {
		var out []string
		for _, r := range releasesOf(registry.Parse(ref)) {
			out = append(out, r.product+" "+r.cycle)
		}
		return out
	}
	for ref, want := range map[string][]string{
		"alpine:3.19":               {"alpine-linux 3.19"},
		"python:3.12-slim-bookworm": {"python 3.12", "debian bookworm"},
		"debian:bookworm-slim":      {"debian bookworm"},
		"valkey/valkey:8-bookworm":  {"debian bookworm"},
		"postgres:16.4-alpine3.20":  {"postgresql 16", "alpine-linux 3.20"},
		"node:22-alpine":            {"nodejs 22"},
		"ghcr.io/x/postgres:14-abc": nil,
		"quay.io/acme/api":          nil,
	} {
		got := labels(ref)
		if len(got) != len(want) {
			t.Errorf("%s: got %v want %v", ref, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%s: got %v want %v", ref, got, want)
			}
		}
	}
}

func TestBump(t *testing.T) {
	p := filepath.Join(t.TempDir(), "docker-compose.yml")
	os.WriteFile(p, []byte("services:\n  a:\n    image: acme/app:10.11.11\n  b:\n    image: acme/app:10.11.111\n  c:\n    image: docker.io/acme/app:10.11.11 # pinned\n"), 0o640)
	b := findBump([]string{p}, "acme/app:10.11.11", "acme/app:10.11.12")
	if b == nil {
		t.Fatal("not found")
	}
	before, after, err := b.Apply()
	if err != nil || len(before) != 2 || len(after) != 2 {
		t.Fatalf("apply: %v %v %v", before, after, err)
	}
	data, _ := os.ReadFile(p)
	want := "services:\n  a:\n    image: acme/app:10.11.12\n  b:\n    image: acme/app:10.11.111\n  c:\n    image: docker.io/acme/app:10.11.12 # pinned\n"
	if string(data) != want {
		t.Errorf("got\n%s", data)
	}
	if info, _ := os.Stat(p); info.Mode().Perm() != 0o640 {
		t.Errorf("mode changed to %v", info.Mode())
	}
	if err := b.Undo(); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(p)
	if string(data) == want {
		t.Error("undo did nothing")
	}
	if findBump([]string{p}, "acme/app:10.11.1", "x") != nil {
		t.Error("10.11.1 is not in 10.11.11")
	}
}

func TestDue(t *testing.T) {
	it := Item{Container: docker.Container{Labels: map[string]string{LabelDueAfter: "7d"}}}
	if it.due(time.Hour) != 7*24*time.Hour {
		t.Error("7d")
	}
	it.Container.Labels[LabelDueAfter] = "2w"
	if it.due(time.Hour) != 14*24*time.Hour {
		t.Error("2w")
	}
	it.Container.Labels[LabelDueAfter] = "soon"
	if it.due(time.Hour) != time.Hour {
		t.Error("nonsense keeps the default")
	}
}

func TestDays(t *testing.T) {
	if Days(time.Hour) != "today" || Days(30*time.Hour) != "1 day" || Days(50*24*time.Hour) != "50 days" {
		t.Error("days")
	}
}

func TestBumpVariants(t *testing.T) {
	dir := t.TempDir()
	df := filepath.Join(dir, "Dockerfile")
	os.WriteFile(df, []byte("FROM caddy:2.11.4-builder AS builder\nRUN xcaddy build\nFROM caddy:2.11.4\nCOPY --from=builder /x /x\n"), 0o644)
	cf := filepath.Join(dir, "docker-compose.yml")
	os.WriteFile(cf, []byte("services:\n  caddy:\n    build: .\n    image: caddy-custom:2.11.4\n"), 0o644)
	b := findBump([]string{df}, "caddy:2.11.4", "caddy:2.11.7")
	b.Then = findBump([]string{cf}, "caddy-custom:2.11.4", "caddy-custom:2.11.7")
	if _, err := b.All(true); err != nil {
		t.Fatal(err)
	}
	d, _ := os.ReadFile(df)
	c, _ := os.ReadFile(cf)
	if string(d) != "FROM caddy:2.11.7-builder AS builder\nRUN xcaddy build\nFROM caddy:2.11.7\nCOPY --from=builder /x /x\n" {
		t.Errorf("Dockerfile:\n%s", d)
	}
	if !strings.Contains(string(c), "image: caddy-custom:2.11.7") {
		t.Errorf("compose:\n%s", c)
	}
	b.UndoAll()
	if d, _ := os.ReadFile(df); !strings.Contains(string(d), "caddy:2.11.4-builder") {
		t.Errorf("undo:\n%s", d)
	}
}
