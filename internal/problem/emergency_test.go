package problem

import "testing"

func TestEmergencyPackLoads(t *testing.T) {
	p, err := Load("../../problems/2-emergency")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tests) < 15 || len(p.Entries) != 3 || len(p.Directory) != 7 {
		t.Fatalf("tests=%d entries=%d dir=%d", len(p.Tests), len(p.Entries), len(p.Directory))
	}
	if _, err := p.Render(42, 1); err != nil {
		t.Fatal(err)
	}
}
