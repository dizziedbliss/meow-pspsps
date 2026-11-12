package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLeaderboardBestAttemptOfflineAndRefunds(t *testing.T) {
	st := testStore(t)
	if err := st.SetRoundSettings(1, 10, 0, true); err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: st}
	a1, _ := st.Admit(newRun(1, KindAttempt, StatusQueued))
	finish(t, st, a1, 3, 5)
	a2, _ := st.Admit(newRun(1, KindAttempt, StatusQueued))
	finish(t, st, a2, 5, 5)
	a3, _ := st.Admit(newRun(1, KindAttempt, StatusQueued))
	finish(t, st, a3, 2, 5) // worse later attempt: best still counts
	b1, _ := st.Admit(newRun(2, KindAttempt, StatusQueued))
	finish(t, st, b1, 4, 5)
	b2, _ := st.Admit(newRun(2, KindAttempt, StatusQueued))
	_, _ = st.FinishRun(b2, StatusFailed, "", "boom", 0, 0, "") // refunded: ignored
	s1, _ := st.Admit(newRun(2, KindSample, StatusQueued))
	finish(t, st, s1, 5, 5) // samples never score
	if err := st.SetOffline(2, 1, 25, "great demo"); err != nil {
		t.Fatal(err)
	}

	rounds, rows, err := srv.leaderboard(true)
	if err != nil || len(rounds) != 1 || len(rows) != 2 {
		t.Fatalf("%v %v %v", err, rounds, rows)
	}
	// A: 50, B: 40 + 25 offline = 65 → B first.
	if rows[0].Team.Code != "B" || rows[0].Total != 65 || rows[0].Rank != 1 {
		t.Fatalf("row 0: %+v", rows[0])
	}
	if rows[1].Team.Code != "A" || rows[1].Total != 50 || rows[1].Rounds[1].Attempts != 3 {
		t.Fatalf("row 1: %+v %+v", rows[1], rows[1].Rounds[1])
	}
	if rows[0].Rounds[1].Attempts != 1 {
		t.Fatalf("refunded attempt counted: %+v", rows[0].Rounds[1])
	}
	// Hidden rounds don't show.
	if err := st.SetRoundSettings(1, 10, 0, false); err != nil {
		t.Fatal(err)
	}
	if rounds, rows, _ := srv.leaderboard(true); len(rounds) != 0 || len(rows) != 0 {
		t.Fatalf("hidden round on the board: %v %v", rounds, rows)
	}
}

func TestBoardPageAndCSV(t *testing.T) {
	srv, err := New(Config{DBPath: t.TempDir() + "/j.db", ProblemsDir: "../../problems", Rounds: map[int]string{1: "dummy"}})
	if err != nil {
		t.Fatal(err)
	}
	st := srv.store
	_ = st.AddTeam("T1", "Cat <b>Herders</b>", "x")
	_ = st.SetRoundStatus(1, "open")
	_ = st.SetRoundSettings(1, 2, 0, true)
	id, err := st.Admit(&Run{TeamID: 1, Round: 1, ProblemID: "dummy", Status: StatusQueued, Kind: KindAttempt, Workflow: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	finish(t, st, id, 3, 4)

	h := srv.Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/board", nil)) // public: no login
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "Automathon 2.0") || !strings.Contains(body, "VeryJudgyCat") ||
		!strings.Contains(body, `http-equiv="refresh" content="15"`) || !strings.Contains(body, "Cat &lt;b&gt;Herders") {
		t.Fatalf("board: %d\n%s", rec.Code, body)
	}
	if strings.Contains(body, "hidden") && strings.Contains(body, "sample_") {
		t.Fatal("board leaks test details")
	}
	// CSV is admin only.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/export.csv", nil))
	if rec.Code != 303 {
		t.Fatalf("csv without admin: %d", rec.Code)
	}
	_ = st.CreateSession("adm", 0, true)
	req := httptest.NewRequest("GET", "/admin/export.csv", nil)
	req.AddCookie(&http.Cookie{Name: adminCookieName, Value: "adm"})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "rank,team_code,team_name,round1_judge") || !strings.Contains(rec.Body.String(), "1,T1,Cat <b>Herders</b>,6,0,3,4,1") {
		t.Fatalf("csv:\n%s", rec.Body.String())
	}
}
