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
> Pre-alpha. The live view and the plan/apply summary work; exit-code policy
> and prompts under the live view are tracked in
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

While it runs, a block on stderr repaints in place: one row per play (finished
ones collapse to a check mark and their duration), the task running now with
each host's outcome as it reports, and the run's counters:

```
  site.yml · plan (--check) · 2 hosts                         1:42

  ✓ Base             6/6  0:21
  ⠼ Deploy apps     9/31  [app] Write the app config
                          web-1 ~  web-2 …

  ok 61   changed 3   skipped 22   failed 0   unreachable 0
  warnings 4 (3 deprecation)
```

When it ends, the block gives way to the summary:

```
  plan: 3 changes on 2 hosts, nothing failed                  2:05

  web-1
    ~ [app] Write the app config     (diff: +4 -1)
    ~ [nginx] Write the site config  (diff: +4)

  web-2
    ~ [nginx] Write the site config  (diff: +4)

  warnings
    3× deprecation: a module option is deprecated  (web-1, web-2)
    1× Failed to look up user with uid 1001

  raw log: kyo-20261123-120000.log
```

With `--diff`, each change carries its diff beneath it (an identical diff on a
later host points at the first). Without `--check` the summary reads
`apply: 3 changed on 2 hosts`, with failed and unreachable hosts listed first. The exit
code is ansible-playbook's own. Commands that only print and exit
(`--list-tasks`, `--list-hosts`, `--list-tags`, `--syntax-check`) run unwrapped.

## How It Works

kyo carries a small Ansible callback plugin inside the binary and installs it
into your user cache dir on first run. It adds that dir in front of your own
`callback_plugins` (which keep loading), and the plugin streams JSON events to
kyo over an inherited pipe. Your configured stdout callback keeps producing its
usual output, which becomes the raw log, so nothing is scraped from text for
the main signal.

If the plugin can't load (an ignored plugin path, an ansible-core it doesn't
support, a locked-down setup), kyo falls back to parsing the default stdout
callback's text into the same events. The result is the same summary, minus
per-play task totals and diff sizes. Other stdout callbacks (`minimal`,
`dense`, custom ones) aren't parsed: kyo points you at the raw log instead.

## Replay

Every run also records what the view was built from, next to the raw log:
`kyo-<timestamp>.log` gets a `kyo-<timestamp>.events.jsonl`. Replay it to see
the run again, live block and summary, with no ansible and no hosts:

```sh
kyo --replay kyo-20261123-074237.events.jsonl
```

The replay takes 12 seconds whatever the run took, shows the run's own times,
and exits with the run's exit code. The recording holds the same host names,
task names and diffs as the raw log, so keep it out of version control too.

## Configuration

Environment variables only, so nothing collides with ansible-playbook's flags.

| Variable | Effect |
|---|---|
| `KYO_LOG=path` | Write the raw log here instead of `./kyo-<timestamp>.log`. `/dev/null` discards it, and records no events. |
| `KYO_REPLAY_SECONDS=n` | How long `--replay` takes (default 12). `0` goes straight to the summary. |
| `KYO_PLAIN=1` | No repainted block: a line per finished play, per failure, and every 15s of quiet. Also on when `CI` or `CLAUDECODE` is set, or stderr is not a terminal. |
| `NO_COLOR=1` | No color. The block still repaints. |
| `KYO_PASSTHROUGH=1` | Don't wrap at all: ansible-playbook's own output and exit code. |
| `KYO_NO_PLUGIN=1` | Inject nothing into ansible: no plugin, no extra variables. kyo reads the default callback's text instead. |
| `KYO_ANSIBLE_PLAYBOOK=path` | The ansible-playbook to run (default: the one on `PATH`). |
| `KYO_ANSIBLE_CONFIG=path` | The ansible-config used to read your `callback_plugins` (default: the one on `PATH`). |

## Requirements

`ansible-playbook` and `ansible-config` on your `PATH`, from ansible-core 2.17
or newer. CI tests 2.17, 2.19 and the latest release.

## Feedback

kyo is a solo, opinionated project - but if you stumbled upon it and have
ideas, questions, or bug reports, an [issue](https://github.com/amberpixels/kyo/issues) is always welcome :)

## License

[MIT](LICENSE) © [amberpixels](https://amberpixels.io)
