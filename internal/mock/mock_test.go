package mock

import "testing"

func TestAttributionStripped(t *testing.T) {
	in := "Booked for Friday, 6 Nov 2026 🎉\n\n_This message was sent automatically with _[n8n](https://n8n.io/?utm_source=x)"
	if got := attribution.ReplaceAllString(in, ""); got != "Booked for Friday, 6 Nov 2026 🎉" {
		t.Fatalf("got %q", got)
	}
}
