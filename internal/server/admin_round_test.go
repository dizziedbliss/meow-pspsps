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
