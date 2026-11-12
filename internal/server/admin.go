package server

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"github.com/dizziedbliss/meow-pspsps/internal/workflow"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (s *Server) adminLoginPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "login.html", map[string]any{"Error": r.URL.Query().Get("e") != "", "Admin": true})
}

func (s *Server) adminLoginPost(w http.ResponseWriter, r *http.Request) {
	pw := r.FormValue("password")
	if s.cfg.AdminPassword == "" || subtle.ConstantTimeCompare([]byte(pw), []byte(s.cfg.AdminPassword)) != 1 {
		time.Sleep(500 * time.Millisecond)
		http.Redirect(w, r, "/admin/login?e=1", http.StatusSeeOther)
		return
	}
	tok := token()
	if err := s.store.CreateSession(tok, 0, true); err != nil {
		http.Error(w, "could not log in", 500)
		return
	}
	setSession(w, r, adminCookieName, tok)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// teamRow is one line of the evaluator table.
type teamRow struct {
	Team         Team
	Attempts     int // counted
	Refunded     int
	Samples      int
	Invalid      int
	BestPassed   float64
	BestScore    float64
	BestRun      int64
	Total        float64
	Offline      float64
	HasOffline   bool
	OfflineNote  string
	FirstFull    int64 // unix time of the first attempt passing everything
	LastActivity int64
	Nodes        string
	Integr       string
}

func (s *Server) roundRows(round int) ([]teamRow, error) {
	teams, err := s.store.Teams()
	if err != nil {
		return nil, err
	}
	runs, err := s.store.AllRunsInRound(round)
	if err != nil {
		return nil, err
	}
	off, err := s.store.OfflineScores()
	if err != nil {
		return nil, err
	}
	byTeam := map[int64]*teamRow{}
	rows := make([]teamRow, len(teams))
	for i, t := range teams {
		rows[i] = teamRow{Team: t}
		if o, ok := off[round][t.ID]; ok {
			rows[i].Offline, rows[i].HasOffline, rows[i].OfflineNote = o.Score, true, o.Note
		}
		byTeam[t.ID] = &rows[i]
	}
	for _, run := range runs {
		row := byTeam[run.TeamID]
		if row == nil {
			continue
		}
		row.LastActivity = run.Created
		if run.Kind == KindSample {
			row.Samples++
			continue
		}
		if !run.Eligible {
			row.Refunded++
			continue
		}
		row.Attempts++
		if run.Invalid.Valid {
			row.Invalid++
		}
		if run.Status == StatusDone {
			row.Total = run.Total.Float64
			if row.BestRun == 0 || run.Score.Float64 > row.BestScore {
				row.BestPassed, row.BestScore, row.BestRun = run.Passed.Float64, run.Score.Float64, run.ID
				row.Nodes, row.Integr = featureCounts(run.Features.String)
			}
			if row.FirstFull == 0 && run.Total.Float64 > 0 && run.Passed.Float64 == run.Total.Float64 {
				row.FirstFull = run.Finished.Int64
			}
		}
	}
	return rows, nil
}

func (s *Server) adminPage(w http.ResponseWriter, r *http.Request) {
	rounds, err := s.store.Rounds()
	if err != nil {
		http.Error(w, "db error", 500)
		return
	}
	sel, _ := strconv.Atoi(r.URL.Query().Get("round"))
	if sel == 0 {
		for _, rd := range rounds {
			if rd.Status == "open" || (sel == 0 && rd.Status == "closed") {
				sel = rd.N
			}
		}
		if open, _ := s.store.OpenRound(); open != nil {
			sel = open.N
		}
		if sel == 0 && len(rounds) > 0 {
			sel = rounds[0].N
		}
	}
	rows, err := s.roundRows(sel)
	if err != nil {
		http.Error(w, "db error", 500)
		return
	}
	type workerView struct {
		Name string
		Ago  string
	}
	var workers []workerView
	s.workers.Range(func(k, v any) bool {
		workers = append(workers, workerView{Name: k.(string), Ago: time.Since(v.(time.Time)).Round(time.Second).String()})
		return true
	})
	titles := map[string]string{}
	for id, p := range s.problems {
		titles[id] = p.Title
	}
	incidents, _ := s.store.Incidents(100)
	var selRound Round
	for _, rd := range rounds {
		if rd.N == sel {
			selRound = rd
		}
	}
	s.render(w, "admin.html", map[string]any{
		"Rounds": rounds, "Selected": sel, "SelRound": selRound, "Rows": rows, "Titles": titles, "Incidents": incidents, "OpenIncidents": s.store.OpenIncidentCount(),
		"MaxAttempts": MaxAttempts,
		"Queue":       s.queue.length(), "Workers": workers, "Msg": r.URL.Query().Get("msg"),
		"JudgeOn": s.cfg.Judge != nil,
	})
}

func (s *Server) adminRound(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.FormValue("n"))
	action := r.FormValue("action")
	msg := ""
	switch action {
	case "open", "reopen":
		if open, _ := s.store.OpenRound(); open != nil && open.N != n {
			msg = s.closeRound(open.N) + " "
		}
		if err := s.store.SetRoundStatus(n, "open"); err != nil {
			http.Error(w, "db error", 500)
			return
		}
		if action == "reopen" {
			msg += fmt.Sprintf("Round %d reopened.", n)
		} else {
			msg += fmt.Sprintf("Round %d is open.", n)
		}
	case "close":
		msg = s.closeRound(n)
	}
	log.Printf("admin: %s", msg)
	http.Redirect(w, r, "/admin?round="+strconv.Itoa(n)+"&msg="+urlQuery(msg), http.StatusSeeOther)
}

// closeRound stops new runs; runs already admitted still finish and count.
func (s *Server) closeRound(n int) string {
	_ = s.store.SetRoundStatus(n, "closed")
	return fmt.Sprintf("Round %d closed. Runs already in flight will still finish and count.", n)
}

func (s *Server) adminRoundSettings(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.FormValue("n"))
	mpt, err1 := strconv.ParseFloat(strings.TrimSpace(r.FormValue("marks_per_test")), 64)
	max, err2 := strconv.ParseFloat(strings.TrimSpace(r.FormValue("max_marks")), 64)
	msg := "Saved."
	if err1 != nil || err2 != nil || math.IsNaN(mpt) || math.IsInf(mpt, 0) || math.IsNaN(max) || math.IsInf(max, 0) || mpt < 0 || max < 0 {
		msg = "Marks must be numbers (0 or more)."
	} else if err := s.store.SetRoundSettings(n, mpt, max, r.FormValue("show_on_board") != ""); err != nil {
		http.Error(w, "db error", 500)
		return
	}
	http.Redirect(w, r, "/admin?round="+strconv.Itoa(n)+"&msg="+urlQuery(msg), http.StatusSeeOther)
}

func (s *Server) adminOffline(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.FormValue("n"))
	tid, _ := strconv.ParseInt(r.FormValue("team"), 10, 64)
	msg := "Offline score saved."
	if v := strings.TrimSpace(r.FormValue("score")); v == "" {
		_ = s.store.ClearOffline(tid, n)
		msg = "Offline score cleared."
	} else if sc, err := strconv.ParseFloat(v, 64); err != nil || math.IsNaN(sc) || math.IsInf(sc, 0) || sc < 0 {
		msg = "The offline score must be a number (0 or more)."
	} else if err := s.store.SetOffline(tid, n, sc, strings.TrimSpace(r.FormValue("note"))); err != nil {
		http.Error(w, "db error", 500)
		return
	}
	http.Redirect(w, r, "/admin?round="+strconv.Itoa(n)+"&msg="+urlQuery(msg), http.StatusSeeOther)
}

func (s *Server) adminIncident(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	_ = s.store.ResolveIncident(id, r.FormValue("action") != "reopen")
	http.Redirect(w, r, "/admin?round="+urlQuery(r.FormValue("n")), http.StatusSeeOther)
}

func urlQuery(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '.' || c == '-' {
			b = append(b, c)
		} else {
			b = append(b, fmt.Sprintf("%%%02X", c)...)
		}
	}
	return string(b)
}

func (s *Server) adminRun(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	run, err := s.store.GetRun(id)
	if err != nil || run == nil {
		http.NotFound(w, r)
		return
	}
	team, _ := s.store.TeamByID(run.TeamID)
	runs, _ := s.store.RunsFor(run.TeamID, run.Round)
	s.render(w, "admin_run.html", map[string]any{"Run": run, "View": s.viewRun(run, true), "Team": team, "Runs": runs, "Features": featureLines(run.Features.String), "Report": featureReport(run.Features.String), "OpenIncidents": s.store.OpenIncidentCount()})
}

func (s *Server) adminRunWorkflow(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	run, err := s.store.GetRun(id)
	if err != nil || run == nil {
		http.NotFound(w, r)
		return
	}
	team, _ := s.store.TeamByID(run.TeamID)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s-round%d-run%d.json", team.Code, run.Round, run.ID))
	_, _ = w.Write([]byte(run.Workflow))
}

// featureLines flattens the stored feature report into "path: value" lines so the admin page can
// show it whatever its exact shape is.
func featureLines(raw string) []string {
	if raw == "" {
		return nil
	}
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return []string{raw}
	}
	var out []string
	var walk func(path string, v any)
	walk = func(path string, v any) {
		switch x := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(x))
			for k := range x {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				p := k
				if path != "" {
					p = path + " › " + k
				}
				walk(p, x[k])
			}
		case []any:
			scalar := true
			parts := make([]string, len(x))
			for i, e := range x {
				switch e.(type) {
				case map[string]any, []any:
					scalar = false
				default:
					parts[i] = fmt.Sprint(e)
				}
			}
			if scalar {
				out = append(out, path+": "+strings.Join(parts, ", "))
				return
			}
			for i, e := range x {
				walk(fmt.Sprintf("%s[%d]", path, i+1), e)
			}
		default:
			out = append(out, fmt.Sprintf("%s: %v", path, x))
		}
	}
	walk("", v)
	return out
}

// featureSection is one group of node types in the feature report.
type featureSection struct {
	Title string
	Types []workflow.TypeCount
}

// featureView is workflow.Features laid out for the admin run page.
type featureView struct {
	NodeCount        int
	Sections         []featureSection
	Extras           []string
	NotInIdeal       []string
	MissingFromIdeal []string
}

// featureReport reads the stored feature report; nil when it isn't the expected shape (the page then
// falls back to the flattened lines).
func featureReport(raw string) *featureView {
	if raw == "" {
		return nil
	}
	var f workflow.Features
	if json.Unmarshal([]byte(raw), &f) != nil || (f.NodeCount == 0 && len(f.Groups) == 0) {
		return nil
	}
	v := &featureView{NodeCount: f.NodeCount, Extras: f.Extras, NotInIdeal: f.NotInIdeal, MissingFromIdeal: f.MissingFromIdeal}
	for _, g := range []struct{ key, title string }{{"triggers", "Triggers"}, {"ai", "AI"}, {"integrations", "Integrations"}, {"code", "Code"}, {"logic", "Logic"}} {
		if ts := f.Groups[g.key]; len(ts) > 0 {
			v.Sections = append(v.Sections, featureSection{g.title, ts})
		}
	}
	return v
}
