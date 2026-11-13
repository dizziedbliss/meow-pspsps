// Package server is the central judge site: team pages, admin pages, the job queue and the API
// workers pull jobs from.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/dizziedbliss/meow-pspsps/internal/catalog"
	"github.com/dizziedbliss/meow-pspsps/internal/grade"
	"github.com/dizziedbliss/meow-pspsps/internal/problem"
	"github.com/dizziedbliss/meow-pspsps/internal/runner"
	"github.com/dizziedbliss/meow-pspsps/web"
)

// Config is how `judge serve` is set up.
type Config struct {
	DBPath        string
	ProblemsDir   string
	Rounds        map[int]string // round number → problem id
	AdminPassword string
	WorkerToken   string
	Judge         grade.Judge
	KeyTTL        time.Duration
}

// Server is the central site.
type Server struct {
	cfg      Config
	store    *Store
	problems map[string]*problem.Problem
	cat      *catalog.Catalog
	tmpl     *template.Template
	keys     *keyStore
	queue    *queue
	workers  sync.Map // worker name → last seen time
}

// New opens the store, loads problems and prepares templates.
func New(cfg Config) (*Server, error) {
	st, err := OpenStore(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	probs, err := problem.LoadAll(cfg.ProblemsDir)
	if err != nil {
		return nil, err
	}
	for n, id := range cfg.Rounds {
		if probs[id] == nil {
			return nil, fmt.Errorf("round %d uses problem %q, which isn't in %s", n, id, cfg.ProblemsDir)
		}
		if err := st.EnsureRound(n, id); err != nil {
			return nil, err
		}
	}
	cat, err := catalog.Default()
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("").Funcs(tmplFuncs).ParseFS(web.FS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	if cfg.KeyTTL == 0 {
		cfg.KeyTTL = 15 * time.Minute
	}
	s := &Server{cfg: cfg, store: st, problems: probs, cat: cat, tmpl: tmpl, keys: newKeyStore(cfg.KeyTTL), queue: newQueue()}
	// Keys live only in memory, so anything queued before a restart can't run any more.
	if n, err := st.FailStale(now()+1, now()+1, plainJudgeFault, "restart"); err == nil && n > 0 {
		log.Printf("marked %d unfinished runs as failed after restart", n)
	}
	return s, nil
}

// Handler returns the HTTP routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(web.FS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.loginPost)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /{$}", s.team(s.appPage))
	mux.HandleFunc("GET /api/state", s.team(s.apiState))
	mux.HandleFunc("POST /api/analyze", s.team(s.apiAnalyze))
	mux.HandleFunc("POST /api/run", s.team(s.apiRun))
	mux.HandleFunc("GET /api/runs/{id}", s.team(s.apiRunGet))
	mux.HandleFunc("GET /api/starter", s.team(s.apiStarter))
	mux.HandleFunc("GET /board", s.boardPage)

	mux.HandleFunc("GET /admin/login", s.adminLoginPage)
	mux.HandleFunc("POST /admin/login", s.adminLoginPost)
	mux.HandleFunc("GET /admin", s.admin(s.adminPage))
	mux.HandleFunc("POST /admin/round", s.admin(s.adminRound))
	mux.HandleFunc("POST /admin/round-settings", s.admin(s.adminRoundSettings))
	mux.HandleFunc("POST /admin/offline", s.admin(s.adminOffline))
	mux.HandleFunc("POST /admin/incident", s.admin(s.adminIncident))
	mux.HandleFunc("GET /admin/export.csv", s.admin(s.adminExport))
	mux.HandleFunc("GET /admin/runs/{id}", s.admin(s.adminRun))
	mux.HandleFunc("GET /admin/runs/{id}/workflow.json", s.admin(s.adminRunWorkflow))

	// ping lets the worker installer check the URL and token without claiming a run.
	mux.HandleFunc("GET /api/worker/ping", s.worker(func(w http.ResponseWriter, r *http.Request) {
		jsonOut(w, map[string]string{"ok": "worker token accepted"})
	}))
	mux.HandleFunc("POST /api/worker/claim", s.worker(s.workerClaim))
	mux.HandleFunc("POST /api/worker/result", s.worker(s.workerResult))
	return securityHeaders(mux)
}

// Start runs background housekeeping.
func (s *Server) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.keys.sweep()
				s.store.SweepSessions()
				// Queued runs wait at most as long as their keys live; a run a worker took (measured
				// from when it started, not from when it was queued) is lost after 12 minutes.
				if n, err := s.store.FailStale(now()-int64(s.cfg.KeyTTL.Seconds())-60, now()-12*60, plainJudgeFault, "worker-timeout"); err == nil && n > 0 {
					log.Printf("failed %d stale runs", n)
				}
			}
		}
	}()
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'")
		h.ServeHTTP(w, r)
	})
}

// --- sessions -------------------------------------------------------------------------------

// Teams and organisers get different cookies, so logging out of one never logs out the other in the same browser.
const (
	cookieName      = "judge_session"
	adminCookieName = "judge_admin"
)

func token() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func setSession(w http.ResponseWriter, r *http.Request, name, tok string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", MaxAge: 60 * 60 * 24})
}

type teamCtxKey struct{}

func (s *Server) team(h func(http.ResponseWriter, *http.Request, *Team)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		var t *Team
		if err == nil {
			if tid, _, ok := s.store.Session(c.Value); ok && tid > 0 {
				t, _ = s.store.TeamByID(tid)
			}
		}
		if t == nil {
			if r.URL.Path == "/" {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			jsonErr(w, http.StatusUnauthorized, "please log in again")
			return
		}
		// JSON APIs require a JSON body: browsers can't send that cross-site without CORS, so
		// this doubles as CSRF protection together with SameSite cookies.
		if r.Method == http.MethodPost && r.Header.Get("Content-Type") != "application/json" {
			jsonErr(w, http.StatusUnsupportedMediaType, "send JSON")
			return
		}
		h(w, r, t)
	}
}

func (s *Server) admin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(adminCookieName)
		if err == nil {
			if _, admin, ok := s.store.Session(c.Value); ok && admin {
				h(w, r)
				return
			}
		}
		http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
	}
}

func (s *Server) worker(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		want := "Bearer " + s.cfg.WorkerToken
		if s.cfg.WorkerToken == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			jsonErr(w, http.StatusUnauthorized, "bad worker token")
			return
		}
		h(w, r)
	}
}

func jsonOut(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// --- in-memory key store --------------------------------------------------------------------

// keyStore holds a run's credentials in memory only, until a worker claims the run or they expire.
// Nothing here is ever written to disk or logged.
type keyStore struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[int64]keyEntry
}

type keyEntry struct {
	creds   map[string]map[string]string
	expires time.Time
}

func newKeyStore(ttl time.Duration) *keyStore {
	return &keyStore{ttl: ttl, m: map[int64]keyEntry{}}
}

func (k *keyStore) put(runID int64, creds map[string]map[string]string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[runID] = keyEntry{creds: creds, expires: time.Now().Add(k.ttl)}
}

// take removes and returns a run's credentials.
func (k *keyStore) take(runID int64) (map[string]map[string]string, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	e, ok := k.m[runID]
	delete(k.m, runID)
	if !ok || time.Now().After(e.expires) {
		return nil, false
	}
	return e.creds, true
}

func (k *keyStore) sweep() {
	k.mu.Lock()
	defer k.mu.Unlock()
	for id, e := range k.m {
		if time.Now().After(e.expires) {
			delete(k.m, id)
		}
	}
}

// --- queue ----------------------------------------------------------------------------------

// queue is FIFO over run ids, with a signal for long-polling workers.
type queue struct {
	mu     sync.Mutex
	ids    []int64
	notify chan struct{}
}

func newQueue() *queue { return &queue{notify: make(chan struct{}, 1)} }

func (q *queue) push(id int64) {
	q.mu.Lock()
	q.ids = append(q.ids, id)
	q.mu.Unlock()
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *queue) pop() (int64, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.ids) == 0 {
		return 0, false
	}
	id := q.ids[0]
	q.ids = q.ids[1:]
	if len(q.ids) > 0 { // wake the next waiting worker too
		select {
		case q.notify <- struct{}{}:
		default:
		}
	}
	return id, true
}

// position is 1-based, or 0 if not queued.
func (q *queue) position(id int64) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, x := range q.ids {
		if x == id {
			return i + 1
		}
	}
	return 0
}

func (q *queue) length() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.ids)
}

// --- worker API -----------------------------------------------------------------------------

type claimReq struct {
	Worker string `json:"worker"`
}

// workerClaim long-polls for up to 25s and hands out the next job.
func (s *Server) workerClaim(w http.ResponseWriter, r *http.Request) {
	var req claimReq
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req)
	if req.Worker == "" {
		req.Worker = r.RemoteAddr
	}
	s.workers.Store(req.Worker, time.Now())
	deadline := time.After(25 * time.Second)
	for {
		if id, ok := s.queue.pop(); ok {
			job, err := s.buildJob(id, req.Worker)
			if err != nil {
				log.Printf("run %d: %v", id, err)
				continue
			}
			if job == nil {
				continue
			}
			jsonOut(w, job)
			return
		}
		select {
		case <-s.queue.notify:
		case <-deadline:
			w.WriteHeader(http.StatusNoContent)
			return
		case <-r.Context().Done():
			return
		}
	}
}

// plainJudgeFault is what a team sees when something broke on our side; the raw error goes to the
// organisers as an incident.
const plainJudgeFault = "Something failed on our side while testing your workflow. This attempt is refunded and the organisers have been told."

// failRun ends a run as a judge-side failure: refunded, with the raw error kept for organisers.
func (s *Server) failRun(run *Run, kind, test, raw string) {
	ok, err := s.store.FinishRun(run.ID, StatusFailed, "", plainJudgeFault, 0, 0, "")
	if err != nil {
		log.Printf("run %d: finish: %v", run.ID, err)
	}
	if ok {
		_ = s.store.AddIncident(run.TeamID, run.ID, test, kind, raw)
	}
}

// renderFor renders a run's tests; a sample run only gets the visible ones.
func (s *Server) renderFor(run *Run) ([]*problem.Rendered, error) {
	rendered, err := s.problems[run.ProblemID].RenderAt(run.Seed, run.ID, time.Unix(run.RefTime, 0))
	if err != nil || run.Kind != KindSample {
		return rendered, err
	}
	var vis []*problem.Rendered
	for _, rt := range rendered {
		if rt.Def.Visible {
			vis = append(vis, rt)
		}
	}
	return vis, nil
}

func (s *Server) buildJob(id int64, worker string) (*runner.Job, error) {
	run, err := s.store.GetRun(id)
	if err != nil || run == nil {
		return nil, fmt.Errorf("load: %v", err)
	}
	if run.Status != StatusQueued {
		return nil, nil // failed as stale while it waited
	}
	creds, ok := s.keys.take(id)
	if !ok {
		s.failRun(run, "keys-expired", "", "keys expired before a test machine was free (queue too long or server restarted)")
		return nil, nil
	}
	p := s.problems[run.ProblemID]
	rendered, err := s.renderFor(run)
	if err != nil {
		s.failRun(run, "render", "", "rendering tests: "+err.Error())
		return nil, err
	}
	an, err := s.analyzeRaw([]byte(run.Workflow), p)
	if err != nil || len(an.Errors) > 0 {
		s.failRun(run, "reanalyze", "", fmt.Sprintf("re-reading the stored workflow: %v %v", err, an.Errors))
		return nil, fmt.Errorf("re-analyze: %v %v", err, an.Errors)
	}
	job := p.NewJob(id, json.RawMessage(run.Workflow), an, creds)
	for _, rt := range rendered {
		job.Tests = append(job.Tests, rt.Job)
	}
	if ok, err := s.store.MarkRunning(id, worker); err != nil || !ok {
		return nil, err
	}
	return job, nil
}

func (s *Server) workerResult(w http.ResponseWriter, r *http.Request) {
	var res runner.Result
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&res); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad result")
		return
	}
	run, err := s.store.GetRun(res.RunID)
	if err != nil || run == nil || run.Status != StatusRunning {
		jsonErr(w, http.StatusConflict, "run not running")
		return
	}
	if ok, _ := s.store.SetStatus(run.ID, StatusRunning, StatusGrading); !ok {
		jsonErr(w, http.StatusConflict, "run not running")
		return
	}
	w.WriteHeader(http.StatusAccepted)
	go s.gradeRun(run, &res) // rubric grading can take a few seconds; the worker needn't wait
}

func (s *Server) gradeRun(run *Run, res *runner.Result) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if res.Infra != "" {
		s.failRun(run, "infra", "", res.Infra)
		return
	}
	rendered, err := s.renderFor(run)
	if err != nil {
		s.failRun(run, "render", "", "rendering tests for grading: "+err.Error())
		return
	}
	verdicts := make([]grade.TestVerdict, len(rendered))
	var passed, total float64
	if res.Invalid != "" {
		for i, rt := range rendered {
			verdicts[i] = grade.TestVerdict{ID: rt.Def.ID, Category: rt.Def.Category, Visible: rt.Def.Visible, Weight: rt.Def.Weight, Verdict: grade.Invalid}
			total += rt.Def.Weight
		}
	} else {
		byID := map[string]*runner.TestResult{}
		for i := range res.Tests {
			byID[res.Tests[i].ID] = &res.Tests[i]
		}
		var wg sync.WaitGroup
		for i, rt := range rendered {
			wg.Add(1)
			go func(i int, rt *problem.Rendered) {
				defer wg.Done()
				verdicts[i] = grade.Test(ctx, rt, byID[rt.Def.ID], s.cfg.Judge)
			}(i, rt)
		}
		wg.Wait()
		for _, v := range verdicts {
			if v.Verdict == grade.JudgeError || v.Verdict == grade.NotRun {
				log.Printf("run %d: grading incomplete", run.ID)
				kind := "grader"
				if v.Verdict == grade.NotRun {
					kind = "test-not-run"
				}
				s.failRun(run, kind, v.ID, v.Error)
				return
			}
		}
		for _, v := range verdicts {
			total += v.Weight
			if v.Verdict == grade.Accepted {
				passed += v.Weight
			}
		}
	}
	b, _ := json.Marshal(verdicts)
	ok, err := s.store.FinishRun(run.ID, StatusDone, res.Invalid, "", passed, total, string(b))
	if err != nil || !ok {
		log.Printf("run %d: result dropped (finished=%v err=%v): it was already closed", run.ID, ok, err)
		return
	}
	// A test still rate-limited after the retries is not the team's fault: refund the attempt but
	// keep the verdicts visible.
	for _, v := range verdicts {
		if v.Verdict == grade.RateLimited {
			_ = s.store.Refund(run.ID)
			_ = s.store.AddIncident(run.TeamID, run.ID, v.ID, "rate-limited", "test still Key Rate-Limited after retries; attempt refunded")
			break
		}
	}
	log.Printf("run %d team %d (%s): %.0f/%.0f in %.1fs on %s", run.ID, run.TeamID, run.Kind, passed, total, res.Seconds, res.Worker)
}
