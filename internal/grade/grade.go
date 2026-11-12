// Package grade turns a worker's raw results into per-test verdicts.
package grade

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dizziedbliss/meow-pspsps/internal/problem"
	"github.com/dizziedbliss/meow-pspsps/internal/runner"
)

// Verdicts, LeetCode-style.
const (
	Accepted    = "Accepted"
	WrongAnswer = "Wrong Answer"
	RuntimeErr  = "Runtime Error"
	TimeLimit   = "Time Limit"
	RateLimited = "Key Rate-Limited"
	KeyRejected = "Key Rejected"
	ModelBad    = "Model Unavailable"
	Invalid     = "Invalid Workflow"
	NotRun      = "Not Run"
	JudgeError  = "Judge Error"
)

// Check is one assertion's outcome.
type Check struct {
	What   string `json:"what"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Infra  bool   `json:"infra,omitempty"`
	// Admin-only (stripped from team views): which grader answered an AI-judged check.
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Cached   bool   `json:"cached,omitempty"`
}

// StepRecord is everything the judge saw for one step. Admin-only: team views never include it.
type StepRecord struct {
	Entry       string `json:"entry,omitempty"`
	Status      int    `json:"status"`
	Body        string `json:"body,omitempty"` // the full response (up to the runner's 256 KB cap)
	Mock        string `json:"mock,omitempty"` // JSON: what the simulated services saw during the step
	Ms          int64  `json:"ms"`
	ExecError   string `json:"execError,omitempty"`
	ErrorNode   string `json:"errorNode,omitempty"`
	ViaLastNode bool   `json:"viaLastNode,omitempty"` // the answer is the last node's output (webhook responds immediately)
	TimedOut    bool   `json:"timedOut,omitempty"`
	ConnErr     string `json:"connErr,omitempty"`
}

// TestVerdict is one test's grade.
type TestVerdict struct {
	ID       string  `json:"id"`
	Category string  `json:"category"`
	Visible  bool    `json:"visible"`
	Weight   float64 `json:"weight"`
	Verdict  string  `json:"verdict"`
	Checks   []Check `json:"checks,omitempty"`
	// Shown to teams only for visible tests:
	Input    string `json:"input,omitempty"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Error    string `json:"error,omitempty"`
	Ms       int64  `json:"ms"`
	// Admin-only, kept in full for after-the-event checks (storage is plentiful):
	Steps []StepRecord `json:"steps,omitempty"`
}

// Judge grades a free-text answer against a rubric.
type Judge interface {
	Grade(ctx context.Context, rubric, input, answer string) (ok bool, reason string, err error)
}

// JudgeInfo says who answered a rubric check.
type JudgeInfo struct {
	Provider string
	Model    string
	Cached   bool
}

// Informer is an optional extension of Judge that also reports which provider and model answered.
type Informer interface {
	GradeInfo(ctx context.Context, rubric, input, answer string) (ok bool, reason string, info JudgeInfo, err error)
}

// Test grades one rendered test against its raw result.
func Test(ctx context.Context, rt *problem.Rendered, tr *runner.TestResult, judge Judge) TestVerdict {
	v := gradeTest(ctx, rt, tr, judge)
	v.Error = withHint(v.Error) // plain next step for common n8n errors; only visible tests ever show it
	return v
}

func gradeTest(ctx context.Context, rt *problem.Rendered, tr *runner.TestResult, judge Judge) TestVerdict {
	d := rt.Def
	v := TestVerdict{ID: d.ID, Category: d.Category, Visible: d.Visible, Weight: d.Weight}
	v.Input = describeInput(rt)
	v.Expected = describeExpect(rt)
	if tr == nil || len(tr.Steps) == 0 {
		v.Verdict = NotRun
		return v
	}
	last := tr.Steps[len(tr.Steps)-1]
	v.Actual = clip(last.Body, 4000) // what a team may see of a sample; the admin gets every step in v.Steps
	for i, s := range tr.Steps {
		rec := StepRecord{Status: s.Status, Body: s.Body, Mock: s.Mock, Ms: s.DurationMs, ExecError: s.ExecError, ErrorNode: s.ErrorNode,
			ViaLastNode: s.ViaLastNode, TimedOut: s.TimedOut, ConnErr: s.ConnErr}
		if i < len(rt.Job.Steps) {
			rec.Entry = rt.Job.Steps[i].Entry
		}
		v.Steps = append(v.Steps, rec)
	}
	for _, s := range tr.Steps {
		v.Ms += s.DurationMs
	}

	for i, sr := range tr.Steps {
		if sr.ExecError != "" && v.Error == "" {
			v.Error = sr.ExecError
			if sr.ErrorNode != "" {
				v.Error = sr.ErrorNode + ": " + sr.ExecError
			}
		}
		exp := (*problem.Expect)(nil)
		if i < len(rt.Expects) {
			exp = rt.Expects[i]
		}
		switch {
		case tr.RateLimited:
			v.Verdict = RateLimited
			return v
		case tr.KeyRejected:
			v.Verdict = KeyRejected
			return v
		case tr.ModelProblem:
			v.Verdict = ModelBad
			return v
		case sr.TimedOut:
			v.Verdict = TimeLimit
			v.Error = fmt.Sprintf("step %d took longer than the time limit", i+1)
			return v
		case sr.Failed:
			// The workflow crashed: whatever the checks would say (e.g. "nothing was booked"), it
			// didn't do the job.
			v.Verdict = RuntimeErr
			return v
		case sr.Infra:
			v.Verdict = JudgeError
			v.Error = sr.ConnErr
			return v
		case sr.ConnErr != "":
			v.Verdict = RuntimeErr
			v.Error = "the workflow dropped the connection: " + sr.ConnErr
			return v
		case sr.Status >= 500 && !expectsStatus(exp, sr.Status):
			v.Verdict = RuntimeErr
			if v.Error == "" {
				v.Error = fmt.Sprintf("HTTP %d: %s", sr.Status, clip(sr.Body, 300))
			}
			return v
		}
		if exp == nil {
			continue
		}
		exp = expandExpect(exp, tr.Steps[:i])
		checks := checkStep(ctx, exp, sr, rt, judge)
		v.Checks = append(v.Checks, checks...)
		for _, c := range checks {
			if !c.OK {
				if c.Infra {
					v.Verdict = JudgeError
					v.Error = c.Detail
				} else if strings.HasPrefix(c.What, "time") {
					v.Verdict = TimeLimit
				} else {
					v.Verdict = WrongAnswer
				}
				v.Actual = clip(sr.Body, 4000)
				return v
			}
		}
	}
	if len(tr.Steps) != len(rt.Job.Steps) {
		v.Verdict = NotRun
		v.Error = "test did not complete every step"
		return v
	}
	v.Verdict = Accepted
	return v
}

func expectsStatus(e *problem.Expect, code int) bool {
	if e == nil {
		return false
	}
	if e.Status == code {
		return true
	}
	for _, s := range e.StatusIn {
		if s == code {
			return true
		}
	}
	return false
}

func checkStep(ctx context.Context, e *problem.Expect, sr runner.StepResult, rt *problem.Rendered, judge Judge) []Check {
	var out []Check
	switch {
	case e.Status != 0 || len(e.StatusIn) > 0:
		ok := expectsStatus(e, sr.Status)
		want := fmt.Sprint(e.Status)
		if len(e.StatusIn) > 0 {
			want = fmt.Sprint(e.StatusIn)
		}
		out = append(out, Check{What: "status " + want, OK: ok, Detail: fmt.Sprintf("got %d", sr.Status)})
	default:
		out = append(out, Check{What: "status 2xx", OK: sr.Status/100 == 2, Detail: fmt.Sprintf("got %d", sr.Status)})
	}
	if e.MaxMs > 0 {
		out = append(out, Check{What: fmt.Sprintf("time ≤ %d ms", e.MaxMs), OK: sr.DurationMs <= int64(e.MaxMs), Detail: fmt.Sprintf("took %d ms", sr.DurationMs)})
	}
	low := strings.ToLower(sr.Body)
	for _, c := range e.Contains {
		out = append(out, Check{What: fmt.Sprintf("response mentions %q", c), OK: strings.Contains(low, strings.ToLower(c))})
	}
	for _, c := range e.NotContains {
		out = append(out, Check{What: fmt.Sprintf("response does not mention %q", c), OK: !strings.Contains(low, strings.ToLower(c))})
	}
	for _, r := range e.Regex {
		re, err := regexp.Compile(r)
		out = append(out, Check{What: "response matches /" + r + "/", OK: err == nil && re.MatchString(sr.Body)})
	}

	var doc any
	parsed := json.Unmarshal([]byte(sr.Body), &doc) == nil
	if len(e.JSON) > 0 && !parsed {
		out = append(out, Check{What: "response is JSON", OK: false, Detail: "body is not valid JSON"})
		return out
	}
	for _, pc := range e.JSON {
		out = append(out, checkPath(doc, pc))
	}

	for _, c := range out {
		if !c.OK {
			return out
		}
	}

	if len(e.Mock) > 0 {
		var md any
		if json.Unmarshal([]byte(sr.Mock), &md) != nil {
			out = append(out, Check{What: "simulated services reported", OK: false, Detail: "no simulator data for this step", Infra: true})
		} else {
			for _, pc := range e.Mock {
				c := checkPath(md, pc)
				c.What = "mock " + c.What
				out = append(out, c)
			}
		}
	}
	if e.Alerts != nil && rt.Prob != nil {
		out = append(out, checkAlerts(e.Alerts, doc, sr.Mock, rt.Prob))
	}

	if e.Rubric != "" {
		answer := sr.Body
		if e.RubricField != "" && parsed {
			if v, ok := Lookup(doc, e.RubricField); ok {
				answer = toText(v)
			}
		}
		c := Check{What: "answer meets the rubric"}
		if fastPass(e.RubricFast, answer) {
			c.OK, c.Detail, c.Provider = true, "matched the expected wording", "pattern"
		} else if judge == nil {
			c.Infra, c.Detail = true, "rubric judge is not configured"
		} else {
			ok, reason, info, err := gradeWithInfo(ctx, judge, e.Rubric, describeInput(rt), answer)
			c.Provider, c.Model, c.Cached = info.Provider, info.Model, info.Cached
			if err != nil {
				c.Infra, c.Detail = true, "rubric judge unavailable: "+err.Error()
			} else {
				c.OK, c.Detail = ok, reason
			}
		}
		out = append(out, c)
	}
	return out
}

func checkPath(doc any, pc problem.PathCheck) Check {
	name := pc.Path
	if name == "" {
		name = "(root)"
	}
	v, found := Lookup(doc, pc.Path)
	fail := func(what, detail string) Check { return Check{What: name + " " + what, OK: false, Detail: detail} }
	pass := func(what string) Check { return Check{What: name + " " + what, OK: true} }
	got := "got " + clip(toJSON(v), 200)

	if pc.Exists != nil {
		if found != *pc.Exists {
			return fail(fmt.Sprintf("exists=%v", *pc.Exists), fmt.Sprintf("exists=%v", found))
		}
		if !found {
			return pass("absent")
		}
	}
	if !found {
		return fail("exists", "field is missing")
	}
	if pc.Type != "" && typeOf(v) != pc.Type {
		return fail("is "+pc.Type, "got "+typeOf(v))
	}
	if pc.Equals != nil {
		if pc.Approx != nil {
			if math.IsNaN(toNum(v)) || math.IsNaN(toNum(pc.Equals)) || math.Abs(toNum(v)-toNum(pc.Equals)) > *pc.Approx {
				return fail(fmt.Sprintf("≈ %v (±%v)", pc.Equals, *pc.Approx), got)
			}
		} else if !looseEqual(v, pc.Equals) {
			return fail("= "+clip(toJSON(pc.Equals), 200), got)
		}
	}
	if pc.IEquals != "" && !strings.EqualFold(strings.TrimSpace(toText(v)), strings.TrimSpace(pc.IEquals)) {
		return fail(fmt.Sprintf("= %q (any case)", pc.IEquals), got)
	}
	if pc.Contains != "" && !strings.Contains(strings.ToLower(toText(v)), strings.ToLower(pc.Contains)) {
		return fail(fmt.Sprintf("contains %q", pc.Contains), got)
	}
	if pc.NotContains != "" && strings.Contains(strings.ToLower(toText(v)), strings.ToLower(pc.NotContains)) {
		return fail(fmt.Sprintf("doesn't contain %q", pc.NotContains), got)
	}
	if pc.Regex != "" {
		re, err := regexp.Compile(pc.Regex)
		if err != nil || !re.MatchString(toText(v)) {
			return fail("matches /"+pc.Regex+"/", got)
		}
	}
	if pc.NotRegex != "" {
		re, err := regexp.Compile(pc.NotRegex)
		if err != nil || re.MatchString(toText(v)) {
			return fail("doesn't match /"+pc.NotRegex+"/", got)
		}
	}
	if pc.Len != nil || pc.MinLen != nil || pc.MaxLen != nil {
		n := length(v)
		if pc.Len != nil && n != *pc.Len {
			return fail(fmt.Sprintf("has length %d", *pc.Len), fmt.Sprintf("length %d", n))
		}
		if pc.MinLen != nil && n < *pc.MinLen {
			return fail(fmt.Sprintf("has length ≥ %d", *pc.MinLen), fmt.Sprintf("length %d", n))
		}
		if pc.MaxLen != nil && n > *pc.MaxLen {
			return fail(fmt.Sprintf("has length ≤ %d", *pc.MaxLen), fmt.Sprintf("length %d", n))
		}
	}
	if pc.Gte != nil && (math.IsNaN(toNum(v)) || toNum(v) < *pc.Gte) {
		return fail(fmt.Sprintf("≥ %v", *pc.Gte), got)
	}
	if pc.Lte != nil && (math.IsNaN(toNum(v)) || toNum(v) > *pc.Lte) {
		return fail(fmt.Sprintf("≤ %v", *pc.Lte), got)
	}
	if len(pc.OneOf) > 0 {
		ok := false
		for _, o := range pc.OneOf {
			if looseEqual(v, o) || strings.EqualFold(toText(v), toText(o)) {
				ok = true
			}
		}
		if !ok {
			return fail("is one of "+clip(toJSON(pc.OneOf), 200), got)
		}
	}
	return pass("ok")
}

// Lookup reads a dotted path like "items[0].name" from decoded JSON.
func Lookup(doc any, path string) (any, bool) {
	if path == "" {
		return doc, true
	}
	if key, ok := strings.CutPrefix(path, ".."); ok { // ..key: first match at any depth
		return findKey(doc, key)
	}
	cur := doc
	for _, part := range splitPath(path) {
		if idx, err := strconv.Atoi(part); err == nil {
			arr, ok := cur.([]any)
			if !ok {
				return nil, false
			}
			if idx < 0 {
				idx += len(arr)
			}
			if idx < 0 || idx >= len(arr) {
				return nil, false
			}
			cur = arr[idx]
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func splitPath(p string) []string {
	p = strings.ReplaceAll(p, "[", ".")
	p = strings.ReplaceAll(p, "]", "")
	var out []string
	for _, s := range strings.Split(p, ".") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func typeOf(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return reflect.TypeOf(v).String()
}

func length(v any) int {
	switch x := v.(type) {
	case string:
		return len([]rune(x))
	case []any:
		return len(x)
	case map[string]any:
		return len(x)
	}
	return -1
}

func toNum(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return math.NaN()
		}
		return f
	}
	return math.NaN()
}

// looseEqual compares JSON values; numbers compare numerically, a number and its string form match.
func looseEqual(a, b any) bool {
	na, aNum := numeric(a)
	nb, bNum := numeric(b)
	if aNum && bNum {
		return math.Abs(na-nb) < 1e-9
	}
	return reflect.DeepEqual(normalise(a), normalise(b))
}

func numeric(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

func normalise(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

func toText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return toJSON(v)
}

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func describeInput(rt *problem.Rendered) string {
	var parts []string
	for i, s := range rt.Job.Steps {
		var p string
		switch {
		case s.Message != "":
			p = "chat: " + s.Message
		case s.Raw != "":
			p = "raw body: " + s.Raw
		default:
			p = string(s.Body)
		}
		if len(rt.Job.Steps) > 1 {
			p = fmt.Sprintf("step %d → %s", i+1, p)
		}
		parts = append(parts, clip(p, 2000))
	}
	return strings.Join(parts, "\n")
}

func describeExpect(rt *problem.Rendered) string {
	var parts []string
	for _, e := range rt.Expects {
		if e == nil {
			continue
		}
		b, _ := json.MarshalIndent(e, "", "  ")
		parts = append(parts, string(b))
	}
	return strings.Join(parts, "\n")
}

// findKey searches maps and arrays depth-first, in sorted key order, for the first value under key.
func findKey(doc any, key string) (any, bool) {
	switch x := doc.(type) {
	case map[string]any:
		if v, ok := x[key]; ok {
			return v, true
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if v, ok := findKey(x[k], key); ok {
				return v, true
			}
		}
	case []any:
		for _, it := range x {
			if v, ok := findKey(it, key); ok {
				return v, true
			}
		}
	}
	return nil, false
}

// checkAlerts compares who was alerted during a step with who the problem's alert_rules say should
// be, for the severity and team the workflow itself returned. Telegram or email each count.
func checkAlerts(a *problem.Alerts, resp any, mockJSON string, p *problem.Problem) Check {
	c := Check{What: "alerts match the severity returned"}
	sevV, ok1 := Lookup(resp, a.SeverityPath)
	teamV, _ := Lookup(resp, a.TeamPath)
	sev, team := strings.TrimSpace(toText(sevV)), strings.TrimSpace(toText(teamV))
	if !ok1 {
		c.Detail = "response has no " + a.SeverityPath
		return c
	}
	var rule []string
	found := false
	for k, r := range p.AlertRules {
		if strings.EqualFold(k, sev) {
			rule, found = r, true
		}
	}
	if !found {
		c.Detail = fmt.Sprintf("severity %q is not one of the allowed values", sev)
		return c
	}
	var md map[string]any
	if json.Unmarshal([]byte(mockJSON), &md) != nil {
		c.Infra, c.Detail = true, "no simulator data for this step"
		return c
	}
	chats, mails := map[string]bool{}, ""
	if tg, ok := md["telegram"].(map[string]any); ok {
		for _, m := range asSlice(tg["sent"]) {
			if mm, ok := m.(map[string]any); ok {
				chats[toText(mm["chat_id"])] = true
			}
		}
	}
	if em, ok := md["email"].(map[string]any); ok {
		for _, m := range asSlice(em["sent"]) {
			if mm, ok := m.(map[string]any); ok {
				mails += " " + strings.ToLower(toText(mm["to"]))
			}
		}
	}
	want := map[string]bool{}
	for _, slug := range rule {
		if slug == "$team" {
			slug = ""
			for k, ct := range p.Directory {
				if strings.EqualFold(ct.Name, team) {
					slug = k
				}
			}
			if slug == "" {
				c.Detail = fmt.Sprintf("required_team %q is not a team in the directory", team)
				return c
			}
		}
		want[slug] = true
	}
	var missing, extra []string
	slugs := make([]string, 0, len(p.Directory))
	for k := range p.Directory {
		slugs = append(slugs, k)
	}
	sort.Strings(slugs)
	for _, k := range slugs {
		ct := p.Directory[k]
		hit := chats[ct.Chat] || (ct.Email != "" && strings.Contains(mails, strings.ToLower(ct.Email)))
		switch {
		case want[k] && !hit:
			missing = append(missing, ct.Name)
		case !want[k] && hit:
			extra = append(extra, ct.Name)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		c.Detail = fmt.Sprintf("for %s: not alerted %v, alerted by mistake %v", sev, missing, extra)
		return c
	}
	c.OK = true
	return c
}

func asSlice(v any) []any { s, _ := v.([]any); return s }

// expandExpect fills <<stepN.path>> references in a step's expectations with earlier responses.
func expandExpect(e *problem.Expect, prior []runner.StepResult) *problem.Expect {
	if e == nil {
		return nil
	}
	b, _ := json.Marshal(e)
	if !runner.HasRefs(strings.ReplaceAll(string(b), `\u003c`, "<")) {
		return e
	}
	out := &problem.Expect{}
	if json.Unmarshal([]byte(runner.ExpandRefs(string(b), prior, true)), out) != nil {
		return e
	}
	return out
}

// fastPass is true when there are patterns and the answer matches all of them. It only ever lets a
// team pass; anything else goes to the LLM, so a pattern can't unfairly fail a different wording.
func fastPass(patterns []string, answer string) bool {
	if len(patterns) == 0 {
		return false
	}
	for _, p := range patterns {
		re, err := regexp.Compile("(?i)" + p)
		if err != nil || !re.MatchString(answer) {
			return false
		}
	}
	return true
}

// gradeWithInfo uses the judge's provider report when it has one.
func gradeWithInfo(ctx context.Context, j Judge, rubric, input, answer string) (bool, string, JudgeInfo, error) {
	if inf, ok := j.(Informer); ok {
		return inf.GradeInfo(ctx, rubric, input, answer)
	}
	ok, reason, err := j.Grade(ctx, rubric, input, answer)
	return ok, reason, JudgeInfo{}, err
}
