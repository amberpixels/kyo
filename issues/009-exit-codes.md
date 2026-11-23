# Exit code policy and the detailed plan exit

Labels: hardening

- Default: pass the child's exit code through untouched (ansible's codes already mean something: 2 host failed, 3 unreachable, 4 parse error, 99 interrupted)
- `KYO_DETAILED_EXIT=1` on a plan: a distinct code when changes are pending, for CI drift checks. Ansible owns 0-5, 99, 250; pick a free one (proposal: 10) and document it
- Signal death reported as 128+signal
- A run where the plugin never loaded keeps the child's code and says the view was unavailable
