package grade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestAlertsAndAnyDepth(t *testing.T) {
	p := &problem.Problem{
		Directory:  map[string]problem.Contact{"amb": {Name: "Ambulance Team", Chat: "1", Email: "amb@x.in"}, "sup": {Name: "Supervisor", Chat: "2", Email: "sup@x.in"}, "head": {Name: "Department Head", Chat: "3", Email: "head@x.in"}},
		AlertRules: map[string][]string{"High": {"$team", "sup"}, "Low": {}},
	}
	a := &problem.Alerts{SeverityPath: "severity", TeamPath: "required_team"}
	var resp any
	json.Unmarshal([]byte(`{"severity":"High","required_team":"Ambulance Team"}`), &resp)
	ok := `{"telegram":{"sent":[{"chat_id":"1"}]},"email":{"sent":[{"to":["sup@x.in"]}]}}`
	if c := checkAlerts(a, resp, ok, p); !c.OK {
		t.Fatalf("expected match: %+v", c)
	}
	bad := `{"telegram":{"sent":[{"chat_id":"1"},{"chat_id":"3"}]},"email":{"sent":[{"to":["sup@x.in"]}]}}`
	if c := checkAlerts(a, resp, bad, p); c.OK || !strings.Contains(c.Detail, "Department Head") {
		t.Fatalf("expected extra head alert: %+v", c)
	}
	var doc any
	json.Unmarshal([]byte(`{"a":[{"b":{"incident_id":"INC-1"}}]}`), &doc)
	if v, ok := Lookup(doc, "..incident_id"); !ok || v != "INC-1" {
		t.Fatalf("any-depth lookup = %v %v", v, ok)
	}
}

func TestChainFailsOverAndCaches(t *testing.T) {
	calls := map[string]int{}
	mk := func(name string, status int) (*OpenAIJudge, func()) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls[name]++
			if status != 200 {
				w.WriteHeader(status)
				return
			}
			w.Write([]byte(`{"choices":[{"message":{"content":"{\"pass\":true,\"reason\":\"ok\"}"}}]}`))
		}))
		p, _ := NewProvider(name, srv.URL, "", "m", 0, 0, 1)
		return p, srv.Close
	}
	bad, c1 := mk("bad", 503)
	good, c2 := mk("good", 200)
	defer c1()
	defer c2()
	ch, _ := NewChain(bad, good)
	for i := 0; i < 2; i++ {
		ok, _, err := ch.Grade(context.Background(), "r", "i", "a")
		if err != nil || !ok {
			t.Fatalf("grade %d: ok=%v err=%v", i, ok, err)
		}
	}
	if calls["bad"] != 1 || calls["good"] != 1 {
		t.Fatalf("expected one failed try then one good call, second answer cached: %v", calls)
	}
	all, c3 := mk("down", 500)
	defer c3()
	ch2, _ := NewChain(all)
	if _, _, err := ch2.Grade(context.Background(), "r", "i", "a"); err == nil {
		t.Fatal("all providers down must return an error, never a verdict")
	}
}

type failJudge struct{ called bool }

func (f *failJudge) Grade(context.Context, string, string, string) (bool, string, error) {
	f.called = true
	return false, "", errors.New("down")
}

func TestRubricFastPassSkipsLLM(t *testing.T) {
	if !fastPass([]string{`hair|insect`, `curry`}, "Hair found in the CURRY") {
		t.Fatal("all patterns match: should pass")
	}
	if fastPass([]string{`hair`, `curry`}, "hair in the rice") || fastPass(nil, "anything") {
		t.Fatal("a miss or no patterns must go to the LLM")
	}
	e := &problem.Expect{Rubric: "flags hygiene", RubricFast: []string{`kiwi47`}}
	fj := &failJudge{}
	checks := checkStep(context.Background(), e, runner.StepResult{Status: 200, Body: `{"hygiene_concerns":["kiwi47 had a hair"]}`}, &problem.Rendered{}, fj)
	for _, c := range checks {
		if !c.OK {
			t.Fatalf("expected pass: %+v", c)
		}
	}
	if fj.called {
		t.Fatal("LLM must not be called on a fast pass")
	}
}

type infoJudge struct{}

func (infoJudge) Grade(context.Context, string, string, string) (bool, string, error) {
	return true, "fine", nil
}
func (infoJudge) GradeInfo(context.Context, string, string, string) (bool, string, JudgeInfo, error) {
	return true, "fine", JudgeInfo{Provider: "gemini", Model: "gemini-3.1-flash-lite"}, nil
}

func TestVerdictKeepsFullStepDataAndProvider(t *testing.T) {
	big := strings.Repeat("x", 9000)
	body := `{"answer":"` + big + `"}`
	rt := &problem.Rendered{
		Def:     &problem.TestDef{ID: "t1", Weight: 1},
		Job:     runner.JobTest{ID: "t1", Steps: []runner.JobStep{{Entry: "report"}}},
		Expects: []*problem.Expect{{Rubric: "is fine", RubricField: "answer"}},
	}
	tr := &runner.TestResult{ID: "t1", Steps: []runner.StepResult{{Status: 200, Body: body, Mock: `{"email":{"sent":[]}}`, ViaLastNode: true}}}
	v := Test(context.Background(), rt, tr, infoJudge{})
	if v.Verdict != Accepted {
		t.Fatalf("verdict %s: %+v", v.Verdict, v.Checks)
	}
	if len(v.Actual) > 4100 {
		t.Fatalf("the team-facing Actual must stay clipped, got %d chars", len(v.Actual))
	}
	if len(v.Steps) != 1 || v.Steps[0].Body != body || v.Steps[0].Mock == "" || v.Steps[0].Entry != "report" || !v.Steps[0].ViaLastNode {
		t.Fatalf("admin step record incomplete: %+v", v.Steps)
	}
	found := false
	for _, c := range v.Checks {
		if c.Provider == "gemini" && c.Model == "gemini-3.1-flash-lite" {
			found = true
		}
	}
	if !found {
		t.Fatalf("rubric check should name the provider: %+v", v.Checks)
	}
}

func TestChainSticksToWorkingKeyAndCoolsDownFailedOne(t *testing.T) {
	calls := map[string]int{}
	mk := func(name string, status int) *OpenAIJudge {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls[name]++
			if status != 200 {
				w.WriteHeader(status)
				return
			}
			w.Write([]byte(`{"choices":[{"message":{"content":"{\"pass\":true,\"reason\":\"ok\"}"}}]}`))
		}))
		t.Cleanup(srv.Close)
		p, _ := NewProvider(name, srv.URL, "", "m", 0, 0, 1)
		return p
	}
	ch, _ := NewChain(mk("k1", 429), mk("k2", 200), mk("k3", 200))
	for i := 0; i < 4; i++ { // different answers, so the cache can't help
		if ok, _, err := ch.Grade(context.Background(), "r", "i", fmt.Sprint("a", i)); err != nil || !ok {
			t.Fatalf("grade %d: %v %v", i, ok, err)
		}
	}
	if calls["k1"] != 1 || calls["k2"] != 4 || calls["k3"] != 0 {
		t.Fatalf("k1 should be tried once then cool down, k2 used one after another, k3 untouched: %v", calls)
	}
}

func TestHintsForCommonN8nErrors(t *testing.T) {
	cases := map[string]string{
		`Could not get parameter "sheetName"`:                         "real one",
		`Column names were updated after the node's setup`:            "Map Automatically",
		`No Respond to Webhook node found in the workflow`:            "Respond to Webhook",
		`Cannot read properties of undefined (reading 'roll_number')`: "$json.body",
		`Referenced node is unexecuted`:                               "first().json",
		`Some totally unrelated failure`:                              "",
	}
	for msg, want := range cases {
		got := Hint(msg)
		if want == "" && got != "" || want != "" && !strings.Contains(got, want) {
			t.Errorf("%q → %q (want containing %q)", msg, got, want)
		}
	}
	if e := withHint("x: Could not get parameter \"sheetName\""); !strings.Contains(e, "\nHint: ") || withHint(e) != e {
		t.Fatalf("hint should be added once: %q", e)
	}
}
