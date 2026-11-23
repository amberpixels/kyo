# Aggregator: plays, tasks, hosts, counters

Labels: view, core

The one stateful stage, fed events, read via an immutable Snapshot (ele's shape).

- Plays in order; per play: tasks done / total, total growing on `include` events (count the included file's tasks), duration
- Current task (name, role prefix, start time) and which hosts have reported for it
- Counters overall and per host: ok, changed, skipped, failed, ignored, rescued, unreachable
- Changed results kept with host, task, role and diff summary (lines added / removed) for 008
- Failures kept with host, task, message; ignored failures counted apart
- Handlers: `v2_playbook_on_handler_task_start` counts as a task, not in the static total
- Reconcile with the final `stats` event; stats win if counts disagree
- Unit tests from recorded event fixtures (012)
