# Prompts must survive the live block

Labels: hardening, correctness

Unverified, could make kyo unusable for vault users, so test early.

- `--ask-vault-pass`, `--ask-become-pass` (`-K`), `--ask-pass`: getpass writes to `/dev/tty`; the live block must not have painted over it or repaint during input. Likely fix: no live block until the first `play` event
- `vars_prompt`: the plugin gets `v2_playbook_on_vars_prompt`; the prompt text goes through Display to stdout, which kyo captures. Pause the block and pass that output to the terminal while a prompt is open
- `pause` module: same problem, no callback event; detect from task action `pause`
- Write a manual test playbook for each case under `testdata/manual/`
