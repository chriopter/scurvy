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

func TestNewerTag(t *testing.T) {
	tags := []string{"10.11.11", "10.11.12", "10.12.0", "11.0.0", "10.12.0-rc1", "latest", "2025111705", "v10.13.0", "10.11"}
	if got := newerTag(tags, "10.11.11"); got != "10.12.0" {
		t.Errorf("got %q", got)
	}
	if got := newerTag(tags, "10.12.0"); got != "" {
		t.Errorf("nothing newer in 10.x, got %q", got)
	}
	if got := newerTag([]string{"3.19", "3.24", "3.9", "3.24.1"}, "3.19"); got != "3.24" {
		t.Errorf("alpine: got %q", got)
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
	if majorOf("10.11.11") != "10." || majorOf("v2.3") != "v2." {
		t.Error("majorOf")
	}
}

func write(t *testing.T, name, text string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBaseOf(t *testing.T) {
	p := write(t, "Dockerfile", "FROM rust:latest AS tunnel\nRUN cargo build \\\n  --release\nFROM python:3.12-slim\nCOPY --from=tunnel /x /x\n")
	if b, ok := baseOf(p, nil, ""); !ok || b.ref != "python:3.12-slim" {
		t.Errorf("multi-stage: %+v", b)
	}
	p = write(t, "Dockerfile", "ARG PY=3.13\nFROM --platform=linux/amd64 python:${PY}-alpine AS base\nARG PY=9.9\nFROM base AS final\n")
	if b, _ := baseOf(p, nil, ""); b.ref != "python:3.13-alpine" || b.raw != "python:${PY}-alpine" {
		t.Errorf("args and stages (a stage's ARG is not FROM's): %+v", b)
	}
	v := "3.14"
	if b, _ := baseOf(p, map[string]*string{"PY": &v}, ""); b.ref != "python:3.14-alpine" {
		t.Errorf("compose build args win: %+v", b)
	}
	p = write(t, "Dockerfile", "FROM node:22 AS dev\nFROM nginx:1.27 AS prod\n")
	if b, _ := baseOf(p, nil, "dev"); b.ref != "node:22" {
		t.Errorf("target: %+v", b)
	}
	if _, ok := baseOf(p, nil, "missing"); ok {
		t.Error("a missing target has no base")
	}
	if _, ok := baseOf(write(t, "Dockerfile", "FROM golang AS b\nFROM scratch\n"), nil, ""); ok {
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
		"postgres:9.6":              {"postgresql 9.6"},
		"node:20.20.2":              {"nodejs 20"},
		"mariadb:10.6.28":           {"mariadb 10.6"},
		"ghcr.io/x/postgres:14-abc": nil,
		"quay.io/acme/api":          nil,
	} {
		if got := strings.Join(labels(ref), ","); got != strings.Join(want, ",") {
			t.Errorf("%s: got %v want %v", ref, got, want)
		}
	}
}

func TestImageEdit(t *testing.T) {
	p := write(t, "compose.yaml", "services:\n  a:\n    image: acme/app:1.2.11 # pinned\n  b:\n    image: \"ghcr.io/acme/app:1.2.11\"\n  c:\n    image: acme/app:1.2.111\n")
	e, err := imageEdit([]string{p}, "acme/app:1.2.11", "acme/app:1.2.12")
	if err != nil || e.Line != 3 || e.New != "    image: acme/app:1.2.12 # pinned" {
		t.Fatalf("got %+v, %v", e, err)
	}
	q := write(t, "compose.yaml", "services:\n  a:\n    image: acme/app:1.0\n  b:\n    image: acme/app:1.0\n")
	if _, err := imageEdit([]string{q}, "acme/app:1.0", "acme/app:1.1"); err == nil {
		t.Error("two services on one image: which one is not certain")
	}
	if _, err := imageEdit([]string{p}, "acme/app:9", "x"); err == nil {
		t.Error("no line, no edit")
	}
}

func TestFromEdits(t *testing.T) {
	p := write(t, "Dockerfile", "FROM caddy:2.11.4-builder AS builder\n# FROM caddy:2.11.4\nFROM caddy:2.11.4\n")
	all := func(string) bool { return true }
	edits, err := fromEdits(p, "caddy:2.11.4", "caddy:2.11.7", all)
	if err != nil || len(edits) != 2 || edits[0].New != "FROM caddy:2.11.7-builder AS builder" || edits[1].Line != 3 {
		t.Fatalf("got %+v, %v", edits, err)
	}
	if _, err := fromEdits(p, "caddy:2.11.4", "caddy:2.11.7", func(string) bool { return false }); err == nil {
		t.Error("a variant without its new tag moves nothing")
	}
}

func TestDue(t *testing.T) {
	it := Item{Container: docker.Container{Labels: map[string]string{LabelDueAfter: "7d"}}}
	if it.due(time.Hour) != 7*24*time.Hour {
		t.Error("7d")
	}
	it.Container.Labels[LabelDueAfter] = "soon"
	if it.due(time.Hour) != time.Hour {
		t.Error("nonsense keeps the default")
	}
}

func TestUpdatable(t *testing.T) {
	it := Item{Kind: Pulled, Project: docker.Project{Name: "p"}, Image: "alpine:3.20@sha256:abc"}
	if it.Updatable() {
		t.Error("a digest pins it, tag or not")
	}
}
