# Warnings and deprecations: capture, group, dedupe

Labels: view

Two sources (spike finding 4 and 5):

- Task warnings and deprecations arrive structured in `result` events
- Controller warnings (`[WARNING]:`, `[DEPRECATION WARNING]:`, possibly wrapped over several lines) only appear on stderr; scrape them
- The same warning often arrives from both sources; dedupe on normalized text
- Group by fingerprint (paths, hostnames and quoted values blanked, like ele's error fingerprint); show count + one sample
- Classify deprecation vs warning; deprecations collapse to one line by default
- Collect the boilerplate "Deprecation warnings can be disabled..." line and drop it
