package problem

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Starter builds a workflow JSON teams can paste onto their n8n canvas: for every required webhook a
// Webhook node (POST, exact path, "Respond to Webhook" mode), a Respond to Webhook node after it with
// a placeholder reply, and a sticky note saying what arrives and what must go back. It is generated
// from p.Entries so the paths can't drift from what the judge looks for.
func (p *Problem) Starter() ([]byte, error) {
	if len(p.Entries) == 0 {
		return nil, fmt.Errorf("problem %s has no entry points", p.ID)
	}
	var nodes []any
	conns := map[string]any{}
	for i, e := range p.Entries {
		y := 300 * i
		method := e.Method
		if method == "" {
			method = "POST"
		}
		hook := "Webhook: " + e.Name
		reply := "Respond: " + e.Name
		nodes = append(nodes,
			map[string]any{
				"id": fmt.Sprintf("starter-hook-%d", i), "name": hook, "type": "n8n-nodes-base.webhook", "typeVersion": 2,
				"position":   []int{0, y},
				"webhookId":  fmt.Sprintf("starter-%d", i),
				"parameters": map[string]any{"httpMethod": method, "path": e.Path, "responseMode": "responseNode", "options": map[string]any{}},
			},
			map[string]any{
				"id": fmt.Sprintf("starter-reply-%d", i), "name": reply, "type": "n8n-nodes-base.respondToWebhook", "typeVersion": 1.1,
				"position": []int{600, y},
				"parameters": map[string]any{
					"respondWith":  "json",
					"responseBody": `={{ { "replace": "this placeholder with the reply described in the statement" } }}`,
					"options":      map[string]any{},
				},
			},
			map[string]any{
				"id": fmt.Sprintf("starter-note-%d", i), "name": "Note: " + e.Name, "type": "n8n-nodes-base.stickyNote", "typeVersion": 1,
				"position": []int{0, y - 190},
				"parameters": map[string]any{
					"width": 640, "height": 150,
					"content": fmt.Sprintf("**%s** (%s /%s)\n\nThe judge sends the request JSON here. In your nodes read it as `$json.body.<field>`.\n"+
						"Connect this Webhook to the first node of your flow, and the end of the flow to **%s** with the reply from the statement. Keep the path and method unchanged.",
						e.Name, method, strings.Trim(e.Path, "/"), reply),
				},
			},
		)
		conns[hook] = map[string]any{"main": []any{[]any{map[string]any{"node": reply, "type": "main", "index": 0}}}}
	}
	return json.MarshalIndent(map[string]any{
		"name":        "Starter: " + p.Title,
		"nodes":       nodes,
		"connections": conns,
		"settings":    map[string]any{"executionOrder": "v1"},
	}, "", "  ")
}
