package workflow

import "strings"

const typeGoogleSheets = "n8n-nodes-base.googleSheets"

// SheetHeaders finds, per tab name, the columns a team's own Google Sheets write nodes expect
// (their "Map each column manually" schema). A real sheet built for that workflow would already have
// those headers, so the judge seeds its blank fake sheet with them; without that, n8n refuses the
// append with "Column names were updated after the node's setup".
func (w *Workflow) SheetHeaders() map[string][]string {
	out := map[string][]string{}
	add := func(tab, col string) {
		for _, c := range out[tab] {
			if c == col {
				return
			}
		}
		out[tab] = append(out[tab], col)
	}
	for _, n := range w.Nodes {
		if disabled(n) || str(n, "type") != typeGoogleSheets {
			continue
		}
		p := params(n)
		switch str(p, "operation") {
		case "append", "appendOrUpdate", "update":
		default:
			continue
		}
		sn, _ := p["sheetName"].(map[string]any)
		if sn == nil || str(sn, "mode") != "name" {
			continue // a tab picked by id or from a list: the judge can't name it
		}
		tab := strings.TrimSpace(str(sn, "value"))
		cols, _ := p["columns"].(map[string]any)
		if tab == "" || cols == nil || str(cols, "mappingMode") != "defineBelow" {
			continue
		}
		schema, _ := cols["schema"].([]any)
		for _, it := range schema {
			m, _ := it.(map[string]any)
			if m == nil {
				continue
			}
			if rem, _ := m["removed"].(bool); rem {
				continue
			}
			if id := str(m, "id"); id != "" {
				add(tab, id)
			}
		}
		if len(schema) == 0 {
			if vals, _ := cols["value"].(map[string]any); vals != nil {
				for k := range vals { // order isn't kept in a Go map; only used when no schema exists
					add(tab, k)
				}
			}
		}
	}
	return out
}
