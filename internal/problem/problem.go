// Package problem loads a problem's statement and test cases, and renders each test with fresh
// random values for a run (so hardcoded answers can't pass).
package problem

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/dizziedbliss/meow-pspsps/internal/runner"
	"github.com/dizziedbliss/meow-pspsps/internal/workflow"
)

// Problem is one problem statement plus its tests.
type Problem struct {
	ID             string               `yaml:"id"`
	Title          string               `yaml:"title"`
	Trigger        workflow.TriggerKind `yaml:"trigger"` // webhook | chat
	Method         string               `yaml:"method"`  // webhook only; default POST
	StepTimeoutSec int                  `yaml:"step_timeout_sec"`
	Timezone       string               `yaml:"timezone"` // for date helpers and the fake calendar
	Mocked         []string             `yaml:"mocked"`   // credential types the judge simulates
	Parallel       int                  `yaml:"parallel"` // tests run at once per run (default 4); lower it for rate-limited LLMs
	Statement      string               `yaml:"-"`        // statement.md, shown to teams
	Tests          []*TestDef           `yaml:"-"`
	Dir            string               `yaml:"-"`
}

// TestDef is one test case as written in tests/*.yaml.
type TestDef struct {
	ID       string              `yaml:"id"`
	Category string              `yaml:"category"`
	Visible  bool                `yaml:"visible"`
	Weight   float64             `yaml:"weight"`
	Note     string              `yaml:"note"` // organiser-only explanation
	Vars     map[string]VarGen   `yaml:"vars"`
	Let      []map[string]string `yaml:"let"` // computed from vars, in order
	Steps    []StepDef           `yaml:"steps"`
	Expect   *Expect             `yaml:"expect"`   // checked against the last step
	Calendar []CalendarItem      `yaml:"calendar"` // events already on the fake calendar
}

// CalendarItem seeds the fake calendar: an all-day date, or a start/end date-time.
type CalendarItem struct {
	Summary string `yaml:"summary"`
	Date    string `yaml:"date"`
	Start   string `yaml:"start"`
	End     string `yaml:"end"`
}

// VarGen generates one random value. Exactly one field is set.
type VarGen struct {
	Int   []int     `yaml:"int"`   // [min, max]
	Float []float64 `yaml:"float"` // [min, max], 2 decimals
	Pick  []any     `yaml:"pick"`
	Word  bool      `yaml:"word"`
	Name  bool      `yaml:"name"`
	ID    string    `yaml:"id"` // prefix, e.g. "ORD-" → ORD-48213
}

// StepDef is one request.
type StepDef struct {
	Method  string            `yaml:"method"`
	Query   map[string]string `yaml:"query"`
	Headers map[string]string `yaml:"headers"`
	Body    any               `yaml:"body"`
	Raw     string            `yaml:"raw"`
	Session string            `yaml:"session"` // chat: a label; same label = same conversation
	Message string            `yaml:"message"`
	Chat    string            `yaml:"chat"` // telegram: a label; same label = same chat (and memory)
	From    string            `yaml:"from"` // telegram: sender's first name
	Expect  *Expect           `yaml:"expect"`
}

// Expect is what a response must satisfy.
type Expect struct {
	Status      int         `yaml:"status" json:"status,omitempty"` // 0 = any 2xx
	StatusIn    []int       `yaml:"status_in" json:"statusIn,omitempty"`
	JSON        []PathCheck `yaml:"json" json:"json,omitempty"`
	Contains    []string    `yaml:"contains" json:"contains,omitempty"` // case-insensitive, whole body
	NotContains []string    `yaml:"not_contains" json:"notContains,omitempty"`
	Regex       []string    `yaml:"regex" json:"regex,omitempty"`
	MaxMs       int         `yaml:"max_ms" json:"maxMs,omitempty"`
	Rubric      string      `yaml:"rubric" json:"rubric,omitempty"`            // LLM judge grades the answer
	RubricField string      `yaml:"rubric_field" json:"rubricField,omitempty"` // JSON path to the answer; "" = body
}

// PathCheck checks one value inside a JSON response. Path is dotted with [n] indexes; "" = root.
type PathCheck struct {
	Path        string   `yaml:"path" json:"path"`
	Equals      any      `yaml:"equals" json:"equals,omitempty"`
	IEquals     string   `yaml:"iequals" json:"iequals,omitempty"`
	Contains    string   `yaml:"contains" json:"contains,omitempty"`
	NotContains string   `yaml:"not_contains" json:"notContains,omitempty"`
	Regex       string   `yaml:"regex" json:"regex,omitempty"`
	Exists      *bool    `yaml:"exists" json:"exists,omitempty"`
	Type        string   `yaml:"type" json:"type,omitempty"` // string|number|bool|array|object|null
	Len         *int     `yaml:"len" json:"len,omitempty"`
	MinLen      *int     `yaml:"min_len" json:"minLen,omitempty"`
	MaxLen      *int     `yaml:"max_len" json:"maxLen,omitempty"`
	Gte         *float64 `yaml:"gte" json:"gte,omitempty"`
	Lte         *float64 `yaml:"lte" json:"lte,omitempty"`
	Approx      *float64 `yaml:"approx" json:"approx,omitempty"` // with Equals: allowed numeric difference
	OneOf       []any    `yaml:"one_of" json:"oneOf,omitempty"`
}

// Load reads problems/<dir>/problem.yaml, statement.md and tests/*.yaml.
func Load(dir string) (*Problem, error) {
	b, err := os.ReadFile(filepath.Join(dir, "problem.yaml"))
	if err != nil {
		return nil, err
	}
	p := &Problem{Dir: dir}
	if err := yaml.Unmarshal(b, p); err != nil {
		return nil, fmt.Errorf("%s/problem.yaml: %w", dir, err)
	}
	if p.ID == "" {
		p.ID = filepath.Base(dir)
	}
	if p.Trigger == "" {
		p.Trigger = workflow.TriggerWebhook
	}
	if p.Method == "" && p.Trigger == workflow.TriggerWebhook {
		p.Method = "POST"
	}
	if p.StepTimeoutSec == 0 {
		p.StepTimeoutSec = 45
	}
	if p.Timezone == "" {
		p.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return nil, fmt.Errorf("%s: bad timezone %q", dir, p.Timezone)
	}
	if st, err := os.ReadFile(filepath.Join(dir, "statement.md")); err == nil {
		p.Statement = string(st)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "tests", "*.yaml"))
	sort.Strings(files)
	seen := map[string]bool{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var defs []*TestDef
		if err := yaml.Unmarshal(raw, &defs); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		for _, d := range defs {
			if d.ID == "" || seen[d.ID] {
				return nil, fmt.Errorf("%s: test id %q missing or duplicated", f, d.ID)
			}
			seen[d.ID] = true
			if d.Weight == 0 {
				d.Weight = 1
			}
			if len(d.Steps) == 0 {
				return nil, fmt.Errorf("%s: test %s has no steps", f, d.ID)
			}
			p.Tests = append(p.Tests, d)
		}
	}
	if len(p.Tests) == 0 {
		return nil, fmt.Errorf("%s: no tests found in tests/*.yaml", dir)
	}
	return p, nil
}

// LoadAll loads every problem directory under root, keyed by id.
func LoadAll(root string) (map[string]*Problem, error) {
	dirs, _ := filepath.Glob(filepath.Join(root, "*", "problem.yaml"))
	out := map[string]*Problem{}
	for _, f := range dirs {
		p, err := Load(filepath.Dir(f))
		if err != nil {
			return nil, err
		}
		out[p.ID] = p
	}
	return out, nil
}

// Rendered is one test made concrete for one run.
type Rendered struct {
	Def     *TestDef
	Job     runner.JobTest
	Expects []*Expect // one per step; nil = nothing checked on that step
	Vars    map[string]any
}

// Render makes every test concrete for a run. The same seed always gives the same values, so the
// central server re-renders at grading time instead of storing expected answers.
func (p *Problem) Render(seed int64, runID int64) ([]*Rendered, error) {
	out := make([]*Rendered, 0, len(p.Tests))
	loc, _ := time.LoadLocation(p.Timezone)
	fm := funcsFor(loc)
	for _, d := range p.Tests {
		r, err := d.render(seed, runID, fm, p.Timezone)
		if err != nil {
			return nil, fmt.Errorf("test %s: %w", d.ID, err)
		}
		out = append(out, r)
	}
	return out, nil
}

var firstNames = []string{"Asha", "Ravi", "Meera", "Kabir", "Zoya", "Arjun", "Ishaan", "Priya", "Noor", "Dev", "Anika", "Rohan", "Sana", "Vikram", "Tara", "Omar"}
var words = []string{"amber", "basil", "cobalt", "delta", "ember", "fjord", "garnet", "harbor", "indigo", "juniper", "kelp", "lumen", "mosaic", "nimbus", "onyx", "pepper", "quartz", "russet", "sable", "tundra"}

func (d *TestDef) render(seed, runID int64, fm template.FuncMap, tz string) (*Rendered, error) {
	h := fnv.New64a()
	h.Write([]byte(d.ID))
	rng := rand.New(rand.NewSource(seed ^ int64(h.Sum64())))

	vars := map[string]any{}
	names := make([]string, 0, len(d.Vars))
	for k := range d.Vars {
		names = append(names, k)
	}
	sort.Strings(names) // deterministic order for the seeded generator
	for _, k := range names {
		g := d.Vars[k]
		switch {
		case len(g.Int) == 2:
			vars[k] = g.Int[0] + rng.Intn(g.Int[1]-g.Int[0]+1)
		case len(g.Float) == 2:
			v := g.Float[0] + rng.Float64()*(g.Float[1]-g.Float[0])
			vars[k] = math.Round(v*100) / 100
		case len(g.Pick) > 0:
			vars[k] = g.Pick[rng.Intn(len(g.Pick))]
		case g.Word:
			vars[k] = words[rng.Intn(len(words))] + strconv.Itoa(rng.Intn(90)+10)
		case g.Name:
			vars[k] = firstNames[rng.Intn(len(firstNames))]
		case g.ID != "":
			vars[k] = g.ID + strconv.Itoa(10000+rng.Intn(89999))
		default:
			return nil, fmt.Errorf("var %s has no generator", k)
		}
	}
	for _, kv := range d.Let {
		for k, tmpl := range kv {
			v, err := renderValueF(fm, tmpl, vars)
			if err != nil {
				return nil, fmt.Errorf("let %s: %w", k, err)
			}
			vars[k] = v
		}
	}

	r := &Rendered{Def: d, Vars: vars, Job: runner.JobTest{ID: d.ID}}
	if len(d.Calendar) > 0 || tz != "" {
		seed := &runner.MockSeed{Timezone: tz}
		for _, c := range d.Calendar {
			var it runner.MockSeedItem
			var err error
			if it.Summary, err = renderStringF(fm, c.Summary, vars); err != nil {
				return nil, err
			}
			if it.Date, err = renderStringF(fm, c.Date, vars); err != nil {
				return nil, err
			}
			if it.Start, err = renderStringF(fm, c.Start, vars); err != nil {
				return nil, err
			}
			if it.End, err = renderStringF(fm, c.End, vars); err != nil {
				return nil, err
			}
			seed.Events = append(seed.Events, it)
		}
		r.Job.Seed = seed
	}
	for i, s := range d.Steps {
		js := runner.JobStep{Method: s.Method}
		var err error
		if js.Query, err = renderMapF(fm, s.Query, vars); err != nil {
			return nil, err
		}
		if js.Headers, err = renderMapF(fm, s.Headers, vars); err != nil {
			return nil, err
		}
		if s.Body != nil {
			b, err := renderAnyF(fm, s.Body, vars)
			if err != nil {
				return nil, err
			}
			if js.Body, err = json.Marshal(b); err != nil {
				return nil, err
			}
		}
		if s.Raw != "" {
			if js.Raw, err = renderStringF(fm, s.Raw, vars); err != nil {
				return nil, err
			}
		}
		if s.Message != "" {
			if js.Message, err = renderStringF(fm, s.Message, vars); err != nil {
				return nil, err
			}
		}
		switch {
		case s.Chat != "":
			// Telegram chat ids are numbers; derive a stable one per run, test and label.
			h := fnv.New32a()
			fmt.Fprintf(h, "%d/%s/%s", runID, d.ID, s.Chat)
			js.Session = strconv.FormatUint(uint64(h.Sum32())+100000000, 10)
		case s.Session != "":
			js.Session = fmt.Sprintf("r%d-%s-%s", runID, d.ID, s.Session)
		case s.Message != "":
			js.Session = fmt.Sprintf("r%d-%s-%d", runID, d.ID, i) // fresh conversation per step
		}
		if s.From != "" {
			if js.From, err = renderStringF(fm, s.From, vars); err != nil {
				return nil, err
			}
		}
		r.Job.Steps = append(r.Job.Steps, js)

		exp := s.Expect
		if i == len(d.Steps)-1 && d.Expect != nil {
			exp = d.Expect
		}
		if exp != nil {
			re, err := renderExpectF(fm, exp, vars)
			if err != nil {
				return nil, err
			}
			exp = re
		}
		r.Expects = append(r.Expects, exp)
	}
	return r, nil
}

func renderExpectF(fm template.FuncMap, e *Expect, vars map[string]any) (*Expect, error) {
	// Round-trip through a generic value so every string inside gets templated.
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	var generic any
	if err := json.Unmarshal(b, &generic); err != nil {
		return nil, err
	}
	rendered, err := renderAnyF(fm, generic, vars)
	if err != nil {
		return nil, err
	}
	b, _ = json.Marshal(rendered)
	out := &Expect{}
	return out, json.Unmarshal(b, out)
}

var baseFuncs = template.FuncMap{
	"add":   func(a, b any) float64 { return num(a) + num(b) },
	"sub":   func(a, b any) float64 { return num(a) - num(b) },
	"mul":   func(a, b any) float64 { return num(a) * num(b) },
	"div":   func(a, b any) float64 { return num(a) / num(b) },
	"round": func(a any) float64 { return math.Round(num(a)*100) / 100 },
	"lower": strings.ToLower,
	"upper": strings.ToUpper,
	"json": func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	},
	"repeat": func(s string, n int) string { return strings.Repeat(s, n) },
	"seq": func(n any) []int {
		out := make([]int, int(num(n)))
		for i := range out {
			out[i] = i + 1
		}
		return out
	},
}

// funcsFor adds date helpers relative to today in the problem's timezone. N is a day offset:
// day 0 = today "2006-01-02", pretty 1 = "Friday, 6 Nov 2026", dom/month/mon/weekday/year parts.
func funcsFor(loc *time.Location) template.FuncMap {
	fm := template.FuncMap{}
	for k, v := range baseFuncs {
		fm[k] = v
	}
	at := func(n any) time.Time {
		t := time.Now().In(loc)
		return time.Date(t.Year(), t.Month(), t.Day(), 12, 0, 0, 0, loc).AddDate(0, 0, int(num(n)))
	}
	fm["day"] = func(n any) string { return at(n).Format("2006-01-02") }
	fm["pretty"] = func(n any) string { return at(n).Format("Monday, 2 Jan 2006") }
	fm["dom"] = func(n any) int { return at(n).Day() }
	fm["month"] = func(n any) string { return at(n).Format("January") }
	fm["mon"] = func(n any) string { return at(n).Format("Jan") }
	fm["weekday"] = func(n any) string { return at(n).Format("Monday") }
	fm["year"] = func(n any) int { return at(n).Year() }
	fm["ordinal"] = func(n any) string {
		d := int(num(n))
		suf := "th"
		if d%100 < 11 || d%100 > 13 {
			switch d % 10 {
			case 1:
				suf = "st"
			case 2:
				suf = "nd"
			case 3:
				suf = "rd"
			}
		}
		return strconv.Itoa(d) + suf
	}
	return fm
}

func num(v any) float64 {
	switch x := v.(type) {
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

func renderStringF(fm template.FuncMap, s string, vars map[string]any) (string, error) {
	if !strings.Contains(s, "{{") {
		return s, nil
	}
	t, err := template.New("").Funcs(fm).Option("missingkey=error").Parse(s)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, vars); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// renderValue renders a string; if it is a single {{...}} action whose result is JSON (a number,
// bool, array or object), the typed value is returned so bodies keep real numbers.
//
// A string starting with "=json " is rendered and then parsed as JSON, for building arrays/objects.
func renderValueF(fm template.FuncMap, s string, vars map[string]any) (any, error) {
	if rest, ok := strings.CutPrefix(s, "=json "); ok {
		out, err := renderStringF(fm, rest, vars)
		if err != nil {
			return nil, err
		}
		var v any
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			return nil, fmt.Errorf("=json value isn't valid JSON after rendering: %v", err)
		}
		return v, nil
	}
	out, err := renderStringF(fm, s, vars)
	if err != nil {
		return nil, err
	}
	t := strings.TrimSpace(s)
	if strings.HasPrefix(t, "{{") && strings.HasSuffix(t, "}}") && strings.Count(t, "{{") == 1 {
		var v any
		if json.Unmarshal([]byte(out), &v) == nil {
			if _, isStr := v.(string); !isStr {
				return v, nil
			}
		}
	}
	return out, nil
}

func renderAnyF(fm template.FuncMap, v any, vars map[string]any) (any, error) {
	switch x := v.(type) {
	case string:
		return renderValueF(fm, x, vars)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			r, err := renderAnyF(fm, vv, vars)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			r, err := renderAnyF(fm, vv, vars)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	}
	return v, nil
}

func renderMapF(fm template.FuncMap, m map[string]string, vars map[string]any) (map[string]string, error) {
	if len(m) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		r, err := renderStringF(fm, v, vars)
		if err != nil {
			return nil, err
		}
		out[k] = r
	}
	return out, nil
}

// Expect is what the workflow pre-check needs to know about this problem.
func (p *Problem) Expect() workflow.Expect {
	return workflow.Expect{Trigger: p.Trigger, Method: p.Method, Mocked: p.Mocked}
}
