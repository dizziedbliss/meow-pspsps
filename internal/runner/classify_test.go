package runner

import (
	"testing"

	"github.com/dizziedbliss/meow-pspsps/internal/mock"
)

func TestModelErrorRegex(t *testing.T) {
	bad := []string{
		"models/gemini-1.5-flash is not found for API version v1beta",
		"The model `gpt-4-32k` does not exist or you do not have access to it",
		"model_not_found",
		"This model does not support tools",
		"The model claude-2 has been deprecated",
	}
	for _, m := range bad {
		if !reModelBad.MatchString(m) {
			t.Errorf("should be a model problem: %q", m)
		}
	}
	ok := []string{"Incorrect API key provided", "429 rate limit exceeded", "Cannot read properties of undefined (reading 'json')"}
	for _, m := range ok {
		if reModelBad.MatchString(m) {
			t.Errorf("should not be a model problem: %q", m)
		}
	}
}

func TestWithTeamHeaders(t *testing.T) {
	seed := &mock.Seed{Sheets: map[string]mock.SeedSpreadsheet{"*": {Sheets: []mock.SeedSheet{{Title: "Incidents", Header: []string{"x"}}}}}}
	out := withTeamHeaders(seed, map[string][]string{"Incidents": {"ignored"}, "Sheet1": {"a", "b"}})
	tabs := out.Sheets["*"].Sheets
	if len(tabs) != 2 || tabs[0].Header[0] != "x" || tabs[1].Title != "Sheet1" || len(tabs[1].Header) != 2 {
		t.Fatalf("tabs = %+v", tabs)
	}
	if len(seed.Sheets["*"].Sheets) != 1 {
		t.Fatal("the original seed must not be changed")
	}
}
