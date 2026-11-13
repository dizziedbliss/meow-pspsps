// Package workflow parses an exported n8n workflow, checks it can run on the judge, works out which
// credentials it needs, and rewrites it into the form the judge imports.
package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Node type names the judge cares about.
const (
	TypeWebhook     = "n8n-nodes-base.webhook"
	TypeChatTrigger = "@n8n/n8n-nodes-langchain.chatTrigger"
	TypeHTTPRequest = "n8n-nodes-base.httpRequest"
	TypeTelegramTrg = "n8n-nodes-base.telegramTrigger"
)

// TriggerKind is how the judge feeds test input into a workflow.
type TriggerKind string

const (
	TriggerWebhook TriggerKind = "webhook"
	TriggerChat    TriggerKind = "chat"
	// TriggerTelegram: the judge plays Telegram, posting fake updates and reading sendMessage calls.
	TriggerTelegram TriggerKind = "telegram"
)

// Workflow is a parsed export. Raw keeps every field so nothing is lost on rewrite.
type Workflow struct {
	Raw   map[string]any
	Nodes []map[string]any
}

// Parse decodes exported workflow JSON. It accepts a bare workflow object or a one-element array
// (some exports wrap it).
func Parse(data []byte) (*Workflow, error) {
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 {
		return nil, errors.New("the file is empty")
	}
	if data[0] == '[' {
		var arr []map[string]any
		if err := json.Unmarshal(data, &arr); err != nil {
			return nil, fmt.Errorf("not valid JSON: %v", err)
		}
		if len(arr) != 1 {
			return nil, fmt.Errorf("expected one workflow, found %d", len(arr))
		}
		return fromMap(arr[0])
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("not valid JSON: %v", err)
	}
	return fromMap(m)
}

func fromMap(m map[string]any) (*Workflow, error) {
	rawNodes, ok := m["nodes"].([]any)
	if !ok || len(rawNodes) == 0 {
		return nil, errors.New("no nodes found; export the workflow from n8n (Download / Export) and upload that file")
	}
	if _, ok := m["connections"].(map[string]any); !ok {
		m["connections"] = map[string]any{}
	}
	w := &Workflow{Raw: m}
	for i, rn := range rawNodes {
		n, ok := rn.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("node %d is not an object", i)
		}
		w.Nodes = append(w.Nodes, n)
	}
	return w, nil
}

// Name returns the workflow's name, or a fallback.
func (w *Workflow) Name() string {
	if s, _ := w.Raw["name"].(string); s != "" {
		return s
	}
	return "submission"
}

func str(n map[string]any, key string) string {
	s, _ := n[key].(string)
	return s
}

func params(n map[string]any) map[string]any {
	p, ok := n["parameters"].(map[string]any)
	if !ok {
		p = map[string]any{}
		n["parameters"] = p
	}
	return p
}

func disabled(n map[string]any) bool {
	b, _ := n["disabled"].(bool)
	return b
}

// typeVersion returns a node's typeVersion as a float (n8n uses numbers like 1, 1.1, 2).
func typeVersion(n map[string]any) float64 {
	switch v := n["typeVersion"].(type) {
	case float64:
		return v
	case json.Number:
		f, _ := v.Float64()
		return f
	}
	return 1
}

// Trigger is the single node the judge sends test input to.
type Trigger struct {
	Kind     TriggerKind `json:"kind"`
	NodeName string      `json:"nodeName"`
	Method   string      `json:"method,omitempty"` // webhook only, e.g. "POST"
}

// FindTrigger finds the one enabled Webhook or Chat Trigger node.
func (w *Workflow) FindTrigger() (Trigger, error) {
	var found []Trigger
	for _, n := range w.Nodes {
		if disabled(n) {
			continue
		}
		switch str(n, "type") {
		case TypeWebhook:
			m := strings.ToUpper(str(params(n), "httpMethod"))
			if arr, ok := params(n)["httpMethod"].([]any); ok { // "Allow multiple HTTP methods"
				parts := make([]string, 0, len(arr))
				for _, x := range arr {
					parts = append(parts, strings.ToUpper(fmt.Sprint(x)))
				}
				m = strings.Join(parts, ",")
			}
			if m == "" {
				m = "GET" // n8n's default for the Webhook node
			}
			found = append(found, Trigger{Kind: TriggerWebhook, NodeName: str(n, "name"), Method: m})
		case TypeChatTrigger:
			found = append(found, Trigger{Kind: TriggerChat, NodeName: str(n, "name")})
		case TypeTelegramTrg:
			found = append(found, Trigger{Kind: TriggerTelegram, NodeName: str(n, "name")})
		}
	}
	switch len(found) {
	case 0:
		return Trigger{}, errors.New("no Webhook, Chat Trigger or Telegram Trigger node found; the judge needs one of them as the entry point")
	case 1:
		return found[0], nil
	default:
		names := make([]string, len(found))
		for i, t := range found {
			names[i] = t.NodeName
		}
		return Trigger{}, fmt.Errorf("found %d trigger nodes (%s); keep exactly one trigger enabled", len(found), strings.Join(names, ", "))
	}
}

// secretPatterns catch API keys pasted straight into the workflow.
var secretPatterns = []struct {
	label string
	re    *regexp.Regexp
}{
	{"OpenAI-style key (sk-...)", regexp.MustCompile(`sk-(proj-)?[A-Za-z0-9_\-]{20,}`)},
	{"Google API key (AIza...)", regexp.MustCompile(`AIza[0-9A-Za-z_\-]{35}`)},
	{"Slack token (xox...)", regexp.MustCompile(`xox[baprs]-[A-Za-z0-9\-]{10,}`)},
	{"Anthropic key (sk-ant-...)", regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{20,}`)},
	{"Groq key (gsk_...)", regexp.MustCompile(`gsk_[A-Za-z0-9]{20,}`)},
	{"GitHub token", regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{30,}`)},
}

// ScanSecrets returns a warning for each kind of hardcoded key found in the raw upload.
func ScanSecrets(raw []byte) []string {
	out := []string{}
	for _, p := range secretPatterns {
		if p.re.Match(raw) {
			out = append(out, "This workflow seems to contain a hardcoded "+p.label+". Move it into a credential; anything in the JSON is visible to organisers.")
		}
	}
	return out
}

// NodeTypes returns the sorted distinct node types used by enabled nodes.
func (w *Workflow) NodeTypes() []string {
	set := map[string]bool{}
	for _, n := range w.Nodes {
		if !disabled(n) {
			set[str(n, "type")] = true
		}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
