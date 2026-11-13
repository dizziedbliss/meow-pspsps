package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestReopeningRoundClosesOtherRound(t *testing.T) {
	st := testStore(t)
	if err := st.EnsureRound(2, "p2"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRoundStatus(2, "closed"); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}
	f := url.Values{"n": {"2"}, "action": {"reopen"}}
	r := httptest.NewRequest(http.MethodPost, "/admin/round", strings.NewReader(f.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.adminRound(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d", w.Code)
	}
	rds, err := st.Rounds()
	if err != nil {
		t.Fatal(err)
	}
	if rds[0].Status != "closed" || rds[1].Status != "open" {
		t.Fatalf("rounds: %+v", rds)
	}
}

func TestRoundSettingsRejectNonFiniteMarks(t *testing.T) {
	st := testStore(t)
	s := &Server{store: st}
	for _, val := range []string{"NaN", "+Inf", "-Inf"} {
		f := url.Values{"n": {"1"}, "marks_per_test": {val}, "max_marks": {"10"}}
		r := httptest.NewRequest(http.MethodPost, "/admin/round-settings", strings.NewReader(f.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		s.adminRoundSettings(w, r)
		if w.Code != http.StatusSeeOther || !strings.Contains(w.Header().Get("Location"), "Marks") {
			t.Fatalf("%q: status %d redirect %q", val, w.Code, w.Header().Get("Location"))
		}
	}
}

func adminRoundPost(t *testing.T, s *Server, n, action string) {
	t.Helper()
	f := url.Values{"n": {n}, "action": {action}}
	r := httptest.NewRequest(http.MethodPost, "/admin/round", strings.NewReader(f.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.adminRound(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("%s round %s: status %d", action, n, w.Code)
	}
}

func roundStatuses(t *testing.T, st *Store) []string {
	t.Helper()
	rds, err := st.Rounds()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rds {
		out = append(out, r.Status)
	}
	return out
}

func TestOpenTooKeepsOtherRoundOpenAndOpenClosesIt(t *testing.T) {
	st := testStore(t) // round 1 open
	if err := st.EnsureRound(2, "p2"); err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}
	adminRoundPost(t, s, "2", "open_too")
	if got := roundStatuses(t, st); got[0] != "open" || got[1] != "open" {
		t.Fatalf("after open_too: %v", got)
	}
	adminRoundPost(t, s, "1", "open") // exclusive: closes round 2
	if got := roundStatuses(t, st); got[0] != "open" || got[1] != "closed" {
		t.Fatalf("after open: %v", got)
	}
	adminRoundPost(t, s, "2", "reopen_too")
	if got := roundStatuses(t, st); got[0] != "open" || got[1] != "open" {
		t.Fatalf("after reopen_too: %v", got)
	}
}

func TestTeamPicksBetweenTwoOpenRounds(t *testing.T) {
	st := testStore(t)
	if err := st.EnsureRound(2, "p2"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRoundStatus(2, "open"); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureRound(3, "p3"); err != nil { // stays pending
		t.Fatal(err)
	}
	s := &Server{store: st}
	team := &Team{Code: "A"}
	cases := []struct {
		want   int
		strict bool
		n      int // 0 = none
	}{{0, false, 1}, {2, true, 2}, {2, false, 2}, {1, true, 1}, {3, true, 0}, {3, false, 1}, {9, true, 0}}
	for _, c := range cases {
		rd, err := s.roundFor(team, c.want, c.strict)
		if err != nil {
			t.Fatal(err)
		}
		got := 0
		if rd != nil {
			got = rd.N
		}
		if got != c.n {
			t.Errorf("roundFor(want %d, strict %v) = round %d, want %d", c.want, c.strict, got, c.n)
		}
	}
	// A private practice account may also pick the pending round.
	if rd, _ := s.roundFor(&Team{Code: practiceTeamCode}, 3, true); rd == nil || rd.N != 3 {
		t.Errorf("practice should reach the pending round: %+v", rd)
	}
}

func TestAttemptsAreCountedPerRoundWhenBothOpen(t *testing.T) {
	st := testStore(t)
	if err := st.EnsureRound(2, "p2"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRoundStatus(2, "open"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxAttempts; i++ {
		id, err := st.Admit(newRun(1, KindAttempt, StatusQueued))
		if err != nil {
			t.Fatalf("round 1 attempt %d: %v", i, err)
		}
		finish(t, st, id, 1, 2)
	}
	if _, err := st.Admit(newRun(1, KindAttempt, StatusQueued)); err == nil {
		t.Fatal("a 4th attempt in round 1 was admitted")
	}
	r2 := newRun(1, KindAttempt, StatusQueued)
	r2.Round, r2.ProblemID = 2, "p2"
	id, err := st.Admit(r2)
	if err != nil {
		t.Fatalf("round 2 should still have its own attempts: %v", err)
	}
	// One run at a time across rounds.
	r1 := newRun(1, KindSample, StatusQueued)
	if _, err := st.Admit(r1); err == nil {
		t.Fatal("a second run was admitted while one was in flight")
	}
	finish(t, st, id, 1, 2)
}

func TestRoundCanGiveExtraAttempts(t *testing.T) {
	st := testStore(t)
	if err := st.SetRoundAttempts(1, MaxAttempts+1); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxAttempts+1; i++ {
		id, err := st.Admit(newRun(1, KindAttempt, StatusQueued))
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		finish(t, st, id, 1, 2)
	}
	if _, err := st.Admit(newRun(1, KindAttempt, StatusQueued)); err == nil {
		t.Fatal("an attempt beyond the round's limit was admitted")
	}
}
