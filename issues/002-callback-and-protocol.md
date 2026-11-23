# Embedded callback plugin and the event protocol

Labels: skeleton, core

The main signal is not text: a Python callback plugin, embedded in the binary,
streams JSON lines on an inherited fd. See PLAN.md section 2 for the spike.

- [x] `internal/callback/kyo.py`: `aggregate` type, `CALLBACK_NEEDS_ENABLED = False`, writes to the fd named by `KYO_FD`, no-op when unset (so a leftover plugin dir is harmless)
- [x] Protocol v1: every line is `{"v":1,"ev":...}`; events `playbook`, `play`, `task`, `include`, `result`, `prompt`, `stats`
- [x] Static task count per play from `play.compile()` minus implicit tasks
- [x] Public `CallbackTaskResult` accessors (`.host`, `.task`, `.result`) with `_host` / `_task` / `_result` fallback; no `v2_on_any`
- [x] `no_log` results carry no message or diff
- [x] `os.set_inheritable(fd, False)` so SSH ControlPersist masters don't hold the pipe open
- [x] Go side writes the plugin to `<user cache>/kyo/<content hash>/callback/` once
- [ ] Verify the ControlPersist case against a real SSH host (only localhost tested so far)
- [ ] Decide which ansible-core versions to support (proposal: 2.17+), see 012
