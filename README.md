# 🍋 scurvy

See which docker containers on a server are out of date, and update them with Enter.

<img src="docs/screen.png" alt="scurvy: containers by stack on the left, details on the right" width="860">

## Install

```sh
curl -fsSL https://github.com/chriopter/scurvy/releases/latest/download/install.sh | sudo bash
scurvy        # -n: show every step of an update, run none
```

| | means | Enter |
|---|---|---|
| ✓ current | nothing newer | pulls or rebuilds anyway |
| ↑ update | something newer is there | updates |
| ! overdue | the update waited too long | updates |
| ✗ eol | the base gets no fixes any more | updates, raises the version where it can |
| ? unchecked | registry did not answer, or private | nothing |

Enter keeps the running image as a backup, pulls or builds, and waits until the containers are healthy. If anything fails, it puts everything back. [What it checks and runs, per kind of container](docs/how.md).

## Development

```sh
bin/setup    # build and install from this checkout
bin/dev      # run against this machine's containers, dry run
bin/update   # pull and install again
```

Releases are signed with Ed25519; scurvy and the installer refuse anything else. Details in [AGENTS.md](AGENTS.md).
