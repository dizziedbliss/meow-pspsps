package grade

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGraderRejectsMissingPassDecision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer srv.Close()
	j, err := NewProvider("test", srv.URL, "test", "test", 0, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := j.Grade(context.Background(), "rule", "input", "answer"); err == nil {
		t.Fatal("incomplete judge response must be infrastructure error, not wrong answer")
	}
}
