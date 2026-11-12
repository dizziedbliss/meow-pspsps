package server

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// roundScore is one team's result in one round.
type roundScore struct {
	Judge    float64 // best counted attempt's score
	Offline  float64 // organisers' offline score
	HasRun   bool
	Passed   float64
	Total    float64
	Attempts int // counted attempts used
	BestRun  int64
	BestAt   int64 // when the best attempt finished
	Nodes    string
	Integr   string
}

// boardRow is one team on the leaderboard.
type boardRow struct {
	Rank   int
	Team   Team
	Rounds map[int]*roundScore
	Total  float64
	At     int64 // when the team last improved: earlier wins a tie
}

// leaderboard sums every team's rounds. Per round the best counted (not refunded) finished attempt
// counts, plus the offline score. With onlyShown it covers just the rounds organisers chose to show.
func (s *Server) leaderboard(onlyShown bool) ([]Round, []boardRow, error) {
	all, err := s.store.Rounds()
	if err != nil {
		return nil, nil, err
	}
	var rounds []Round
	for _, r := range all {
		if !onlyShown || r.ShowOnBoard {
			rounds = append(rounds, r)
		}
	}
	teams, err := s.store.Teams()
	if err != nil {
		return nil, nil, err
	}
	off, err := s.store.OfflineScores()
	if err != nil {
		return nil, nil, err
	}
	byTeam := map[int64]*boardRow{}
	rows := make([]boardRow, len(teams))
	for i, t := range teams {
		rows[i] = boardRow{Team: t, Rounds: map[int]*roundScore{}}
		byTeam[t.ID] = &rows[i]
	}
	for _, rd := range rounds {
		runs, err := s.store.AllRunsInRound(rd.N)
		if err != nil {
			return nil, nil, err
		}
		for _, run := range runs {
			row := byTeam[run.TeamID]
			if row == nil || !run.Counted() {
				continue
			}
			rs := row.Rounds[rd.N]
			if rs == nil {
				rs = &roundScore{}
				row.Rounds[rd.N] = rs
			}
			rs.Attempts++
			if run.Status != StatusDone {
				continue
			}
			if !rs.HasRun || run.Score.Float64 > rs.Judge { // ties keep the earlier run
				rs.HasRun, rs.Judge, rs.Passed, rs.Total, rs.BestRun, rs.BestAt = true, run.Score.Float64, run.Passed.Float64, run.Total.Float64, run.ID, run.Finished.Int64
				rs.Nodes, rs.Integr = featureCounts(run.Features.String)
			}
		}
		for tid, o := range off[rd.N] {
			if row := byTeam[tid]; row != nil {
				rs := row.Rounds[rd.N]
				if rs == nil {
					rs = &roundScore{}
					row.Rounds[rd.N] = rs
				}
				rs.Offline = o.Score
			}
		}
	}
	for i := range rows {
		for _, rs := range rows[i].Rounds {
			rows[i].Total += rs.Judge + rs.Offline
			if rs.BestAt > rows[i].At {
				rows[i].At = rs.BestAt
			}
		}
	}
	// Teams with nothing yet stay off the board; the admin table lists everyone.
	kept := rows[:0]
	for _, r := range rows {
		if len(r.Rounds) > 0 {
			kept = append(kept, r)
		}
	}
	rows = kept
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Total != rows[j].Total {
			return rows[i].Total > rows[j].Total
		}
		if rows[i].At != rows[j].At {
			return rows[i].At < rows[j].At
		}
		return rows[i].Team.Name < rows[j].Team.Name
	})
	for i := range rows {
		rows[i].Rank = i + 1
		if i > 0 && rows[i].Total == rows[i-1].Total && rows[i].At == rows[i-1].At {
			rows[i].Rank = rows[i-1].Rank
		}
	}
	return rounds, rows, nil
}

// featureCounts pulls a node count and an integration count out of the stored feature report,
// whatever its exact shape: a number is used as is, a list or object counts its entries.
func featureCounts(raw string) (nodes, integrations string) {
	if raw == "" {
		return "", ""
	}
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return "", ""
	}
	nodes = findCount(m, "nodecount", "nodes", "totalnodes", "node_count")
	// workflow.Features keeps node types per group: integrations = sum of their counts.
	if g, ok := m["groups"].(map[string]any); ok {
		n := 0
		list, _ := g["integrations"].([]any)
		for _, it := range list {
			if e, ok := it.(map[string]any); ok {
				c, _ := e["count"].(float64)
				n += int(c)
			}
		}
		return nodes, strconv.Itoa(n)
	}
	return nodes, findCount(m, "integrations", "integrationcount", "integration_count")
}

func findCount(m map[string]any, names ...string) string {
	for k, v := range m {
		lk := strings.ToLower(k)
		for _, n := range names {
			if lk != n {
				continue
			}
			switch x := v.(type) {
			case float64:
				return strconv.FormatFloat(x, 'f', -1, 64)
			case []any:
				return strconv.Itoa(len(x))
			case map[string]any:
				return strconv.Itoa(len(x))
			}
		}
	}
	return ""
}

func fmtScore(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }

func (s *Server) boardPage(w http.ResponseWriter, r *http.Request) {
	rounds, rows, err := s.leaderboard(true)
	if err != nil {
		http.Error(w, "db error", 500)
		return
	}
	s.render(w, "board.html", map[string]any{"Rounds": rounds, "Rows": rows, "Now": time.Now().Format("15:04:05")})
}

func (s *Server) adminExport(w http.ResponseWriter, r *http.Request) {
	rounds, rows, err := s.leaderboard(false)
	if err != nil {
		http.Error(w, "db error", 500)
		return
	}
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=leaderboard.csv")
	cw := csv.NewWriter(w)
	head := []string{"rank", "team_code", "team_name"}
	for _, rd := range rounds {
		p := fmt.Sprintf("round%d_", rd.N)
		head = append(head, p+"judge", p+"offline", p+"tests_passed", p+"tests_total", p+"attempts", p+"nodes", p+"integrations")
	}
	head = append(head, "total")
	_ = cw.Write(head)
	for _, row := range rows {
		rec := []string{strconv.Itoa(row.Rank), row.Team.Code, row.Team.Name}
		for _, rd := range rounds {
			rs := row.Rounds[rd.N]
			if rs == nil {
				rs = &roundScore{}
			}
			rec = append(rec, fmtScore(rs.Judge), fmtScore(rs.Offline), fmtScore(rs.Passed), fmtScore(rs.Total), strconv.Itoa(rs.Attempts), rs.Nodes, rs.Integr)
		}
		rec = append(rec, fmtScore(row.Total))
		_ = cw.Write(rec)
	}
	cw.Flush()
}

// Score is the team's result in a round (zero value if it has none), safe to call from templates.
func (b boardRow) Score(n int) roundScore {
	if rs := b.Rounds[n]; rs != nil {
		return *rs
	}
	return roundScore{}
}
