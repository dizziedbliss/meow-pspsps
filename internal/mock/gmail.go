package mock

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"strings"
)

// Gmail v1 on www.googleapis.com: what n8n's Gmail node 2.1 uses to send mail (messages/send takes
// a base64url MIME message in `raw`), plus profile, labels and drafts so common workflows don't
// trip. The send result is decoded into an Email for grading.

// Email is a message the workflow sent.
type Email struct {
	ID          string   `json:"id"`
	ThreadID    string   `json:"threadId"`
	From        string   `json:"from"`
	To          []string `json:"to"`
	Cc          []string `json:"cc"`
	Bcc         []string `json:"bcc"`
	Subject     string   `json:"subject"`
	Text        string   `json:"text"`
	HTML        string   `json:"html"`
	Body        string   `json:"body"` // HTML if there is any, else the plain text
	Attachments []string `json:"attachments"`
}

func (s *Server) gmail(w http.ResponseWriter, r *http.Request, t *tenant) {
	status := 200
	defer func() { t.GmailCalls = append(t.GmailCalls, Call{Method: r.Method, Path: r.URL.Path, Status: status}) }()
	fail := func(code int, msg string) { status = code; gErr(w, code, msg) }
	p := strings.TrimPrefix(r.URL.Path, "/gmail/v1/users/me")
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	var body map[string]any
	_ = json.Unmarshal(raw, &body)

	switch {
	case p == "/profile" && r.Method == http.MethodGet:
		writeJSON(w, 200, map[string]any{"emailAddress": "judge@example.test", "messagesTotal": len(t.Emails), "threadsTotal": len(t.Emails)})
	case p == "/labels" && r.Method == http.MethodGet:
		writeJSON(w, 200, map[string]any{"labels": []map[string]any{{"id": "INBOX", "name": "INBOX", "type": "system"}, {"id": "SENT", "name": "SENT", "type": "system"}}})
	case p == "/messages/send" && r.Method == http.MethodPost:
		em, err := decodeEmail(str(body["raw"]))
		if err != nil {
			fail(400, "Invalid value for ByteString: "+err.Error())
			return
		}
		s.nextID++
		em.ID = fmt.Sprintf("msg%05d", s.nextID)
		em.ThreadID = em.ID
		t.Emails = append(t.Emails, em)
		writeJSON(w, 200, map[string]any{"id": em.ID, "threadId": em.ThreadID, "labelIds": []string{"SENT"}})
	case p == "/drafts" && r.Method == http.MethodPost:
		msg, _ := body["message"].(map[string]any)
		if _, err := decodeEmail(str(msg["raw"])); err != nil {
			fail(400, "Invalid value for ByteString: "+err.Error())
			return
		}
		s.nextID++
		writeJSON(w, 200, map[string]any{"id": fmt.Sprintf("draft%05d", s.nextID), "message": map[string]any{"id": fmt.Sprintf("msg%05d", s.nextID)}})
	case strings.HasPrefix(p, "/messages/") && r.Method == http.MethodGet:
		id := strings.TrimPrefix(p, "/messages/")
		for _, e := range t.Emails {
			if e.ID == id {
				writeJSON(w, 200, map[string]any{"id": e.ID, "threadId": e.ThreadID, "labelIds": []string{"SENT"}, "snippet": e.Subject,
					"payload": map[string]any{"headers": []map[string]string{{"name": "Subject", "value": e.Subject}, {"name": "To", "value": strings.Join(e.To, ", ")}}}})
				return
			}
		}
		fail(404, "Requested entity was not found.")
	case p == "/messages" && r.Method == http.MethodGet:
		items := []map[string]any{}
		for _, e := range t.Emails {
			items = append(items, map[string]any{"id": e.ID, "threadId": e.ThreadID})
		}
		writeJSON(w, 200, map[string]any{"messages": items, "resultSizeEstimate": len(items)})
	default:
		fail(404, "The judge's Gmail simulator doesn't support "+r.Method+" "+r.URL.Path)
	}
}

func decodeEmail(raw string) (*Email, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "=")
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		if b, err = base64.RawStdEncoding.DecodeString(raw); err != nil {
			return nil, err
		}
	}
	msg, err := mail.ReadMessage(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	dec := new(mime.WordDecoder)
	subj := msg.Header.Get("Subject")
	if d, err := dec.DecodeHeader(subj); err == nil {
		subj = d
	}
	em := &Email{From: msg.Header.Get("From"), Subject: subj, To: addrs(msg.Header.Get("To")),
		Cc: addrs(msg.Header.Get("Cc")), Bcc: addrs(msg.Header.Get("Bcc")), Attachments: []string{}}
	walkPart(em, msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), msg.Header.Get("Content-Disposition"), msg.Body)
	em.Body = em.Text
	if em.HTML != "" {
		em.Body = em.HTML
	}
	return em, nil
}

func addrs(h string) []string {
	out := []string{}
	if strings.TrimSpace(h) == "" {
		return out
	}
	if list, err := mail.ParseAddressList(h); err == nil {
		for _, a := range list {
			out = append(out, a.Address)
		}
		return out
	}
	for _, p := range strings.Split(h, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func walkPart(em *Email, ctype, cte, disp string, body io.Reader) {
	mt, params, err := mime.ParseMediaType(ctype)
	if err != nil || mt == "" {
		mt = "text/plain"
	}
	if strings.HasPrefix(mt, "multipart/") {
		mr := multipart.NewReader(body, params["boundary"])
		for {
			part, err := mr.NextPart()
			if err != nil {
				return
			}
			walkPart(em, part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part.Header.Get("Content-Disposition"), part)
		}
	}
	var rd io.Reader = body
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "quoted-printable":
		rd = quotedprintable.NewReader(body)
	case "base64":
		rd = base64.NewDecoder(base64.StdEncoding, &stripNL{body})
	}
	data, _ := io.ReadAll(io.LimitReader(rd, 4<<20))
	if d, dparams, err := mime.ParseMediaType(disp); err == nil && d == "attachment" {
		em.Attachments = append(em.Attachments, orDefault(dparams["filename"], "attachment"))
		return
	}
	switch mt {
	case "text/plain":
		em.Text += string(data)
	case "text/html":
		em.HTML += string(data)
	}
}

// stripNL drops CR/LF so base64 bodies wrapped at 76 columns decode.
type stripNL struct{ r io.Reader }

func (s *stripNL) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	j := 0
	for i := 0; i < n; i++ {
		if p[i] != '\r' && p[i] != '\n' {
			p[j] = p[i]
			j++
		}
	}
	return j, err
}
