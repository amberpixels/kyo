// Package textparse turns the text of ansible's default stdout callback into
// the same events kyo's callback plugin sends, for runs where the plugin can't
// load. The text carries less: no task totals, no diff sizes, no structured
// warnings, and a rescued failure looks like any other until the recap.
package textparse

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/amberpixels/kyo/internal/events"
)

// loopFailedMsg is the message ansible gives a loop's own result when an item
// failed; the text prints only the items' messages.
const loopFailedMsg = "One or more items failed"

// msgLimit matches the plugin's cap on a result message, in characters.
const msgLimit = 2000

// Playbook is what the playbook event carries; the text never prints it, so
// the caller reads it from the command line.
type Playbook struct {
	File        string
	Check, Diff bool
}

// Parser consumes stdout one line at a time. It is not safe for concurrent use.
type Parser struct {
	pb   Playbook
	now  func() time.Time
	emit func(events.Event)

	started bool // the playbook event went out
	plays   int
	tasks   int
	task    string // current task uuid

	body     *body   // a result whose body is still being read
	held     *result // a failure waiting one line for "...ignoring"
	items    map[string]*result
	order    []string        // item hosts in first-seen order
	included map[string]bool // hosts this task's include lines already reported

	recap map[string]events.HostStats // non-nil inside the PLAY RECAP block
}

// New returns a Parser that sends events to emit, stamped by now (nil means
// time.Now).
func New(pb Playbook, now func() time.Time, emit func(events.Event)) *Parser {
	if now == nil {
		now = time.Now
	}
	return &Parser{pb: pb, now: now, emit: emit, items: map[string]*result{}, included: map[string]bool{}}
}

// Played reports whether a PLAY banner has been seen, the sign that the output
// is the default callback's.
func (p *Parser) Played() bool { return p.plays > 0 }

type result struct {
	host   string
	status events.Status
	msg    string
	item   bool
}

// body accumulates a multi-line result body: JSON until its braces balance,
// YAML while lines stay indented.
type body struct {
	res   *result
	yaml  bool
	lines []string
	depth int
	inStr bool
	esc   bool
}

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// Line feeds one line of stdout.
func (p *Parser) Line(raw string) {
	line := ansiRE.ReplaceAllString(strings.TrimRight(raw, "\r"), "")

	if b := p.body; b != nil {
		if !b.yaml {
			b.add(line)
			if b.depth <= 0 {
				p.endBody()
			}
			return
		}
		// block scalars carry empty lines; only an unindented line ends the body
		if line == "" || strings.HasPrefix(line, " ") {
			b.lines = append(b.lines, line)
			return
		}
		p.endBody()
	}

	if p.held != nil {
		r := p.held
		p.held = nil
		if strings.TrimSpace(line) == "...ignoring" {
			r.status = events.StatusIgnored
			p.result(r)
			return
		}
		p.result(r)
	}
	// After a loop, "...ignoring" follows each failed host's items, but other
	// hosts' items can print in between, so it can't name its host. ignore_errors
	// is a task keyword, so it applies to every host whose items failed.
	if strings.TrimSpace(line) == "...ignoring" {
		for _, r := range p.items {
			if r.status == events.StatusFailed {
				r.status = events.StatusIgnored
			}
		}
		return
	}

	if p.recap != nil {
		if p.recapLine(line) {
			return
		}
		if strings.TrimSpace(line) == "" {
			p.endRecap()
			return
		}
	}

	if kind, name, ok := banner(line); ok {
		p.banner(kind, name)
		return
	}
	p.resultLine(line)
}

// Finish ends whatever is still open, for the end of the output.
func (p *Parser) Finish() {
	if p.body != nil {
		p.endBody()
	}
	if p.held != nil {
		p.result(p.held)
		p.held = nil
	}
	p.flushItems()
	p.endRecap()
}

func (p *Parser) send(ev events.Event) {
	ev.V = 1
	ev.Time = float64(p.now().UnixNano()) / float64(time.Second)
	p.emit(ev)
}

func (p *Parser) start() {
	if p.started {
		return
	}
	p.started = true
	p.send(events.Event{Kind: events.KindPlaybook, File: p.pb.File, Check: p.pb.Check, Diff: p.pb.Diff})
}

// banner returns the kind and name of a PLAY, TASK, RUNNING HANDLER or PLAY
// RECAP banner line.
func banner(line string) (string, string, bool) {
	s := strings.TrimRight(line, " ")
	if !strings.HasSuffix(s, "*") {
		return "", "", false
	}
	s = strings.TrimRight(strings.TrimRight(s, "*"), " ")
	if s == "PLAY RECAP" {
		return "recap", "", true
	}
	s = strings.TrimSuffix(s, " [CHECK MODE]")
	for _, k := range []string{"PLAY", "TASK", "RUNNING HANDLER"} {
		if rest, ok := strings.CutPrefix(s, k+" ["); ok && strings.HasSuffix(rest, "]") {
			return k, strings.TrimSuffix(rest, "]"), true
		}
	}
	return "", "", false
}

func (p *Parser) banner(kind, name string) {
	p.flushItems()
	switch kind {
	case "recap":
		p.recap = map[string]events.HostStats{}
	case "PLAY":
		p.start()
		p.plays++
		p.task = ""
		p.send(events.Event{Kind: events.KindPlay, Name: name, UUID: "text-play-" + strconv.Itoa(p.plays)})
	default:
		p.start()
		p.tasks++
		p.task = "text-task-" + strconv.Itoa(p.tasks)
		role := ""
		if r, _, ok := strings.Cut(name, " : "); ok {
			role = r
		}
		p.send(
			events.Event{
				Kind:    events.KindTask,
				Name:    name,
				Role:    role,
				UUID:    p.task,
				Handler: kind == "RUNNING HANDLER",
			},
		)
	}
}

var resultRE = regexp.MustCompile(`^(ok|changed|skipping|failed|fatal): \[([^\]]*)\](.*)$`)

func (p *Parser) resultLine(line string) {
	if p.task == "" {
		return
	}
	// an include task's result, which the recap counts as ok on each host
	if rest, ok := strings.CutPrefix(line, "included: "); ok {
		if _, hosts, ok := strings.CutLast(rest, " for "); ok {
			hosts, _, _ = strings.Cut(hosts, " => (item=") // a looped include names its item
			// a looped include prints a line per item, but is one result per host
			for h := range strings.SplitSeq(hosts, ", ") {
				if !p.included[h] {
					p.included[h] = true
					p.result(&result{host: h, status: events.StatusOK})
				}
			}
		}
		return
	}
	m := resultRE.FindStringSubmatch(line)
	if m == nil {
		return
	}
	host, _, _ := strings.Cut(m[2], " -> ") // delegate_to prints "host -> delegate"
	r := &result{host: host}
	rest := m[3]
	switch m[1] {
	case "ok":
		r.status = events.StatusOK
	case "changed":
		r.status = events.StatusChanged
	case "skipping":
		r.status = events.StatusSkipped
	case "failed":
		r.status = events.StatusFailed
	case "fatal":
		r.status = events.StatusFailed
		switch {
		case strings.HasPrefix(rest, ": UNREACHABLE!"):
			r.status = events.StatusUnreachable
			rest = strings.TrimPrefix(rest, ": UNREACHABLE!")
		case strings.HasPrefix(rest, ": FAILED!"):
			rest = strings.TrimPrefix(rest, ": FAILED!")
		default:
			return
		}
	}

	// loop items: "ok: [h] => (item=x) => {...}" or "failed: [h] (item=x) => {...}"
	rest = strings.TrimPrefix(rest, " =>")
	if after, ok := strings.CutPrefix(strings.TrimLeft(rest, " "), "(item="); ok {
		r.item = true
		rest = after[itemEnd(after):]
	}

	rest = strings.TrimLeft(rest, " ")
	rest = strings.TrimPrefix(rest, "=>")
	switch rest = strings.TrimSpace(rest); {
	case rest == "":
		if strings.HasSuffix(strings.TrimRight(m[3], " "), "=>") {
			p.body = &body{res: r, yaml: true}
			return
		}
		p.complete(r)
	case strings.HasPrefix(rest, "{"):
		b := &body{res: r}
		b.add(rest)
		if b.depth <= 0 {
			p.body = b
			p.endBody()
			return
		}
		p.body = b
	default:
		p.complete(r)
	}
}

// itemEnd returns the index just past the ")" closing an "(item=" label.
func itemEnd(s string) int {
	depth := 1
	for i, c := range s {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(s)
}

func (b *body) add(line string) {
	b.lines = append(b.lines, line)
	for i := range len(line) {
		c := line[i]
		switch {
		case b.esc:
			b.esc = false
		case b.inStr && c == '\\':
			b.esc = true
		case c == '"':
			b.inStr = !b.inStr
		case b.inStr:
		case c == '{' || c == '[':
			b.depth++
		case c == '}' || c == ']':
			b.depth--
		}
	}
}

func (p *Parser) endBody() {
	b := p.body
	p.body = nil
	if b.yaml {
		b.res.msg = yamlMsg(b.lines)
	} else {
		b.res.msg = jsonMsg(strings.Join(b.lines, "\n"))
	}
	p.complete(b.res)
}

// complete routes a finished result line: items collect per host, a failure
// waits for a possible "...ignoring", the rest go out now.
func (p *Parser) complete(r *result) {
	r.msg = truncate(r.msg)
	// a fully skipped loop prints "skipping: [h]" after its items: the same result
	if r.item || p.items[r.host] != nil {
		p.addItem(r)
		return
	}
	if r.status == events.StatusFailed {
		p.held = r
		return
	}
	p.result(r)
}

func (p *Parser) result(r *result) {
	ev := events.Event{Kind: events.KindResult, Host: r.host, UUID: p.task, Status: r.status, Msg: r.msg}
	p.send(ev)
}

// rank orders statuses for collapsing loop items into the host's one result.
var rank = map[events.Status]int{
	events.StatusSkipped:     0,
	events.StatusOK:          1,
	events.StatusChanged:     2,
	events.StatusIgnored:     3,
	events.StatusFailed:      4,
	events.StatusUnreachable: 5,
}

func (p *Parser) addItem(r *result) {
	cur := p.items[r.host]
	if cur == nil {
		p.items[r.host] = r
		p.order = append(p.order, r.host)
		return
	}
	if rank[r.status] > rank[cur.status] {
		cur.status, cur.msg = r.status, r.msg
	}
}

func (p *Parser) flushItems() {
	if p.body != nil {
		p.endBody()
	}
	if p.held != nil {
		p.result(p.held)
		p.held = nil
	}
	for _, h := range p.order {
		r := p.items[h]
		if r.status == events.StatusFailed || r.status == events.StatusIgnored {
			r.msg = loopFailedMsg
		}
		p.result(r)
	}
	clear(p.items)
	clear(p.included)
	p.order = nil
}

var recapRE = regexp.MustCompile(`^(\S+)\s+:\s+(ok=\d+.*)$`)

func (p *Parser) recapLine(line string) bool {
	m := recapRE.FindStringSubmatch(strings.TrimSpace(line))
	if m == nil {
		return false
	}
	var s events.HostStats
	for f := range strings.FieldsSeq(m[2]) {
		k, v, ok := strings.Cut(f, "=")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			continue
		}
		switch k {
		case "ok":
			s.OK = n
		case "changed":
			s.Changed = n
		case "unreachable":
			s.Unreachable = n
		case "failed":
			s.Failures = n
		case "skipped":
			s.Skipped = n
		case "rescued":
			s.Rescued = n
		case "ignored":
			s.Ignored = n
		}
	}
	p.recap[m[1]] = s
	return true
}

func (p *Parser) endRecap() {
	if p.recap == nil {
		return
	}
	if len(p.recap) > 0 {
		p.start()
		p.send(events.Event{Kind: events.KindStats, Recap: p.recap})
	}
	p.recap = nil
}

// jsonMsg pulls msg out of a JSON result body, formatted the way the plugin's
// str() would for a string, and as JSON otherwise.
func jsonMsg(s string) string {
	var v struct {
		Msg json.RawMessage `json:"msg"`
	}
	if json.Unmarshal([]byte(s), &v) != nil || len(v.Msg) == 0 {
		return ""
	}
	var str string
	if json.Unmarshal(v.Msg, &str) == nil {
		return str
	}
	return string(v.Msg)
}

// yamlMsg pulls a top-level msg key out of a YAML result body: a plain or
// quoted scalar, or a literal or folded block.
func yamlMsg(lines []string) string {
	indent := -1
	for _, l := range lines {
		if t := strings.TrimLeft(l, " "); t != "" {
			indent = len(l) - len(t)
			break
		}
	}
	if indent < 0 {
		return ""
	}
	for i, l := range lines {
		if len(l) <= indent || l[indent] == ' ' || !strings.HasPrefix(l[indent:], "msg:") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(l[indent:], "msg:"))
		switch {
		case strings.HasPrefix(v, "|") || strings.HasPrefix(v, ">"):
			return blockScalar(v, lines[i+1:], indent)
		case strings.HasPrefix(v, "'") && strings.HasSuffix(v, "'") && len(v) >= 2:
			return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		case strings.HasPrefix(v, `"`):
			if s, err := strconv.Unquote(v); err == nil {
				return s
			}
			return strings.Trim(v, `"`)
		default:
			return v
		}
	}
	return ""
}

func blockScalar(header string, rest []string, indent int) string {
	var out []string
	inner := -1
	for _, l := range rest {
		t := strings.TrimLeft(l, " ")
		if t == "" {
			out = append(out, "")
			continue
		}
		n := len(l) - len(t)
		if n <= indent {
			break
		}
		if inner < 0 {
			inner = n
		}
		out = append(out, l[min(inner, n):])
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	sep := "\n"
	if header[0] == '>' {
		sep = " "
	}
	s := strings.Join(out, sep)
	if !strings.Contains(header, "-") {
		s += "\n"
	}
	return s
}

func truncate(s string) string {
	if r := []rune(s); len(r) > msgLimit {
		return string(r[:msgLimit])
	}
	return s
}
