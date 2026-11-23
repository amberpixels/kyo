# Runner: spawn ansible-playbook with the event side channel

Labels: skeleton, core

- [x] Spawn `ansible-playbook` (or `KYO_ANSIBLE_PLAYBOOK`) with args untouched
- [x] Pipe on fd 3 via `ExtraFiles`, `KYO_FD=3`, plugin dir on `ANSIBLE_CALLBACK_PLUGINS`
- [x] stdout and stderr both teed line by line into the raw log; stderr lines also go to a handler (for 006)
- [x] Events decoded and handed to a handler as they arrive
- [x] Forward SIGINT / SIGTERM; report exit code and signal death
- [x] Don't depend on EOF on fd 3: after the child exits, drain what is buffered and stop
- [ ] stdin stays with the child; check prompts in 010
- [ ] Ctrl-C on a terminal already reaches the whole process group, so forwarding SIGINT delivers it twice. Check how ansible-playbook reacts (ele does the same); maybe forward only SIGTERM and SIGINTs that did not come from the tty
