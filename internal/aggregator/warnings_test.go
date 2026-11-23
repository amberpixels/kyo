package aggregator

import (
	"slices"
	"testing"

	"github.com/amberpixels/kyo/internal/events"
)

func groupByLine(s Snapshot, prefix string) *WarningGroup {
	for i, g := range s.Warnings {
		if len(g.Sample) >= len(prefix) && g.Sample[:len(prefix)] == prefix {
			return &s.Warnings[i]
		}
	}
	return nil
}

func TestStderrWarnings(t *testing.T) {
	a := New(fixedNow)
	stderrFixture(t, a, "warnings.stderr")
	a.Finish(fixedNow())
	s := a.Snapshot()

	if g := groupByLine(s, "Deprecation warnings can be disabled"); g != nil {
		t.Errorf("boilerplate kept: %+v", g)
	}

	cond := groupByLine(s, "Conditionals should not be surrounded")
	if cond == nil || cond.Kind != KindDeprecation || cond.Count != 2 {
		t.Fatalf("conditional deprecation %+v", cond)
	}
	if cond.Origin != "/work/site/roles/app/tasks/main.yml:10:13" {
		t.Errorf("origin %q", cond.Origin)
	}
	if cond.Line != "Conditionals should not be surrounded by templating delimiters such as {{ }} or {% %}." {
		t.Errorf("one-line form %q", cond.Line)
	}

	wrapped := groupByLine(s, "The value 8080")
	want := "The value 8080 (type int) in a string field was converted to '8080' (type string). " +
		"If this does not look like what you expect, quote the entire value to ensure it does not change."
	if wrapped == nil || wrapped.Sample != want || wrapped.Count != 1 || wrapped.Kind != KindWarning {
		t.Errorf("wrapped warning %+v", wrapped)
	}

	// the colored line is read like any other
	if g := groupByLine(s, "Could not match supplied host pattern, ignoring: db"); g == nil || g.Count != 1 {
		t.Errorf("colored warning %+v", g)
	}

	// 2 conditional + interpreter + wrapped + two host patterns
	if s.WarnTotal != 6 || s.Deprecations != 2 || len(s.Warnings) != 5 {
		t.Errorf("total %d deprecations %d groups %d: %+v", s.WarnTotal, s.Deprecations, len(s.Warnings), s.Warnings)
	}
	if s.Warnings[0].Template != cond.Template {
		t.Errorf("most frequent group first, got %+v", s.Warnings[0])
	}
}

// The default callback re-prints a result's warning on stderr, with ansible's
// help text appended; the two copies count once whichever stream is read first.
func TestWarningDedupeAcrossSources(t *testing.T) {
	const text = "Host 'web-2' is using the discovered Python interpreter at '/usr/bin/python3.12'."
	const help = " See https://docs.ansible.com/interpreter_discovery.html for more information."
	result := events.Event{
		Kind: events.KindResult, Host: "web-2", UUID: "t1", Status: events.StatusOK, Warnings: []string{text},
	}
	for _, order := range []string{"result first", "stderr first"} {
		t.Run(order, func(t *testing.T) {
			a := New(fixedNow)
			if order == "result first" {
				a.Feed(result)
				a.Stderr("[WARNING]: " + text + help)
			} else {
				a.Stderr("[WARNING]: " + text + help)
				a.Feed(result)
			}
			s := a.Snapshot()
			if len(s.Warnings) != 1 || s.Warnings[0].Count != 1 ||
				!slices.Equal(s.Warnings[0].Hosts, []string{"web-2"}) {
				t.Errorf("warnings %+v", s.Warnings)
			}
		})
	}
}

// A deprecation's stderr copy carries ansible's removal notice.
func TestDeprecationDedupeAcrossSources(t *testing.T) {
	a := New(fixedNow)
	a.Feed(events.Event{
		Kind: events.KindResult, Host: "web-1", UUID: "t", Status: events.StatusOK,
		Deprecations: []string{"The 'warn' parameter is deprecated."},
	})
	a.Stderr("[DEPRECATION WARNING]: The 'warn' parameter is deprecated. " +
		"This feature will be removed from ansible-core version 2.30.")
	a.Stderr("[DEPRECATION WARNING]: The 'warn' parameterization is new.")
	s := a.Snapshot()
	if len(s.Warnings) != 2 || s.Deprecations != 2 {
		t.Errorf("warnings %+v: a longer word is not an extension", s.Warnings)
	}
	if g := groupByLine(s, "The 'warn' parameter is"); g == nil || g.Count != 1 {
		t.Errorf("deprecation %+v", g)
	}
}

func TestWarningsGroupAcrossHosts(t *testing.T) {
	a := New(fixedNow)
	a.Feed(events.Event{Kind: events.KindPlay, UUID: "p", Hosts: []string{"web-1", "web-2", "web-3"}})
	for _, h := range []string{"web-1", "web-2", "web-3"} {
		a.Feed(events.Event{
			Kind:     events.KindResult,
			Host:     h,
			UUID:     "t",
			Status:   events.StatusOK,
			Warnings: []string{"Host '" + h + "' is using the discovered Python interpreter at '/usr/bin/python3'"},
			Deprecations: []string{
				"Module defaults for " + h + " from /etc/ansible/defaults/" + h + ".yml are deprecated",
			},
		})
	}
	s := a.Snapshot()
	if len(s.Warnings) != 2 {
		t.Fatalf("groups %+v", s.Warnings)
	}
	for _, g := range s.Warnings {
		if g.Count != 3 || len(g.Hosts) != 3 {
			t.Errorf("group %+v, want 3 occurrences on 3 hosts", g)
		}
	}
	dep := groupByLine(s, "Module defaults")
	if dep == nil || dep.Kind != KindDeprecation ||
		dep.Template != "Module defaults for <host> from <path> are deprecated" {
		t.Errorf("deprecation %+v", dep)
	}
	if s.Deprecations != 3 || s.WarnTotal != 6 {
		t.Errorf("deprecations %d total %d", s.Deprecations, s.WarnTotal)
	}
}

func TestFingerprint(t *testing.T) {
	w := newWarnBook()
	w.hosts["web-1"] = true
	cases := map[string]string{
		"Unable to read '/etc/app.conf' on web-1.":        "Unable to read '…' on <host>.",
		`Using "prod" vars from ./group_vars/all.yml`:     `Using "…" vars from <path>`,
		"See https://docs.ansible.com/x.html for more":    "See https://docs.ansible.com/x.html for more",
		"This doesn't look right, it's fine":              "This doesn't look right, it's fine",
		"Skipping roles/app/tasks/extra.yml (not found)":  "Skipping <path> (not found)",
		"Origin-like /work/site.yml:10:13 is a path, too": "Origin-like <path> is a path, too",
	}
	for in, want := range cases {
		if got := w.fingerprint(normalize(in)); got != want {
			t.Errorf("fingerprint(%q) = %q, want %q", in, got, want)
		}
	}
}
