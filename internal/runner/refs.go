package runner

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// A test step can use a value an earlier step's response returned, written <<step1.incident_id>>
// (steps count from 1; the path is dotted, with [n] or .n for array items). The worker fills these
// in just before sending a step; the grader fills them in the same way before checking a step.
var refRe = regexp.MustCompile(`<<step(\d+)\.([A-Za-z0-9_.\[\]]+)>>`)

// HasRefs reports whether s uses a step reference.
func HasRefs(s string) bool { return strings.Contains(s, "<<step") }

// ExpandRefs replaces step references in s with values from prior steps' response bodies. A value
// that can't be found becomes an empty string. Values are JSON-escaped when jsonCtx is true (s is
// JSON text).
func ExpandRefs(s string, prior []StepResult, jsonCtx bool) string {
	if jsonCtx { // json.Marshal writes < and > as \u003c and \u003e
		s = strings.NewReplacer(`\u003c`, "<", `\u003e`, ">").Replace(s)
	}
	if !HasRefs(s) {
		return s
	}
	return refRe.ReplaceAllStringFunc(s, func(m string) string {
		g := refRe.FindStringSubmatch(m)
		n, _ := strconv.Atoi(g[1])
		val := ""
		if n >= 1 && n <= len(prior) {
			var doc any
			if json.Unmarshal([]byte(prior[n-1].Body), &doc) == nil {
				if v, ok := walk(doc, g[2]); ok {
					if str, isStr := v.(string); isStr {
						val = str
					} else {
						b, _ := json.Marshal(v)
						val = string(b)
					}
				}
			}
		}
		if jsonCtx {
			b, _ := json.Marshal(val)
			return string(b[1 : len(b)-1])
		}
		return val
	})
}

func walk(doc any, path string) (any, bool) {
	path = strings.ReplaceAll(strings.ReplaceAll(path, "[", "."), "]", "")
	cur := doc
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			continue
		}
		switch x := cur.(type) {
		case map[string]any:
			v, ok := x[part]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(x) {
				return nil, false
			}
			cur = x[i]
		default:
			return nil, false
		}
	}
	return cur, true
}
