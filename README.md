# 🍋 scurvy

**Which of your docker containers have scurvy?**

scurvy looks at the containers running on a server, finds the ones that
miss their updates, and updates them with <kbd>Enter</kbd>. One small
program, no config, no daemon, nothing listening.

```
 🍋 scurvy   home-server  ·  9 containers
 ✗ 1 eol    ! 1 overdue    ↑ 2 updates    ✓ 5 current
╭──────────────────────────────────────────────────────────────────────────────────╮
│     CONTAINER     VERSION          WAITS  WHAT                                   │
│   ── shop ────────────────────────────────── 1 overdue · 1 update · 2 current ── │
│ ▌ ! web       ↓  latest             15d  new image                               │
│   ↑ db        #  16.4 → 16.6         9d  new version                             │
│   ── standalone ──────────────────────────────── 1 eol · 1 update · 3 current ── │
│   ✗ worker    ⚒  3.19 → 3.24        25d  alpine 3.19 end of life                 │
│   ↑ proxy     ↓  latest              0d  new image                               │
│   ── 5 more containers current · a shows them ────────────────────────────────── │
╰──────────────────────────────────────────────────────────────────────────────────╯
╭──────────────────────────────────────────────────────────────────────────────────╮
│ ! overdue  web  ghcr.io/acme/web:latest                                          │
│ ↓ pulled from a registry                                                         │
│ ! new image for latest, built 15 days ago                                        │
│ ⏎ pulls and recreates                                                            │
╰──────────────────────────────────────────────────────────────────────────────────╯
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

## The screen

Every container is **✓ current**, **↑ update** (something newer is there),
**! overdue** (it has waited longer than its grace), **✗ eol** (its base gets
no fixes any more) or **? unchecked** (the registry did not answer).

Containers of one compose project stand in a section of their own; those
alone stand under *standalone*. The sign after a name says what kind of
container it is, and that decides how scurvy checks it and what
<kbd>Enter</kbd> runs. Nothing is pulled to check: scurvy asks the
registries what a tag points to now.

## By kind of container

### ↓ Pulled, moving tag (`:latest`, `:release`, `:3.12-slim`)

Checks: the registry's digest for the tag against the running image's;
an image already pulled but not yet running. ↑ update, ! overdue after 14 days.

```
docker tag <running image> <image>:bak-<time>
docker compose pull <service>
docker compose up -d <service>
docker image prune -f
```

### # Pulled, pinned version (`:16.4`, `:2.11.4`)

Checks: the registry's tags for a newer one of the same major; a new major
is only mentioned. ↑ update, ! overdue after 90 days.

```
<compose file>: postgres:16.4 → postgres:16.6   (shown as a diff)
docker tag <running image> <image>:bak-<time>
docker compose pull <service>
docker compose up -d <service>
docker image prune -f
```

### @ Pulled, pinned by digest

Checks: only end of life. Enter does nothing: change the digest by hand.

### ⚒ Own build (`build:` in the compose file)

Checks: the build's age (! overdue after 30 days); a newer image behind a
moving `FROM`; a newer version of a pinned `FROM`; `git log` of the build
context newer than the build.

```
Dockerfile: FROM alpine:3.19 → FROM alpine:3.24   (pinned FROM only)
docker tag <running image> <image>:bak-<time>
docker compose build --pull <service>
docker compose up -d <service>
docker builder prune -f --max-used-space 10GB
docker image prune -f
```

### ⚒ Own build from a git clone

When the build context is a git clone of its own with an upstream, scurvy
also asks the remote (`git ls-remote`, no fetch) whether it moved on.
Enter first runs `git pull --ff-only` in the clone, then builds as above.
It does not write into a clone's Dockerfile: that belongs upstream.

### Stacks with their own `docker-update.sh`

When the compose project's directory has a `docker-update.sh`, Enter keeps
the backup tag and then runs that script instead of pull, build and up:

```
docker tag <running image> <image>:bak-<time>
cd <project dir> && bash docker-update.sh
docker image prune -f
```

### ↗ External, ▸ docker run

Containers another tool started (an all-in-one installer, Portainer) or
`docker run` started: checked like pulled ones, never touched. Update them
where they were started.

### Every kind

Checks [endoflife.date](https://endoflife.date) for the base: alpine,
debian, ubuntu, python, node, golang, postgres, redis, mariadb, mysql, php,
ruby, nginx. ✗ eol once it is over, ! overdue 60 days before.

After every update scurvy keeps the two newest `bak-` tags of the image and
prints the line that brings the old one back:

```
cd <project dir> && docker tag <image>:bak-<time> <image>:<tag> && docker compose up -d <service>
```

On a section's line, Enter updates its containers that need it, one after
the other.

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
