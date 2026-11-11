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
CREATE TABLE IF NOT EXISTS submissions (
  team_id INTEGER NOT NULL REFERENCES teams(id),
  round INTEGER NOT NULL,
  run_id INTEGER NOT NULL REFERENCES runs(id),
  auto INTEGER NOT NULL DEFAULT 0,
  at INTEGER NOT NULL,
  PRIMARY KEY (team_id, round)
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
	return &Store{db: db}, nil
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
}

func (s *Store) EnsureRound(n int, problemID string) error {
	_, err := s.db.Exec(`INSERT INTO rounds(n, problem_id) VALUES (?, ?) ON CONFLICT(n) DO UPDATE SET problem_id = excluded.problem_id`, n, problemID)
	return err
}

func (s *Store) Rounds() ([]Round, error) {
	rows, err := s.db.Query(`SELECT n, problem_id, status, opened_at, closed_at FROM rounds ORDER BY n`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Round
	for rows.Next() {
		var r Round
		if err := rows.Scan(&r.N, &r.ProblemID, &r.Status, &r.OpenedAt, &r.ClosedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) OpenRound() (*Round, error) {
	r := &Round{}
	err := s.db.QueryRow(`SELECT n, problem_id, status, opened_at, closed_at FROM rounds WHERE status = 'open'`).Scan(&r.N, &r.ProblemID, &r.Status, &r.OpenedAt, &r.ClosedAt)
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

// Run is one attempt.
type Run struct {
	ID        int64
	TeamID    int64
	Round     int
	ProblemID string
	Seed      int64
	Status    string
	Created   int64
	Started   sql.NullInt64
	Finished  sql.NullInt64
	Worker    sql.NullString
	WfName    sql.NullString
	Workflow  string
	Invalid   sql.NullString
	Infra     sql.NullString
	Passed    sql.NullFloat64
	Total     sql.NullFloat64
	Verdicts  sql.NullString
}

const runCols = `id, team_id, round, problem_id, seed, status, created, started, finished, worker, wf_name, workflow, invalid, infra, passed, total, verdicts`

func scanRun(sc interface{ Scan(...any) error }) (*Run, error) {
	r := &Run{}
	err := sc.Scan(&r.ID, &r.TeamID, &r.Round, &r.ProblemID, &r.Seed, &r.Status, &r.Created, &r.Started, &r.Finished, &r.Worker, &r.WfName, &r.Workflow, &r.Invalid, &r.Infra, &r.Passed, &r.Total, &r.Verdicts)
	return r, err
}

func (s *Store) InsertRun(r *Run) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO runs(team_id, round, problem_id, seed, status, created, wf_name, workflow, invalid, passed, total, verdicts, finished)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TeamID, r.Round, r.ProblemID, r.Seed, r.Status, now(), r.WfName, r.Workflow, r.Invalid, r.Passed, r.Total, r.Verdicts, r.Finished)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) GetRun(id int64) (*Run, error) {
	r, err := scanRun(s.db.QueryRow(`SELECT `+runCols+` FROM runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// RunsFor lists a team's runs in a round, newest first, without the workflow body.
func (s *Store) RunsFor(teamID int64, round int) ([]*Run, error) {
	rows, err := s.db.Query(`SELECT `+runCols+` FROM runs WHERE team_id = ? AND round = ? ORDER BY id DESC`, teamID, round)
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

func (s *Store) AllRunsInRound(round int) ([]*Run, error) {
	rows, err := s.db.Query(`SELECT `+runCols+` FROM runs WHERE round = ? ORDER BY id`, round)
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

func (s *Store) MarkRunning(id int64, worker string) error {
	_, err := s.db.Exec(`UPDATE runs SET status = ?, started = ?, worker = ? WHERE id = ?`, StatusRunning, now(), worker, id)
	return err
}

func (s *Store) SetStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE runs SET status = ? WHERE id = ?`, status, id)
	return err
}

func (s *Store) FinishRun(id int64, status string, invalid, infra string, passed, total float64, verdicts string) error {
	_, err := s.db.Exec(`UPDATE runs SET status = ?, finished = ?, invalid = NULLIF(?, ''), infra = NULLIF(?, ''), passed = ?, total = ?, verdicts = ? WHERE id = ?`,
		status, now(), invalid, infra, passed, total, verdicts, id)
	return err
}

// FailStale marks queued/running runs older than cutoff as failed (keys are gone after a restart).
func (s *Store) FailStale(cutoffUnix int64, reason string) (int64, error) {
	res, err := s.db.Exec(`UPDATE runs SET status = ?, infra = ?, finished = ? WHERE status IN ('queued','running','grading') AND created < ?`,
		StatusFailed, reason, now(), cutoffUnix)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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

// Submission is a team's final answer for a round.
type Submission struct {
	TeamID int64
	Round  int
	RunID  int64
	Auto   bool
	At     int64
}

func (s *Store) GetSubmission(teamID int64, round int) (*Submission, error) {
	sub := &Submission{}
	err := s.db.QueryRow(`SELECT team_id, round, run_id, auto, at FROM submissions WHERE team_id = ? AND round = ?`, teamID, round).Scan(&sub.TeamID, &sub.Round, &sub.RunID, &sub.Auto, &sub.At)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return sub, err
}

func (s *Store) Submissions(round int) (map[int64]*Submission, error) {
	rows, err := s.db.Query(`SELECT team_id, round, run_id, auto, at FROM submissions WHERE round = ?`, round)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]*Submission{}
	for rows.Next() {
		sub := &Submission{}
		if err := rows.Scan(&sub.TeamID, &sub.Round, &sub.RunID, &sub.Auto, &sub.At); err != nil {
			return nil, err
		}
		out[sub.TeamID] = sub
	}
	return out, rows.Err()
}

// Submit records a submission; it fails if one exists (submit is final).
func (s *Store) Submit(teamID int64, round int, runID int64, auto bool) error {
	_, err := s.db.Exec(`INSERT INTO submissions(team_id, round, run_id, auto, at) VALUES (?, ?, ?, ?, ?)`, teamID, round, runID, auto, now())
	return err
}

// AutoSubmitRound submits each team's latest finished run in the round, for teams that didn't submit.
func (s *Store) AutoSubmitRound(round int) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO submissions(team_id, round, run_id, auto, at)
		SELECT r.team_id, r.round, MAX(r.id), 1, ? FROM runs r
		WHERE r.round = ? AND r.status = 'done'
		  AND NOT EXISTS (SELECT 1 FROM submissions s WHERE s.team_id = r.team_id AND s.round = r.round)
		GROUP BY r.team_id`, now(), round)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
