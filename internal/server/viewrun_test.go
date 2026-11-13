package server

import (
	"database/sql"
	"encoding/json"
	"html/template"
	"strings"
	"testing"

	"github.com/dizziedbliss/meow-pspsps/internal/grade"
	"github.com/dizziedbliss/meow-pspsps/web"
)

func TestViewRunStripsAdminOnlyData(t *testing.T) {
	vs := []grade.TestVerdict{
		{ID: "secret-id", Category: "c", Visible: true, Verdict: grade.Accepted, Input: "in", Actual: "out",
			Checks: []grade.Check{{What: "answer meets the rubric", OK: true, Provider: "gemini", Model: "m", Cached: true}},
			Steps:  []grade.StepRecord{{Status: 200, Body: "full body", Mock: `{"email":{}}`}}},
		{ID: "hidden-id", Category: "c2", Visible: false, Verdict: grade.WrongAnswer, Input: "in", Actual: "out",
			Steps: []grade.StepRecord{{Status: 200, Body: "hidden body"}}},
	}
	b, _ := json.Marshal(vs)
	run := &Run{ID: 1, Status: StatusDone, Verdicts: sql.NullString{String: string(b), Valid: true}}
	s := &Server{}

	team := s.viewRun(run, false)
	for _, tv := range team.Tests {
		if tv.Steps != nil {
			t.Fatalf("team view leaked step data: %+v", tv)
		}
		for _, c := range tv.Checks {
			if c.Provider != "" || c.Model != "" || c.Cached {
				t.Fatalf("team view leaked the grader provider: %+v", c)
			}
		}
	}
	if len(team.Tests) != 1 {
		t.Fatalf("hidden tests must not reach the team: %+v", team.Tests)
	}
	if team.Tests[0].Input != "in" || team.Tests[0].Actual != "out" {
		t.Fatalf("a sample's input and output should still show: %+v", team.Tests[0])
	}

	admin := s.viewRun(run, true)
	if len(admin.Tests[0].Steps) != 1 || admin.Tests[0].Checks[0].Provider != "gemini" || len(admin.Tests[1].Steps) != 1 {
		t.Fatalf("admin view lost data: %+v", admin.Tests)
	}
}

func TestAdminRunPageRendersSteps(t *testing.T) {
	tmpl, err := template.New("").Funcs(tmplFuncs).ParseFS(web.FS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	vs := []grade.TestVerdict{{ID: "t1", Category: "c", Verdict: grade.Accepted, Ms: 5,
		Checks: []grade.Check{{What: "answer meets the rubric", OK: true, Provider: "gemini", Model: "lite", Cached: true}},
		Steps:  []grade.StepRecord{{Entry: "report", Status: 200, Body: `{"a":"FULLBODY"}`, Mock: `{"email":"SNAP"}`, ViaLastNode: true}}}}
	b, _ := json.Marshal(vs)
	run := &Run{ID: 7, Round: 1, Kind: KindAttempt, Status: StatusDone, Eligible: true, Verdicts: sql.NullString{String: string(b), Valid: true}}
	s := &Server{}
	data := map[string]any{"Run": run, "View": s.viewRun(run, true), "Team": &Team{Code: "AA01", Name: "x"}, "Runs": []*Run{run}}
	var out strings.Builder
	if err := tmpl.ExecuteTemplate(&out, "admin_run.html", data); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FULLBODY", "SNAP", "gemini", "cached", "answer taken from the last node", "Step 1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("admin run page is missing %q", want)
		}
	}
}

func TestTeamViewHidesHiddenTestsCompletely(t *testing.T) {
	vs := []grade.TestVerdict{
		{ID: "sample-one", Category: "sample cat", Visible: true, Verdict: grade.Accepted, Input: "sample in", Actual: "sample out", Ms: 11},
		{ID: "hid-secret-1", Category: "SECRET-CATEGORY-A", Visible: false, Verdict: grade.Accepted, Input: "HIDDEN-INPUT-1", Expected: "HIDDEN-EXPECT-1", Actual: "HIDDEN-ACTUAL-1", Ms: 222,
			Checks: []grade.Check{{What: "HIDDEN-CHECK-1", OK: true}}, Steps: []grade.StepRecord{{Status: 200, Body: "HIDDEN-STEP-BODY"}}},
		{ID: "hid-secret-2", Category: "SECRET-CATEGORY-B", Visible: false, Verdict: grade.RateLimited, Input: "HIDDEN-INPUT-2", Error: "HIDDEN-ERROR-2", Ms: 333},
		{ID: "hid-secret-3", Category: "SECRET-CATEGORY-C", Visible: false, Verdict: grade.ModelBad, Input: "HIDDEN-INPUT-3"},
		{ID: "hid-secret-4", Category: "SECRET-CATEGORY-D", Visible: false, Verdict: grade.WrongAnswer, Input: "HIDDEN-INPUT-4"},
	}
	b, _ := json.Marshal(vs)
	run := &Run{ID: 9, Status: StatusDone, Verdicts: sql.NullString{String: string(b), Valid: true}}
	s := &Server{}

	team := s.viewRun(run, false)
	out, _ := json.Marshal(team)
	for _, secret := range []string{"hid-secret", "SECRET-CATEGORY", "HIDDEN-INPUT", "HIDDEN-EXPECT", "HIDDEN-ACTUAL", "HIDDEN-ERROR", "HIDDEN-CHECK", "HIDDEN-STEP-BODY", `"ms":222`, `"ms":333`} {
		if strings.Contains(string(out), secret) {
			t.Fatalf("team JSON leaks %q: %s", secret, out)
		}
	}
	if len(team.Tests) != 1 || team.Tests[0].ID != "sample-one" || team.Tests[0].Input != "sample in" {
		t.Fatalf("only the visible sample should remain, in full: %+v", team.Tests)
	}
	if team.Hidden == nil || team.Hidden.Total != 4 || team.Hidden.Passed != 1 {
		t.Fatalf("hidden aggregate wrong: %+v", team.Hidden)
	}
	if !team.Notices.RateLimited || !team.Notices.ModelUnavailable || team.Notices.KeyRejected {
		t.Fatalf("notices must come from hidden verdicts too: %+v", team.Notices)
	}

	admin := s.viewRun(run, true)
	if len(admin.Tests) != 5 || admin.Tests[1].Input != "HIDDEN-INPUT-1" || admin.Tests[2].Error != "HIDDEN-ERROR-2" || len(admin.Tests[1].Steps) != 1 {
		t.Fatalf("admin must still see everything: %+v", admin.Tests)
	}
}

// A team logging out must not end an organiser's session in the same browser (separate cookies).
func TestTeamAndAdminCookiesAreSeparate(t *testing.T) {
	if cookieName == adminCookieName {
		t.Fatal("team and admin must use different cookies")
	}
}
