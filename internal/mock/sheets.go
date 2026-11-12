package mock

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Google Sheets v4 (what n8n 2.47.4's Google Sheets node 4.5 calls on sheets.googleapis.com) plus the
// two Drive v3 calls it makes (list spreadsheets, delete one). Spreadsheets are per tenant; an id
// the tenant never saw is created on first touch (from the tenant's "*" seed template, else a blank
// spreadsheet with one sheet called "Sheet1"), so teams may point at any document id.

type spreadsheet struct {
	ID     string
	Title  string
	Sheets []*sheet
	nextID int
}

type sheet struct {
	ID    int
	Title string
	Rows  [][]any // cells are string, float64, bool or nil (empty)
}

// SeedSpreadsheet is a pre-existing spreadsheet.
type SeedSpreadsheet struct {
	Title  string      `json:"title,omitempty"`
	Sheets []SeedSheet `json:"sheets"`
}

// SeedSheet is one tab. Header (if any) becomes row 1, then Rows follow.
type SeedSheet struct {
	Title  string   `json:"title"`
	Header []string `json:"header,omitempty"`
	Rows   [][]any  `json:"rows,omitempty"`
}

// SnapSpreadsheet is a spreadsheet as grading sees it.
type SnapSpreadsheet struct {
	Title  string      `json:"title"`
	Sheets []SnapSheet `json:"sheets"`
}

// SnapSheet is a tab: raw Rows, and Records = rows after the header keyed by the header row (cells
// as strings; an empty cell is "").
type SnapSheet struct {
	ID      int                 `json:"id"`
	Title   string              `json:"title"`
	Rows    [][]any             `json:"rows"`
	Records []map[string]string `json:"records"`
}

func newSpreadsheet(id, title string) *spreadsheet {
	return &spreadsheet{ID: id, Title: title}
}

func (ss *spreadsheet) addSheet(title string) *sheet {
	id := 0
	if len(ss.Sheets) > 0 {
		ss.nextID++
		id = 1000000 + ss.nextID
	}
	sh := &sheet{ID: id, Title: title}
	ss.Sheets = append(ss.Sheets, sh)
	return sh
}

func (ss *spreadsheet) byTitle(title string) *sheet {
	for _, sh := range ss.Sheets {
		if sh.Title == title {
			return sh
		}
	}
	return nil
}

func (ss *spreadsheet) byID(id int) *sheet {
	for _, sh := range ss.Sheets {
		if sh.ID == id {
			return sh
		}
	}
	return nil
}

func buildSpreadsheet(id string, seed SeedSpreadsheet) *spreadsheet {
	ss := newSpreadsheet(id, orDefault(seed.Title, "Judge spreadsheet"))
	for _, sd := range seed.Sheets {
		sh := ss.addSheet(orDefault(sd.Title, "Sheet1"))
		if len(sd.Header) > 0 {
			row := make([]any, len(sd.Header))
			for i, h := range sd.Header {
				row[i] = h
			}
			sh.Rows = append(sh.Rows, row)
		}
		for _, r := range sd.Rows {
			sh.Rows = append(sh.Rows, append([]any{}, r...))
		}
	}
	if len(ss.Sheets) == 0 {
		ss.addSheet("Sheet1")
	}
	return ss
}

func (ss *spreadsheet) clone(id string) *spreadsheet {
	c := &spreadsheet{ID: id, Title: ss.Title, nextID: ss.nextID}
	for _, sh := range ss.Sheets {
		n := &sheet{ID: sh.ID, Title: sh.Title}
		for _, r := range sh.Rows {
			n.Rows = append(n.Rows, append([]any{}, r...))
		}
		c.Sheets = append(c.Sheets, n)
	}
	return c
}

func (t *tenant) spreadsheet(id string) *spreadsheet {
	if ss := t.Spreadsheets[id]; ss != nil {
		return ss
	}
	if t.Spreadsheets == nil {
		t.Spreadsheets = map[string]*spreadsheet{}
	}
	var ss *spreadsheet
	if t.SheetTemplate != nil {
		ss = t.SheetTemplate.clone(id)
	} else {
		ss = buildSpreadsheet(id, SeedSpreadsheet{})
	}
	t.Spreadsheets[id] = ss
	return ss
}

func (t *tenant) snapshotSheets() map[string]SnapSpreadsheet {
	out := map[string]SnapSpreadsheet{}
	for id, ss := range t.Spreadsheets {
		sn := SnapSpreadsheet{Title: ss.Title, Sheets: []SnapSheet{}}
		for _, sh := range ss.Sheets {
			rows := make([][]any, len(sh.Rows))
			for i, r := range sh.Rows {
				rows[i] = append([]any{}, r...)
			}
			recs := []map[string]string{}
			if len(sh.Rows) > 1 {
				for _, r := range sh.Rows[1:] {
					rec := map[string]string{}
					for ci, h := range sh.Rows[0] {
						if k := cellString(h); k != "" {
							rec[k] = cellString(cellAt(r, ci))
						}
					}
					recs = append(recs, rec)
				}
			}
			sn.Sheets = append(sn.Sheets, SnapSheet{ID: sh.ID, Title: sh.Title, Rows: rows, Records: recs})
		}
		out[id] = sn
	}
	return out
}

// --- cells and ranges -------------------------------------------------------------------------

func cellAt(r []any, i int) any {
	if i < len(r) {
		return r[i]
	}
	return nil
}

func cellEmpty(v any) bool { return v == nil || v == "" }

func cellString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "TRUE"
		}
		return "FALSE"
	default:
		return fmt.Sprint(x)
	}
}

// userEntered mimics USER_ENTERED parsing for the cases graders can see: numbers and booleans.
func userEntered(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	t := strings.TrimSpace(s)
	if f, err := strconv.ParseFloat(t, 64); err == nil && t != "" && !strings.ContainsAny(t, "eEnNiIxX_") {
		return f
	}
	switch strings.ToUpper(t) {
	case "TRUE":
		return true
	case "FALSE":
		return false
	}
	return v
}

func (sh *sheet) set(r, c int, v any) { // 0-based
	for len(sh.Rows) <= r {
		sh.Rows = append(sh.Rows, nil)
	}
	for len(sh.Rows[r]) <= c {
		sh.Rows[r] = append(sh.Rows[r], nil)
	}
	sh.Rows[r][c] = v
}

// used is the number of rows up to the last one holding data.
func (sh *sheet) used() int {
	n := len(sh.Rows)
	for n > 0 {
		empty := true
		for _, c := range sh.Rows[n-1] {
			if !cellEmpty(c) {
				empty = false
				break
			}
		}
		if !empty {
			break
		}
		n--
	}
	return n
}

func (sh *sheet) width() int {
	w := 0
	for _, r := range sh.Rows {
		if len(r) > w {
			w = len(r)
		}
	}
	return w
}

// a1 is a parsed range. Rows are 1-based and columns 0-based; -1 / 0 mean "open".
type a1 struct {
	sheet          string
	c1, c2, r1, r2 int
}

var a1Part = regexp.MustCompile(`^([A-Za-z]*)(\d*)$`)

func colIndex(s string) int {
	n := 0
	for _, ch := range strings.ToUpper(s) {
		n = n*26 + int(ch-'A') + 1
	}
	return n - 1
}

func colName(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('A'+(i-1)%26)) + s
	}
	return s
}

func parseA1(raw string) (a1, error) {
	var r a1
	r.c1, r.c2 = -1, -1
	ref := ""
	name := raw
	if i := strings.LastIndex(raw, "!"); i >= 0 {
		name, ref = raw[:i], raw[i+1:]
	}
	if len(name) >= 2 && name[0] == '\'' && name[len(name)-1] == '\'' {
		name = strings.ReplaceAll(name[1:len(name)-1], "''", "'")
	}
	r.sheet = name
	if ref == "" {
		return r, nil
	}
	first, second, _ := strings.Cut(ref, ":")
	parse := func(p string) (col, row int, err error) {
		m := a1Part.FindStringSubmatch(p)
		if m == nil {
			return -1, 0, fmt.Errorf("bad range %q", raw)
		}
		col = -1
		if m[1] != "" {
			col = colIndex(m[1])
		}
		if m[2] != "" {
			row, _ = strconv.Atoi(m[2])
		}
		return
	}
	c1, r1, err := parse(first)
	if err != nil {
		return r, err
	}
	r.c1, r.r1, r.c2, r.r2 = c1, r1, c1, r1
	if second != "" {
		c2, r2, err := parse(second)
		if err != nil {
			return r, err
		}
		r.c2, r.r2 = c2, r2
	}
	return r, nil
}

func a1String(sheetTitle string, r1, c1, r2, c2 int) string { // 1-based rows, 0-based cols
	return fmt.Sprintf("%s!%s%d:%s%d", sheetName(sheetTitle), colName(c1), r1, colName(c2), r2)
}

func sheetName(t string) string {
	if strings.ContainsAny(t, " !'") {
		return "'" + strings.ReplaceAll(t, "'", "''") + "'"
	}
	return t
}

// --- HTTP -------------------------------------------------------------------------------------

type sheetsReq struct {
	s      *Server
	t      *tenant
	w      http.ResponseWriter
	r      *http.Request
	status int
}

func (q *sheetsReq) fail(code int, msg string) { q.status = code; gErr(q.w, code, msg) }
func (q *sheetsReq) ok(v any)                  { q.status = 200; writeJSON(q.w, 200, v) }

// sheets serves /v4/spreadsheets... on sheets.googleapis.com.
func (s *Server) sheets(w http.ResponseWriter, r *http.Request, t *tenant) {
	q := &sheetsReq{s: s, t: t, w: w, r: r, status: 200}
	defer func() {
		t.SheetsCalls = append(t.SheetsCalls, Call{Method: r.Method, Path: r.URL.Path, Status: q.status})
	}()

	rest := strings.TrimPrefix(r.URL.EscapedPath(), "/v4/spreadsheets")
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	if rest == "" || rest == "/" {
		if r.Method != http.MethodPost {
			q.fail(405, "Method not allowed")
			return
		}
		title := "Untitled spreadsheet"
		if p, ok := body["properties"].(map[string]any); ok && str(p["title"]) != "" {
			title = str(p["title"])
		}
		s.nextID++
		id := fmt.Sprintf("sheet%05d", s.nextID)
		ss := newSpreadsheet(id, title)
		if list, ok := body["sheets"].([]any); ok {
			for _, it := range list {
				if m, ok := it.(map[string]any); ok {
					p, _ := m["properties"].(map[string]any)
					ss.addSheet(orDefault(str(p["title"]), "Sheet1"))
				}
			}
		}
		if len(ss.Sheets) == 0 {
			ss.addSheet("Sheet1")
		}
		if t.Spreadsheets == nil {
			t.Spreadsheets = map[string]*spreadsheet{}
		}
		t.Spreadsheets[id] = ss
		q.ok(q.metadata(ss))
		return
	}
	segs := strings.SplitN(strings.TrimPrefix(rest, "/"), "/", 3)
	idPart, action, _ := strings.Cut(segs[0], ":")
	id, _ := url.PathUnescape(idPart)
	ss := t.spreadsheet(id)

	switch {
	case len(segs) == 1 && action == "" && r.Method == http.MethodGet:
		q.ok(q.metadata(ss))
	case len(segs) == 1 && action == "batchUpdate" && r.Method == http.MethodPost:
		q.structural(ss, body)
	case len(segs) >= 2 && strings.HasPrefix(segs[1], "values"):
		q.values(ss, segs, body)
	default:
		q.fail(404, "The judge's Sheets simulator doesn't support "+r.Method+" "+r.URL.Path)
	}
}

func (q *sheetsReq) metadata(ss *spreadsheet) map[string]any {
	sheets := []map[string]any{}
	for i, sh := range ss.Sheets {
		sheets = append(sheets, map[string]any{"properties": map[string]any{
			"sheetId": sh.ID, "title": sh.Title, "index": i, "sheetType": "GRID",
			"gridProperties": map[string]any{"rowCount": max(1000, len(sh.Rows)), "columnCount": max(26, sh.width())},
		}})
	}
	return map[string]any{"spreadsheetId": ss.ID, "properties": map[string]any{"title": ss.Title, "timeZone": "Etc/UTC"},
		"sheets": sheets, "spreadsheetUrl": "https://docs.google.com/spreadsheets/d/" + ss.ID + "/edit"}
}

// structural handles spreadsheets/{id}:batchUpdate.
func (q *sheetsReq) structural(ss *spreadsheet, body map[string]any) {
	reqs, _ := body["requests"].([]any)
	replies := []map[string]any{}
	for _, it := range reqs {
		m, _ := it.(map[string]any)
		reply := map[string]any{}
		switch {
		case m["addSheet"] != nil:
			p, _ := m["addSheet"].(map[string]any)["properties"].(map[string]any)
			title := orDefault(str(p["title"]), fmt.Sprintf("Sheet%d", len(ss.Sheets)+1))
			if ss.byTitle(title) != nil {
				q.fail(400, fmt.Sprintf("Invalid requests[0].addSheet: A sheet with the name \"%s\" already exists. Please enter another name.", title))
				return
			}
			sh := ss.addSheet(title)
			reply["addSheet"] = map[string]any{"properties": map[string]any{"sheetId": sh.ID, "title": sh.Title, "index": len(ss.Sheets) - 1}}
		case m["deleteSheet"] != nil:
			id := int(num(m["deleteSheet"].(map[string]any)["sheetId"]))
			for i, sh := range ss.Sheets {
				if sh.ID == id {
					ss.Sheets = append(ss.Sheets[:i], ss.Sheets[i+1:]...)
					break
				}
			}
		case m["deleteDimension"] != nil:
			rng, _ := m["deleteDimension"].(map[string]any)["range"].(map[string]any)
			sh := ss.byID(int(num(rng["sheetId"])))
			if sh == nil {
				q.fail(400, "Invalid requests[0].deleteDimension: No grid with id: "+str(rng["sheetId"]))
				return
			}
			from, to := int(num(rng["startIndex"])), int(num(rng["endIndex"]))
			if str(rng["dimension"]) == "COLUMNS" {
				for i, row := range sh.Rows {
					if from < len(row) {
						sh.Rows[i] = append(row[:from:from], row[min(to, len(row)):]...)
					}
				}
			} else if from < len(sh.Rows) {
				sh.Rows = append(sh.Rows[:from:from], sh.Rows[min(to, len(sh.Rows)):]...)
			}
		case m["updateSheetProperties"] != nil:
			u := m["updateSheetProperties"].(map[string]any)
			p, _ := u["properties"].(map[string]any)
			if sh := ss.byID(int(num(p["sheetId"]))); sh != nil && str(p["title"]) != "" {
				sh.Title = str(p["title"])
			}
		}
		replies = append(replies, reply) // appendDimension and unknown kinds are accepted as no-ops
	}
	q.ok(map[string]any{"spreadsheetId": ss.ID, "replies": replies})
}

func num(v any) float64 {
	f, _ := strconv.ParseFloat(str(v), 64)
	return f
}

// resolve finds the sheet a range names. create=true makes a missing tab (write calls only).
func (q *sheetsReq) resolve(ss *spreadsheet, ref a1, create bool) (*sheet, bool) {
	if ref.sheet == "" {
		return ss.Sheets[0], true
	}
	if sh := ss.byTitle(ref.sheet); sh != nil {
		return sh, true
	}
	if create {
		return ss.addSheet(ref.sheet), true
	}
	q.fail(400, fmt.Sprintf("Unable to parse range: %s", ref.sheet))
	return nil, false
}

func (q *sheetsReq) values(ss *spreadsheet, segs []string, body map[string]any) {
	method := q.r.Method
	vio := q.r.URL.Query().Get("valueInputOption")
	switch {
	case segs[1] == "values:batchUpdate" && method == http.MethodPost:
		vio = str(body["valueInputOption"])
		data, _ := body["data"].([]any)
		resp := []map[string]any{}
		var cells, rows int
		for _, d := range data {
			m, _ := d.(map[string]any)
			rr, ok := q.write(ss, str(m["range"]), m["values"], vio, false)
			if !ok {
				return
			}
			resp = append(resp, rr)
			cells += int(num(rr["updatedCells"]))
			rows += int(num(rr["updatedRows"]))
		}
		q.ok(map[string]any{"spreadsheetId": ss.ID, "totalUpdatedRows": rows, "totalUpdatedCells": cells, "responses": resp})
	case segs[1] == "values:batchGet" && method == http.MethodGet:
		out := []map[string]any{}
		for _, rg := range q.r.URL.Query()["ranges"] {
			v, ok := q.read(ss, rg)
			if !ok {
				return
			}
			out = append(out, v)
		}
		q.ok(map[string]any{"spreadsheetId": ss.ID, "valueRanges": out})
	case segs[1] == "values:batchClear" && method == http.MethodPost:
		cleared := []string{}
		rgs, _ := body["ranges"].([]any)
		for _, rg := range rgs {
			if !q.clear(ss, str(rg)) {
				return
			}
			cleared = append(cleared, str(rg))
		}
		q.ok(map[string]any{"spreadsheetId": ss.ID, "clearedRanges": cleared})
	case segs[1] == "values" && len(segs) == 3:
		rangePart, action, _ := cutLast(segs[2], ":")
		rng, err := url.PathUnescape(rangePart)
		if err != nil {
			q.fail(400, "bad range")
			return
		}
		switch {
		case action == "" && method == http.MethodGet:
			if v, ok := q.read(ss, rng); ok {
				q.ok(v)
			}
		case action == "" && method == http.MethodPut:
			if rr, ok := q.write(ss, rng, body["values"], vio, false); ok {
				q.ok(rr)
			}
		case action == "append" && method == http.MethodPost:
			if rr, ok := q.write(ss, rng, body["values"], vio, true); ok {
				q.ok(map[string]any{"spreadsheetId": ss.ID, "tableRange": rr["updatedRange"], "updates": rr})
			}
		case action == "clear" && method == http.MethodPost:
			if q.clear(ss, rng) {
				q.ok(map[string]any{"spreadsheetId": ss.ID, "clearedRange": rng})
			}
		default:
			q.fail(404, "The judge's Sheets simulator doesn't support "+method+" "+q.r.URL.Path)
		}
	default:
		q.fail(404, "The judge's Sheets simulator doesn't support "+method+" "+q.r.URL.Path)
	}
}

// cutLast splits "A1:B2:append" into ("A1:B2", "append") only when the tail is a known action.
func cutLast(s, sep string) (string, string, bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	switch s[i+1:] {
	case "append", "clear":
		return s[:i], s[i+1:], true
	}
	return s, "", false
}

func (q *sheetsReq) read(ss *spreadsheet, rng string) (map[string]any, bool) {
	ref, err := parseA1(rng)
	if err != nil {
		q.fail(400, "Unable to parse range: "+rng)
		return nil, false
	}
	sh, ok := q.resolve(ss, ref, false)
	if !ok {
		return nil, false
	}
	formatted := q.r.URL.Query().Get("valueRenderOption") == "" || q.r.URL.Query().Get("valueRenderOption") == "FORMATTED_VALUE"
	r1, r2 := max(ref.r1, 1), ref.r2
	if r2 == 0 || r2 > sh.used() {
		r2 = sh.used()
	}
	var values [][]any
	for ri := r1 - 1; ri < r2; ri++ {
		var row []any
		if ri < len(sh.Rows) {
			row = sh.Rows[ri]
		}
		c1, c2 := max(ref.c1, 0), ref.c2
		if c2 < 0 || c2 >= len(row) {
			c2 = len(row) - 1
		}
		cut := []any{}
		for ci := c1; ci <= c2; ci++ {
			v := cellAt(row, ci)
			if v == nil {
				v = ""
			} else if formatted {
				v = cellString(v)
			}
			cut = append(cut, v)
		}
		for len(cut) > 0 && cellEmpty(cut[len(cut)-1]) {
			cut = cut[:len(cut)-1]
		}
		values = append(values, cut)
	}
	for len(values) > 0 && len(values[len(values)-1]) == 0 {
		values = values[:len(values)-1]
	}
	out := map[string]any{"range": rng, "majorDimension": "ROWS"}
	if len(values) > 0 {
		out["values"] = values
	}
	return out, true
}

// write sets values at the range's top-left corner; with appendMode it goes under the last row
// that holds data instead.
func (q *sheetsReq) write(ss *spreadsheet, rng string, vals any, vio string, appendMode bool) (map[string]any, bool) {
	ref, err := parseA1(rng)
	if err != nil {
		q.fail(400, "Unable to parse range: "+rng)
		return nil, false
	}
	sh, ok := q.resolve(ss, ref, true)
	if !ok {
		return nil, false
	}
	rows, _ := vals.([]any)
	row0 := max(ref.r1, 1) - 1
	if appendMode {
		row0 = sh.used()
	}
	col0 := max(ref.c1, 0)
	cells, widest := 0, 0
	for ri, rv := range rows {
		cols, _ := rv.([]any)
		widest = max(widest, len(cols))
		for ci, v := range cols {
			if v == nil && appendMode {
				continue
			}
			if vio == "USER_ENTERED" {
				v = userEntered(v)
			}
			sh.set(row0+ri, col0+ci, v)
			cells++
		}
		if len(cols) == 0 {
			continue
		}
	}
	if len(rows) == 0 {
		return map[string]any{"spreadsheetId": ss.ID, "updatedRows": 0, "updatedColumns": 0, "updatedCells": 0, "updatedRange": rng}, true
	}
	return map[string]any{"spreadsheetId": ss.ID, "updatedRange": a1String(sh.Title, row0+1, col0, row0+len(rows), col0+max(widest, 1)-1),
		"updatedRows": len(rows), "updatedColumns": widest, "updatedCells": cells}, true
}

func (q *sheetsReq) clear(ss *spreadsheet, rng string) bool {
	ref, err := parseA1(rng)
	if err != nil {
		q.fail(400, "Unable to parse range: "+rng)
		return false
	}
	sh, ok := q.resolve(ss, ref, false)
	if !ok {
		return false
	}
	r1, r2 := max(ref.r1, 1), ref.r2
	if r2 == 0 || r2 > len(sh.Rows) {
		r2 = len(sh.Rows)
	}
	for ri := r1 - 1; ri < r2; ri++ {
		c1, c2 := max(ref.c1, 0), ref.c2
		if c2 < 0 || c2 >= len(sh.Rows[ri]) {
			c2 = len(sh.Rows[ri]) - 1
		}
		for ci := c1; ci <= c2; ci++ {
			sh.Rows[ri][ci] = nil
		}
	}
	return true
}

// drive serves the two Drive v3 calls the Sheets node makes outside the Sheets API.
func (s *Server) drive(w http.ResponseWriter, r *http.Request, t *tenant) {
	p := strings.TrimPrefix(r.URL.Path, "/drive/v3/files")
	switch {
	case p == "" && r.Method == http.MethodGet:
		files := []map[string]any{}
		for id, ss := range t.Spreadsheets {
			files = append(files, map[string]any{"id": id, "name": ss.Title, "mimeType": "application/vnd.google-apps.spreadsheet"})
		}
		writeJSON(w, 200, map[string]any{"kind": "drive#fileList", "files": files})
	case strings.HasPrefix(p, "/") && r.Method == http.MethodDelete:
		delete(t.Spreadsheets, strings.TrimPrefix(p, "/"))
		w.WriteHeader(204)
	default:
		gErr(w, 404, "The judge's Drive simulator doesn't support "+r.Method+" "+r.URL.Path)
	}
}
