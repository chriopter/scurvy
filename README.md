# 🍋 scurvy

**Which of your docker containers have scurvy?**

scurvy looks at the containers running on a server, finds the ones that
miss their updates, and updates them with <kbd>Enter</kbd>. One small
program, no config, no daemon, nothing listening.

```
 🍋 scurvy   home-server  ·  9 containers
 ● 1 end of life    ● 1 overdue    ● 3 fresh    ● 4 healthy
╭────────────────────────────────────────────────────────────────────────────────────────────╮
│   CONTAINER         SOURCE     RUNNING            STATUS     FINDING                       │
│ ────────────────────────────────────────────────────────────────────────────────────────── │
│ ▌ worker            own build  built 41 days ago   EOL       alpine 3.19 is end of life  +1│
│   shop              stack                          OVERDUE   3 containers · 2 to update    │
│   ├ web             registry   latest              OVERDUE   new image, built 15 days ago  │
│   └ db              pinned     16.4                FRESH     16.4 → 16.6, released 9 days …│
│   proxy             registry   latest              FRESH     new image, built today        │
│   + 4 healthy · a shows them                                                               │
╰────────────────────────────────────────────────────────────────────────────────────────────╯
╭────────────────────────────────────────────────────────────────────────────────────────────╮
│ worker  worker-worker                                                                      │
│ ● alpine 3.19 is end of life since 2025-11-01                                              │
│ ● base alpine:3.19 → 3.24, released 25 days ago                                            │
│ → Enter writes alpine:3.24 into Dockerfile.                                                │
╰────────────────────────────────────────────────────────────────────────────────────────────╯
 ↑/↓ move • enter update • a show all • r look again • u check scurvy • q quit
```

## Install

On the server, as root:

```sh
curl -fsSL https://github.com/chriopter/scurvy/releases/latest/download/install.sh | sudo bash
```

Then `scurvy`. When it starts it also looks for a newer scurvy in the
background; if there is one, the top line says so and <kbd>u</kbd> installs it.
<kbd>u</kbd> also looks again by hand.

## What it finds

It works everything out from the containers themselves: compose puts the
project, the service and the compose files on every container it starts.

| | how it knows | 🤒 fresh | 🤢 overdue |
|---|---|---|---|
| image with a moving tag (`latest`, `release`, `3.12-slim`) | the registry's digest for the tag differs from the running one | new image | after 14 days |
| image with a pinned version (`10.11.11`) | the registry has a newer tag of the same major | newer version | after 90 days |
| image built from a Dockerfile | build date, the base image's date, `git log` of the build context | newer base, newer code | build older than 30 days |
| any of them | [endoflife.date](https://endoflife.date) for the base (alpine, debian, python, node, postgres, …) | | ☠ end of life |

Nothing is pulled to check: scurvy asks the registries what a tag points to now.

Containers of one compose project stand together under its name, as a tree. The
SOURCE column says where an image comes from: `registry` (a moving tag),
`pinned` (a fixed version), `digest`, `own build` (a Dockerfile of yours),
`external` (started by another tool) or `docker run`.

## What Enter does

On a stack's line, Enter updates its containers that need it, one after the other.

1. A pinned version: writes the new one into the compose file or the Dockerfile, shown as a diff.
2. Keeps the running image as `<image>:bak-<time>`; the two newest stay.
3. `docker compose pull` or `docker compose build --pull` for that service.
4. `docker compose up -d` for that service.
5. Prints the one line that brings the old image back.

Containers not started from a compose file (`docker run`, containers an
all-in-one installer starts itself) are checked, not updated.

## Labels

```yaml
services:
  database:
    labels:
      scurvy.ignore: "true"      # leave it out
  app:
    labels:
      scurvy.due-after: "7d"     # overdue sooner (h, d, w)
```

## Also

```sh
scurvy list        # the same as text, for scripts
scurvy fix web     # update one container without the screen
scurvy update      # update scurvy itself
```

🔏 Releases are signed (Ed25519) and their builds attested by GitHub; scurvy
and the installer refuse anything without the release key's signature. To
check the installer first:

```sh
curl -fsSLO https://github.com/chriopter/scurvy/releases/latest/download/install.sh
gh attestation verify install.sh -R chriopter/scurvy && sudo bash install.sh
```

Private images need a `docker login` on the server; scurvy uses its credentials.
