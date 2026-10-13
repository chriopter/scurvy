# 🍋 scurvy

See which docker containers on a server are out of date, and update them with Enter.

<img src="docs/screen.png" alt="scurvy: containers by stack on the left, details on the right" width="860">

## Install

```sh
curl -fsSL https://github.com/chriopter/scurvy/releases/latest/download/install.sh | sudo bash
scurvy        # -n: show every step of an update, run none
```

## How it works

scurvy finds the running containers and reads their compose files and Dockerfiles. It asks the registries what each tag points to now, and [endoflife.date](https://endoflife.date) whether the base still gets fixes; nothing is pulled to check.

| status | means |
|---|---|
| up to date | nothing newer |
| update available | a new image or version is there |
| update overdue | waiting too long: 14 days for an image, 90 for a version, 30 for a build |
| end of life | the base (alpine, debian, python, node, …) gets no fixes any more |
| not checked | the registry did not answer, or the image is private |

Enter keeps the running image as a backup, updates, and waits until the containers are healthy. If anything fails, it puts everything back.

| container | Enter |
|---|---|
| `image: app:latest` | pulls, restarts |
| `image: postgres:16.4` | writes `16.6` into the compose file, pulls, restarts |
| `build:` | raises a pinned `FROM` and its `-builder` stages, rebuilds, restarts |
| `build:` from a git clone | `git pull`, rebuilds, restarts |
| stack with `docker-update.sh` | runs that script |
| pinned by digest, another tool's, `docker run` | nothing |

[Details](docs/how.md)

## Development

```sh
bin/setup    # build and install from this checkout
bin/dev      # run against this machine's containers, dry run
bin/update   # pull and install again
```

Releases are signed with Ed25519; scurvy and the installer refuse anything else. See [AGENTS.md](AGENTS.md).
