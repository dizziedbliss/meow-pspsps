package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"html/template"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"golang.org/x/crypto/bcrypt"

	"github.com/dizziedbliss/meow-pspsps/internal/grade"
	"github.com/dizziedbliss/meow-pspsps/internal/problem"
	"github.com/dizziedbliss/meow-pspsps/internal/workflow"
)

var tmplFuncs = template.FuncMap{
	"inc": func(i int) int { return i + 1 },
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
	setSession(w, r, cookieName, tok)
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
	Team     string       `json:"team"`
	Round    *roundView   `json:"round"`
	Rounds   []roundTab   `json:"rounds"`   // every round the team can work in (tabs on the page)
	Upcoming bool         `json:"upcoming"` // rounds exist but none open
	Runs     []runSummary `json:"runs"`
	InFlight int64        `json:"inFlight"`
	// InFlightRound is the round the unfinished run belongs to: a team has one run going at a time, across rounds.
	InFlightRound int          `json:"inFlightRound,omitempty"`
	N8n           string       `json:"n8nVersion"`
	Workers       int          `json:"workers"` // worker slots that asked for work in the last minute
	Attempts      attemptsView `json:"attempts"`
	Best          *bestView    `json:"best"`
	SampleWait    int64        `json:"sampleWait"` // seconds until the next sample run is allowed
}

type attemptsView struct {
	Used      int  `json:"used"`
	Max       int  `json:"max"`
	Left      int  `json:"left"`
	Unlimited bool `json:"unlimited,omitempty"`
}

// bestView is the attempt that currently counts for the team.
type bestView struct {
	RunID  int64   `json:"runId"`
	Passed float64 `json:"passed"`
	Total  float64 `json:"total"`
	Score  float64 `json:"score"`
}

// roundTab is one round a team can switch to.
type roundTab struct {
	N     int    `json:"n"`
	Title string `json:"title"`
}

type roundView struct {
	N         int              `json:"n"`
	Title     string           `json:"title"`
	Statement string           `json:"statementHtml"`
	Trigger   string           `json:"trigger"`
	Method    string           `json:"method,omitempty"`
	Samples   int              `json:"samples"`
	Hidden    int              `json:"hidden"`
	Requests  int              `json:"requests"` // calls to the workflow per run (for the token warning)
	Tests     []testStub       `json:"tests"`
	OpenedAt  int64            `json:"openedAt"`
	Starter   bool             `json:"hasStarter,omitempty"` // a paste-able starter workflow is available
	Entries   []workflow.Entry `json:"entries,omitempty"`    // required webhooks (multi-entry problems)
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
	Kind    string  `json:"kind"`
	Refund  bool    `json:"refunded"`
	Score   float64 `json:"score"`
}

func (s *Server) apiState(w http.ResponseWriter, r *http.Request, t *Team) {
	resp := stateResp{Team: t.Name, Runs: []runSummary{}, N8n: s.cat.N8nVersion, Workers: s.liveWorkers(), Attempts: attemptsView{Max: MaxAttempts}}
	want, _ := strconv.Atoi(r.URL.Query().Get("round"))
	rd, err := s.roundFor(t, want, false)
	if err != nil {
		jsonErr(w, 500, "db error")
		return
	}
	resp.Rounds = s.roundTabs(t)
	if rd == nil {
		resp.Upcoming = true
		jsonOut(w, resp)
		return
	}
	p := s.problems[rd.ProblemID]
	var html bytes.Buffer
	_ = statementMD.Convert([]byte(p.Statement), &html)
	rv := &roundView{N: rd.N, Title: p.Title, Statement: html.String(), Trigger: string(p.Trigger), Method: p.Method, OpenedAt: rd.OpenedAt.Int64}
	rv.Starter = len(p.Entries) > 0
	rv.Entries = p.Entries
	for _, td := range p.Tests {
		if td.Visible {
			rv.Samples++
			rv.Tests = append(rv.Tests, testStub{Category: td.Category, Visible: true}) // hidden tests get no row, not even a category
		} else {
			rv.Hidden++
		}
		rv.Requests += len(td.Steps)
	}
	resp.Round = rv
	runs, _ := s.store.RunsFor(t.ID, rd.N)
	for _, run := range runs {
		resp.Runs = append(resp.Runs, runSummary{ID: run.ID, Status: run.Status, Passed: run.Passed.Float64, Total: run.Total.Float64, Created: run.Created,
			Invalid: run.Invalid.Valid, Name: run.WfName.String, Kind: run.Kind, Refund: run.Kind == KindAttempt && !run.Eligible, Score: run.Score.Float64})
		if run.Counted() {
			resp.Attempts.Used++
		}
		if run.Counted() && run.Status == StatusDone && (resp.Best == nil || run.Score.Float64 > resp.Best.Score) {
			resp.Best = &bestView{RunID: run.ID, Passed: run.Passed.Float64, Total: run.Total.Float64, Score: run.Score.Float64}
		}
	}
	resp.Attempts.Max = rd.MaxAttempts
	resp.Attempts.Left = rd.MaxAttempts - resp.Attempts.Used
	if resp.Attempts.Left < 0 {
		resp.Attempts.Left = 0
	}
	resp.Attempts.Unlimited = t.Code == practiceTeamCode
	if last := s.store.LastSample(t.ID, rd.N); last > 0 && !resp.Attempts.Unlimited {
		if w := last + SampleThrottle - now(); w > 0 {
			resp.SampleWait = w
		}
	}
	resp.InFlight, _ = s.store.InFlight(t.ID)
	if resp.InFlight > 0 {
		if fr, _ := s.store.GetRun(resp.InFlight); fr != nil {
			resp.InFlightRound = fr.Round
		}
	}
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

// usableRounds are the rounds a team can work in, lowest number first: the open ones, plus (for
// the organiser's private practice account) pending ones, so they can be previewed.
func (s *Server) usableRounds(t *Team) ([]Round, error) {
	all, err := s.store.Rounds()
	if err != nil {
		return nil, err
	}
	var out []Round
	for _, rd := range all {
		if rd.Status == "open" || (t.Code == practiceTeamCode && rd.Status == "pending") {
			out = append(out, rd)
		}
	}
	return out, nil
}

// roundFor picks the round a team is looking at. want is the round it asked for (0 = no
// preference). If that round isn't available, strict callers get nil and the others fall back to the
// default: the lowest open round (for practice with none open, the first pending one).
func (s *Server) roundFor(t *Team, want int, strict bool) (*Round, error) {
	list, err := s.usableRounds(t)
	if err != nil {
		return nil, err
	}
	if want > 0 {
		for i := range list {
			if list[i].N == want {
				return &list[i], nil
			}
		}
		if strict {
			return nil, nil
		}
	}
	for i := range list {
		if list[i].Status == "open" {
			return &list[i], nil
		}
	}
	if len(list) > 0 {
		return &list[0], nil
	}
	return nil, nil
}

func (s *Server) roundTabs(t *Team) []roundTab {
	tabs := []roundTab{}
	list, _ := s.usableRounds(t)
	for _, rd := range list {
		if p := s.problems[rd.ProblemID]; p != nil {
			tabs = append(tabs, roundTab{N: rd.N, Title: p.Title})
		}
	}
	return tabs
}

// currentProblem is roundFor plus the round's problem. Calls that act on a round (analyze, run,
// starter) pass it strictly, so a request for a closed round is refused instead of redirected.
func (s *Server) currentProblem(t *Team, want int, strict bool) (*Round, *problem.Problem, error) {
	rd, err := s.roundFor(t, want, strict)
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
	Round    int             `json:"round"` // 0 = the default open round
}

const maxWorkflow = 5 << 20

func (s *Server) apiAnalyze(w http.ResponseWriter, r *http.Request, t *Team) {
	var req analyzeReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkflow+4096)).Decode(&req); err != nil {
		jsonErr(w, 400, "that file isn't valid JSON (or is over 5 MB)")
		return
	}
	_, p, _ := s.currentProblem(t, req.Round, true)
	if p == nil {
		jsonErr(w, 409, "that round isn't open right now")
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
	Kind     string                       `json:"kind"`  // "sample" (free, visible tests) or "attempt"
	Round    int                          `json:"round"` // 0 = the default open round
}

func (s *Server) apiRun(w http.ResponseWriter, r *http.Request, t *Team) {
	var req runReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxWorkflow+64<<10)).Decode(&req); err != nil {
		jsonErr(w, 400, "that file isn't valid JSON (or is over 5 MB)")
		return
	}
	rd, p, _ := s.currentProblem(t, req.Round, true)
	if p == nil {
		jsonErr(w, 409, "that round isn't open right now")
		return
	}
	if req.Kind != KindSample && req.Kind != KindAttempt {
		jsonErr(w, 400, "choose a sample run or an attempt")
		return
	}
	wf, err := workflow.Parse(req.Workflow)
	if err != nil {
		jsonErr(w, 400, err.Error())
		return
	}
	an := workflow.Analyze(wf, req.Workflow, s.cat, p.Expect())
	run := &Run{TeamID: t.ID, Round: rd.N, ProblemID: p.ID, Seed: rand.Int63(), Workflow: string(req.Workflow), Kind: req.Kind}
	run.Features = featuresJSON(an)
	run.WfName.String, run.WfName.Valid = wf.Name(), true

	if len(an.Errors) > 0 {
		// Graded instantly, never queued; an attempt like this still uses one of the three.
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
		id, err := s.store.Admit(run)
		if err != nil {
			admitErr(w, err)
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
	// Workers only learn the id from the queue, which is pushed after the keys are stored.
	id, err := s.store.Admit(run)
	if err != nil {
		admitErr(w, err)
		return
	}
	s.keys.put(id, creds)
	s.queue.push(id)
	jsonOut(w, map[string]any{"runId": id})
}

// hiddenView is all a team learns about hidden tests: how many there were and how many passed.
type hiddenView struct {
	Passed int `json:"passed"`
	Total  int `json:"total"`
}

// runNotices are run-level flags derived from every test (hidden ones included), so the page can
// explain a rate-limited key or a missing model without showing any hidden test.
type runNotices struct {
	RateLimited      bool `json:"rateLimited,omitempty"`
	KeyRejected      bool `json:"keyRejected,omitempty"`
	ModelUnavailable bool `json:"modelUnavailable,omitempty"`
	TimeLimit        bool `json:"timeLimit,omitempty"`
	JudgeError       bool `json:"judgeError,omitempty"`
}

// runView is a run as its team sees it: the visible samples in full, hidden tests only as a count.
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
	Kind     string              `json:"kind"`
	Refunded bool                `json:"refunded,omitempty"`
	Score    float64             `json:"score"`
	Hidden   *hiddenView         `json:"hidden,omitempty"`
	Notices  runNotices          `json:"notices"`
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
	v := runView{ID: run.ID, Status: run.Status, Passed: run.Passed.Float64, Total: run.Total.Float64, Invalid: run.Invalid.String, Infra: run.Infra.String, Created: run.Created, Tests: []grade.TestVerdict{}, Kind: run.Kind, Refunded: run.Kind == KindAttempt && !run.Eligible, Score: run.Score.Float64}
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
	// Counts and notices come from every test, before anything hidden is dropped.
	var hid hiddenView
	for _, tv := range v.Tests {
		if !tv.Visible {
			hid.Total++
			if tv.Verdict == grade.Accepted {
				hid.Passed++
			}
		}
		switch tv.Verdict {
		case grade.RateLimited:
			v.Notices.RateLimited = true
		case grade.KeyRejected:
			v.Notices.KeyRejected = true
		case grade.ModelBad:
			v.Notices.ModelUnavailable = true
		case grade.TimeLimit:
			v.Notices.TimeLimit = true
		case grade.JudgeError:
			v.Notices.JudgeError = true
		}
	}
	if hid.Total > 0 {
		v.Hidden = &hid
	}
	if !admin {
		v.Infra = teamInfra(run)
		visible := v.Tests[:0:0]
		for _, tv := range v.Tests {
			if !tv.Visible {
				continue // hidden tests never reach a team: no id, category, verdict row or timing
			}
			tv.Steps = nil // full step data (responses, simulator snapshots) is for organisers only
			for ci := range tv.Checks {
				tv.Checks[ci].Provider, tv.Checks[ci].Model, tv.Checks[ci].Cached = "", "", false
			}
			visible = append(visible, tv)
		}
		v.Tests = visible
	}
	return v
}

// teamInfra is the plain-language message a team sees for a judge-side problem; raw errors stay
// in the incident log for organisers.
func teamInfra(run *Run) string {
	if run.Infra.String == "" {
		return ""
	}
	return plainJudgeFault
}

func admitErr(w http.ResponseWriter, err error) {
	var ae *AdmitError
	if errors.As(err, &ae) {
		jsonErr(w, ae.Code, ae.Msg)
		return
	}
	jsonErr(w, 500, "db error")
}

// featuresJSON extracts the analysis' feature report (added by the workflow package) as raw JSON,
// without depending on its Go type.
func featuresJSON(an workflow.Analysis) sql.NullString {
	b, err := json.Marshal(an)
	if err != nil {
		return sql.NullString{}
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return sql.NullString{}
	}
	for _, k := range []string{"features", "Features", "feature_report", "featureReport"} {
		if raw, ok := m[k]; ok && string(raw) != "null" {
			return sql.NullString{String: string(raw), Valid: true}
		}
	}
	return sql.NullString{}
}

// apiStarter serves the open round's starter workflow (the required webhooks, ready to paste).
func (s *Server) apiStarter(w http.ResponseWriter, r *http.Request, t *Team) {
	want, _ := strconv.Atoi(r.URL.Query().Get("round"))
	_, p, err := s.currentProblem(t, want, true)
	if err != nil || p == nil {
		jsonErr(w, 404, "no round is open")
		return
	}
	b, err := p.Starter()
	if err != nil {
		jsonErr(w, 404, "this problem has no starter file")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=starter-"+p.ID+".json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

// statementMD renders organiser-written statements; tables are on, raw HTML stays off.
var statementMD = goldmark.New(goldmark.WithExtensions(extension.Table))
