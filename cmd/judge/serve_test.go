package main

import (
	"os"
	"testing"

	"github.com/dizziedbliss/meow-pspsps/internal/grade"
)

func TestJudgeNeverGuessesProvider(t *testing.T) {
	t.Setenv("JUDGE_LLM_CONFIG", "")
	t.Setenv("JUDGE_LLM_KEY", "AIzaSyNotARealKeyJustAPrefixCheck000000000")
	t.Setenv("JUDGE_LLM_KEY_FILE", "")
	t.Setenv("JUDGE_LLM_BASE", "")
	t.Setenv("JUDGE_LLM_MODEL", "")
	if judgeFromEnv() != nil {
		t.Fatal("a key without an explicit base URL and model must not configure a provider")
	}
	t.Setenv("JUDGE_LLM_BASE", "https://example.invalid/v1")
	if judgeFromEnv() != nil {
		t.Fatal("a missing model must not be defaulted")
	}
	t.Setenv("JUDGE_LLM_MODEL", "some-model")
	if judgeFromEnv() == nil {
		t.Fatal("explicit base and model should configure the judge")
	}
}

func TestSplitTeamLine(t *testing.T) {
	cases := []struct{ in, code, name string }{
		{"AA07\tGuardians of git", "AA07", "Guardians of git"},
		{"AA35    Control X [2]", "AA35", "Control X [2]"},
		{"Plain Team Name", "", "Plain Team Name"},
		{"CLAUD'S PLAN", "", "CLAUD'S PLAN"},
	}
	for _, c := range cases {
		if code, name := splitTeamLine(c.in); code != c.code || name != c.name {
			t.Errorf("%q → %q, %q", c.in, code, name)
		}
	}
}

func TestKeysFileExpandsToOneProviderPerKey(t *testing.T) {
	dir := t.TempDir()
	keys := dir + "/keys.txt"
	if err := os.WriteFile(keys, []byte("# my keys\nkey-one\n\n  key-two  \nkey-three\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := dir + "/grader.json"
	body := `[{"name":"gemini","base":"https://example.invalid/v1","model":"m","keys_file":"` + keys + `","gap_ms":1}]`
	if err := os.WriteFile(cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JUDGE_LLM_CONFIG", cfg)
	j, ok := judgeFromEnv().(*grade.Chain)
	if !ok || len(j.Providers) != 3 {
		t.Fatalf("want a chain of 3 providers, got %#v", j)
	}
	if j.Providers[1].APIKey != "key-two" || j.Providers[2].Name != "gemini-3" {
		t.Fatalf("keys not read in order: %+v", j.Providers[1])
	}
}
