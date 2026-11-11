package server

import (
	"path/filepath"
	"testing"
)

func TestSessionExpiresServerSide(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "judge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if err := s.CreateSession("test", 0, true); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.Session("test"); !ok {
		t.Fatal("new session rejected")
	}
	if _, err := s.db.Exec(`UPDATE sessions SET created = ? WHERE token = ?`, now()-24*60*60-1, "test"); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := s.Session("test"); ok {
		t.Fatal("expired session accepted")
	}
}
