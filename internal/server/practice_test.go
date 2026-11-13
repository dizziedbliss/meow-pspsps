package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPracticeAccountIsUnlimitedAndExcludedFromBoard(t *testing.T) {
	s := testStore(t)
	if err := s.AddTeam(practiceTeamCode, "Practice", "hash"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRoundStatus(1, "pending"); err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: s}
	rd, err := srv.roundFor(&Team{Code: practiceTeamCode}, 0, false)
	if err != nil || rd == nil || rd.N != 1 {
		t.Fatalf("practice preview: %+v %v", rd, err)
	}
	if _, err := s.Admit(newRun(1, KindAttempt, StatusQueued)); err == nil {
		t.Fatal("ordinary team entered a pending round")
	}
	for i := 0; i < 5; i++ {
		id, err := s.Admit(newRun(3, KindAttempt, StatusQueued))
		if err != nil {
			t.Fatalf("practice attempt %d: %v", i, err)
		}
		finish(t, s, id, 1, 1)
	}
	for i := 0; i < 2; i++ {
		id, err := s.Admit(newRun(3, KindSample, StatusQueued))
		if err != nil {
			t.Fatalf("practice sample %d: %v", i, err)
		}
		finish(t, s, id, 1, 1)
	}
	if err := s.SetRoundSettings(1, 1, 0, true); err != nil {
		t.Fatal(err)
	}
	_, rows, err := srv.leaderboard(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Team.Code == practiceTeamCode {
			t.Fatal("practice account on leaderboard")
		}
	}
}

func TestPracticeSeesPendingRoundAndUnlimitedState(t *testing.T) {
	srv, err := New(Config{DBPath: t.TempDir() + "/j.db", ProblemsDir: "../../problems", Rounds: map[int]string{1: "dummy"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.store.AddTeam(practiceTeamCode, "Practice", "hash"); err != nil {
		t.Fatal(err)
	}
	team, err := srv.store.TeamByCode(practiceTeamCode)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.store.CreateSession("test-token", team.ID, false); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "test-token"})
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	var state stateResp
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || state.Round == nil || !state.Attempts.Unlimited {
		t.Fatalf("bad practice state: status %d, state %+v", w.Code, state)
	}
}

func TestStateFollowsRequestedRoundWhenTwoAreOpen(t *testing.T) {
	srv, err := New(Config{DBPath: t.TempDir() + "/j.db", ProblemsDir: "../../problems", Rounds: map[int]string{1: "dummy", 2: "dummy"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.store.AddTeam("TEAM1", "Team One", "hash"); err != nil {
		t.Fatal(err)
	}
	team, err := srv.store.TeamByCode("TEAM1")
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.store.CreateSession("tok", team.ID, false); err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{1, 2} {
		if err := srv.store.SetRoundStatus(n, "open"); err != nil {
			t.Fatal(err)
		}
	}
	get := func(path string) (int, stateResp) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: cookieName, Value: "tok"})
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		var st stateResp
		_ = json.Unmarshal(w.Body.Bytes(), &st)
		return w.Code, st
	}
	for path, want := range map[string]int{"/api/state": 1, "/api/state?round=1": 1, "/api/state?round=2": 2, "/api/state?round=9": 1} {
		code, st := get(path)
		if code != http.StatusOK || st.Round == nil || st.Round.N != want || len(st.Rounds) != 2 {
			t.Fatalf("%s: status %d round %+v tabs %+v, want round %d with 2 tabs", path, code, st.Round, st.Rounds, want)
		}
	}
	// Acting on a round that isn't open is refused, not redirected to another round.
	body := strings.NewReader(`{"workflow":{},"kind":"sample","round":9}`)
	req := httptest.NewRequest(http.MethodPost, "/api/run", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "tok"})
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("run in a closed round: status %d, want 409", w.Code)
	}
}
