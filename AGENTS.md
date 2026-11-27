# Instructions for coding agents

You are helping someone contribute to unifi-openwrt, a daemon that lets a
UniFi controller manage an OpenWrt access point. The rules for people are in
[CONTRIBUTING.md](CONTRIBUTING.md). Read it first. This file adds what an
agent tends to get wrong.

## Before you propose a commit

- Run `gofmt -l .`, `go vet ./...` and `go test ./...`. All three must be clean.
- Keep the change to the one topic you were asked for. No drive-by refactors,
  no reformatting of lines you did not change, no new dependencies.
- Rebase on `main`. Do not merge `main` into the branch.
- Do not change the version number. Releases are made by the maintainer.

## Text you write

- Commit message: `area: What changed` (areas: `openwrt`, `translate`, `uow`,
  `inform`, `packaging`, `README`, `docs`), then a short body that says what
  was wrong and what the change does.
- Pull request: Follow "How to write it up" in CONTRIBUTING.md and fill in the
  template. No summary of the diff file by file.
- State only what was run. If your user tested it on a device, ask which
  device, OpenWrt version and controller version, and write that down. If
  nothing ran on real hardware, say so in those words.
- A new row in the README's "Tested devices" table needs all six cells. Ask
  your user for the ones you do not know. Do not guess a version.

## Never

- Never put real MAC or IP addresses, SSIDs, passphrases, keys or host names
  into code, tests, logs or dumps. Use `02:00:00:00:00:xx` and `192.0.2.x`.
- Never weaken a safety check to make a test pass: The rollback timer, the
  health check after an apply, and the refusal of firmware upgrades are there
  to keep an AP reachable.
- Never write to a UniFi controller from a test or a tool. The daemon talks
  to it as a device only.
