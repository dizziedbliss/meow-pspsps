package server

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestSessionExpiresServerSide(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "judge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if err := s.CreateSession("test", 0, true); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.Session("test"); !ok {
		t.Fatal("new session rejected")
	}
	if _, err := s.db.Exec(`UPDATE sessions SET created = ? WHERE token = ?`, now()-24*60*60-1, "test"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.Session("test"); ok {
		t.Fatal("expired session accepted")
	}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "judge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.Close() })
	for _, c := range []string{"A", "B"} {
		if err := s.AddTeam(c, "Team "+c, "x"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EnsureRound(1, "p1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRoundStatus(1, "open"); err != nil {
		t.Fatal(err)
	}
	return s
}

func newRun(team int64, kind, status string) *Run {
	return &Run{TeamID: team, Round: 1, ProblemID: "p1", Seed: 1, Status: status, Kind: kind, Workflow: "{}"}
}

// finish an attempt immediately so the next one is admitted.
func finish(t *testing.T, s *Store, id int64, passed, total float64) {
	t.Helper()
	if ok, err := s.FinishRun(id, StatusDone, "", "", passed, total, "[]"); err != nil || !ok {
		t.Fatalf("finish: %v %v", ok, err)
	}
}

func TestAdmissionConcurrentCannotExceedLimits(t *testing.T) {
	s := testStore(t)
	var wg sync.WaitGroup
	var admitted atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Admit(newRun(1, KindAttempt, StatusQueued)); err == nil {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatalf("20 concurrent requests admitted %d runs, want 1 (one in flight at a time)", admitted.Load())
	}
	// Sequentially: three attempts, the fourth refused.
	s2 := testStore(t)
	for i := 0; i < MaxAttempts; i++ {
		id, err := s2.Admit(newRun(1, KindAttempt, StatusQueued))
		if err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
		finish(t, s2, id, 1, 2)
	}
	_, err := s2.Admit(newRun(1, KindAttempt, StatusQueued))
	var ae *AdmitError
	if !errors.As(err, &ae) || ae.Code != 409 {
		t.Fatalf("4th attempt: %v, want 409", err)
	}
	// Another team is unaffected.
	if _, err := s2.Admit(newRun(2, KindAttempt, StatusQueued)); err != nil {
		t.Fatal(err)
	}
}

func TestAdmissionConcurrentFinishedAttemptsCapAtThree(t *testing.T) {
	s := testStore(t)
	var wg sync.WaitGroup
	var admitted atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				id, err := s.Admit(newRun(1, KindAttempt, StatusQueued))
				if err == nil {
					admitted.Add(1)
					_, _ = s.FinishRun(id, StatusDone, "", "", 1, 2, "[]")
					return
				}
				var ae *AdmitError
				if errors.As(err, &ae) && ae.Code == 429 {
					continue // someone else's run is in flight; retry
				}
				return
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != MaxAttempts {
		t.Fatalf("admitted %d attempts, want exactly %d", admitted.Load(), MaxAttempts)
	}
}

func TestAdmissionClosedRoundAndSampleThrottle(t *testing.T) {
	s := testStore(t)
	id, err := s.Admit(newRun(1, KindSample, StatusQueued))
	if err != nil {
		t.Fatal(err)
	}
	finish(t, s, id, 1, 1)
	if _, err := s.Admit(newRun(1, KindSample, StatusQueued)); err == nil {
		t.Fatal("second sample within 2 minutes was admitted")
	}
	// Samples never use attempts.
	if n, _ := s.AttemptsUsed(1, 1); n != 0 {
		t.Fatalf("samples used %d attempts", n)
	}
	if _, err := s.db.Exec(`UPDATE runs SET created = created - 130`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(newRun(1, KindSample, StatusQueued)); err != nil {
		t.Fatalf("sample after throttle: %v", err)
	}
	if err := s.SetRoundStatus(1, "closed"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admit(newRun(2, KindAttempt, StatusQueued)); err == nil {
		t.Fatal("run admitted in a closed round")
	}
}

func TestScoringCapAndRefunds(t *testing.T) {
	s := testStore(t)
	if err := s.SetRoundSettings(1, 5, 40, true); err != nil {
		t.Fatal(err)
	}
	id, _ := s.Admit(newRun(1, KindAttempt, StatusQueued))
	finish(t, s, id, 6, 10)
	r, _ := s.GetRun(id)
	if r.Score.Float64 != 30 {
		t.Fatalf("score %v, want 30 (6 × 5)", r.Score.Float64)
	}
	id2, _ := s.Admit(newRun(1, KindAttempt, StatusQueued))
	finish(t, s, id2, 10, 10)
	if r, _ := s.GetRun(id2); r.Score.Float64 != 40 {
		t.Fatalf("score %v, want 40 (capped)", r.Score.Float64)
	}
	if err := s.SetRoundSettings(1, 2, 0, true); err != nil { // changing marks rescores
		t.Fatal(err)
	}
	if r, _ := s.GetRun(id2); r.Score.Float64 != 20 {
		t.Fatalf("rescored %v, want 20", r.Score.Float64)
	}
	// A judge-side failure is refunded: it doesn't use an attempt.
	id3, _ := s.Admit(newRun(1, KindAttempt, StatusQueued))
	if n, _ := s.AttemptsUsed(1, 1); n != 3 {
		t.Fatalf("used %d, want 3", n)
	}
	if ok, _ := s.FinishRun(id3, StatusFailed, "", "boom", 0, 0, ""); !ok {
		t.Fatal("fail not applied")
	}
	if n, _ := s.AttemptsUsed(1, 1); n != 2 {
		t.Fatalf("after refund used %d, want 2", n)
	}
	if _, err := s.Admit(newRun(1, KindAttempt, StatusQueued)); err != nil {
		t.Fatalf("refunded attempt not given back: %v", err)
	}
}

func TestLateResultCannotOverwriteStaleFailure(t *testing.T) {
	s := testStore(t)
	id, _ := s.Admit(newRun(1, KindAttempt, StatusQueued))
	if ok, _ := s.MarkRunning(id, "w"); !ok {
		t.Fatal("not marked running")
	}
	// A run that waited long in the queue but only just started must not be expired.
	if _, err := s.db.Exec(`UPDATE runs SET created = created - 3600`); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.FailStale(now()-900, now()-720, "x", "worker-timeout"); n != 0 {
		t.Fatalf("a run that just started was failed as stale (%d)", n)
	}
	if _, err := s.db.Exec(`UPDATE runs SET started = started - 3600`); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.FailStale(now()-900, now()-720, "x", "worker-timeout"); n != 1 {
		t.Fatalf("stuck run not failed (%d)", n)
	}
	if ok, _ := s.FinishRun(id, StatusDone, "", "", 5, 5, "[]"); ok {
		t.Fatal("late result overwrote a failed run")
	}
	inc, _ := s.Incidents(10)
	if len(inc) != 1 || inc[0].Kind != "worker-timeout" || inc[0].TeamCode != "A" {
		t.Fatalf("incidents: %+v", inc)
	}
}

func TestSweepSessions(t *testing.T) {
	s := testStore(t)
	_ = s.CreateSession("old", 1, false)
	_ = s.CreateSession("new", 1, false)
	_, _ = s.db.Exec(`UPDATE sessions SET created = ? WHERE token = 'old'`, now()-25*3600)
	s.SweepSessions()
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d sessions left, want 1", n)
	}
}

func TestRunListsPreserveRunDetailsWithoutWorkflow(t *testing.T) {
	s := testStore(t)
	run := newRun(1, KindAttempt, StatusQueued)
	run.Workflow = `{"nodes":[{"name":"example"}]}`
	id, err := s.Admit(run)
	if err != nil {
		t.Fatal(err)
	}
	for _, list := range []func() ([]*Run, error){func() ([]*Run, error) { return s.RunsFor(1, 1) }, func() ([]*Run, error) { return s.AllRunsInRound(1) }} {
		rows, err := list()
		if err != nil || len(rows) != 1 || rows[0].ID != id || rows[0].Workflow != "" {
			t.Fatalf("bad list: %+v, %v", rows, err)
		}
	}
	got, err := s.GetRun(id)
	if err != nil || got.Workflow != run.Workflow {
		t.Fatalf("workflow not preserved: %+v, %v", got, err)
	}
}
