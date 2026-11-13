package mock

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAttributionStripped(t *testing.T) {
	in := "Booked for Friday, 6 Nov 2026 🎉\n\n_This message was sent automatically with _[n8n](https://n8n.io/?utm_source=x)"
	if got := attribution.ReplaceAllString(in, ""); got != "Booked for Friday, 6 Nov 2026 🎉" {
		t.Fatalf("got %q", got)
	}
}

const tok = "tenant-1"

func setup(t *testing.T, seed string) (public, admin *httptest.Server) {
	t.Helper()
	s := New()
	s.SetAdminToken("secret")
	p, a := s.Handlers()
	public, admin = httptest.NewServer(p), httptest.NewServer(a)
	t.Cleanup(func() { public.Close(); admin.Close() })
	req, _ := http.NewRequest("PUT", admin.URL+"/admin/tenants/"+tok, strings.NewReader(seed))
	req.Header.Set("Authorization", "Bearer secret")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 204 {
		t.Fatalf("seed failed: %v %v", err, resp)
	}
	return
}

func call(t *testing.T, method, url, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func snapshot(t *testing.T, admin *httptest.Server) Snapshot {
	t.Helper()
	req, _ := http.NewRequest("GET", admin.URL+"/admin/tenants/"+tok, nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var s Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAdminNeedsToken(t *testing.T) {
	_, admin := setup(t, `{}`)
	for _, h := range []string{"", "Bearer wrong"} {
		req, _ := http.NewRequest("GET", admin.URL+"/admin/tenants/"+tok, nil)
		if h != "" {
			req.Header.Set("Authorization", h)
		}
		resp, _ := http.DefaultClient.Do(req)
		if resp.StatusCode != 401 {
			t.Fatalf("auth %q: status %d", h, resp.StatusCode)
		}
	}
	s := New() // no token set: closed
	_, a := s.Handlers()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/admin/tenants/x", nil)
	req.Header.Set("Authorization", "Bearer ")
	a.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("no-token server answered %d", rec.Code)
	}
}

func TestSheetsFlow(t *testing.T) {
	pub, admin := setup(t, `{"sheets":{"*":{"sheets":[{"title":"Responses","header":["roll","meal","rating"],"rows":[["R1","Lunch",4]]}]}}}`)
	base := pub.URL + "/v4/spreadsheets/doc1"
	// metadata (auto-created from the template)
	_, md := call(t, "GET", base+"?fields=sheets.properties", "")
	if sh := md["sheets"].([]any)[0].(map[string]any)["properties"].(map[string]any); sh["title"] != "Responses" || sh["sheetId"].(float64) != 0 {
		t.Fatalf("metadata %v", md)
	}
	// append two rows
	code, ap := call(t, "POST", base+"/values/Responses%21A%3AC:append?valueInputOption=USER_ENTERED", `{"range":"Responses!A:C","values":[["R2","Dinner","3"],["R3","Dinner",null]]}`)
	if code != 200 || ap["updates"].(map[string]any)["updatedRange"] != "Responses!A3:C4" {
		t.Fatalf("append %d %v", code, ap)
	}
	// read formatted and unformatted
	_, rd := call(t, "GET", base+"/values/Responses%21A1%3AC", "")
	rows := rd["values"].([]any)
	if len(rows) != 4 || rows[1].([]any)[2] != "4" || len(rows[3].([]any)) != 2 {
		t.Fatalf("read %v", rd)
	}
	_, rd = call(t, "GET", base+"/values/Responses%21A2%3AC2?valueRenderOption=UNFORMATTED_VALUE", "")
	if rd["values"].([]any)[0].([]any)[2].(float64) != 4 {
		t.Fatalf("unformatted %v", rd)
	}
	// whole-tab read through quotes, row-range update, batchUpdate
	_, rd = call(t, "GET", base+"/values/%27Responses%27", "")
	if len(rd["values"].([]any)) != 4 {
		t.Fatalf("whole tab %v", rd)
	}
	call(t, "PUT", base+"/values/Responses%212%3A2?valueInputOption=RAW", `{"range":"Responses!2:2","values":[["R1","Lunch","5"]]}`)
	call(t, "POST", base+"/values:batchUpdate", `{"valueInputOption":"USER_ENTERED","data":[{"range":"Responses!C3","values":[[1]]}]}`)
	// delete row 3 (0-based 2), add a tab, clear
	call(t, "POST", base+":batchUpdate", `{"requests":[{"deleteDimension":{"range":{"sheetId":0,"dimension":"ROWS","startIndex":2,"endIndex":3}}},{"addSheet":{"properties":{"title":"Log"}}}]}`)
	if code, _ := call(t, "POST", base+":batchUpdate", `{"requests":[{"addSheet":{"properties":{"title":"Log"}}}]}`); code != 400 {
		t.Fatalf("duplicate tab: %d", code)
	}
	call(t, "POST", base+"/values/Log%21A%3AZ:append", `{"values":[["x"]]}`)
	if code, _ := call(t, "GET", base+"/values/Nope%21A1", ""); code != 400 {
		t.Fatalf("unknown tab read: %d", code)
	}
	call(t, "POST", base+"/values/Log%21A%3AZ:clear", `{}`)

	snap := snapshot(t, admin)
	rs := snap.Sheets["doc1"].Sheets[0]
	if len(rs.Rows) != 3 || rs.Records[0]["rating"] != "5" || rs.Records[1]["meal"] != "Dinner" || rs.Records[1]["rating"] != "" {
		t.Fatalf("snapshot %+v", rs)
	}
	if snap.Sheets["doc1"].Sheets[1].Title != "Log" || len(snap.Sheets["doc1"].Sheets[1].Records) != 0 {
		t.Fatalf("log tab %+v", snap.Sheets["doc1"].Sheets)
	}
	if len(snap.Gsheet.Calls) == 0 {
		t.Fatal("calls not recorded")
	}
}

func TestParseA1(t *testing.T) {
	for in, want := range map[string]a1{
		"Sheet1!A1:C5": {"Sheet1", 0, 2, 1, 5},
		"Sheet1!A:Z":   {"Sheet1", 0, 25, 0, 0},
		"Sheet1!3:3":   {"Sheet1", -1, -1, 3, 3},
		"'My Tab'!B7":  {"My Tab", 1, 1, 7, 7},
		"Sheet1":       {"Sheet1", -1, -1, 0, 0},
	} {
		got, err := parseA1(in)
		if err != nil || got != want {
			t.Errorf("%s: got %+v %v want %+v", in, got, err, want)
		}
	}
}

func TestGmailSendDecoded(t *testing.T) {
	pub, admin := setup(t, `{}`)
	msg := "From: a@x.test\r\nTo: Boss <boss@x.test>, ops@x.test\r\nSubject: =?UTF-8?B?w4RsZXJ0?=\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"b1\"\r\n\r\n--b1\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nHello =E2=9C=93\r\n" +
		"--b1\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString([]byte("<b>Hi</b>")) + "\r\n--b1--\r\n"
	raw := base64.URLEncoding.EncodeToString([]byte(msg))
	code, out := call(t, "POST", pub.URL+"/gmail/v1/users/me/messages/send", `{"raw":"`+raw+`"}`)
	if code != 200 || out["id"] == "" {
		t.Fatalf("send %d %v", code, out)
	}
	sent := snapshot(t, admin).Email.Sent
	if len(sent) != 1 {
		t.Fatalf("sent %v", sent)
	}
	e := sent[0]
	if e.Subject != "Älert" || len(e.To) != 2 || e.To[0] != "boss@x.test" || e.Text != "Hello ✓" || e.HTML != "<b>Hi</b>" || e.Body != "<b>Hi</b>" {
		t.Fatalf("decoded %+v", e)
	}
	if code, _ := call(t, "POST", pub.URL+"/gmail/v1/users/me/messages/send", `{"raw":"!!"}`); code != 400 {
		t.Fatalf("bad raw: %d", code)
	}
}

func TestTelegramAlertStillWorks(t *testing.T) {
	pub, admin := setup(t, `{}`)
	code, _ := call(t, "POST", pub.URL+"/bot"+tok+"/sendMessage", `{"chat_id":"42","text":"Fire at gate"}`)
	if code != 200 || len(snapshot(t, admin).Telegram.Sent) != 1 {
		t.Fatalf("telegram %d", code)
	}
}
