// Package grade turns a worker's raw results into per-test verdicts.
package grade

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
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
}

// Judge grades a free-text answer against a rubric.
type Judge interface {
	Grade(ctx context.Context, rubric, input, answer string) (ok bool, reason string, err error)
}

// Test grades one rendered test against its raw result.
func Test(ctx context.Context, rt *problem.Rendered, tr *runner.TestResult, judge Judge) TestVerdict {
	d := rt.Def
	v := TestVerdict{ID: d.ID, Category: d.Category, Visible: d.Visible, Weight: d.Weight}
	v.Input = describeInput(rt)
	v.Expected = describeExpect(rt)
	if tr == nil || len(tr.Steps) == 0 {
		v.Verdict = NotRun
		return v
	}
	last := tr.Steps[len(tr.Steps)-1]
	v.Actual = clip(last.Body, 4000)
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
		case sr.TimedOut:
			v.Verdict = TimeLimit
			v.Error = fmt.Sprintf("step %d took longer than the time limit", i+1)
			return v
		case sr.Failed:
			// The workflow crashed: whatever the checks would say (e.g. "nothing was booked"), it
			// didn't do the job.
			v.Verdict = RuntimeErr
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

	if e.Rubric != "" {
		answer := sr.Body
		if e.RubricField != "" && parsed {
			if v, ok := Lookup(doc, e.RubricField); ok {
				answer = toText(v)
			}
		}
		c := Check{What: "answer meets the rubric"}
		if judge == nil {
			c.Infra, c.Detail = true, "rubric judge is not configured"
		} else if ok, reason, err := judge.Grade(ctx, e.Rubric, describeInput(rt), answer); err != nil {
			c.Infra, c.Detail = true, "rubric judge unavailable: "+err.Error()
		} else {
			c.OK, c.Detail = ok, reason
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
