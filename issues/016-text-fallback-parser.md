# Fallback: parse the raw text output when the plugin is unavailable

Labels: hardening, core

The embedded callback is the primary event source. A second source keeps kyo
useful when that plugin can't load, instead of degrading to "no events, see the
log".

## When the fallback is used

- The plugin produced no events by the time ansible-playbook printed its first
  `PLAY [...]` banner (plugin path ignored, an ansible-core too old or too new
  for it, a locked-down environment)
- `KYO_NO_PLUGIN=1`: the user doesn't want anything injected into ansible

## What it parses

The `default` stdout callback's text, the format most people run:

- `PLAY [name] ***`, `TASK [role : name] ***`, `RUNNING HANDLER [...]`
- `ok: [host]`, `changed: [host]`, `skipping: [host]`, `fatal: [host]: FAILED! => {...}`,
  `...ignoring`, `fatal: [host]: UNREACHABLE!`, loop items `(item=...)`
- `PLAY RECAP` lines, which give the exact final counts
- Result bodies in both `callback_result_format` styles (json and yaml), only
  far enough to skip them and pull `msg`

It emits the same `events.Event` values the plugin does, so the aggregator and
renderer don't know which source fed them. What is lost: per-play task totals
(the text doesn't carry them, so progress runs countless, like ele's stdin mode),
diff sizes, structured warnings.

Other stdout callbacks (`minimal`, `dense`, custom ones) aren't parsed: kyo says
the view is unavailable and keeps the raw log, as today.

## Not doing: ansible.posix.jsonl

Considered and dropped. It is a `stdout` callback, so it would replace the
user's own output (the raw log turns into JSON), its format belongs to a
collection we don't control, and it loads through the same machinery as kyo's
plugin, so it rarely works where ours doesn't.
