package workflow

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Entry is one webhook the problem requires: a short name, the fixed path teams must use and the method.
type Entry struct {
	Name   string `yaml:"name" json:"name"`
	Path   string `yaml:"path" json:"path"`
	Method string `yaml:"method" json:"method"`
}

// EntryNode is an Entry found in a team's workflow.
type EntryNode struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	NodeName string `json:"nodeName"`
}

func normPath(p string) string { return strings.ToLower(strings.Trim(strings.TrimSpace(p), "/")) }

// webhookInfo returns an enabled Webhook node's path and allowed methods.
func webhookInfo(n map[string]any) (path, methods string) {
	t, _ := FindTriggerOf(n)
	return normPath(str(params(n), "path")), t.Method
}

// FindTriggerOf reads the Webhook method(s) of one node.
func FindTriggerOf(n map[string]any) (Trigger, bool) {
	if str(n, "type") != TypeWebhook {
		return Trigger{}, false
	}
	w := &Workflow{Nodes: []map[string]any{n}}
	t, err := w.FindTrigger()
	return t, err == nil
}

// MapEntries matches each required entry to an enabled Webhook node by path (trimmed, case
// insensitive). Missing entries come back as errors that list the team's own webhook paths.
func (w *Workflow) MapEntries(entries []Entry) ([]EntryNode, []string) {
	have := map[string]string{} // normalised path → node name
	methods := map[string]string{}
	var paths []string
	for _, n := range w.Nodes {
		if disabled(n) || str(n, "type") != TypeWebhook {
			continue
		}
		p, m := webhookInfo(n)
		if _, dup := have[p]; !dup {
			have[p] = str(n, "name")
			methods[p] = m
		}
		paths = append(paths, strings.TrimSpace(str(params(n), "path")))
	}
	sort.Strings(paths)
	var out []EntryNode
	var errs []string
	for _, e := range entries {
		p := normPath(e.Path)
		node, ok := have[p]
		if !ok {
			mine := "none"
			if len(paths) > 0 {
				mine = strings.Join(paths, ", ")
			}
			errs = append(errs, fmt.Sprintf("no Webhook node with the path %q (needed for %s). Your Webhook paths: %s", e.Path, e.Name, mine))
			continue
		}
		if e.Method != "" && !methodAllowed(methods[p], e.Method) {
			errs = append(errs, fmt.Sprintf("the Webhook node %q (path %s) accepts %s, but tests send %s; set its HTTP Method to %s", node, e.Path, methods[p], e.Method, e.Method))
		}
		out = append(out, EntryNode{Name: e.Name, Path: e.Path, NodeName: node})
	}
	return out, errs
}

// isTriggerType is true for nodes that start a workflow on their own.
func isTriggerType(t string) bool {
	l := strings.ToLower(t)
	return strings.HasSuffix(l, "trigger") || t == TypeWebhook || t == "n8n-nodes-base.cron" || t == "n8n-nodes-base.interval" || t == "n8n-nodes-base.emailReadImap"
}

// OtherTriggers lists enabled trigger nodes that are not one of the mapped entries.
func (w *Workflow) OtherTriggers(entries []EntryNode) []string {
	keep := map[string]bool{}
	for _, e := range entries {
		keep[e.NodeName] = true
	}
	var out []string
	for _, n := range w.Nodes {
		if !disabled(n) && isTriggerType(str(n, "type")) && !keep[str(n, "name")] {
			out = append(out, str(n, "name"))
		}
	}
	return out
}

// PrepareEntries is Prepare for problems with several webhook entry points: every entry's path
// becomes prefix+"-"+entry name, every other trigger is switched off for the test, auth is off,
// credentials point at the judge's, and the timezone is the problem's.
func PrepareEntries(raw []byte, entries []EntryNode, needs []CredNeed, refs map[string]CredRef, prefix, tz string) ([]byte, error) {
	w, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	byNode := credsByNode(needs)
	path := map[string]string{}
	for _, e := range entries {
		path[e.NodeName] = prefix + "-" + e.Name
	}
	nodes := make([]any, 0, len(w.Nodes))
	for _, n := range w.Nodes {
		name := str(n, "name")
		if p, ok := path[name]; ok {
			pr := params(n)
			pr["path"] = p
			pr["authentication"] = "none"
			n["webhookId"] = p
			delete(n, "credentials")
		} else if isTriggerType(str(n, "type")) {
			n["disabled"] = true
			delete(n, "webhookId")
		}
		if err := patchCreds(n, byNode[name], refs); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	settings := map[string]any{"executionOrder": "v1"}
	if s, ok := w.Raw["settings"].(map[string]any); ok {
		if eo, ok := s["executionOrder"].(string); ok && eo != "" {
			settings["executionOrder"] = eo
		}
	}
	if tz != "" {
		settings["timezone"] = tz
	}
	name := w.Name()
	if len(name) > 90 {
		name = name[:90]
	}
	return json.Marshal(map[string]any{"name": "judge: " + name, "nodes": nodes, "connections": w.Raw["connections"], "settings": settings})
}

func credsByNode(needs []CredNeed) map[string][]string {
	byNode := map[string][]string{}
	for _, n := range needs {
		for _, node := range n.Nodes {
			byNode[node] = append(byNode[node], n.Type)
		}
	}
	return byNode
}

func patchCreds(n map[string]any, types []string, refs map[string]CredRef) error {
	if len(types) == 0 {
		return nil
	}
	creds, _ := n["credentials"].(map[string]any)
	if creds == nil {
		creds = map[string]any{}
	}
	for _, t := range types {
		ref, ok := refs[t]
		if !ok {
			return fmt.Errorf("internal: no credential created for %s", t)
		}
		creds[t] = map[string]any{"id": ref.ID, "name": ref.Name}
	}
	n["credentials"] = creds
	return nil
}

// Reachable returns names of enabled, non-note nodes that no entry can reach through connections.
func (w *Workflow) Unreachable(entries []EntryNode) []string {
	conns, _ := w.Raw["connections"].(map[string]any)
	seen := map[string]bool{}
	var walk func(string)
	walk = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		outs, _ := conns[name].(map[string]any)
		for _, kinds := range outs {
			lists, _ := kinds.([]any)
			for _, l := range lists {
				items, _ := l.([]any)
				for _, it := range items {
					if m, ok := it.(map[string]any); ok {
						walk(str(m, "node"))
					}
				}
			}
		}
	}
	for _, e := range entries {
		walk(e.NodeName)
	}
	// Sub-nodes (AI models, parsers) connect *into* their parent; count a parent's inputs as reached.
	for from, outs := range conns {
		o, _ := outs.(map[string]any)
		for kind, kinds := range o {
			if !strings.HasPrefix(kind, "ai_") {
				continue
			}
			lists, _ := kinds.([]any)
			for _, l := range lists {
				items, _ := l.([]any)
				for _, it := range items {
					if m, ok := it.(map[string]any); ok && seen[str(m, "node")] {
						seen[from] = true
					}
				}
			}
		}
	}
	var out []string
	for _, n := range w.Nodes {
		t := str(n, "type")
		if disabled(n) || t == "n8n-nodes-base.stickyNote" || isTriggerType(t) || seen[str(n, "name")] {
			continue
		}
		out = append(out, str(n, "name"))
	}
	sort.Strings(out)
	return out
}

// RespondsImmediately lists the entry Webhook nodes that answer on receipt (responseMode missing or
// "onReceived", n8n's default): their real answer is whatever the last node produces.
func (w *Workflow) RespondsImmediately(entries []EntryNode) []string {
	want := map[string]bool{}
	for _, e := range entries {
		want[e.NodeName] = true
	}
	var out []string
	for _, n := range w.Nodes {
		if !want[str(n, "name")] || disabled(n) {
			continue
		}
		if m := str(params(n), "responseMode"); m == "" || m == "onReceived" {
			out = append(out, str(n, "name"))
		}
	}
	sort.Strings(out)
	return out
}
