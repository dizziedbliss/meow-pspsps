// Package mock fakes the external services a problem's workflow talks to (Telegram Bot API and
// Google Calendar), so tests are deterministic and nobody's real accounts are touched. Each test
// gets its own "tenant", identified by the bot token / OAuth access token in its credentials.
package mock

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // the mock runs in a scratch image with no zoneinfo
)

// Server holds every tenant's fake world.
type Server struct {
	mu      sync.Mutex
	tenants map[string]*tenant
	nextID  int
}

type tenant struct {
	TZ       *time.Location
	Events   []*Event
	Calls    []Call
	Webhook  struct{ URL, Secret string }
	Sent     []Sent
	Telegram []string // method names called
}

// Event is a calendar event as the mock stores it.
type Event struct {
	ID      string    `json:"id"`
	Summary string    `json:"summary"`
	AllDay  bool      `json:"allDay"`
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Date    string    `json:"date"` // local start date, YYYY-MM-DD
	Created bool      `json:"created"`
	Deleted bool      `json:"deleted,omitempty"`
}

// Call is one request the workflow made to the fake calendar.
type Call struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Status int    `json:"status"`
}

// Sent is a Telegram message the workflow sent.
type Sent struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}

var attribution = regexp.MustCompile(`(?s)\s*_?This message was sent automatically with _?\[?n8n\]?(\([^)]*\))?\s*$`)

// New returns an empty mock.
func New() *Server { return &Server{tenants: map[string]*tenant{}} }

// --- admin API (worker only) ------------------------------------------------------------------

// Seed is how a test sets up a tenant.
type Seed struct {
	Timezone string     `json:"timezone"`
	Events   []SeedItem `json:"events"`
}

// SeedItem is a pre-existing calendar entry: an all-day date, or start/end date-times.
type SeedItem struct {
	Summary string `json:"summary"`
	Date    string `json:"date,omitempty"`
	Start   string `json:"start,omitempty"`
	End     string `json:"end,omitempty"`
}

// Snapshot is what a test's grading sees.
type Snapshot struct {
	Telegram struct {
		WebhookURL string   `json:"webhookUrl"`
		Secret     string   `json:"secret"`
		Sent       []Sent   `json:"sent"`
		Calls      []string `json:"calls"`
	} `json:"telegram"`
	Calendar struct {
		Events  []*Event `json:"events"`  // everything on the calendar now
		Created []*Event `json:"created"` // only what the workflow added
		Calls   []Call   `json:"calls"`
	} `json:"calendar"`
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/admin/tenants/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		var seed Seed
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&seed); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		loc, err := time.LoadLocation(orDefault(seed.Timezone, "UTC"))
		if err != nil {
			http.Error(w, "bad timezone", 400)
			return
		}
		t := &tenant{TZ: loc}
		for _, it := range seed.Events {
			ev, err := s.eventFrom(t, it.Summary, it.Date, it.Start, it.End)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			t.Events = append(t.Events, ev)
		}
		s.tenants[id] = t
		w.WriteHeader(204)
	case http.MethodGet:
		t := s.tenants[id]
		if t == nil {
			http.NotFound(w, r)
			return
		}
		var snap Snapshot
		snap.Telegram.WebhookURL, snap.Telegram.Secret = t.Webhook.URL, t.Webhook.Secret
		snap.Telegram.Sent = append([]Sent{}, t.Sent...)
		snap.Telegram.Calls = append([]string{}, t.Telegram...)
		snap.Calendar.Events, snap.Calendar.Created = []*Event{}, []*Event{}
		for _, e := range t.Events {
			if e.Deleted {
				continue
			}
			snap.Calendar.Events = append(snap.Calendar.Events, e)
			if e.Created {
				snap.Calendar.Created = append(snap.Calendar.Created, e)
			}
		}
		snap.Calendar.Calls = append([]Call{}, t.Calls...)
		writeJSON(w, 200, snap)
	case http.MethodDelete:
		delete(s.tenants, id)
		w.WriteHeader(204)
	default:
		w.WriteHeader(405)
	}
}

// --- Telegram Bot API -------------------------------------------------------------------------

// telegram serves /bot<token>/<method>.
func (s *Server) telegram(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/bot")
	token, method, _ := strings.Cut(rest, "/")
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tenants[token]
	if t == nil {
		writeJSON(w, 401, map[string]any{"ok": false, "error_code": 401, "description": "Unauthorized"})
		return
	}
	params := map[string]any{}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	_ = json.Unmarshal(body, &params)
	_ = r.ParseForm()
	for k, v := range r.Form {
		if _, ok := params[k]; !ok && len(v) > 0 {
			params[k] = v[0]
		}
	}
	t.Telegram = append(t.Telegram, method)
	ok := func(result any) { writeJSON(w, 200, map[string]any{"ok": true, "result": result}) }
	switch method {
	case "getMe":
		ok(map[string]any{"id": 777000, "is_bot": true, "first_name": "Judge Bot", "username": "judge_bot"})
	case "setWebhook":
		t.Webhook.URL, t.Webhook.Secret = str(params["url"]), str(params["secret_token"])
		ok(true)
	case "deleteWebhook":
		t.Webhook.URL, t.Webhook.Secret = "", ""
		ok(true)
	case "getWebhookInfo":
		ok(map[string]any{"url": t.Webhook.URL, "has_custom_certificate": false, "pending_update_count": 0})
	case "sendMessage":
		chat, text := str(params["chat_id"]), str(params["text"])
		if chat == "" || strings.TrimSpace(text) == "" {
			writeJSON(w, 400, map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: message text is empty"})
			return
		}
		// n8n's Telegram node appends its own "sent automatically with n8n" line by default; that
		// isn't the bot's answer, so graders never see it.
		t.Sent = append(t.Sent, Sent{ChatID: chat, Text: strings.TrimSpace(attribution.ReplaceAllString(text, ""))})
		s.nextID++
		cid, _ := strconv.ParseInt(chat, 10, 64)
		ok(map[string]any{"message_id": s.nextID, "date": time.Now().Unix(), "text": text,
			"chat": map[string]any{"id": cid, "type": "private"}, "from": map[string]any{"id": 777000, "is_bot": true, "first_name": "Judge Bot"}})
	default:
		ok(true) // sendChatAction and friends: accept quietly
	}
}

// --- Google Calendar --------------------------------------------------------------------------

func (s *Server) google(w http.ResponseWriter, r *http.Request) {
	// OAuth token refresh: hand back the refresh token as the access token (that's the tenant).
	if strings.HasSuffix(r.URL.Path, "/token") {
		_ = r.ParseForm()
		rt := r.Form.Get("refresh_token")
		writeJSON(w, 200, map[string]any{"access_token": rt, "expires_in": 3599, "token_type": "Bearer", "scope": "https://www.googleapis.com/auth/calendar"})
		return
	}
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tenants[token]
	if t == nil {
		gErr(w, 401, "Invalid Credentials")
		return
	}
	status := 200
	defer func() { t.Calls = append(t.Calls, Call{Method: r.Method, Path: r.URL.Path, Status: status}) }()
	fail := func(code int, msg string) { status = code; gErr(w, code, msg) }

	p := strings.TrimPrefix(r.URL.Path, "/calendar/v3")
	var body map[string]any
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	_ = json.Unmarshal(raw, &body)

	switch {
	case p == "/freeBusy" && r.Method == http.MethodPost:
		min, err1 := parseTime(str(body["timeMin"]), t.TZ)
		max, err2 := parseTime(str(body["timeMax"]), t.TZ)
		if err1 != nil || err2 != nil {
			fail(400, "Bad Request: invalid timeMin/timeMax")
			return
		}
		busy := []map[string]string{}
		for _, e := range t.overlapping(min, max) {
			busy = append(busy, map[string]string{"start": e.Start.Format(time.RFC3339), "end": e.End.Format(time.RFC3339)})
		}
		cals := map[string]any{}
		if items, ok := body["items"].([]any); ok {
			for _, it := range items {
				if m, ok := it.(map[string]any); ok {
					cals[str(m["id"])] = map[string]any{"busy": busy}
				}
			}
		}
		if len(cals) == 0 {
			cals["primary"] = map[string]any{"busy": busy}
		}
		writeJSON(w, 200, map[string]any{"kind": "calendar#freeBusy", "timeMin": str(body["timeMin"]), "timeMax": str(body["timeMax"]), "calendars": cals})

	case strings.HasPrefix(p, "/users/me/calendarList"):
		writeJSON(w, 200, map[string]any{"kind": "calendar#calendarList", "items": []map[string]any{{"id": "primary", "summary": "Bookings", "timeZone": t.TZ.String(), "accessRole": "owner"}}})

	case strings.HasPrefix(p, "/calendars/") && strings.Contains(p, "/events"):
		_, after, _ := strings.Cut(p, "/events")
		eventID := strings.TrimPrefix(after, "/")
		switch {
		case eventID == "" && r.Method == http.MethodGet:
			q := r.URL.Query()
			min, max := time.Time{}, time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)
			if v := q.Get("timeMin"); v != "" {
				if tm, err := parseTime(v, t.TZ); err == nil {
					min = tm
				}
			}
			if v := q.Get("timeMax"); v != "" {
				if tm, err := parseTime(v, t.TZ); err == nil {
					max = tm
				}
			}
			items := []map[string]any{}
			for _, e := range t.overlapping(min, max) {
				items = append(items, e.resource(t.TZ))
			}
			writeJSON(w, 200, map[string]any{"kind": "calendar#events", "summary": "Bookings", "timeZone": t.TZ.String(), "items": items})
		case eventID == "" && r.Method == http.MethodPost:
			start, end := timeField(body["start"]), timeField(body["end"])
			ev, err := s.eventFrom(t, str(body["summary"]), dateOf(body["start"]), start, end)
			if err != nil {
				fail(400, "Bad Request: "+err.Error())
				return
			}
			ev.Created = true
			t.Events = append(t.Events, ev)
			writeJSON(w, 200, ev.resource(t.TZ))
		case eventID != "":
			e := t.find(eventID)
			if e == nil {
				fail(404, "Not Found")
				return
			}
			switch r.Method {
			case http.MethodGet:
				writeJSON(w, 200, e.resource(t.TZ))
			case http.MethodDelete:
				e.Deleted = true
				status = 204
				w.WriteHeader(204)
			case http.MethodPatch, http.MethodPut:
				if v := str(body["summary"]); v != "" {
					e.Summary = v
				}
				if st := timeField(body["start"]); st != "" || dateOf(body["start"]) != "" {
					ne, err := s.eventFrom(t, e.Summary, dateOf(body["start"]), st, timeField(body["end"]))
					if err == nil {
						e.Start, e.End, e.AllDay, e.Date = ne.Start, ne.End, ne.AllDay, ne.Date
					}
				}
				writeJSON(w, 200, e.resource(t.TZ))
			default:
				fail(405, "Method not allowed")
			}
		default:
			fail(405, "Method not allowed")
		}
	default:
		fail(404, "The judge's calendar simulator doesn't support "+r.Method+" "+r.URL.Path)
	}
}

func gErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": map[string]any{"code": code, "message": msg, "errors": []map[string]string{{"message": msg}}}})
}

func (s *Server) eventFrom(t *tenant, summary, date, start, end string) (*Event, error) {
	s.nextID++
	ev := &Event{ID: fmt.Sprintf("evt%05d", s.nextID), Summary: summary}
	switch {
	case date != "" && start == "":
		d, err := time.ParseInLocation("2006-01-02", date, t.TZ)
		if err != nil {
			return nil, fmt.Errorf("bad date %q", date)
		}
		ev.AllDay, ev.Start, ev.End = true, d, d.AddDate(0, 0, 1)
		if ed := end; ed != "" {
			if e, err := parseTime(ed, t.TZ); err == nil && e.After(d) {
				ev.End = e
			}
		}
	case start != "":
		st, err := parseTime(start, t.TZ)
		if err != nil {
			return nil, fmt.Errorf("bad start %q", start)
		}
		ev.Start = st
		ev.End = st.Add(time.Hour)
		if end != "" {
			if e, err := parseTime(end, t.TZ); err == nil {
				ev.End = e
			}
		}
		if len(strings.TrimSpace(start)) == 10 { // a bare date given as dateTime
			ev.AllDay = true
			if !ev.End.After(ev.Start) {
				ev.End = ev.Start.AddDate(0, 0, 1)
			}
		}
		if !ev.End.After(ev.Start) {
			return nil, fmt.Errorf("end must be after start")
		}
	default:
		return nil, fmt.Errorf("start is required")
	}
	ev.Date = ev.Start.In(t.TZ).Format("2006-01-02")
	return ev, nil
}

func (e *Event) resource(tz *time.Location) map[string]any {
	ts := func(tm time.Time) map[string]any {
		if e.AllDay {
			return map[string]any{"date": tm.In(tz).Format("2006-01-02")}
		}
		return map[string]any{"dateTime": tm.In(tz).Format(time.RFC3339), "timeZone": tz.String()}
	}
	return map[string]any{"kind": "calendar#event", "id": e.ID, "status": "confirmed", "summary": e.Summary,
		"start": ts(e.Start), "end": ts(e.End), "htmlLink": "https://calendar.google.com/event?eid=" + e.ID}
}

func (t *tenant) overlapping(min, max time.Time) []*Event {
	var out []*Event
	for _, e := range t.Events {
		if !e.Deleted && e.Start.Before(max) && e.End.After(min) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

func (t *tenant) find(id string) *Event {
	for _, e := range t.Events {
		if e.ID == id && !e.Deleted {
			return e
		}
	}
	return nil
}

var timeLayouts = []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"}

// parseTime accepts the shapes LLM tools tend to produce; zone-less values are in the calendar's zone.
func parseTime(v string, tz *time.Location) (time.Time, error) {
	v = strings.TrimSpace(v)
	for _, l := range timeLayouts {
		if t, err := time.ParseInLocation(l, v, tz); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable time %q", v)
}

func timeField(v any) string {
	if m, ok := v.(map[string]any); ok {
		return str(m["dateTime"])
	}
	return str(v)
}

func dateOf(v any) string {
	if m, ok := v.(map[string]any); ok {
		return str(m["date"])
	}
	return ""
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Handlers returns the public handler (Telegram + Google, by Host) and the admin handler.
func (s *Server) Handlers() (public http.Handler, admin http.Handler) {
	public = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/bot") {
			s.telegram(w, r)
			return
		}
		s.google(w, r)
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/tenants/", s.admin)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	return logged(public), mux
}

func logged(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("X-Mock-Debug"), "1") {
			log.Printf("%s %s %s", r.Host, r.Method, r.URL.Path)
		}
		h.ServeHTTP(w, r)
	})
}
