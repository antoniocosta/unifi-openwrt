# Contributing

Issues and pull requests are welcome. The project has been tested on a few
devices only, so a report from another device helps as much as code.

## Before you send code

- Run `gofmt -l .` (no output), `go vet ./...` and `go test ./...`.
- One topic per pull request, rebased on `main`, no merge commits.
- Add a test for what you fix. Most of the code is tested with text captured
  from a real AP, so a new case is usually a few lines.
- No secrets and no personal data in code, tests, logs or dumps: No real MAC
  or IP addresses, SSIDs, passphrases or keys.
- Change `docs/internals.md` when the behaviour it describes changes.

## How to write it up

Short and factual, in the commit message and in the pull request:

1. What was wrong and why it matters, in one or two sentences.
2. What changed, one fact per bullet, with the exact option, key or function.
3. How it was tested: Which device, OpenWrt version and controller version,
   and what you did. Say what you did not test.

If a change restarts the Wi-Fi or changes what an AP does after an upgrade,
say so. It goes into the release notes.

## Reporting a device

Tested on a device that is not in the README's table? Add a row, or open an
issue with the same facts. All six cells are needed:

```md
| [Device name](link to its OpenWrt page) | SoC (`driver`), Wi-Fi generation | OpenWrt version | UniFi model (`CODE`) | Network version on which console | By [@you](https://github.com/you): What you tested |
```

- **Adopted as**: The model you set with `-model` (UCI `model`), for example
  UAP-AC-Pro (`U7PG2`).
- **UniFi**: The Network application's version and the console it runs on,
  for example "Network 10.6.109 on a UCG Ultra".
- **Tested**: Only what you ran yourself, and what did not work.

## Reporting a problem

Say which device, OpenWrt version, model (`-model`) and controller version,
and attach `logread -e uow`. The daemon never logs keys or passphrases.

A dump of what the daemon reads from your AP helps even more, because it
becomes a test case for your device:

```sh
uow -dump /tmp/uow-dump.txt
```

MAC and IP addresses, SSIDs and the hostname are replaced by made-up ones,
and Wi-Fi passwords and every text field the daemon does not know are left
out. Read the file before you attach it.
