# Plan and apply summaries

Labels: view, ui

The summary is the product. See PLAN.md section 4 for the sketch.

- Detect plan from `--check` / `-C` in the args (and from the plugin, which can read `context.CLIARGS['check']`; prefer the plugin)
- Plan: "N changes on M hosts", per host the would-change tasks; "no changes" stated plainly when clean
- Apply: same with "changed", plus a failures section on top (red)
- Unreachable hosts listed separately
- Diffs: inline counts always; full diff text when `--diff` is in the args. Decide whether kyo renders from the structured diff or quotes the default callback's output
- Warnings section from 006
- Raw log path last
