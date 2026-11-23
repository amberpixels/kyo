# Manual prompt checks

Each playbook here asks for input the way a real run would. They need a
terminal, so CI does not run them. Run each one under kyo on a terminal,
then again with `KYO_PLAIN=1` and with `KYO_NO_PLUGIN=1`:

```sh
cd testdata/manual
export ANSIBLE_LOCALHOST_WARNING=false ANSIBLE_PYTHON_INTERPRETER=auto_silent
kyo -i localhost, -c local <playbook> [flags]
```

In every case the prompt must be readable when it appears, the live block
must not repaint over it while you type, and the block comes back once the
prompt is answered.

| Playbook | Flags | Do this | Expect |
|---|---|---|---|
| `vault.yml` | `--ask-vault-pass` | type `kyo-manual` | `Vault password:`, then exit 0 |
| `become.yml` | `-K` | type your sudo password | `BECOME password:`, then exit 0 (2 with a wrong password) |
| `vars-prompt.yml` | | answer each prompt; repeat the token for `confirm` | four prompts: two in the first play, then `A token, twice` and its confirm in the second, while the block is up. Exit 0 |
| `pause.yml` | | Enter, then text and Enter, then wait | `[Bare pause]` and `Press enter to continue`, then `[Pause with a prompt]`, then `Pausing for 3 seconds`. Exit 0 |
| `pause.yml` | | at the timed pause, Ctrl+C then `C` | `Press 'C' to continue the play or 'A' to abort`, then exit 0 |
| `pause.yml` | | at the timed pause, Ctrl+C then `A` | the pause fails with `user requested abort!`, exit 2 |

`--ask-pass` (`-k`) prompts the same way as `-K`, before the first play. Check
it against any host that takes an SSH password.

Known gap: under `KYO_NO_PLUGIN=1`, kyo cannot tell that a private
`vars_prompt` in a later play is waiting, so the block can repaint over its
prompt line. The input still reaches ansible.

Ctrl-C: run any long task (for example `ansible.builtin.command: sleep 30`)
and press Ctrl-C once. kyo should exit 99, ansible-playbook's "interrupted"
code, not 130.
