package textparse

import (
	"strings"
	"testing"
	"time"

	"github.com/amberpixels/kyo/internal/events"
)

func parse(t *testing.T, text string) []events.Event {
	t.Helper()
	var evs []events.Event
	p := New(Playbook{File: "site.yml"}, func() time.Time { return time.Unix(100, 0) },
		func(ev events.Event) { evs = append(evs, ev) })
	for line := range strings.SplitSeq(strings.TrimPrefix(text, "\n"), "\n") {
		p.Line(line)
	}
	p.Finish()
	return evs
}

// results renders the result events as "task|host|status|msg" lines.
func results(evs []events.Event) []string {
	names := map[string]string{}
	var out []string
	for _, ev := range evs {
		switch ev.Kind {
		case events.KindTask:
			names[ev.UUID] = ev.Name
		case events.KindResult:
			out = append(out, names[ev.UUID]+"|"+ev.Host+"|"+string(ev.Status)+"|"+ev.Msg)
		case events.KindPlaybook, events.KindPlay, events.KindInclude, events.KindPrompt, events.KindStats:
		}
	}
	return out
}

func TestResults(t *testing.T) {
	cases := []struct {
		name, text string
		want       []string
	}{
		{
			name: "plain statuses and delegation",
			text: `
PLAY [Base] ********
TASK [One] *********
ok: [web-1]
changed: [web-2 -> db-1]
skipping: [web-3]
fatal: [web-4]: UNREACHABLE! => {"changed": false, "msg": "Failed to connect", "unreachable": true}
`,
			want: []string{
				"One|web-1|ok|",
				"One|web-2|changed|",
				"One|web-3|skipped|",
				"One|web-4|unreachable|Failed to connect",
			},
		},
		{
			name: "a failure waits for ...ignoring",
			text: `
PLAY [Base] ****
TASK [Two] ****
fatal: [web-1]: FAILED! => {"changed": false, "msg": "boom"}
...ignoring
fatal: [web-2]: FAILED! => {"changed": false, "msg": "boom"}
`,
			want: []string{"Two|web-1|ignored|boom", "Two|web-2|failed|boom"},
		},
		{
			name: "loop items collapse per host, worst status wins",
			text: `
PLAY [Base] ****
TASK [Loop] ****
ok: [web-1] => (item=a)
changed: [web-2] => (item=a)
changed: [web-1] => (item=b)
failed: [web-2] (item=b) => {"changed": false, "msg": "item b broke"}
ok: [web-2] => (item=c)
TASK [Next] ****
ok: [web-1]
`,
			want: []string{"Loop|web-1|changed|", "Loop|web-2|failed|One or more items failed", "Next|web-1|ok|"},
		},
		{
			name: "...ignoring after failed items",
			text: `
PLAY [Base] ****
TASK [Loop] ****
failed: [web-1] (item=a) => {"msg": "no"}
...ignoring
`,
			want: []string{"Loop|web-1|ignored|One or more items failed"},
		},
		{
			name: "multi-line JSON with braces in strings",
			text: `
PLAY [Base] ****
TASK [Debug] ****
ok: [web-1] => {
    "msg": "a } and a { and \"quotes\""
}
ok: [web-2] => (item={'k': 'v'}) => {
    "msg": [1, 2]
}
`,
			want: []string{`Debug|web-1|ok|a } and a { and "quotes"`, "Debug|web-2|ok|[1, 2]"},
		},
		{
			name: "YAML bodies: plain, quoted, block",
			text: `
PLAY [Base] ****
TASK [Yaml] ****
ok: [web-1] =>
    changed: false
    msg: plain text
fatal: [web-2]: FAILED! =>
    changed: false
    msg: |-
        line one
        line two
    rc: 1
ok: [web-3] =>
    msg: 'it''s quoted'
ok: [web-4] =>
    nested:
        msg: not this one
`,
			want: []string{
				"Yaml|web-1|ok|plain text",
				"Yaml|web-2|failed|line one\nline two",
				"Yaml|web-3|ok|it's quoted",
				"Yaml|web-4|ok|",
			},
		},
		{
			name: "noise between banners is skipped",
			text: `
PLAY [Base] ****
TASK [Write] ****
--- before
+++ after: /tmp/x
@@ -0,0 +1 @@
+ok: [not-a-host]

[ERROR]: Task failed: boom
Origin: site.yml:3:7

3     - name: ok: [nope]

FAILED - RETRYING: [web-1]: Write (3 retries left).
changed: [web-1]
`,
			want: []string{"Write|web-1|changed|"},
		},
		{
			name: "...ignoring after interleaved hosts' items ignores both",
			text: `
PLAY [Base] ****
TASK [Loop] ****
failed: [web-1] (item=1) => {"msg": "no"}
failed: [web-1] (item=2) => {"msg": "no"}
failed: [web-2] (item=1) => {"msg": "no"}
...ignoring
failed: [web-2] (item=2) => {"msg": "no"}
...ignoring
`,
			want: []string{
				"Loop|web-1|ignored|One or more items failed",
				"Loop|web-2|ignored|One or more items failed",
			},
		},
		{
			name: "a blank line inside a YAML body keeps the body open",
			text: `
PLAY [Base] ****
TASK [Shell] ****
fatal: [web-1]: FAILED! => 
    changed: true
    msg: non-zero return code
    stdout: |-
        one

        two
...ignoring

TASK [Next] ****
ok: [web-1]
`,
			want: []string{"Shell|web-1|ignored|non-zero return code", "Next|web-1|ok|"},
		},
		{
			name: "a looped include names its item after the hosts",
			text: `
PLAY [Base] ****
TASK [Include] ****
included: /src/inc.yml for web-1, web-2 => (item=a)
included: /src/inc.yml for web-1, web-2 => (item=b)
`,
			want: []string{"Include|web-1|ok|", "Include|web-2|ok|"},
		},
		{
			name: "a fully skipped loop's own line folds into its items",
			text: `
PLAY [Base] ****
TASK [Skip] ****
skipping: [web-1] => (item=1)
skipping: [web-1] => (item=2)
skipping: [web-1]
`,
			want: []string{"Skip|web-1|skipped|"},
		},
		{
			name: "an include is an ok result per host",
			text: `
PLAY [Base] ****
TASK [Include] ****
included: /src/tasks for web.yml for web-1, web-2
`,
			want: []string{"Include|web-1|ok|", "Include|web-2|ok|"},
		},
		{
			name: "color codes are stripped",
			text: "\nPLAY [Base] ****\nTASK [Color] ****\n\x1b[0;33mchanged: [web-1]\x1b[0m\n",
			want: []string{"Color|web-1|changed|"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := results(parse(t, c.text))
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(c.want, "\n"))
			}
		})
	}
}

func TestBannersAndRecap(t *testing.T) {
	evs := parse(t, `
PLAY [Web [prod]] [CHECK MODE] *************************************************

TASK [nginx : Write the site config] *******************************************
changed: [web-1]

RUNNING HANDLER [nginx : Reload] ***********************************************
changed: [web-1]

PLAY RECAP *********************************************************************
web-1                      : ok=2    changed=2    unreachable=0    failed=0    skipped=0    rescued=1    ignored=0
web-2  : ok=1 changed=0 unreachable=1 failed=0
`)
	if evs[0].Kind != events.KindPlaybook || evs[0].File != "site.yml" || evs[0].Time != 100 {
		t.Errorf("first event %+v, want the playbook", evs[0])
	}
	play := evs[1]
	if play.Kind != events.KindPlay || play.Name != "Web [prod]" || play.UUID == "" {
		t.Errorf("play %+v", play)
	}
	if _, ok := play.TaskCount(); ok {
		t.Error("the text has no task count")
	}
	task, handler := evs[2], evs[4]
	if task.Name != "nginx : Write the site config" || task.Role != "nginx" || task.Handler {
		t.Errorf("task %+v", task)
	}
	if !handler.Handler || handler.Role != "nginx" || handler.UUID == task.UUID {
		t.Errorf("handler %+v", handler)
	}
	stats := evs[len(evs)-1]
	if stats.Kind != events.KindStats {
		t.Fatalf("last event %+v, want stats", stats)
	}
	if s := stats.Recap["web-1"]; s.OK != 2 || s.Changed != 2 || s.Rescued != 1 {
		t.Errorf("web-1 %+v", s)
	}
	if s := stats.Recap["web-2"]; s.OK != 1 || s.Unreachable != 1 {
		t.Errorf("web-2 %+v", s)
	}
}

func TestNotDefaultCallback(t *testing.T) {
	var n int
	p := New(Playbook{}, nil, func(events.Event) { n++ })
	for _, l := range []string{"web-1 | SUCCESS => {", `    "changed": false`, "}", "web-1 | CHANGED | rc=0 >>"} {
		p.Line(l)
	}
	p.Finish()
	if p.Played() || n != 0 {
		t.Errorf("played %v, %d events from minimal output", p.Played(), n)
	}
}
