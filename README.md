<p align="center">
  <img src="logo.svg" alt="kyo" width="200">
</p>

<div align="center">

### Hear every host as one.

A drop-in `ansible-playbook` wrapper that turns the task scroll into a short answer: what changed, or with `--check`, what would.

[![CI](https://github.com/amberpixels/kyo/actions/workflows/ci.yml/badge.svg)](https://github.com/amberpixels/kyo/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/github/go-mod/go-version/amberpixels/kyo)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-yellow.svg)](LICENSE)

</div>

---

A playbook run prints a banner and an `ok: [host]` line for every task on every
host, plus deprecation warnings and debug dumps. On a plan (`--check`) the few
lines that matter, the `changed` ones, scroll past between hundreds that don't.

`kyo` runs the same command, keeps that output in a log file, and prints the
answer instead. It's a sibling of [ele](https://github.com/amberpixels/ele),
which does the same for `pg_restore`. The name comes from Kyo, the telepathic
companion in Ursula K. Le Guin's *Rocannon's World*, the novel that coined the
word "ansible".

> [!NOTE]
> Pre-alpha. Today kyo prints a per-host recap from structured events; the
> live progress view and the per-task plan summary are in progress, tracked in
> [issues](https://github.com/amberpixels/kyo/issues).

## Install

```sh
go install github.com/amberpixels/kyo/cmd/kyo@latest
```

## Quick Start

Put `kyo` where `ansible-playbook` was. Every argument passes through untouched:

```sh
# before
ansible-playbook site.yml --check
# after
kyo site.yml --check
```

```
plan site.yml: 3 would change on 2 of 2 hosts  (2m5s)
  web-1                    ok 61   changed 2    skipped 22   failed 0    unreachable 0    ignored 0
  web-2                    ok 58   changed 1    skipped 22   failed 0    unreachable 0    ignored 0
raw log: kyo-20261123-120000.log
```

Without `--check` the same line reads `apply site.yml: ... changed`. The exit
code is ansible-playbook's own. Commands that only print and exit
(`--list-tasks`, `--list-hosts`, `--list-tags`, `--syntax-check`) run unwrapped.

## How It Works

kyo carries a small Ansible callback plugin inside the binary and installs it
into your user cache dir on first run. It adds that dir in front of your own
`callback_plugins` (which keep loading), and the plugin streams JSON events to
kyo over an inherited pipe. Your configured stdout callback keeps producing its
usual output, which becomes the raw log, so nothing is scraped from text for
the main signal.

## Configuration

Environment variables only, so nothing collides with ansible-playbook's flags.

| Variable | Effect |
|---|---|
| `KYO_LOG=path` | Write the raw log here instead of `./kyo-<timestamp>.log`. `/dev/null` discards it. |
| `KYO_PASSTHROUGH=1` | Don't wrap at all: ansible-playbook's own output and exit code. |
| `KYO_ANSIBLE_PLAYBOOK=path` | The ansible-playbook to run (default: the one on `PATH`). |
| `KYO_ANSIBLE_CONFIG=path` | The ansible-config used to read your `callback_plugins` (default: the one on `PATH`). |

## Requirements

`ansible-playbook` and `ansible-config` on your `PATH` (ansible-core).
Developed against ansible-core 2.22; which older versions to support is still
being decided.

## Feedback

kyo is a solo, opinionated project - but if you stumbled upon it and have
ideas, questions, or bug reports, an [issue](https://github.com/amberpixels/kyo/issues) is always welcome :)

## License

[MIT](LICENSE) © [amberpixels](https://amberpixels.io)
