package grade

import (
	"context"
	"encoding/json"
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
