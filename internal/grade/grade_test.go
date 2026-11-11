package grade

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/dizziedbliss/meow-pspsps/internal/runner"

	"github.com/dizziedbliss/meow-pspsps/internal/problem"
)

func TestLookupAndChecks(t *testing.T) {
	var doc any
	_ = json.Unmarshal([]byte(`{"a":{"b":[{"c":"Hello"},{"c":2.5}]}}`), &doc)
	if v, ok := Lookup(doc, "a.b[1].c"); !ok || v.(float64) != 2.5 {
		t.Fatalf("lookup = %v %v", v, ok)
	}
	if v, ok := Lookup(doc, "a.b[-1].c"); !ok || v.(float64) != 2.5 {
		t.Fatal("negative index")
	}
	if c := checkPath(doc, problem.PathCheck{Path: "a.b[1].c", Equals: "2.5"}); !c.OK {
		t.Fatalf("number vs numeric string should match: %+v", c)
	}
	if c := checkPath(doc, problem.PathCheck{Path: "a.b[0].c", IEquals: "hello"}); !c.OK {
		t.Fatal("iequals")
	}
	if c := checkPath(doc, problem.PathCheck{Path: "a.zzz"}); c.OK {
		t.Fatal("missing field must fail")
	}
}

func TestCrashedWorkflowIsRuntimeError(t *testing.T) {
	rt := &problem.Rendered{Def: &problem.TestDef{ID: "x", Weight: 1},
		Expects: []*problem.Expect{{JSON: []problem.PathCheck{{Path: "calendar.created", Len: intp(0)}}}}}
	tr := &runner.TestResult{ID: "x", Steps: []runner.StepResult{{Status: 200, Body: `{"calendar":{"created":[]}}`, Failed: true, ExecError: "model not found"}}}
	if v := Test(context.Background(), rt, tr, nil); v.Verdict != RuntimeErr {
		t.Fatalf("verdict = %s, want Runtime Error", v.Verdict)
	}
}

func intp(n int) *int { return &n }

type stubJudge struct {
	pass bool
	err  error
}

func (s stubJudge) Grade(context.Context, string, string, string) (bool, string, error) {
	return s.pass, "", s.err
}

func TestRubricInfrastructureAndWrongAnswer(t *testing.T) {
	rt := &problem.Rendered{Def: &problem.TestDef{ID: "x"}, Job: runner.JobTest{Steps: []runner.JobStep{{}}}, Expects: []*problem.Expect{{Rubric: "must ask for a date"}}}
	tr := &runner.TestResult{Steps: []runner.StepResult{{Status: 200, Body: "What date?"}}}
	for _, tc := range []struct {
		name string
		j    Judge
		want string
	}{
		{"missing", nil, JudgeError}, {"unavailable", stubJudge{err: errors.New("offline")}, JudgeError},
		{"bad answer", stubJudge{}, WrongAnswer}, {"good answer", stubJudge{pass: true}, Accepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Test(context.Background(), rt, tr, tc.j).Verdict; got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
func TestIncompleteConversationIsNotAccepted(t *testing.T) {
	rt := &problem.Rendered{Def: &problem.TestDef{ID: "x"}, Job: runner.JobTest{Steps: []runner.JobStep{{}, {}}}, Expects: []*problem.Expect{{}, {}}}
	tr := &runner.TestResult{Steps: []runner.StepResult{{Status: 200}}}
	if v := Test(context.Background(), rt, tr, nil); v.Verdict != NotRun {
		t.Fatalf("got %s", v.Verdict)
	}
}

func TestBookySampleChecksCalendarOutcome(t *testing.T) {
	p, err := problem.Load("../../problems/1-booky")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := p.Render(42, 1)
	if err != nil {
		t.Fatal(err)
	}
	var rt *problem.Rendered
	for _, r := range rendered {
		if r.Def.ID == "sample-book-free-day" {
			rt = r
			break
		}
	}
	if rt == nil {
		t.Fatal("missing sample")
	}
	fields := map[string]any{}
	for _, c := range rt.Expects[0].JSON {
		fields[c.Path] = c.Equals
		if c.IEquals != "" {
			fields[c.Path] = c.IEquals
		}
	}
	makeResult := func(date any, replyCount int) *runner.TestResult {
		body, err := json.Marshal(map[string]any{"reply": "Confirmed!", "reply_count": replyCount, "calendar": map[string]any{"created": []any{map[string]any{"date": date, "summary": fields["calendar.created[0].summary"]}}}})
		if err != nil {
			t.Fatal(err)
		}
		return &runner.TestResult{Steps: []runner.StepResult{{Status: 200, Body: string(body)}}}
	}
	for _, tc := range []struct {
		name  string
		date  any
		count int
		judge stubJudge
		want  string
	}{
		{"valid alternative wording", fields["calendar.created[0].date"], 1, stubJudge{pass: true}, Accepted},
		{"wrong date", "1900-01-01", 1, stubJudge{pass: true}, WrongAnswer},
		{"no customer reply", fields["calendar.created[0].date"], 0, stubJudge{pass: true}, WrongAnswer},
		{"not a confirmation", fields["calendar.created[0].date"], 1, stubJudge{}, WrongAnswer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Test(context.Background(), rt, makeResult(tc.date, tc.count), tc.judge).Verdict; got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestNumericConstraintsRejectNonNumbers(t *testing.T) {
	v := 1.0
	for _, c := range []problem.PathCheck{{Path: "value", Gte: &v}, {Path: "value", Lte: &v}, {Path: "value", Equals: 1.0, Approx: &v}} {
		if got := checkPath(map[string]any{"value": "not a number"}, c); got.OK {
			t.Fatalf("invalid value passed: %+v", got)
		}
	}
}
