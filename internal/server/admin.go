package server

import (
	"crypto/subtle"
	"encoding/csv"
	"fmt"
	"log"
	"net/http"
	"strconv"
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
	setSession(w, r, tok)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// teamRow is one line of the evaluator table.
type teamRow struct {
	Team         Team
	Attempts     int
	Invalid      int
	Best         float64
	Latest       float64
	Total        float64
	FirstFull    int64 // unix time of the first run passing everything
	LastActivity int64
	Sub          *Submission
	SubPassed    float64
	SubTotal     float64
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
	subs, err := s.store.Submissions(round)
	if err != nil {
		return nil, err
	}
	byTeam := map[int64]*teamRow{}
	rows := make([]teamRow, len(teams))
	for i, t := range teams {
		rows[i] = teamRow{Team: t, Sub: subs[t.ID]}
		byTeam[t.ID] = &rows[i]
	}
	runScore := map[int64][2]float64{}
	for _, run := range runs {
		row := byTeam[run.TeamID]
		if row == nil || run.Status == StatusFailed {
			continue
		}
		runScore[run.ID] = [2]float64{run.Passed.Float64, run.Total.Float64}
		row.Attempts++
		if run.Invalid.Valid {
			row.Invalid++
		}
		if run.Status == StatusDone {
			row.Latest, row.Total = run.Passed.Float64, run.Total.Float64
			if run.Passed.Float64 > row.Best {
				row.Best = run.Passed.Float64
			}
			if row.FirstFull == 0 && run.Total.Float64 > 0 && run.Passed.Float64 == run.Total.Float64 {
				row.FirstFull = run.Finished.Int64
			}
		}
		row.LastActivity = run.Created
	}
	for i := range rows {
		if rows[i].Sub != nil {
			sc := runScore[rows[i].Sub.RunID]
			rows[i].SubPassed, rows[i].SubTotal = sc[0], sc[1]
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
	s.render(w, "admin.html", map[string]any{
		"Rounds": rounds, "Selected": sel, "Rows": rows, "Titles": titles,
		"Queue": s.queue.length(), "Workers": workers, "Msg": r.URL.Query().Get("msg"),
		"JudgeOn": s.cfg.Judge != nil,
	})
}

func (s *Server) adminRound(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.FormValue("n"))
	action := r.FormValue("action")
	msg := ""
	switch action {
	case "open":
		if open, _ := s.store.OpenRound(); open != nil && open.N != n {
			msg = s.closeRound(open.N) + " "
		}
		if err := s.store.SetRoundStatus(n, "open"); err != nil {
			http.Error(w, "db error", 500)
			return
		}
		msg += fmt.Sprintf("Round %d is open.", n)
	case "close":
		msg = s.closeRound(n)
	case "reopen":
		if err := s.store.SetRoundStatus(n, "open"); err != nil {
			http.Error(w, "db error", 500)
			return
		}
		msg = fmt.Sprintf("Round %d reopened (existing submissions stay).", n)
	}
	log.Printf("admin: %s", msg)
	http.Redirect(w, r, "/admin?round="+strconv.Itoa(n)+"&msg="+urlQuery(msg), http.StatusSeeOther)
}

func (s *Server) closeRound(n int) string {
	_ = s.store.SetRoundStatus(n, "closed")
	auto, _ := s.store.AutoSubmitRound(n)
	return fmt.Sprintf("Round %d closed; %d teams auto-submitted their latest run.", n, auto)
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

func (s *Server) adminExport(w http.ResponseWriter, r *http.Request) {
	n, _ := strconv.Atoi(r.URL.Query().Get("round"))
	rows, err := s.roundRows(n)
	if err != nil {
		http.Error(w, "db error", 500)
		return
	}
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=round-%d.csv", n))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"team_code", "team_name", "attempts", "invalid_uploads", "best_passed", "latest_passed", "tests_total",
		"first_full_pass", "submitted", "auto_submitted", "submit_time", "submitted_passed", "submitted_run_id", "last_activity"})
	t := func(u int64) string {
		if u == 0 {
			return ""
		}
		return time.Unix(u, 0).Local().Format(time.DateTime)
	}
	f := func(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }
	for _, row := range rows {
		sub, auto, at, sp, sid := "no", "", "", "", ""
		if row.Sub != nil {
			sub, at, sp, sid = "yes", t(row.Sub.At), f(row.SubPassed), strconv.FormatInt(row.Sub.RunID, 10)
			auto = strconv.FormatBool(row.Sub.Auto)
		}
		_ = cw.Write([]string{row.Team.Code, row.Team.Name, strconv.Itoa(row.Attempts), strconv.Itoa(row.Invalid), f(row.Best), f(row.Latest), f(row.Total),
			t(row.FirstFull), sub, auto, at, sp, sid, t(row.LastActivity)})
	}
	cw.Flush()
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
	s.render(w, "admin_run.html", map[string]any{"Run": run, "View": s.viewRun(run, true), "Team": team, "Runs": runs})
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
