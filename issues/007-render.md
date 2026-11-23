# Live block and plain mode

Labels: view, ui

Port ele's render approach.

- Live block on stderr on a TTY: header (playbook, plan/apply, host count, clock), one row per play (done ones collapsed to a check line with duration), current task with reporting hosts, counter row, warnings count
- Measure width and height every tick; keep the block one row short of the screen (ele's drift fix)
- Plain mode (`KYO_PLAIN`, `CI`, `CLAUDECODE`, non-TTY): a line per finished play and periodic progress, never the firehose
- `NO_COLOR`
- Golden tests on Frame output like ele's render tests
