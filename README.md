# 🍋 scurvy

See which docker containers on a server are out of date, and update them with Enter.

<img src="docs/screen.png" alt="scurvy: containers by stack on the left, details on the right" width="860">

## Install

```sh
curl -fsSL https://github.com/chriopter/scurvy/releases/latest/download/install.sh | sudo bash
scurvy        # -n: show every step of an update, run none
```

| status | means |
|---|---|
| up to date | nothing newer |
| update available | a new image or version is there |
| update overdue | it waited too long: 14 days for an image, 90 for a version, 30 for a build |
| end of life | the base (alpine, debian, python, node, postgres, …) gets no fixes any more |
| not checked | the registry did not answer, or the image is private |

What scurvy checks and what Enter runs depends on how the container is made:

| container | checked | Enter |
|---|---|---|
| compose `image:` with a moving tag (`:latest`) | the registry's digest | `pull`, `up` |
| compose `image:` with a version (`:16.4`) | newer versions of the same major | writes `:16.6` into the compose file, `pull`, `up` |
| compose `build:` (Dockerfile) | build age, base image, git log | raises a pinned `FROM`, also `-builder` stages, `build --pull`, `up` |
| compose `build:` from a git clone | also the clone's remote | `git pull`, `build --pull`, `up` |
| stack with its own `docker-update.sh` | as above | runs the script instead |
| pinned by digest (`@sha256:…`) | end of life only | nothing |
| started by another tool or `docker run` | as above | nothing |

Every Enter keeps the running image as a backup, waits until the containers are healthy, and puts everything back if a step fails. [Details](docs/how.md).

## Development

```sh
bin/setup    # build and install from this checkout
bin/dev      # run against this machine's containers, dry run
bin/update   # pull and install again
```

Releases are signed with Ed25519; scurvy and the installer refuse anything else. See [AGENTS.md](AGENTS.md).
