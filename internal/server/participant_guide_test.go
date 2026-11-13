package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParticipantGuideIsServedWithoutLogin(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/static/participant-guide.pdf", nil)
	(&Server{}).Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), "%PDF-") {
		t.Fatalf("guide not served: status %d", w.Code)
	}
}
