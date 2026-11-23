package events

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDecodeKinds(t *testing.T) {
	cases := []struct {
		name  string
		line  string
		check func(t *testing.T, ev Event)
	}{
		{
			"playbook",
			`{"v":1,"ev":"playbook","t":1.5,"file":"site.yml","check":true,"diff":true}`,
			func(t *testing.T, ev Event) {
				if ev.File != "site.yml" || !ev.Check || !ev.Diff || ev.Time != 1.5 {
					t.Fatalf("got %+v", ev)
				}
			},
		},
		{
			"play",
			`{"v":1,"ev":"play","name":"Web","uuid":"u1","tasks":12,"hosts":["web-1"]}`,
			func(t *testing.T, ev Event) {
				if n, ok := ev.TaskCount(); ev.Kind != KindPlay || ev.Name != "Web" || !ok || n != 12 ||
					len(ev.Hosts) != 1 {
					t.Fatalf("got %+v", ev)
				}
			},
		},
		{"play with unknown count", `{"v":1,"ev":"play","name":"Web","tasks":-1}`, func(t *testing.T, ev Event) {
			if _, ok := ev.TaskCount(); ok {
				t.Fatalf("-1 must read as unknown: %+v", ev)
			}
		}},
		{
			"failed result caught by a rescue",
			`{"v":1,"ev":"result","host":"web-1","uuid":"u3","status":"failed","rescued":true}`,
			func(t *testing.T, ev Event) {
				if !ev.Rescued || ev.Status != StatusFailed {
					t.Fatalf("got %+v", ev)
				}
			},
		},
		{
			"result with diff",
			`{"v":1,"ev":"result","host":"web-1","uuid":"u2","status":"changed","diff":{"added":3,"removed":1},"warnings":["w"]}`,
			func(t *testing.T, ev Event) {
				if ev.Status != StatusChanged || ev.DiffStat == nil || ev.DiffStat.Added != 3 ||
					ev.DiffStat.Removed != 1 ||
					len(ev.Warnings) != 1 {
					t.Fatalf("got %+v", ev)
				}
				if ev.Diff {
					t.Fatal("result diff object must not set the playbook Diff flag")
				}
			},
		},
		{
			"result with diff text",
			`{"v":1,"ev":"result","host":"web-1","uuid":"u2","status":"changed","diff":{"added":1,"removed":0},"diff_text":"--- before\n+++ after\n+x"}`,
			func(t *testing.T, ev Event) {
				if ev.DiffText != "--- before\n+++ after\n+x" || ev.DiffStat == nil {
					t.Fatalf("got %+v", ev)
				}
			},
		},
		{
			"include",
			`{"v":1,"ev":"include","file":"extra.yml","hosts":["web-1","web-2"],"tasks":0}`,
			func(t *testing.T, ev Event) {
				if n, ok := ev.TaskCount(); len(ev.Hosts) != 2 || ev.Recap != nil || !ok || n != 0 {
					t.Fatalf("got %+v", ev)
				}
			},
		},
		{
			"stats",
			`{"v":1,"ev":"stats","hosts":{"web-1":{"ok":5,"changed":1,"failures":0,"unreachable":0,"skipped":2,"rescued":0,"ignored":1}}}`,
			func(t *testing.T, ev Event) {
				r, ok := ev.Recap["web-1"]
				if !ok || r.OK != 5 || r.Changed != 1 || r.Skipped != 2 || r.Ignored != 1 || ev.Hosts != nil {
					t.Fatalf("got %+v", ev)
				}
			},
		},
		{"unknown kind and field", `{"v":2,"ev":"future","novel":true}`, func(t *testing.T, ev Event) {
			if _, ok := ev.TaskCount(); ev.Kind != "future" || ev.V != 2 || ok {
				t.Fatalf("got %+v", ev)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ev, err := Decode([]byte(c.line))
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, ev)
		})
	}
}

func TestDecodeRejects(t *testing.T) {
	for _, line := range []string{`not json`, `{"v":1}`} {
		if _, err := Decode([]byte(line)); err == nil {
			t.Errorf("Decode(%q) should fail", line)
		}
	}
}

// TestEncodeRoundTrip pins Encode against every recorded fixture: decoding
// what it writes gives back the event it was given.
func TestEncodeRoundTrip(t *testing.T) {
	paths, err := filepath.Glob("../aggregator/testdata/*.jsonl")
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
			want, err := Decode(line)
			if err != nil {
				t.Fatalf("%s:%d: %v", path, i+1, err)
			}
			enc, err := Encode(want)
			if err != nil {
				t.Fatalf("%s:%d: %v", path, i+1, err)
			}
			got, err := Decode(enc)
			if err != nil {
				t.Fatalf("%s:%d: decoding %s: %v", path, i+1, enc, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s:%d: round trip\n got %+v\nwant %+v\n enc %s", path, i+1, got, want, enc)
			}
		}
	}
}
