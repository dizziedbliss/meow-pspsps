package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"html/template"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"golang.org/x/crypto/bcrypt"

	"github.com/dizziedbliss/meow-pspsps/internal/grade"
	"github.com/dizziedbliss/meow-pspsps/internal/problem"
	"github.com/dizziedbliss/meow-pspsps/internal/workflow"
)

var tmplFuncs = template.FuncMap{
	"ts": func(v any) string {
		var u int64
		switch x := v.(type) {
		case int64:
			u = x
		case sql.NullInt64:
			u = x.Int64
		}
		if u == 0 {
			return "–"
		}
		return time.Unix(u, 0).Local().Format("15:04:05")
	},
	"pct": func(p, t float64) string {
		if t == 0 {
			return "–"
		}
		return strconv.FormatFloat(p, 'f', -1, 64) + "/" + strconv.FormatFloat(t, 'f', -1, 64)
	},
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	var buf bytes.Buffer
	if err := s.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	_, _ = buf.WriteTo(w)
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "login.html", map[string]any{"Error": r.URL.Query().Get("e") != "", "Admin": false})
}

func (s *Server) loginPost(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(strings.ToUpper(r.FormValue("code")))
	t, err := s.store.TeamByCode(code)
	if err != nil || t == nil || bcrypt.CompareHashAndPassword([]byte(t.PassHash), []byte(r.FormValue("password"))) != nil {
		time.Sleep(400 * time.Millisecond) // blunt brute-force damping
		http.Redirect(w, r, "/login?e=1", http.StatusSeeOther)
		return
	}
	tok := token()
	if err := s.store.CreateSession(tok, t.ID, false); err != nil {
		http.Error(w, "could not log in", http.StatusInternalServerError)
		return
	}
	setSession(w, r, tok)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.store.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) appPage(w http.ResponseWriter, r *http.Request, t *Team) {
	s.render(w, "app.html", map[string]any{"Team": t})
}

// stateResp is everything the team page shows.
type stateResp struct {
	Team       string          `json:"team"`
	Round      *roundView      `json:"round"`
	Upcoming   bool            `json:"upcoming"` // rounds exist but none open
	Runs       []runSummary    `json:"runs"`
	Submission *submissionView `json:"submission"`
	InFlight   int64           `json:"inFlight"`
	N8n        string          `json:"n8nVersion"`
	Workers    int             `json:"workers"` // worker slots that asked for work in the last minute
}

type roundView struct {
	N         int        `json:"n"`
	Title     string     `json:"title"`
	Statement string     `json:"statementHtml"`
	Trigger   string     `json:"trigger"`
	Method    string     `json:"method,omitempty"`
	Samples   int        `json:"samples"`
	Hidden    int        `json:"hidden"`
	Requests  int        `json:"requests"` // calls to the workflow per run (for the token warning)
	Tests     []testStub `json:"tests"`
	OpenedAt  int64      `json:"openedAt"`
}

// testStub lets the page draw every test row before a run has results.
type testStub struct {
	Category string `json:"category"`
	Visible  bool   `json:"visible"`
}

type runSummary struct {
	ID      int64   `json:"id"`
	Status  string  `json:"status"`
	Passed  float64 `json:"passed"`
	Total   float64 `json:"total"`
	Created int64   `json:"created"`
	Invalid bool    `json:"invalid"`
	Name    string  `json:"name"`
}

type submissionView struct {
	RunID  int64   `json:"runId"`
	Auto   bool    `json:"auto"`
	At     int64   `json:"at"`
	Passed float64 `json:"passed"`
	Total  float64 `json:"total"`
}

func (s *Server) apiState(w http.ResponseWriter, r *http.Request, t *Team) {
	resp := stateResp{Team: t.Name, Runs: []runSummary{}, N8n: s.cat.N8nVersion, Workers: s.liveWorkers()}
	rd, err := s.store.OpenRound()
	if err != nil {
		jsonErr(w, 500, "db error")
		return
	}
	if rd == nil {
		resp.Upcoming = true
		jsonOut(w, resp)
		return
	}
	p := s.problems[rd.ProblemID]
	var html bytes.Buffer
	_ = goldmark.Convert([]byte(p.Statement), &html)
	rv := &roundView{N: rd.N, Title: p.Title, Statement: html.String(), Trigger: string(p.Trigger), Method: p.Method, OpenedAt: rd.OpenedAt.Int64}
	for _, td := range p.Tests {
		if td.Visible {
			rv.Samples++
		} else {
			rv.Hidden++
		}
		rv.Requests += len(td.Steps)
		rv.Tests = append(rv.Tests, testStub{Category: td.Category, Visible: td.Visible})
	}
	resp.Round = rv
	runs, _ := s.store.RunsFor(t.ID, rd.N)
	for _, run := range runs {
		resp.Runs = append(resp.Runs, runSummary{ID: run.ID, Status: run.Status, Passed: run.Passed.Float64, Total: run.Total.Float64, Created: run.Created, Invalid: run.Invalid.Valid, Name: run.WfName.String})
	}
	if sub, _ := s.store.GetSubmission(t.ID, rd.N); sub != nil {
		sv := &submissionView{RunID: sub.RunID, Auto: sub.Auto, At: sub.At}
		if run, _ := s.store.GetRun(sub.RunID); run != nil {
			sv.Passed, sv.Total = run.Passed.Float64, run.Total.Float64
		}
		resp.Submission = sv
	}
	resp.InFlight, _ = s.store.InFlight(t.ID)
	jsonOut(w, resp)
}

func (s *Server) liveWorkers() int {
	n := 0
	s.workers.Range(func(_, v any) bool {
		if time.Since(v.(time.Time)) < time.Minute {
			n++
		}
		return true
	})
	return n
}

func (s *Server) currentProblem() (*Round, *problem.Problem, error) {
	rd, err := s.store.OpenRound()
	if err != nil || rd == nil {
		return nil, nil, err
	}
	return rd, s.problems[rd.ProblemID], nil
}

func (s *Server) analyzeRaw(raw []byte, p *problem.Problem) (workflow.Analysis, error) {
	wf, err := workflow.Parse(raw)
	if err != nil {
		return workflow.Analysis{}, err
	}
	return workflow.Analyze(wf, raw, s.cat, p.Expect()), nil
}

type analyzeReq struct {
	Workflow json.RawMessage `json:"workflow"`
}

const maxWorkflow = 5 << 20

func (s *Server) apiAnalyze(w http.ResponseWriter, r *http.Request, t *Team) {
	var req analyzeReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkflow+4096)).Decode(&req); err != nil {
		jsonErr(w, 400, "that file isn't valid JSON (or is over 5 MB)")
		return
	}
	_, p, _ := s.currentProblem()
	if p == nil {
		jsonErr(w, 409, "no round is open right now")
		return
	}
	an, err := s.analyzeRaw(req.Workflow, p)
	if err != nil {
		jsonOut(w, map[string]any{"errors": []string{err.Error()}, "warnings": []string{}, "needs": []any{}})
		return
	}
	jsonOut(w, an)
}

type runReq struct {
	Workflow json.RawMessage              `json:"workflow"`
	Creds    map[string]map[string]string `json:"creds"`
}

func (s *Server) apiRun(w http.ResponseWriter, r *http.Request, t *Team) {
	var req runReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkflow+64<<10)).Decode(&req); err != nil {
		jsonErr(w, 400, "that file isn't valid JSON (or is over 5 MB)")
		return
	}
	rd, p, _ := s.currentProblem()
	if p == nil {
		jsonErr(w, 409, "no round is open right now")
		return
	}
	if sub, _ := s.store.GetSubmission(t.ID, rd.N); sub != nil {
		jsonErr(w, 409, "you've already submitted for this round")
		return
	}
	if id, _ := s.store.InFlight(t.ID); id != 0 {
		jsonErr(w, 429, "your previous run is still going; wait for it to finish")
		return
	}
	wf, err := workflow.Parse(req.Workflow)
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	an := workflow.Analyze(wf, req.Workflow, s.cat, p.Expect())
	run := &Run{TeamID: t.ID, Round: rd.N, ProblemID: p.ID, Seed: rand.Int63(), Workflow: string(req.Workflow)}
	run.WfName.String, run.WfName.Valid = wf.Name(), true

	if len(an.Errors) > 0 {
		// Counted as an attempt (organisers see it), graded instantly, never queued.
		vs := make([]grade.TestVerdict, 0, len(p.Tests))
		for _, td := range p.Tests {
			vs = append(vs, grade.TestVerdict{ID: td.ID, Category: td.Category, Visible: td.Visible, Weight: td.Weight, Verdict: grade.Invalid})
			run.Total.Float64 += td.Weight
		}
		b, _ := json.Marshal(vs)
		run.Status = StatusDone
		run.Invalid.String, run.Invalid.Valid = strings.Join(an.Errors, "\n"), true
		run.Passed.Valid, run.Total.Valid = true, true
		run.Verdicts.String, run.Verdicts.Valid = string(b), true
		run.Finished.Int64, run.Finished.Valid = now(), true
		id, err := s.store.InsertRun(run)
		if err != nil {
			jsonErr(w, 500, "db error")
			return
		}
		jsonOut(w, map[string]any{"runId": id})
		return
	}

	// Keep only fields the workflow's credentials actually have.
	creds := map[string]map[string]string{}
	for _, need := range an.Needs {
		vals := map[string]string{}
		for _, f := range need.Fields {
			v := strings.TrimSpace(req.Creds[need.Type][f.Name])
			if v == "" && f.Required && f.Default == "" {
				jsonErr(w, 400, "fill in "+f.DisplayName+" for "+need.DisplayName)
				return
			}
			if v != "" {
				vals[f.Name] = v
			}
		}
		creds[need.Type] = vals
	}
	run.Status = StatusQueued
	id, err := s.store.InsertRun(run)
	if err != nil {
		jsonErr(w, 500, "db error")
		return
	}
	s.keys.put(id, creds)
	s.queue.push(id)
	jsonOut(w, map[string]any{"runId": id})
}

// runView is a run as its team sees it: hidden tests show only category and verdict.
type runView struct {
	ID       int64               `json:"id"`
	Status   string              `json:"status"`
	Position int                 `json:"position,omitempty"`
	Queue    int                 `json:"queueLength,omitempty"`
	Passed   float64             `json:"passed"`
	Total    float64             `json:"total"`
	Invalid  string              `json:"invalid,omitempty"`
	Infra    string              `json:"infra,omitempty"`
	Tests    []grade.TestVerdict `json:"tests"`
	Created  int64               `json:"created"`
	Seconds  int64               `json:"seconds,omitempty"`
}

func (s *Server) apiRunGet(w http.ResponseWriter, r *http.Request, t *Team) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	run, err := s.store.GetRun(id)
	if err != nil || run == nil || run.TeamID != t.ID {
		jsonErr(w, 404, "no such run")
		return
	}
	jsonOut(w, s.viewRun(run, false))
}

func (s *Server) viewRun(run *Run, admin bool) runView {
	v := runView{ID: run.ID, Status: run.Status, Passed: run.Passed.Float64, Total: run.Total.Float64, Invalid: run.Invalid.String, Infra: run.Infra.String, Created: run.Created, Tests: []grade.TestVerdict{}}
	if run.Status == StatusQueued {
		v.Position = s.queue.position(run.ID)
		v.Queue = s.queue.length()
	}
	if run.Finished.Valid && run.Started.Valid {
		v.Seconds = run.Finished.Int64 - run.Started.Int64
	}
	if run.Verdicts.Valid {
		_ = json.Unmarshal([]byte(run.Verdicts.String), &v.Tests)
	}
	if !admin {
		for i := range v.Tests {
			tv := &v.Tests[i]
			if !tv.Visible {
				tv.Input, tv.Expected, tv.Actual, tv.Error, tv.Checks = "", "", "", "", nil
				tv.ID = "" // ids can hint at the trick; the category is the hint teams get
			}
		}
	}
	return v
}

type submitReq struct {
	RunID int64 `json:"runId"`
}

func (s *Server) apiSubmit(w http.ResponseWriter, r *http.Request, t *Team) {
	var req submitReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		jsonErr(w, 400, "bad request")
		return
	}
	rd, _, _ := s.currentProblem()
	if rd == nil {
		jsonErr(w, 409, "no round is open right now")
		return
	}
	run, err := s.store.GetRun(req.RunID)
	if err != nil || run == nil || run.TeamID != t.ID || run.Round != rd.N {
		jsonErr(w, 404, "no such run in this round")
		return
	}
	if run.Status != StatusDone {
		jsonErr(w, 409, "only a finished run can be submitted")
		return
	}
	if err := s.store.Submit(t.ID, rd.N, run.ID, false); err != nil {
		jsonErr(w, 409, "you've already submitted for this round")
		return
	}
	jsonOut(w, map[string]any{"ok": true})
}
