# kyo

A drop-in `ansible-playbook` wrapper that turns the scroll of `TASK [...]`
banners into a live progress view and a summary of what changed (or, with
`--check`, what would).

> [!NOTE]
> Pre-alpha. Open work is tracked in [issues/](issues/README.md) until it
> moves to GitHub issues.

```sh
# before
ansible-playbook site.yml --check
# after
kyo site.yml --check
```

Today it runs the playbook with its normal output going to a log file and
prints a recap built from structured events:

```
plan site.yml: 3 would change on 2 of 2 hosts  (2m5s)
  web-1     ok 61   changed 2   skipped 22   failed 0   unreachable 0   ignored 0
  web-2     ok 58   changed 1   skipped 22   failed 0   unreachable 0   ignored 0
raw log: kyo-20261123-074237.log
```

## How it works

kyo ships a small Ansible callback plugin inside the binary. At run time it
installs it into the user cache dir and puts it on `ANSIBLE_CALLBACK_PLUGINS`
(ahead of any `callback_plugins` you configured, which keep loading). The
plugin streams JSON events over an inherited pipe while your own stdout
callback keeps producing the usual output, which kyo stores as the raw log.
No text scraping for the main signal.

## Configuration

Environment variables only, so nothing collides with ansible-playbook's flags.

| Variable | Effect |
|---|---|
| `KYO_LOG=path` | Write the raw log here instead of `./kyo-<timestamp>.log`. `/dev/null` discards it. |
| `KYO_PASSTHROUGH=1` | Don't wrap at all: ansible-playbook's own output and exit code. |
| `KYO_ANSIBLE_PLAYBOOK=path` | The ansible-playbook to run (default: the one on `PATH`). |
| `KYO_ANSIBLE_CONFIG=path` | The ansible-config used to read your `callback_plugins` setting (default: the one on `PATH`). |

## License

[MIT](LICENSE)
