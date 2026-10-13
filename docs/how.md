# How scurvy checks and updates

## Checks

- **Moving tag** (`:latest`, `:release`, `:3.12-slim`): the registry's digest for the tag against the running image's.
- **Version** (`:16.4`): the registry's tags for a newer one of the same major. A new major is a decision of its own and not offered.
- **Own build**: the build's age, a newer image behind a moving `FROM`, a newer version of a pinned `FROM`, `git log` of the build context. A build context that is a git clone of its own is also checked against its remote (`git ls-remote`, nothing fetched). Git runs only in repositories owned by the user scurvy runs as.
- **Every container**: [endoflife.date](https://endoflife.date) for its base.
- Docker Hub is asked through hub.docker.com, which does not count against its pull limit. Private images use the credentials of `docker login`.

## Enter

1. A lock: one update at a time.
2. Every running image is kept as `<image>:bak-<time>`; the two newest stay.
3. Version edits are made line by line, only if each line still says what scurvy read and only where exactly one line fits. `-builder` stages move along if their new tag exists.
4. A clean git clone is pulled (`--ff-only`).
5. `docker compose pull` or `build --pull`, or the stack's `docker-update.sh` instead.
6. `docker compose up -d --wait`: the containers must run and be healthy.
7. If any step fails: the files, the git clones, the images and the containers go back as they were.

Volumes are never touched. Only old backup tags, dangling images and build cache are deleted.

## More

```sh
scurvy list       # the same, as text
scurvy fix web    # update one container, or a stack, without the screen
scurvy update     # update scurvy itself
```

Compose labels: `scurvy.ignore: "true"` leaves a container out, `scurvy.due-after: "7d"` makes it overdue sooner.
