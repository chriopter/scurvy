# Working on scurvy

scurvy stays small: one program, no config file, no daemon, no web page.
A feature that needs one of those is not for scurvy.

## The safe core and the rest

- `internal/fix` is the core, and the only code that changes anything. It
  gets a plan (one compose project: backups, exact line edits, git pulls,
  pull/build, up) and carries it out: lock, backup tags first, edits
  written atomically, `up --wait` as the health check, and on any failure
  everything put back. Keep it small, and tested for every way back.
- `internal/scan` only looks and describes. Edits it proposes are exact
  (file, line, old line, new line) and only where exactly one line fits.
- `internal/tui` and `main.go` show and ask. They never change anything
  themselves; they hand plans to the core.
- git runs only in repositories the running user alone owns; scurvy runs
  as root and a repository's config can run commands.

## Rules

- Everything is found from what runs: compose labels, `docker compose
  config`, Dockerfiles, registries. Settings are compose labels
  (`scurvy.*`), nothing else.
- Checking never changes anything (git is asked with `ls-remote`, never
  fetched). Only `fix` (Enter) does, and it shows every command it runs.
- What Enter runs per kind of container is listed in the README; change
  both together.
- The screen may joke (scurvy, limes, abandon ship); `scurvy list` and
  errors say plainly what is the matter.

## Commits

Start with `New:`, `Changed:` or `Fixed:` and the concrete change
(`Changed:` for docs, tests and upkeep too), in plain words. Explain the
earlier behaviour or the reason in the body when it helps. No co-author
lines.

## Releases

- A signed tag `vX.Y.Z` on main releases: the workflow tests, builds for
  amd64 and arm64, signs SHA256SUMS with SCURVY_SIGNING_KEY and publishes.
  The public key is in `internal/update/update.go` and `bin/install`;
  both change together or not at all.
- The workflow refuses a tag that is not `vX.Y.Z`, not on main, or not
  signed in a way GitHub verifies.
- Notes afterwards by hand (`gh release edit vX.Y.Z --notes-file …`):
  sections `## New`, `## Changed`, `## Fixed`; one emoji and one short line
  per point; no em dashes; end with
  ``**Update:** press u in scurvy when it shows a newer version, or run `scurvy update`.``
  scurvy shows these points when it offers the update (u).
- A tag with a hyphen (`v0.2.0-rc1`) is a pre-release nobody is offered.

## Testing

`go test ./...`, then `bin/dev` (a dry run) against real containers. The
README's picture is the real screen with made-up containers:
`go run ./tools/shot | freeze -o docs/screen.png` ([freeze](https://github.com/charmbracelet/freeze)).
`SCURVY_UPDATE_BASE=http://host:port` points the updater at a test server
(`<base>/latest`, `<base>/<tag>/<file>`); signatures are checked all the same.
