# Keep the user's callback_plugins path working

Labels: skeleton, correctness

Setting `ANSIBLE_CALLBACK_PLUGINS` replaces the `callback_plugins` from
ansible.cfg (verified: the user's plugin stops loading). kyo must prepend its
dir to the effective value instead.

- [x] Read `DEFAULT_CALLBACK_PLUGIN_PATH` from `ansible-config dump --format json` (run in the same cwd and env as the playbook, so the same ansible.cfg is found)
- [x] If the user already set `ANSIBLE_CALLBACK_PLUGINS`, prepend to that and skip the lookup
- [x] On lookup failure, fall back to ours plus ansible's default path and say so in the log, not on screen
- [x] Measured: about 0.4 s of a 1.1 s localhost run
- [ ] If it hurts on real runs, cache per cwd + ansible.cfg mtime
