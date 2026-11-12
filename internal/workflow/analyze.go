package workflow

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/dizziedbliss/meow-pspsps/internal/catalog"
)

// CredNeed is one credential type the workflow needs, which the team fills in on the Run form.
type CredNeed struct {
	Type           string              `json:"type"`
	DisplayName    string              `json:"displayName"`
	Nodes          []string            `json:"nodes"`
	Fields         []catalog.CredField `json:"fields"`
	GatewayManaged bool                `json:"gatewayManaged,omitempty"`
	Mocked         bool                `json:"mocked,omitempty"` // the judge supplies a simulated account; teams enter nothing
}

// Analysis is everything the judge learns from an upload before running it.
type Analysis struct {
	Trigger      Trigger             `json:"trigger"`
	Needs        []CredNeed          `json:"needs"`
	SheetHeaders map[string][]string `json:"sheetHeaders,omitempty"` // tab → columns the team's Sheets nodes expect
	Entries      []EntryNode         `json:"entries,omitempty"`      // multi-entry problems: required webhooks found in the workflow
	Features     *Features           `json:"features,omitempty"`
	Errors       []string            `json:"errors"`   // any error means Invalid Workflow; nothing runs
	Warnings     []string            `json:"warnings"` // shown, but the run goes ahead
}

// Expect is what the problem requires of the workflow's entry point.
type Expect struct {
	Trigger TriggerKind
	Method  string   // webhook only
	Mocked  []string // credential types the judge simulates (e.g. telegramApi, googleCalendarOAuth2Api)
	Entries []Entry  // several required webhooks (replaces the single-trigger rules)
	Ideal   []string // node types of the ideal workflow, for the feature diff
}

// webhookAuthCreds are the Webhook node's own auth credentials; the judge switches auth off instead.
var webhookAuthCreds = map[string]bool{"httpBasicAuth": true, "httpHeaderAuth": true, "jwtAuth": true}

// Analyze checks a parsed workflow against the catalog and the problem's entry-point contract.
func Analyze(w *Workflow, raw []byte, cat *catalog.Catalog, exp Expect) Analysis {
	a := Analysis{Errors: []string{}, Warnings: ScanSecrets(raw)}

	var trig Trigger
	var err error
	if len(exp.Entries) > 0 {
		var errs []string
		a.Entries, errs = w.MapEntries(exp.Entries)
		a.Errors = append(a.Errors, errs...)
		if len(a.Entries) > 0 {
			trig = Trigger{Kind: TriggerWebhook, NodeName: a.Entries[0].NodeName, Method: "POST"}
			a.Trigger = trig
		}
		if now := w.RespondsImmediately(a.Entries); len(now) > 0 {
			a.Warnings = append(a.Warnings, "Webhook "+strings.Join(now, ", ")+" responds immediately, so the judge will use the output of the last node that ran as your answer. To control the reply exactly, set Respond to \"Using 'Respond to Webhook' node\" or \"When last node finishes\".")
		}
		if len(errs) == 0 { // with a required webhook missing these warnings would only mislead
			if other := w.OtherTriggers(a.Entries); len(other) > 0 {
				a.Warnings = append(a.Warnings, "These triggers are switched off while the judge tests: "+strings.Join(other, ", "))
			}
			if un := w.Unreachable(a.Entries); len(un) > 0 {
				a.Warnings = append(a.Warnings, "Not reachable from any required webhook (never runs in tests): "+strings.Join(un, ", "))
			}
		}
		f := w.Describe(a.Entries, exp.Ideal)
		a.Features = &f
		a.SheetHeaders = w.SheetHeaders()
	} else {
		trig, err = w.FindTrigger()
	}
	if len(exp.Entries) > 0 {
		// entry rules handled above
	} else if err != nil {
		a.Errors = append(a.Errors, err.Error())
	} else {
		a.Trigger = trig
		if exp.Trigger != "" && trig.Kind != exp.Trigger {
			a.Errors = append(a.Errors, fmt.Sprintf("this problem needs a %s trigger, but the workflow starts with a %s trigger (%q)", label(exp.Trigger), label(trig.Kind), trig.NodeName))
		}
		if trig.Kind == TriggerWebhook && exp.Method != "" && !methodAllowed(trig.Method, exp.Method) {
			a.Errors = append(a.Errors, fmt.Sprintf("the Webhook node %q accepts %s, but tests send %s; set its HTTP Method to %s", trig.NodeName, trig.Method, exp.Method, exp.Method))
		}
	}

	needs := map[string]*CredNeed{}
	addNeed := func(credType, node string, gateway bool) {
		cn := needs[credType]
		if cn == nil {
			cn = &CredNeed{Type: credType, DisplayName: credType}
			if ct, ok := cat.Creds[credType]; ok {
				cn.DisplayName = ct.DisplayName
				cn.Fields = ct.Fields
			}
			needs[credType] = cn
		}
		cn.Nodes = append(cn.Nodes, node)
		if gateway {
			cn.GatewayManaged = true
		}
	}

	entryNode := map[string]bool{}
	for _, e := range a.Entries {
		entryNode[e.NodeName] = true
	}
	for _, n := range w.Nodes {
		if disabled(n) {
			continue
		}
		name, typ := str(n, "name"), str(n, "type")
		if len(exp.Entries) > 0 && isTriggerType(typ) && !entryNode[name] {
			continue // switched off during tests; its credentials and version don't matter
		}
		v := typeVersion(n)
		known, vOK := cat.HasVersion(typ, v)
		switch {
		case !known && !strings.HasPrefix(typ, "n8n-nodes-base.") && !strings.HasPrefix(typ, "@n8n/n8n-nodes-langchain."):
			a.Errors = append(a.Errors, fmt.Sprintf("node %q uses %s, a community node; the judge only runs built-in n8n nodes", name, typ))
		case !known:
			a.Errors = append(a.Errors, fmt.Sprintf("node %q (%s) isn't available on the judge (n8n %s); it may be newer than the judge or disabled for safety", name, typ, cat.N8nVersion))
		case !vOK:
			a.Errors = append(a.Errors, fmt.Sprintf("node %q is %s version %s, which the judge's n8n %s doesn't have (it has %s); re-add the node in n8n %s or older", name, typ, fmtVer(v), cat.N8nVersion, fmtVers(cat.Nodes[typ].Versions), cat.N8nVersion))
		}

		isTrigger := trig.NodeName == name || entryNode[name]
		if creds, ok := n["credentials"].(map[string]any); ok && len(creds) > 0 {
			for credType, ref := range creds {
				if isTrigger && webhookAuthCreds[credType] {
					continue
				}
				gw := false
				if m, ok := ref.(map[string]any); ok {
					gw, _ = m["__aiGatewayManaged"].(bool)
				}
				addNeed(credType, name, gw)
			}
			continue
		}
		// Stripped export: infer from the node's parameters or its catalog entry.
		if isTrigger {
			continue
		}
		p := params(n)
		if typ == TypeHTTPRequest {
			switch str(p, "authentication") {
			case "genericCredentialType":
				if t := str(p, "genericAuthType"); t != "" {
					addNeed(t, name, false)
				}
			case "predefinedCredentialType":
				if t := str(p, "nodeCredentialType"); t != "" {
					addNeed(t, name, false)
				}
			}
			continue
		}
		if nt, ok := cat.Nodes[typ]; ok {
			for _, c := range nt.Creds {
				if c.Required && !c.Conditional {
					addNeed(c.Name, name, false)
				}
			}
		}
	}

	mocked := map[string]bool{}
	for _, m := range exp.Mocked {
		mocked[m] = true
	}
	types := make([]string, 0, len(needs))
	for t := range needs {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		cn := needs[t]
		if mocked[t] {
			cn.Mocked, cn.Fields, cn.GatewayManaged = true, nil, false
			a.Needs = append(a.Needs, *cn)
			continue
		}
		if ct, ok := cat.Creds[t]; ok && ct.OAuth {
			a.Errors = append(a.Errors, fmt.Sprintf("%s (used by %s) signs in with OAuth, which can't be set up from a pasted key on the judge; use an API-key credential instead", cn.DisplayName, strings.Join(cn.Nodes, ", ")))
			continue
		}
		if _, ok := cat.Creds[t]; !ok {
			a.Errors = append(a.Errors, fmt.Sprintf("credential type %s (used by %s) isn't known to the judge's n8n %s", t, strings.Join(cn.Nodes, ", "), cat.N8nVersion))
			continue
		}
		if cn.GatewayManaged {
			a.Warnings = append(a.Warnings, fmt.Sprintf("%s uses n8n's free AI credits, which only exist on n8n Cloud; enter your own %s key below", strings.Join(cn.Nodes, ", "), cn.DisplayName))
		}
		a.Needs = append(a.Needs, *cn)
	}
	if a.Needs == nil {
		a.Needs = []CredNeed{}
	}
	return a
}

func methodAllowed(nodeMethod, want string) bool {
	// Webhook 2.x can accept several methods ("multipleMethods"); n8n stores them comma-separated.
	for _, m := range strings.Split(nodeMethod, ",") {
		if strings.TrimSpace(m) == want {
			return true
		}
	}
	return false
}

func label(k TriggerKind) string {
	if k == TriggerChat {
		return "Chat Trigger"
	}
	return "Webhook"
}

func fmtVer(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func fmtVers(vs []float64) string {
	s := make([]string, len(vs))
	for i, v := range vs {
		s[i] = fmtVer(v)
	}
	return strings.Join(s, ", ")
}
