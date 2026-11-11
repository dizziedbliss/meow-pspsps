package main

import "testing"

func TestJudgeRequiresExplicitEndpointForUnknownKey(t *testing.T) {
	t.Setenv("JUDGE_LLM_KEY", "test-nonstandard-key")
	t.Setenv("JUDGE_LLM_KEY_FILE", "")
	t.Setenv("JUDGE_LLM_BASE", "")
	if judgeFromEnv() != nil {
		t.Fatal("unknown keys must not be sent to a guessed provider")
	}
	t.Setenv("JUDGE_LLM_BASE", "https://example.invalid/v1")
	if judgeFromEnv() == nil {
		t.Fatal("explicit endpoint should configure judge")
	}
}
