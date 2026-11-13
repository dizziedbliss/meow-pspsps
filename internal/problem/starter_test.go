package problem

import (
	"encoding/json"
	"testing"

	"github.com/dizziedbliss/meow-pspsps/internal/workflow"
)

func TestStarterMatchesEntries(t *testing.T) {
	p := &Problem{ID: "x", Title: "X", Entries: []workflow.Entry{
		{Name: "report", Path: "emergency-report", Method: "POST"},
		{Name: "status", Path: "/Emergency-Status/", Method: "POST"},
	}}
	b, err := p.Starter()
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) {
		t.Fatal("not valid JSON")
	}
	w, err := workflow.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	en, errs := w.MapEntries(p.Entries)
	if len(errs) != 0 || len(en) != 2 {
		t.Fatalf("entries=%v errs=%v", en, errs)
	}
	if _, err := (&Problem{ID: "y"}).Starter(); err == nil {
		t.Fatal("a problem without entries should not produce a starter")
	}
}
