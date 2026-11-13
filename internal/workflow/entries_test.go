package workflow

import (
	"encoding/json"
	"strings"
	"testing"
)

const multi = `{"name":"t","nodes":[
 {"name":"Form","type":"n8n-nodes-base.formTrigger","parameters":{}},
 {"name":"Hook A","type":"n8n-nodes-base.webhook","webhookId":"x","parameters":{"path":"/Mess-Feedback/ ","httpMethod":"POST"}},
 {"name":"Hook B","type":"n8n-nodes-base.webhook","parameters":{"path":"mess-report","httpMethod":"POST"}},
 {"name":"Sheet","type":"n8n-nodes-base.googleSheets","parameters":{}},
 {"name":"Orphan","type":"n8n-nodes-base.set","parameters":{}},
 {"name":"Note","type":"n8n-nodes-base.stickyNote","parameters":{}}],
 "connections":{"Hook A":{"main":[[{"node":"Sheet"}]]}}}`

var entries = []Entry{{Name: "feedback", Path: "mess-feedback", Method: "POST"}, {Name: "report", Path: "mess-report", Method: "POST"}}

func TestMapEntriesAndPrepare(t *testing.T) {
	w, err := Parse([]byte(multi))
	if err != nil {
		t.Fatal(err)
	}
	en, errs := w.MapEntries(entries)
	if len(errs) != 0 || len(en) != 2 || en[0].NodeName != "Hook A" {
		t.Fatalf("entries=%v errs=%v", en, errs)
	}
	if got := w.OtherTriggers(en); len(got) != 1 || got[0] != "Form" {
		t.Fatalf("other triggers = %v", got)
	}
	if got := w.Unreachable(en); len(got) != 1 || got[0] != "Orphan" {
		t.Fatalf("unreachable = %v", got)
	}
	out, err := PrepareEntries([]byte(multi), en, nil, nil, "judge-ab", "Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	json.Unmarshal(out, &m)
	s := string(out)
	for _, want := range []string{`"path":"judge-ab-feedback"`, `"path":"judge-ab-report"`, `"timezone":"Asia/Kolkata"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
	for _, n := range m["nodes"].([]any) {
		nn := n.(map[string]any)
		if nn["name"] == "Form" && nn["disabled"] != true {
			t.Error("form trigger should be disabled")
		}
	}
}

func TestMapEntriesMissing(t *testing.T) {
	w, _ := Parse([]byte(multi))
	_, errs := w.MapEntries([]Entry{{Name: "x", Path: "nope", Method: "POST"}})
	if len(errs) != 1 || !strings.Contains(errs[0], "mess-report") {
		t.Fatalf("errs = %v", errs)
	}
}

func TestRespondsImmediatelyWarning(t *testing.T) {
	raw := `{"nodes":[
 {"name":"A","type":"n8n-nodes-base.webhook","parameters":{"path":"a","httpMethod":"POST"}},
 {"name":"B","type":"n8n-nodes-base.webhook","parameters":{"path":"b","httpMethod":"POST","responseMode":"onReceived"}},
 {"name":"C","type":"n8n-nodes-base.webhook","parameters":{"path":"c","httpMethod":"POST","responseMode":"responseNode"}}],"connections":{}}`
	w, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	en, errs := w.MapEntries([]Entry{{Name: "a", Path: "a", Method: "POST"}, {Name: "b", Path: "b", Method: "POST"}, {Name: "c", Path: "c", Method: "POST"}})
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	got := w.RespondsImmediately(en)
	if len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatalf("immediate responders = %v", got)
	}
}
