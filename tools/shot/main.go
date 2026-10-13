// shot prints scurvy's screen for made-up containers, for the README:
//
//	go run ./tools/shot | freeze -o docs/screen.png
//
// The screen is the real one; only the containers are invented.
package main

import (
	"fmt"
	"time"

	"github.com/chriopter/scurvy/internal/docker"
	"github.com/chriopter/scurvy/internal/scan"
	"github.com/chriopter/scurvy/internal/tui"
)

func main() {
	fmt.Print(tui.Snapshot("harbor", items(), 112, 32, 1))
}

func ago(days int) time.Time { return time.Now().Add(-time.Duration(days) * 24 * time.Hour) }

func item(project, service, image string, kind scan.Kind, current string, level scan.Level, short, text string, since time.Time) scan.Item {
	it := scan.Item{
		Container: docker.Container{Name: service, Labels: map[string]string{}},
		Project:   docker.Project{Name: project, Dir: "/srv/" + project, Files: []string{"/srv/" + project + "/compose.yaml"}},
		Service:   service,
		Kind:      kind,
		Image:     image,
		Running:   image,
		Current:   current,
		Level:     level,
	}
	if text != "" {
		it.Findings = []scan.Finding{{Level: level, Short: short, Text: text, Since: since}}
	}
	return it
}

func items() []scan.Item {
	db := item("paperless", "db", "postgres:16.4", scan.Pulled, "16.4", scan.Update,
		"new version", "16.4 → 16.6, released 9 days ago", ago(9))
	db.Edits = []scan.Edit{{File: "/srv/paperless/compose.yaml", Line: 9, Old: "    image: postgres:16.4", New: "    image: postgres:16.6"}}

	proxy := item("proxy", "proxy", "proxy-proxy", scan.Built, "built 12 days ago", scan.Update,
		"new base version", "base traefik:v3.1.2 → v3.1.6, released 6 days ago", ago(6))
	proxy.Edits = []scan.Edit{{File: "/srv/proxy/Dockerfile", Line: 1, Old: "FROM traefik:v3.1.2", New: "FROM traefik:v3.1.6"}}

	worker := item("worker", "worker", "worker-worker", scan.Built, "built 41 days ago", scan.EOL,
		"alpine 3.19 end of life", "alpine 3.19 is end of life since 2025-11-01", time.Time{})
	worker.Edits = []scan.Edit{{File: "/srv/worker/Dockerfile", Line: 1, Old: "FROM alpine:3.19", New: "FROM alpine:3.24"}}

	return []scan.Item{
		item("paperless", "webserver", "ghcr.io/paperless-ngx/paperless-ngx:latest", scan.Pulled, "latest", scan.Overdue,
			"new image", "new image for latest, published 19 days ago", ago(19)),
		db,
		item("paperless", "broker", "redis:7-alpine", scan.Pulled, "7-alpine", scan.Update,
			"new image", "new image for 7-alpine, published 3 days ago", ago(3)),
		item("paperless", "gotenberg", "gotenberg/gotenberg:8", scan.Pulled, "8", scan.OK, "", "", time.Time{}),
		item("vaultwarden", "vaultwarden", "vaultwarden/server:latest", scan.Pulled, "latest", scan.OK, "", "", time.Time{}),
		worker,
		proxy,
		item("homepage", "homepage", "ghcr.io/gethomepage/homepage:latest", scan.Pulled, "latest", scan.Update,
			"new image", "new image for latest, published today", ago(0)),
		item("mealie", "mealie", "ghcr.io/mealie-recipes/mealie:latest", scan.Pulled, "latest", scan.OK, "", "", time.Time{}),
	}
}
