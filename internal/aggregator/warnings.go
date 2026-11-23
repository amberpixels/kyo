package aggregator

import (
	"path"
	"regexp"
	"slices"
	"strings"
)

// WarningKind classifies a warning group.
type WarningKind string

const (
	KindWarning     WarningKind = "warning"
	KindDeprecation WarningKind = "deprecation"
)

// WarningGroup is a fingerprinted family of warnings, as the panel shows it.
type WarningGroup struct {
	Kind     WarningKind // deprecation if any member is one
	Template string      // the fingerprint: paths, hosts and quoted values blanked
	Line     string      // first sentence of the sample, for the collapsed one-line form
	Sample   string      // full text of the first occurrence
	Origin   string      // "path:line:col" from the controller's Origin line, when it gave one
	Hosts    []string    // hosts that raised it, from structured results; sorted
	Count    int
}

type source int

const (
	sourceResult source = iota // structured, from a result event
	sourceStderr               // scraped from the controller's stderr
)

// warnBook groups warnings from both sources. The default callback re-prints
// every structured warning on stderr, so the same warning arrives twice, the
// stderr copy extended by ansible's help text or removal notice. A stderr text
// that starts with a structured one counts under it, and counting structured
// if any arrived, stderr otherwise, keeps the count independent of which
// stream was read first.
type warnBook struct {
	groups map[string]*warnGroup // by template
	seq    int
	hosts  map[string]bool // known host names, blanked in fingerprints

	open *stderrBlock // the stderr warning being read
}

type warnGroup struct {
	kind     WarningKind
	template string
	sample   string
	origin   string
	texts    map[string]*textCount // by normalized text
	hosts    map[string]bool
	seq      int
}

type textCount struct{ structured, stderr int }

type stderrBlock struct {
	kind      WarningKind
	text      string
	tmpl      string // its group, fixed at add time
	extending bool   // still reading wrapped continuation lines
}

func newWarnBook() warnBook {
	return warnBook{groups: map[string]*warnGroup{}, hosts: map[string]bool{}}
}

const boilerplate = "Deprecation warnings can be disabled"

var (
	ansiRE   = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)
	headerRE = regexp.MustCompile(`^\[(DEPRECATION WARNING|WARNING)\]:\s?(.*)$`)
	// any other "[LEVEL]:" header (an error, say) ends a warning block
	otherHeaderRE = regexp.MustCompile(`^\[[A-Z][A-Z ]*\]:`)
	// a quote opens only after a boundary, so "doesn't ... it's" is left alone
	singleQuotedRE = regexp.MustCompile(`(^|[\s(\[{=:,])'[^'\n]*'`)
	doubleQuotedRE = regexp.MustCompile(`(^|[\s(\[{=:,])"[^"\n]*"`)
)

// stderr reads one stderr line. A header opens a block that counts at once;
// lines up to the first blank or Origin line extend it (ansible-core 2.17
// wraps long messages); the source snippet after it is skipped.
func (w *warnBook) stderr(line string) {
	line = strings.TrimRight(ansiRE.ReplaceAllString(line, ""), "\r")

	if m := headerRE.FindStringSubmatch(line); m != nil {
		w.closeBlock()
		if strings.HasPrefix(m[2], boilerplate) {
			return
		}
		kind := KindWarning
		if m[1] == "DEPRECATION WARNING" {
			kind = KindDeprecation
		}
		w.open = &stderrBlock{kind: kind, text: m[2], extending: true}
		w.open.tmpl = w.add(kind, m[2], sourceStderr, "")
		return
	}
	if otherHeaderRE.MatchString(line) {
		w.closeBlock()
		return
	}
	b := w.open
	if b == nil || !b.extending {
		return
	}
	if strings.TrimSpace(line) == "" {
		b.extending = false
		return
	}
	if origin, ok := strings.CutPrefix(line, "Origin: "); ok {
		b.extending = false
		if g := w.groups[b.tmpl]; g != nil && g.origin == "" {
			g.origin = strings.TrimSpace(origin)
		}
		return
	}
	w.remove(b.tmpl, b.text)
	b.text += " " + line
	b.tmpl = w.add(b.kind, b.text, sourceStderr, "")
}

func (w *warnBook) closeBlock() { w.open = nil }

// add records one occurrence and returns its group's template.
func (w *warnBook) add(kind WarningKind, text string, src source, host string) string {
	norm := normalize(text)
	if norm == "" {
		return ""
	}
	if src == sourceStderr {
		if g, key := w.structuredPrefix(norm); g != nil {
			g.texts[key].stderr++
			if kind == KindDeprecation {
				g.kind = KindDeprecation
			}
			return g.template
		}
	}
	tmpl := w.fingerprint(norm)
	g := w.groups[tmpl]
	if g == nil {
		w.seq++
		g = &warnGroup{
			kind: kind, template: tmpl, sample: norm,
			texts: map[string]*textCount{}, hosts: map[string]bool{}, seq: w.seq,
		}
		w.groups[tmpl] = g
	}
	if kind == KindDeprecation {
		g.kind = KindDeprecation
	}
	tc := g.texts[norm]
	if tc == nil {
		tc = &textCount{}
		g.texts[norm] = tc
	}
	if src == sourceResult {
		tc.structured++
		w.absorbStderr(norm, tc)
	} else {
		tc.stderr++
	}
	if host != "" {
		g.hosts[host] = true
	}
	return tmpl
}

// structuredPrefix finds a structured text that stderr text norm extends.
func (w *warnBook) structuredPrefix(norm string) (*warnGroup, string) {
	for _, g := range w.groups {
		for key, tc := range g.texts {
			if tc.structured > 0 && extends(norm, key) {
				return g, key
			}
		}
	}
	return nil, ""
}

// absorbStderr moves stderr-only texts that extend structured text norm into
// its count, for a stderr copy that arrived first.
func (w *warnBook) absorbStderr(norm string, into *textCount) {
	for tmpl, g := range w.groups {
		for key, tc := range g.texts {
			if tc.structured == 0 && key != norm && extends(key, norm) {
				into.stderr += tc.stderr
				delete(g.texts, key)
			}
		}
		if len(g.texts) == 0 {
			delete(w.groups, tmpl)
		}
	}
}

// extends reports whether text is base, or base followed by more words.
func extends(text, base string) bool {
	return text == base || strings.HasPrefix(text, base+" ")
}

// remove takes back one stderr occurrence, so a wrapped block can be re-added
// with its longer text.
func (w *warnBook) remove(tmpl, text string) {
	norm := normalize(text)
	g := w.groups[tmpl]
	if g == nil || g.texts[norm] == nil {
		return
	}
	tc := g.texts[norm]
	tc.stderr--
	if tc.structured <= 0 && tc.stderr <= 0 {
		delete(g.texts, norm)
	}
	if len(g.texts) == 0 {
		delete(w.groups, g.template)
	}
}

func (g *warnGroup) count() int {
	n := 0
	for _, tc := range g.texts {
		if tc.structured > 0 {
			n += tc.structured
		} else {
			n += tc.stderr
		}
	}
	return n
}

func (w *warnBook) snapshot() ([]WarningGroup, int, int) {
	var out []WarningGroup
	seq := map[string]int{}
	total, deprecations := 0, 0
	for _, g := range w.groups {
		n := g.count()
		hosts := make([]string, 0, len(g.hosts))
		for h := range g.hosts {
			hosts = append(hosts, h)
		}
		slices.Sort(hosts)
		out = append(out, WarningGroup{
			Kind: g.kind, Template: g.template, Line: firstSentence(g.sample), Sample: g.sample,
			Origin: g.origin, Hosts: hosts, Count: n,
		})
		seq[g.template] = g.seq
		total += n
		if g.kind == KindDeprecation {
			deprecations += n
		}
	}
	slices.SortFunc(out, func(x, y WarningGroup) int {
		if x.Count != y.Count {
			return y.Count - x.Count
		}
		return seq[x.Template] - seq[y.Template]
	})
	return out, total, deprecations
}

// normalize collapses whitespace: the dedupe key between the two sources.
func normalize(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// fingerprint blanks what varies between instances of one warning: quoted
// values, paths and known host names. norm must be normalized.
func (w *warnBook) fingerprint(norm string) string {
	s := singleQuotedRE.ReplaceAllString(norm, "$1'…'")
	s = doubleQuotedRE.ReplaceAllString(s, `$1"…"`)
	words := strings.Split(s, " ")
	for i, word := range words {
		core := strings.TrimRight(word, ".,;:)")
		trail := word[len(core):]
		switch {
		case isPath(core):
			words[i] = "<path>" + trail
		case w.hosts[core]:
			words[i] = "<host>" + trail
		}
	}
	return strings.Join(words, " ")
}

func isPath(s string) bool {
	if s == "" || strings.Contains(s, "://") {
		return false
	}
	for _, p := range []string{"/", "~/", "./", "../"} {
		if strings.HasPrefix(s, p) && len(s) > len(p) {
			return true
		}
	}
	return strings.Contains(s, "/") && path.Ext(s) != ""
}

// firstSentence is the text up to its first ". ", for a one-line form.
func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i >= 0 {
		return s[:i+1]
	}
	return s
}
