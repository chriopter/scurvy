# How scurvy checks and updates

## Checks

- **Moving tag** (`:latest`, `:release`, `:3.12-slim`): the registry's digest for the tag against the running image's.
- **Fixed version** (`:16.4`): a pin is a choice, so such a container is up to date while its version's image is; a newer image under the same tag is an update. Newer versions, of the same major or a later one, are shown as news; <kbd>e</kbd> pins to one.
- **Own build**: the build's age; a newer image behind a moving `FROM` (compared by date, the best that can be told); a newer version of a fixed `FROM`; `git log` of the build context. A build context that is a git clone of its own is also checked against its remote (`git ls-remote`, nothing fetched). Compose build args and targets are followed.
- **Every container**: [endoflife.date](https://endoflife.date) for its base.
- Docker Hub is asked through hub.docker.com, which does not count against its pull limit. Private images use the credentials of `docker login`.
- git runs only in repositories whose files and config, includes too, belong to the user scurvy runs as and nobody else may change; never with hooks or a password prompt.

## Enter

1. A lock (`/run/lock/scurvy.lock`): one update at a time on the machine.
2. git clones to pull must be clean.
3. Every planned container's running image is kept as `<image>:bak-<time>-<service>`; the two newest per service stay.
4. git clones are pulled (`--ff-only`), then the version edits are made (a fixed `FROM` raised, or the version chosen with <kbd>e</kbd>): line by line, only if each line still says what scurvy read, only in the service's own block, `-builder` stages along if their new tag exists.
5. `docker compose pull` or `build --pull` (`--no-cache` with <kbd>R</kbd>), or the stack's `docker-update.sh` instead; such a stack updates as a whole.
6. `docker compose up -d --no-deps --pull never --no-build --wait`: only the planned containers, on exactly the images just made, running and healthy.

**Nextcloud All-in-One** starts its containers itself and keeps their versions together, so scurvy never swaps them. Enter on one of them asks AIO's mastercontainer to update all of AIO, as AIO documents for scripts: `docker exec --env AUTOMATIC_UPDATES=1 [--env DAILY_BACKUP=1] nextcloud-aio-mastercontainer /daily-backup.sh`, with the backup if one is set up in AIO. When AIO renews its mastercontainer on the way, scurvy waits for it and runs the command once more, then waits until every AIO container is healthy. The way back is AIO's backup, not scurvy's.

If a step fails, or the update is interrupted, scurvy puts back what it changed: the files, the git clones (only if nobody changed them since), the image tags, and, if it had touched them, the containers. What a `docker-update.sh` changed itself, it cannot know; it tells so.

Volumes are never touched. Only old backup tags, dangling images and build cache are deleted.

Compose labels: `scurvy.ignore: "true"` leaves a container out, `scurvy.due-after: "7d"` makes it overdue sooner.
