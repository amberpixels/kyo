// Package events decodes the JSON lines kyo's callback plugin writes into
// typed Go values. Unknown event kinds and fields are kept or ignored, never
// fatal, so a newer plugin can't break an older reader.
package events

import (
	"encoding/json"
	"fmt"
)

// Kind names an event.
type Kind string

const (
	KindPlaybook Kind = "playbook"
	KindPlay     Kind = "play"
	KindTask     Kind = "task"
	KindInclude  Kind = "include"
	KindResult   Kind = "result"
	KindPrompt   Kind = "prompt"
	KindStats    Kind = "stats"
)

// Status is a per-host task outcome.
type Status string

const (
	StatusOK          Status = "ok"
	StatusChanged     Status = "changed"
	StatusSkipped     Status = "skipped"
	StatusFailed      Status = "failed"
	StatusIgnored     Status = "ignored"
	StatusUnreachable Status = "unreachable"
)

// DiffStat is the approximate size of a result's diff.
type DiffStat struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// HostStats is one host's line of the final recap.
type HostStats struct {
	OK          int `json:"ok"`
	Changed     int `json:"changed"`
	Failures    int `json:"failures"`
	Unreachable int `json:"unreachable"`
	Skipped     int `json:"skipped"`
	Rescued     int `json:"rescued"`
	Ignored     int `json:"ignored"`
}

// Event is one decoded line. Which fields are set depends on Kind.
type Event struct {
	V    int     `json:"v"`
	Kind Kind    `json:"ev"`
	Time float64 `json:"t"` // unix seconds, from the controller's clock

	// playbook
	File  string `json:"file,omitempty"`
	Check bool   `json:"check,omitempty"`
	Diff  bool   `json:"diff,omitempty"`

	// play, task
	Name    string `json:"name,omitempty"`
	UUID    string `json:"uuid,omitempty"`
	Role    string `json:"role,omitempty"`
	Action  string `json:"action,omitempty"`
	Handler bool   `json:"handler,omitempty"`

	// play, include: Tasks is the real task count (play: static and
	// tag-filtered; include: the included file's). Nil or -1 means unknown, so
	// an older plugin that omits it reads the same as one that couldn't count.
	Tasks *int `json:"tasks,omitempty"`
	// play: its resolved hosts (this serial batch); include: the hosts it ran for
	Hosts []string `json:"hosts,omitempty"`

	// result
	Host         string    `json:"host,omitempty"`
	Status       Status    `json:"status,omitempty"`
	Msg          string    `json:"msg,omitempty"`
	Rescued      bool      `json:"rescued,omitempty"` // a failure a rescue section catches
	DiffStat     *DiffStat `json:"-"`                 // wire key "diff"
	Warnings     []string  `json:"warnings,omitempty"`
	Deprecations []string  `json:"deprecations,omitempty"`

	// prompt
	Var string `json:"var,omitempty"`

	// stats
	Recap map[string]HostStats `json:"-"` // wire key "hosts"
}

// TaskCount returns Tasks and whether it is known.
func (ev Event) TaskCount() (int, bool) {
	if ev.Tasks == nil || *ev.Tasks < 0 {
		return 0, false
	}
	return *ev.Tasks, true
}

// Decode parses one line from the plugin.
func Decode(line []byte) (Event, error) {
	// "diff" is a bool on playbook events and an object on result events;
	// "hosts" is a list on play and include events and a map on stats events. Both are
	// held raw and resolved by kind.
	var ev Event
	type plain Event
	var p struct {
		plain

		Diff  json.RawMessage `json:"diff"`
		Hosts json.RawMessage `json:"hosts"`
	}
	if err := json.Unmarshal(line, &p); err != nil {
		return ev, fmt.Errorf("decoding event: %w", err)
	}
	ev = Event(p.plain)
	if ev.Kind == "" {
		return ev, fmt.Errorf("decoding event: no event kind in %q", line)
	}

	if len(p.Diff) > 0 {
		switch ev.Kind {
		case KindResult:
			var ds DiffStat
			if err := json.Unmarshal(p.Diff, &ds); err == nil {
				ev.DiffStat = &ds
			}
		default:
			_ = json.Unmarshal(p.Diff, &ev.Diff)
		}
	}
	if len(p.Hosts) > 0 {
		switch ev.Kind {
		case KindStats:
			_ = json.Unmarshal(p.Hosts, &ev.Recap)
		default:
			_ = json.Unmarshal(p.Hosts, &ev.Hosts)
		}
	}
	return ev, nil
}
