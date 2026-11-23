# kyo side-channel callback: streams playbook events as JSON lines to the file
# descriptor named by KYO_FD. Loaded automatically (CALLBACK_NEEDS_ENABLED is
# False) next to whatever stdout callback the user runs, which keeps producing
# the normal output kyo stores as the raw log. Without KYO_FD it does nothing,
# so a stale copy on a plugin path is harmless.
from __future__ import annotations

import json
import os
import time

from ansible import context
from ansible.plugins.callback import CallbackBase

DOCUMENTATION = '''
name: kyo
type: aggregate
short_description: stream playbook events to kyo as JSON lines
description:
  - Internal to kyo. Writes one JSON object per line to the fd in KYO_FD.
'''

PROTOCOL = 1
MSG_LIMIT = 2000


def _host(result):
    h = getattr(result, 'host', None)
    return h if h is not None else result._host


def _task(result):
    t = getattr(result, 'task', None)
    return t if t is not None else result._task


def _data(result):
    d = getattr(result, 'result', None)
    return d if isinstance(d, dict) else result._result


def _role(task):
    role = getattr(task, '_role', None)
    return role.get_name() if role is not None else ''


def _count_tasks(blocks):
    """Counts the real tasks in compiled blocks, skipping implicit meta tasks."""
    n = 0
    for b in blocks:
        for attr in ('block', 'rescue', 'always'):
            for t in getattr(b, attr, None) or []:
                if hasattr(t, 'block'):  # nested block
                    n += _count_tasks([t])
                elif not getattr(t, 'implicit', False):
                    n += 1
    return n


def _diff_stats(diffs):
    """Summarises a result's diff as added/removed line counts.

    Approximate: lines present on one side only, not a real line diff. Good
    enough for "(+4 -1)" next to a task; the full diff stays in the raw log.
    """
    if isinstance(diffs, dict):
        diffs = [diffs]
    added = removed = 0
    for d in diffs or []:
        if not isinstance(d, dict):
            continue
        before = d.get('before')
        after = d.get('after')
        if isinstance(before, str) and isinstance(after, str):
            b, a = set(before.splitlines()), set(after.splitlines())
            added += len(a - b)
            removed += len(b - a)
        elif 'prepared' in d:
            added += 1
    return {'added': added, 'removed': removed}


class CallbackModule(CallbackBase):
    CALLBACK_VERSION = 2.0
    CALLBACK_TYPE = 'aggregate'
    CALLBACK_NAME = 'kyo'
    CALLBACK_NEEDS_ENABLED = False

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self._out = None
        fd = os.environ.get('KYO_FD')
        if not fd:
            return
        try:
            fd = int(fd)
            # Not inherited by ssh: a ControlPersist master outliving the run
            # would otherwise hold the pipe open.
            os.set_inheritable(fd, False)
            self._out = os.fdopen(fd, 'w', buffering=1, encoding='utf-8')
        except (ValueError, OSError):
            self._out = None

    def _emit(self, ev, **fields):
        if self._out is None:
            return
        rec = {'v': PROTOCOL, 'ev': ev, 't': round(time.time(), 3)}
        rec.update(fields)
        try:
            self._out.write(json.dumps(rec, default=str) + '\n')
        except (OSError, ValueError):
            self._out = None  # reader went away; keep the playbook running

    def v2_playbook_on_start(self, playbook):
        args = context.CLIARGS
        self._emit('playbook', file=os.path.basename(getattr(playbook, '_file_name', '') or ''),
                   check=bool(args.get('check')), diff=bool(args.get('diff')))

    def v2_playbook_on_play_start(self, play):
        try:
            total = _count_tasks(play.compile())
        except Exception:  # noqa: BLE001 - a count is a nicety, never fatal
            total = -1
        self._emit('play', name=play.get_name(), uuid=play._uuid, tasks=total)

    def v2_playbook_on_task_start(self, task, is_conditional):
        self._emit('task', name=task.get_name(), role=_role(task), action=task.action, uuid=task._uuid)

    def v2_playbook_on_handler_task_start(self, task):
        self._emit('task', name=task.get_name(), role=_role(task), action=task.action,
                   uuid=task._uuid, handler=True)

    def v2_playbook_on_include(self, included_file):
        self._emit('include', file=os.path.basename(included_file._filename),
                   hosts=[h.get_name() for h in included_file._hosts])

    def v2_playbook_on_vars_prompt(self, varname, *args, **kwargs):
        self._emit('prompt', var=varname)

    def _result(self, status, result):
        data = _data(result)
        task = _task(result)
        fields = {'host': _host(result).get_name(), 'uuid': task._uuid, 'status': status}
        if not data.get('_ansible_no_log'):
            msg = data.get('msg')
            if msg:
                fields['msg'] = str(msg)[:MSG_LIMIT]
            if 'diff' in data:
                fields['diff'] = _diff_stats(data['diff'])
        for key in ('warnings', 'deprecations'):
            if data.get(key):
                fields[key] = [w if isinstance(w, str) else (w.get('msg') if isinstance(w, dict) else str(w))
                               for w in data[key]]
        self._emit('result', **fields)

    def v2_runner_on_ok(self, result):
        self._result('changed' if _data(result).get('changed') else 'ok', result)

    def v2_runner_on_failed(self, result, ignore_errors=False):
        self._result('ignored' if ignore_errors else 'failed', result)

    def v2_runner_on_skipped(self, result):
        self._result('skipped', result)

    def v2_runner_on_unreachable(self, result):
        self._result('unreachable', result)

    def v2_playbook_on_stats(self, stats):
        hosts = {h: stats.summarize(h) for h in sorted(stats.processed)}
        self._emit('stats', hosts=hosts)
        if self._out is not None:
            try:
                self._out.flush()
            except OSError:
                pass
