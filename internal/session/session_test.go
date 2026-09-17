package session

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	key := make([]byte, 32)
	m, err := NewManager(key, time.Hour, true)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func TestSessionRoundTrip(t *testing.T) {
	m := newTestManager(t)
	rec := httptest.NewRecorder()
	want := Identity{User: "alice@example.com", Groups: []string{"dev", "sre"}, Email: "alice@example.com"}
	if err := m.Issue(rec, want); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	cookie := rec.Result().Cookies()[0]
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie attributes wrong: %+v", cookie)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)
	got, err := m.Read(req)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.User != want.User || len(got.Groups) != 2 {
		t.Errorf("round trip mismatch: got %+v", got)
	}
}

func TestExpiredSessionRejected(t *testing.T) {
	key := make([]byte, 32)
	m, _ := NewManager(key, -time.Hour, true) // already expired on issue
	rec := httptest.NewRecorder()
	_ = m.Issue(rec, Identity{User: "bob"})
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(rec.Result().Cookies()[0])
	if _, err := m.Read(req); err == nil {
		t.Error("expected expired session to be rejected")
	}
}

func TestTamperedCookieRejected(t *testing.T) {
	m := newTestManager(t)
	rec := httptest.NewRecorder()
	_ = m.Issue(rec, Identity{User: "carol"})
	c := rec.Result().Cookies()[0]
	c.Value = c.Value[:len(c.Value)-2] + "xx" // flip trailing bytes

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(c)
	if _, err := m.Read(req); err == nil {
		t.Error("expected tampered cookie to fail authentication")
	}
}

func TestWrongKeyRejected(t *testing.T) {
	k1 := make([]byte, 32)
	k2 := make([]byte, 32)
	k2[0] = 1
	m1, _ := NewManager(k1, time.Hour, true)
	m2, _ := NewManager(k2, time.Hour, true)

	rec := httptest.NewRecorder()
	_ = m1.Issue(rec, Identity{User: "dave"})
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(rec.Result().Cookies()[0])
	if _, err := m2.Read(req); err == nil {
		t.Error("cookie sealed with one key must not open with another")
	}
}
