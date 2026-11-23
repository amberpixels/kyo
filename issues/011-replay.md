# Record events and replay them offline

Labels: hardening, dx

ele's `--replay` was the best dev tool it had. kyo's is easier: the event
stream is the recording.

- Write the event stream next to the raw log (`kyo-<ts>.events.jsonl`), same lifetime and gitignore
- Stderr lines (for 006) recorded as events too, with timestamps, so a replay is complete
- `kyo --replay <events.jsonl>`: drive the live block paced over `KYO_REPLAY_SECONDS` (default 12), then the summary; exit code from the recording
