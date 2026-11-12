package server

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA busy_timeout=5000;
PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS teams (
  id INTEGER PRIMARY KEY,
  code TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  pass_hash TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
  token TEXT PRIMARY KEY,
  team_id INTEGER,
  admin INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS rounds (
  n INTEGER PRIMARY KEY,
  problem_id TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending',
  opened_at INTEGER,
  closed_at INTEGER
);
CREATE TABLE IF NOT EXISTS runs (
  id INTEGER PRIMARY KEY,
  team_id INTEGER NOT NULL REFERENCES teams(id),
  round INTEGER NOT NULL,
  problem_id TEXT NOT NULL,
  seed INTEGER NOT NULL,
  status TEXT NOT NULL,
  created INTEGER NOT NULL,
  started INTEGER,
  finished INTEGER,
  worker TEXT,
  wf_name TEXT,
  workflow TEXT NOT NULL,
  invalid TEXT,
  infra TEXT,
  passed REAL,
  total REAL,
  verdicts TEXT
);
CREATE INDEX IF NOT EXISTS runs_team_round ON runs(team_id, round);
CREATE TABLE IF NOT EXISTS offline_scores (
  team_id INTEGER NOT NULL REFERENCES teams(id),
  round INTEGER NOT NULL,
  score REAL NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (team_id, round)
);
CREATE TABLE IF NOT EXISTS incidents (
  id INTEGER PRIMARY KEY,
  at INTEGER NOT NULL,
  team_id INTEGER,
  run_id INTEGER,
  test TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL,
  detail TEXT NOT NULL,
  resolved INTEGER NOT NULL DEFAULT 0
);
`

// Run statuses.
const (
	StatusQueued  = "queued"
	StatusRunning = "running"
	StatusGrading = "grading"
	StatusDone    = "done"
	StatusFailed  = "failed" // judge-side problem; not counted against the team
)

// Store wraps the SQLite database.
type Store struct{ db *sql.DB }

// OpenStore opens (and migrates) the database file.
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: one writer; plenty for 100 teams
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	for _, m := range migrations {
		if err := addColumn(db, m[0], m[1], m[2]); err != nil {
			return nil, fmt.Errorf("migrate %s.%s: %w", m[0], m[1], err)
		}
	}
	return &Store{db: db}, nil
}

// migrations add columns to databases created by older builds: {table, column, definition}.
var migrations = [][3]string{
	{"runs", "kind", "TEXT NOT NULL DEFAULT 'attempt'"},
	{"runs", "ref_time", "INTEGER"},
	{"runs", "eligible", "INTEGER NOT NULL DEFAULT 1"}, // 0 = refunded: doesn't use an attempt or count for the score
	{"runs", "score", "REAL"},
	{"runs", "features", "TEXT"}, // raw JSON feature report from the workflow analysis, shown to organisers
	{"rounds", "marks_per_test", "REAL NOT NULL DEFAULT 1"},
	{"rounds", "max_marks", "REAL NOT NULL DEFAULT 0"}, // 0 = no cap
	{"rounds", "show_on_board", "INTEGER NOT NULL DEFAULT 0"},
}

func addColumn(db *sql.DB, table, col, def string) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, col).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + col + ` ` + def)
	return err
}

func now() int64 { return time.Now().Unix() }

// Team is a login.
type Team struct {
	ID       int64
	Code     string
	Name     string
	PassHash string
}

func (s *Store) TeamByCode(code string) (*Team, error) {
	t := &Team{}
	err := s.db.QueryRow(`SELECT id, code, name, pass_hash FROM teams WHERE code = ?`, code).Scan(&t.ID, &t.Code, &t.Name, &t.PassHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

func (s *Store) TeamByID(id int64) (*Team, error) {
	t := &Team{}
	err := s.db.QueryRow(`SELECT id, code, name, pass_hash FROM teams WHERE id = ?`, id).Scan(&t.ID, &t.Code, &t.Name, &t.PassHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

func (s *Store) Teams() ([]Team, error) {
	rows, err := s.db.Query(`SELECT id, code, name FROM teams ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Team
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.Code, &t.Name); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) AddTeam(code, name, passHash string) error {
	_, err := s.db.Exec(`INSERT INTO teams(code, name, pass_hash) VALUES (?, ?, ?)`, code, name, passHash)
	return err
}

func (s *Store) CreateSession(token string, teamID int64, admin bool) error {
	var tid any
	if teamID > 0 {
		tid = teamID
	}
	_, err := s.db.Exec(`INSERT INTO sessions(token, team_id, admin, created) VALUES (?, ?, ?, ?)`, token, tid, admin, now())
	return err
}

// Session returns (teamID, admin, ok).
func (s *Store) Session(token string) (int64, bool, bool) {
	var tid sql.NullInt64
	var admin bool
	err := s.db.QueryRow(`SELECT team_id, admin FROM sessions WHERE token = ? AND created > ?`, token, now()-24*60*60).Scan(&tid, &admin)
	if err != nil {
		return 0, false, false
	}
	return tid.Int64, admin, true
}

func (s *Store) DeleteSession(token string) {
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
}

// Round is one 2-hour round.
type Round struct {
	N         int
	ProblemID string
	Status    string // pending | open | closed
	OpenedAt  sql.NullInt64
	ClosedAt  sql.NullInt64

	MarksPerTest float64
	MaxMarks     float64 // 0 = no cap
	ShowOnBoard  bool
}

const roundCols = `n, problem_id, status, opened_at, closed_at, marks_per_test, max_marks, show_on_board`

func scanRound(sc interface{ Scan(...any) error }) (*Round, error) {
	r := &Round{}
	err := sc.Scan(&r.N, &r.ProblemID, &r.Status, &r.OpenedAt, &r.ClosedAt, &r.MarksPerTest, &r.MaxMarks, &r.ShowOnBoard)
	return r, err
}

func (s *Store) EnsureRound(n int, problemID string) error {
	_, err := s.db.Exec(`INSERT INTO rounds(n, problem_id) VALUES (?, ?) ON CONFLICT(n) DO UPDATE SET problem_id = excluded.problem_id`, n, problemID)
	return err
}

func (s *Store) Rounds() ([]Round, error) {
	rows, err := s.db.Query(`SELECT ` + roundCols + ` FROM rounds ORDER BY n`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Round
	for rows.Next() {
		r, err := scanRound(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *Store) OpenRound() (*Round, error) {
	r, err := scanRound(s.db.QueryRow(`SELECT ` + roundCols + ` FROM rounds WHERE status = 'open'`))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func (s *Store) SetRoundStatus(n int, status string) error {
	col := "opened_at"
	if status == "closed" {
		col = "closed_at"
	}
	_, err := s.db.Exec(`UPDATE rounds SET status = ?, `+col+` = ? WHERE n = ?`, status, now(), n)
	return err
}

// SettingsRound saves a round's marking settings and rescores its runs.
func (s *Store) SetRoundSettings(n int, marksPerTest, maxMarks float64, show bool) error {
	if _, err := s.db.Exec(`UPDATE rounds SET marks_per_test = ?, max_marks = ?, show_on_board = ? WHERE n = ?`, marksPerTest, maxMarks, show, n); err != nil {
		return err
	}
	return s.rescore(n, 0)
}

// rescore recomputes score = passed × marks_per_test, capped at the round's max_marks (0 = no cap).
// runID 0 means every run in the round.
func (s *Store) rescore(round int, runID int64) error {
	_, err := s.db.Exec(`UPDATE runs SET score = CASE WHEN passed IS NULL THEN NULL ELSE
		MIN(passed * (SELECT marks_per_test FROM rounds WHERE n = runs.round),
		    CASE WHEN (SELECT max_marks FROM rounds WHERE n = runs.round) > 0 THEN (SELECT max_marks FROM rounds WHERE n = runs.round) ELSE 1e18 END) END
		WHERE round = ? AND (? = 0 OR id = ?)`, round, runID, runID)
	return err
}

// Run kinds.
const (
	KindSample  = "sample"  // visible tests only, free, throttled
	KindAttempt = "attempt" // every test, counts toward the score and the attempt limit
)

// Run is one run of a team's workflow.
type Run struct {
	ID        int64
	TeamID    int64
	Round     int
	ProblemID string
	Seed      int64
	Status    string
	Kind      string
	Created   int64
	RefTime   int64 // frozen at creation so dates render the same at run time and at grade time
	Started   sql.NullInt64
	Finished  sql.NullInt64
	Worker    sql.NullString
	WfName    sql.NullString
	Workflow  string
	Invalid   sql.NullString
	Infra     sql.NullString
	Passed    sql.NullFloat64
	Total     sql.NullFloat64
	Score     sql.NullFloat64
	Eligible  bool // false once refunded
	Verdicts  sql.NullString
	Features  sql.NullString
}

const runCols = `id, team_id, round, problem_id, seed, status, kind, created, COALESCE(ref_time, created), started, finished, worker, wf_name, workflow, invalid, infra, passed, total, score, eligible, verdicts, features`

func scanRun(sc interface{ Scan(...any) error }) (*Run, error) {
	r := &Run{}
	err := sc.Scan(&r.ID, &r.TeamID, &r.Round, &r.ProblemID, &r.Seed, &r.Status, &r.Kind, &r.Created, &r.RefTime, &r.Started, &r.Finished, &r.Worker, &r.WfName, &r.Workflow, &r.Invalid, &r.Infra, &r.Passed, &r.Total, &r.Score, &r.Eligible, &r.Verdicts, &r.Features)
	return r, err
}

// Counted reports whether the run uses one of the team's attempts.
func (r *Run) Counted() bool { return r.Kind == KindAttempt && r.Eligible }

// Admission limits.
const (
	MaxAttempts    = 3
	SampleThrottle = 2 * 60 // seconds between a team's sample runs
)

// AdmitError is a refused admission with the HTTP status to answer with.
type AdmitError struct {
	Code int
	Msg  string
}

func (e *AdmitError) Error() string { return e.Msg }

// Admit checks the rules and inserts the run in one transaction, so concurrent requests can't slip
// past them: the round is open, no other run of the team is in flight, an attempt has one of the
// three counted attempts left, and a sample respects the throttle.
func (s *Store) Admit(r *Run) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRow(`SELECT status FROM rounds WHERE n = ?`, r.Round).Scan(&status); err != nil || status != "open" {
		return 0, &AdmitError{409, "that round isn't open right now"}
	}
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM runs WHERE team_id = ? AND status IN ('queued','running','grading')`, r.TeamID).Scan(&n); err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, &AdmitError{429, "your previous run is still going; wait for it to finish"}
	}
	switch r.Kind {
	case KindAttempt:
		if err := tx.QueryRow(`SELECT COUNT(*) FROM runs WHERE team_id = ? AND round = ? AND kind = 'attempt' AND eligible = 1`, r.TeamID, r.Round).Scan(&n); err != nil {
			return 0, err
		}
		if n >= MaxAttempts {
			return 0, &AdmitError{409, fmt.Sprintf("you've used all %d attempts for this round", MaxAttempts)}
		}
	case KindSample:
		var last sql.NullInt64
		if err := tx.QueryRow(`SELECT MAX(created) FROM runs WHERE team_id = ? AND round = ? AND kind = 'sample'`, r.TeamID, r.Round).Scan(&last); err != nil {
			return 0, err
		}
		if wait := last.Int64 + SampleThrottle - now(); last.Valid && wait > 0 {
			return 0, &AdmitError{429, fmt.Sprintf("sample runs are limited to one every 2 minutes; try again in %d s", wait)}
		}
	default:
		return 0, &AdmitError{400, "unknown run kind"}
	}
	t := now()
	res, err := tx.Exec(`INSERT INTO runs(team_id, round, problem_id, seed, status, kind, created, ref_time, wf_name, workflow, invalid, passed, total, score, verdicts, finished, features)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TeamID, r.Round, r.ProblemID, r.Seed, r.Status, r.Kind, t, t, r.WfName, r.Workflow, r.Invalid, r.Passed, r.Total, r.Passed, r.Verdicts, r.Finished, r.Features)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, s.rescore(r.Round, id)
}

// InsertRun adds a run without admission checks (tests and tools).
func (s *Store) InsertRun(r *Run) (int64, error) {
	if r.Kind == "" {
		r.Kind = KindAttempt
	}
	t := now()
	res, err := s.db.Exec(`INSERT INTO runs(team_id, round, problem_id, seed, status, kind, created, ref_time, wf_name, workflow, invalid, passed, total, verdicts, finished, features)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TeamID, r.Round, r.ProblemID, r.Seed, r.Status, r.Kind, t, t, r.WfName, r.Workflow, r.Invalid, r.Passed, r.Total, r.Verdicts, r.Finished, r.Features)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, s.rescore(r.Round, id)
}

func (s *Store) GetRun(id int64) (*Run, error) {
	r, err := scanRun(s.db.QueryRow(`SELECT `+runCols+` FROM runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func (s *Store) queryRuns(q string, args ...any) ([]*Run, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		r.Workflow = ""
		out = append(out, r)
	}
	return out, rows.Err()
}

// RunsFor lists a team's runs in a round, newest first, without the workflow body.
func (s *Store) RunsFor(teamID int64, round int) ([]*Run, error) {
	return s.queryRuns(`SELECT `+runCols+` FROM runs WHERE team_id = ? AND round = ? ORDER BY id DESC`, teamID, round)
}

func (s *Store) AllRunsInRound(round int) ([]*Run, error) {
	return s.queryRuns(`SELECT `+runCols+` FROM runs WHERE round = ? ORDER BY id`, round)
}

// MarkRunning moves a queued run to running; false if it is no longer queued.
func (s *Store) MarkRunning(id int64, worker string) (bool, error) {
	res, err := s.db.Exec(`UPDATE runs SET status = ?, started = ?, worker = ? WHERE id = ? AND status = 'queued'`, StatusRunning, now(), worker, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SetStatus moves a run from one status to another; false if it wasn't in `from`.
func (s *Store) SetStatus(id int64, from, to string) (bool, error) {
	res, err := s.db.Exec(`UPDATE runs SET status = ? WHERE id = ? AND status = ?`, to, id, from)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// FinishRun records a result. It only applies while the run is still unfinished, so a late worker
// result can't overwrite a run that was already failed as stale. A failed run is refunded.
func (s *Store) FinishRun(id int64, status string, invalid, infra string, passed, total float64, verdicts string) (bool, error) {
	res, err := s.db.Exec(`UPDATE runs SET status = ?, finished = ?, invalid = NULLIF(?, ''), infra = NULLIF(?, ''), passed = ?, total = ?, verdicts = ?,
		eligible = CASE WHEN ? = 'failed' THEN 0 ELSE eligible END
		WHERE id = ? AND status IN ('queued','running','grading')`,
		status, now(), invalid, infra, passed, total, verdicts, status, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}
	var round int
	if err := s.db.QueryRow(`SELECT round FROM runs WHERE id = ?`, id).Scan(&round); err != nil {
		return true, err
	}
	return true, s.rescore(round, id)
}

// Refund gives an attempt back without changing the run's status (the team still sees its verdicts).
func (s *Store) Refund(id int64) error {
	_, err := s.db.Exec(`UPDATE runs SET eligible = 0 WHERE id = ?`, id)
	return err
}

// FailStale fails runs that are stuck: queued ones older than queuedBefore (by creation time) and
// running/grading ones that started before activeBefore. Each is refunded and logged as an incident.
func (s *Store) FailStale(queuedBefore, activeBefore int64, plain, kind string) (int64, error) {
	rows, err := s.db.Query(`SELECT id, team_id, status FROM runs WHERE
		(status = 'queued' AND created < ?) OR (status IN ('running','grading') AND COALESCE(started, created) < ?)`, queuedBefore, activeBefore)
	if err != nil {
		return 0, err
	}
	type stale struct {
		id, team int64
		status   string
	}
	var list []stale
	for rows.Next() {
		var x stale
		if err := rows.Scan(&x.id, &x.team, &x.status); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, x)
	}
	rows.Close()
	var n int64
	for _, x := range list {
		ok, err := s.FinishRun(x.id, StatusFailed, "", plain, 0, 0, "")
		if err != nil {
			return n, err
		}
		if ok {
			n++
			_ = s.AddIncident(x.team, x.id, "", kind, "run was "+x.status+" and never finished")
		}
	}
	return n, nil
}

// InFlight reports whether the team has a run that hasn't finished.
func (s *Store) InFlight(teamID int64) (int64, error) {
	var id int64
	err := s.db.QueryRow(`SELECT id FROM runs WHERE team_id = ? AND status IN ('queued','running','grading') ORDER BY id DESC LIMIT 1`, teamID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// AttemptsUsed counts the team's attempts in a round that haven't been refunded.
func (s *Store) AttemptsUsed(teamID int64, round int) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE team_id = ? AND round = ? AND kind = 'attempt' AND eligible = 1`, teamID, round).Scan(&n)
	return n, err
}

// LastSample is the creation time of the team's latest sample run in the round (0 if none).
func (s *Store) LastSample(teamID int64, round int) int64 {
	var t sql.NullInt64
	_ = s.db.QueryRow(`SELECT MAX(created) FROM runs WHERE team_id = ? AND round = ? AND kind = 'sample'`, teamID, round).Scan(&t)
	return t.Int64
}

// SweepSessions deletes expired sessions.
func (s *Store) SweepSessions() {
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE created <= ?`, now()-24*60*60)
}

// --- offline scores -------------------------------------------------------------------------

func (s *Store) SetOffline(teamID int64, round int, score float64, note string) error {
	_, err := s.db.Exec(`INSERT INTO offline_scores(team_id, round, score, note) VALUES (?, ?, ?, ?)
		ON CONFLICT(team_id, round) DO UPDATE SET score = excluded.score, note = excluded.note`, teamID, round, score, note)
	return err
}

func (s *Store) ClearOffline(teamID int64, round int) error {
	_, err := s.db.Exec(`DELETE FROM offline_scores WHERE team_id = ? AND round = ?`, teamID, round)
	return err
}

type Offline struct {
	Score float64
	Note  string
}

// OfflineScores returns round → team → score.
func (s *Store) OfflineScores() (map[int]map[int64]Offline, error) {
	rows, err := s.db.Query(`SELECT team_id, round, score, note FROM offline_scores`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]map[int64]Offline{}
	for rows.Next() {
		var tid int64
		var rd int
		var o Offline
		if err := rows.Scan(&tid, &rd, &o.Score, &o.Note); err != nil {
			return nil, err
		}
		if out[rd] == nil {
			out[rd] = map[int64]Offline{}
		}
		out[rd][tid] = o
	}
	return out, rows.Err()
}

// --- incidents ------------------------------------------------------------------------------

// Incident is something that broke on the judge's side, for organisers to fix or check by hand.
type Incident struct {
	ID       int64
	At       int64
	TeamID   int64
	TeamCode string
	RunID    int64
	Test     string
	Kind     string
	Detail   string
	Resolved bool
}

func (s *Store) AddIncident(teamID, runID int64, test, kind, detail string) error {
	if len(detail) > 4000 {
		detail = detail[:4000] + "…"
	}
	_, err := s.db.Exec(`INSERT INTO incidents(at, team_id, run_id, test, kind, detail) VALUES (?, NULLIF(?, 0), NULLIF(?, 0), ?, ?, ?)`, now(), teamID, runID, test, kind, detail)
	return err
}

// Incidents lists incidents, open ones first and newest first within each group.
func (s *Store) Incidents(limit int) ([]Incident, error) {
	rows, err := s.db.Query(`SELECT i.id, i.at, COALESCE(i.team_id, 0), COALESCE(t.code, ''), COALESCE(i.run_id, 0), i.test, i.kind, i.detail, i.resolved
		FROM incidents i LEFT JOIN teams t ON t.id = i.team_id ORDER BY i.resolved, i.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Incident
	for rows.Next() {
		var x Incident
		if err := rows.Scan(&x.ID, &x.At, &x.TeamID, &x.TeamCode, &x.RunID, &x.Test, &x.Kind, &x.Detail, &x.Resolved); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) ResolveIncident(id int64, resolved bool) error {
	_, err := s.db.Exec(`UPDATE incidents SET resolved = ? WHERE id = ?`, resolved, id)
	return err
}

func (s *Store) OpenIncidentCount() int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM incidents WHERE resolved = 0`).Scan(&n)
	return n
}
