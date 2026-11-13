package problem

import (
	"encoding/json"
	"testing"
)

func TestRenderIsDeterministicAndTyped(t *testing.T) {
	p, err := Load("../../problems/0-dummy")
	if err != nil {
		t.Fatal(err)
	}
	a, err := p.Render(42, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := p.Render(42, 1)
	c, _ := p.Render(43, 1)
	var ra, rb, rc string
	for i := range a {
		if a[i].Def.ID == "random-order" {
			ra, rb, rc = string(a[i].Job.Steps[0].Body), string(b[i].Job.Steps[0].Body), string(c[i].Job.Steps[0].Body)
			var body map[string]any
			if err := json.Unmarshal(a[i].Job.Steps[0].Body, &body); err != nil {
				t.Fatal(err)
			}
			qty := body["items"].([]any)[0].(map[string]any)["qty"]
			if _, ok := qty.(float64); !ok {
				t.Fatalf("qty should be a number, got %T", qty)
			}
		}
	}
	if ra == "" || ra != rb {
		t.Fatal("same seed must give the same test")
	}
	if ra == rc {
		t.Fatal("different seeds should give different values")
	}
}
