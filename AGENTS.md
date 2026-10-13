# Working on scurvy

scurvy stays small: one program, no config file, no daemon, no web page.
A feature that needs one of those is not for scurvy.

- Everything is found from what runs: compose labels, `docker compose
  config`, Dockerfiles, registries. Settings are compose labels
  (`scurvy.*`), nothing else.
- Checking never changes anything (git is asked with `ls-remote`, never
  fetched). Only `fix` (Enter) does, and it shows every command it runs.
- What Enter runs per kind of container is listed in the README; change
  both together.
- The screen may joke (scurvy, limes, abandon ship); `scurvy list` and
  errors say plainly what is the matter.

## Releases

- A signed tag `vX.Y.Z` on main releases: the workflow tests, builds for
  amd64 and arm64, signs SHA256SUMS with SCURVY_SIGNING_KEY and publishes.
  The public key is in `internal/update/update.go` and `bin/install`;
  both change together or not at all.
- Notes afterwards by hand (`gh release edit vX.Y.Z --notes-file …`):
  sections `## New`, `## Changed`, `## Fixed`; one emoji and one short line
  per point; no em dashes; end with
  ``**Update:** press u in scurvy when it shows a newer version, or run `scurvy update`.``
  scurvy shows these points when it offers the update (u).
- A tag with a hyphen (`v0.2.0-rc1`) is a pre-release nobody is offered.

## Testing

`go test ./...`, then `go build && ./scurvy list` against real containers.
`SCURVY_UPDATE_BASE=http://host:port` points the updater at a test server
(`<base>/latest`, `<base>/<tag>/<file>`); signatures are checked all the same.
