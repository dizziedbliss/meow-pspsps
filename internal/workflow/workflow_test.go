package workflow

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dizziedbliss/meow-pspsps/internal/catalog"
)

func load(t *testing.T) ([]byte, *Workflow, *catalog.Catalog) {
	t.Helper()
	raw, err := os.ReadFile("../../problems/0-dummy/ideal.json")
	if err != nil {
		t.Fatal(err)
	}
	w, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.Default()
	if err != nil {
		t.Fatal(err)
	}
	return raw, w, cat
}

func TestAnalyzeIdealIsValid(t *testing.T) {
	raw, w, cat := load(t)
	a := Analyze(w, raw, cat, Expect{Trigger: TriggerWebhook, Method: "POST"})
	if len(a.Errors) != 0 {
		t.Fatalf("errors: %v", a.Errors)
	}
	if a.Trigger.NodeName != "Webhook" || a.Trigger.Method != "POST" {
		t.Fatalf("trigger = %+v", a.Trigger)
	}
}

func TestAnalyzeRejectsWrongMethodAndUnknownNode(t *testing.T) {
	raw, w, cat := load(t)
	w.Nodes[0]["parameters"].(map[string]any)["httpMethod"] = "GET"
	w.Nodes[1]["type"] = "n8n-nodes-base.doesNotExist"
	a := Analyze(w, raw, cat, Expect{Trigger: TriggerWebhook, Method: "POST"})
	all := strings.Join(a.Errors, "\n")
	if !strings.Contains(all, "accepts GET") || !strings.Contains(all, "isn't available") {
		t.Fatalf("errors: %v", a.Errors)
	}
}

func TestAnalyzeInfersStrippedCredentials(t *testing.T) {
	raw := []byte(`{"nodes":[
	 {"name":"Chat","type":"@n8n/n8n-nodes-langchain.chatTrigger","typeVersion":1.4,"parameters":{}},
	 {"name":"Model","type":"@n8n/n8n-nodes-langchain.lmChatOpenAi","typeVersion":1.2,"parameters":{}},
	 {"name":"Gem","type":"@n8n/n8n-nodes-langchain.lmChatGoogleGemini","typeVersion":1,"parameters":{},
	  "credentials":{"googlePalmApi":{"id":null,"name":"x","__aiGatewayManaged":true}}}],"connections":{}}`)
	w, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	cat, _ := catalog.Default()
	a := Analyze(w, raw, cat, Expect{Trigger: TriggerChat})
	if len(a.Errors) != 0 {
		t.Fatalf("errors: %v", a.Errors)
	}
	got := map[string]bool{}
	for _, n := range a.Needs {
		got[n.Type] = true
		if len(n.Fields) == 0 {
			t.Errorf("%s has no form fields", n.Type)
		}
	}
	if !got["openAiApi"] || !got["googlePalmApi"] {
		t.Fatalf("needs = %+v", a.Needs)
	}
	if len(a.Warnings) == 0 {
		t.Fatal("expected a gateway-credits warning")
	}
}

func TestPrepareRewritesTriggerAndStripsPinData(t *testing.T) {
	raw, w, cat := load(t)
	a := Analyze(w, raw, cat, Expect{Trigger: TriggerWebhook, Method: "POST"})
	out, err := Prepare(raw, a.Trigger, a.Needs, nil, "judge-abc")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(out, &m)
	if _, ok := m["pinData"]; ok {
		t.Fatal("pinData not stripped")
	}
	p := m["nodes"].([]any)[0].(map[string]any)["parameters"].(map[string]any)
	if p["path"] != "judge-abc" || p["authentication"] != "none" {
		t.Fatalf("trigger params = %v", p)
	}
}

func TestScanSecrets(t *testing.T) {
	if len(ScanSecrets([]byte(`{"k":"sk-proj-abcdefghijklmnopqrstuvwxyz123"}`))) != 1 {
		t.Fatal("missed an OpenAI key")
	}
	if len(ScanSecrets([]byte(`{"k":"hello"}`))) != 0 {
		t.Fatal("false positive")
	}
}
