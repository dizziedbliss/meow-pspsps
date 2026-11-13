package workflow

import (
	"sort"
	"strconv"
	"strings"
)

// TypeCount is one node type and how many nodes use it.
type TypeCount struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// Features describes a workflow for coordinators who judge extras in person. It never affects the score.
type Features struct {
	NodeCount        int                    `json:"nodeCount"`
	Groups           map[string][]TypeCount `json:"groups"`           // triggers, ai, integrations, code, logic
	Extras           []string               `json:"extras"`           // plain-language notes: extra triggers, error handling, docs
	NotInIdeal       []string               `json:"notInIdeal"`       // node types the team used that the ideal doesn't
	MissingFromIdeal []string               `json:"missingFromIdeal"` // node types the ideal uses that the team doesn't
}

var logicTypes = map[string]bool{"if": true, "switch": true, "merge": true, "set": true, "filter": true, "splitInBatches": true,
	"wait": true, "respondToWebhook": true, "noOp": true, "aggregate": true, "sort": true, "limit": true, "removeDuplicates": true,
	"itemLists": true, "dateTime": true, "stopAndError": true, "splitOut": true, "executeWorkflow": true, "summarize": true,
	"editFields": true, "compareDatasets": true, "convertToFile": true, "extractFromFile": true, "html": true, "markdown": true, "crypto": true}

func group(t string) string {
	short := t[strings.LastIndex(t, ".")+1:]
	switch {
	case isTriggerType(t):
		return "triggers"
	case strings.HasPrefix(t, "@n8n/n8n-nodes-langchain."):
		return "ai"
	case short == "code" || short == "function" || short == "functionItem":
		return "code"
	case logicTypes[short]:
		return "logic"
	}
	return "integrations"
}

// IdealTypes returns the distinct node types of an ideal workflow, for the diff.
func IdealTypes(raw []byte) []string {
	w, err := Parse(raw)
	if err != nil {
		return nil
	}
	return w.NodeTypes()
}

// Describe builds the feature report. ideal may be nil.
func (w *Workflow) Describe(entries []EntryNode, ideal []string) Features {
	f := Features{Groups: map[string][]TypeCount{}, Extras: []string{}, NotInIdeal: []string{}, MissingFromIdeal: []string{}}
	counts := map[string]map[string]int{}
	stickies, errorHandling := 0, 0
	have := map[string]bool{}
	for _, n := range w.Nodes {
		t := str(n, "type")
		if t == "n8n-nodes-base.stickyNote" {
			stickies++
			continue
		}
		if disabled(n) {
			continue
		}
		f.NodeCount++
		have[t] = true
		g := group(t)
		if counts[g] == nil {
			counts[g] = map[string]int{}
		}
		counts[g][t]++
		if oe, _ := n["onError"].(string); strings.HasPrefix(oe, "continue") {
			errorHandling++
		}
		if t == "n8n-nodes-base.errorTrigger" || t == "n8n-nodes-base.stopAndError" {
			errorHandling++
		}
	}
	for g, m := range counts {
		for t, c := range m {
			f.Groups[g] = append(f.Groups[g], TypeCount{t, c})
		}
		sort.Slice(f.Groups[g], func(i, j int) bool { return f.Groups[g][i].Type < f.Groups[g][j].Type })
	}
	if extra := w.OtherTriggers(entries); len(extra) > 0 {
		f.Extras = append(f.Extras, "extra triggers (switched off during tests): "+strings.Join(extra, ", "))
	}
	if errorHandling > 0 {
		f.Extras = append(f.Extras, "error handling in "+strconv.Itoa(errorHandling)+" node(s)")
	}
	if stickies > 0 {
		f.Extras = append(f.Extras, strconv.Itoa(stickies)+" sticky note(s) of documentation")
	}
	idealSet := map[string]bool{}
	for _, t := range ideal {
		idealSet[t] = true
		if !have[t] {
			f.MissingFromIdeal = append(f.MissingFromIdeal, t)
		}
	}
	for t := range have {
		if len(ideal) > 0 && !idealSet[t] {
			f.NotInIdeal = append(f.NotInIdeal, t)
		}
	}
	sort.Strings(f.NotInIdeal)
	sort.Strings(f.MissingFromIdeal)
	return f
}
