# Fixtures, CI ansible matrix, public-repo hygiene

Labels: hardening, test

- Fixture playbooks under `testdata/` running against `localhost` with `-c local`: ok/changed/skipped, loops, include_tasks, include_role, handlers, ignored failure, real failure, check-mode diff, `no_log`
- Integration tests run the real `ansible-playbook` when it is on PATH, skip otherwise
- CI: install ansible-core with pipx for a matrix (proposal: 2.17, 2.19, latest) and run integration tests on each
- Fixtures and examples use generic names (`web-1`, `app`), never real hosts, apps or paths. Recorded logs from real runs stay out of git
