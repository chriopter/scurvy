# How scurvy checks and updates

The sign after a name says where its image comes from:

| | kind | checked by | Enter runs |
|---|---|---|---|
| ↓ | pulled, moving tag (`:latest`) | the registry's digest for the tag | `pull`, `up` |
| # | pulled, pinned version (`:16.4`) | newer tags of the same major | new version into the compose file, `pull`, `up` |
| ◇ | own build | build age, base image, `git log` of the build context | pinned `FROM` raised, `build --pull`, `up` |
| ◇ | own build from a git clone | also the clone's remote | `git pull`, `build --pull`, `up` |
| @ | pinned by digest | end of life only | nothing |
| ↗ ▸ | started by another tool, or `docker run` | like pulled ones | nothing |

Every kind is checked against [endoflife.date](https://endoflife.date) for its base. Overdue means waiting longer than 14 days for a new image, 90 for a new version, 30 for a build.

A stack with its own `docker-update.sh` gets that script run instead of pull, build and up.

## Enter

1. Every running image is kept as `<image>:bak-<time>`; the two newest stay.
2. The edits shown are made, line by line, only if the lines are unchanged.
3. Pull or build, then `docker compose up -d --wait`: running and healthy.
4. Any failure puts back the files, the git clones, the images and the containers.

Volumes are never touched; only old backup tags, dangling images and build cache are deleted.

## More

```sh
scurvy list       # the same, as text
scurvy fix web    # update one container, or a stack, without the screen
scurvy update     # update scurvy itself
```

Compose labels: `scurvy.ignore: "true"` leaves a container out, `scurvy.due-after: "7d"` makes it overdue sooner.
