package workflow

import (
	"encoding/json"
	"fmt"
)

// CredRef points a node at a credential created inside the judge's n8n.
type CredRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Prepare returns the body for POST /api/v1/workflows: the team's workflow with its trigger moved to
// the judge's path (webhook) or id (chat), trigger auth off, credentials pointed at the ones the
// judge created, and read-only or cheat-prone fields (pinData, ids, tags, active) dropped.
//
// needs maps credential type to the node names that use it (from Analyze). refs maps credential
// type to the credential created in the container.
func Prepare(raw []byte, trig Trigger, needs []CredNeed, refs map[string]CredRef, judgePath string) ([]byte, error) {
	w, err := Parse(raw) // fresh copy; never mutate the caller's workflow
	if err != nil {
		return nil, err
	}
	byNode := map[string][]string{}
	for _, n := range needs {
		for _, node := range n.Nodes {
			byNode[node] = append(byNode[node], n.Type)
		}
	}

	nodes := make([]any, 0, len(w.Nodes))
	for _, n := range w.Nodes {
		name := str(n, "name")
		if name == trig.NodeName {
			p := params(n)
			switch trig.Kind {
			case TriggerWebhook:
				p["path"] = judgePath
				p["authentication"] = "none"
				n["webhookId"] = judgePath
			case TriggerChat:
				p["public"] = true
				p["authentication"] = "none"
				n["webhookId"] = judgePath
				// Hosted chat mode is fine; the judge POSTs straight to the webhook.
			case TriggerTelegram:
				n["webhookId"] = judgePath // unique per deployment; Telegram auth stays as built
			}
			if trig.Kind != TriggerTelegram {
				delete(n, "credentials")
			}
		}
		if types := byNode[name]; len(types) > 0 {
			creds, _ := n["credentials"].(map[string]any)
			if creds == nil {
				creds = map[string]any{}
			}
			for _, t := range types {
				ref, ok := refs[t]
				if !ok {
					return nil, fmt.Errorf("internal: no credential created for %s", t)
				}
				creds[t] = map[string]any{"id": ref.ID, "name": ref.Name}
			}
			n["credentials"] = creds
		}
		nodes = append(nodes, n)
	}

	settings := map[string]any{"executionOrder": "v1"}
	if s, ok := w.Raw["settings"].(map[string]any); ok {
		if eo, ok := s["executionOrder"].(string); ok && eo != "" {
			settings["executionOrder"] = eo
		}
	}
	name := w.Name()
	if len(name) > 90 { // n8n on SQLite rejects names over 128 chars
		name = name[:90]
	}
	out := map[string]any{
		"name":        "judge: " + name,
		"nodes":       nodes,
		"connections": w.Raw["connections"],
		"settings":    settings,
	}
	return json.Marshal(out)
}
