// Package session encodes the authenticated user identity into an encrypted,
// authenticated cookie (AES-256-GCM). No server-side store: replicas only need
// to share the session key.
package session

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// CookieName is the name of the session cookie.
const CookieName = "stepshell_session"

// Identity is the authenticated principal carried in the session.
type Identity struct {
	User   string    `json:"user"`
	Groups []string  `json:"groups"`
	Email  string    `json:"email,omitempty"`
	Issued time.Time `json:"iat"`
	Expiry time.Time `json:"exp"`
}

// Expired reports whether the identity is past its expiry.
func (id Identity) Expired() bool { return time.Now().After(id.Expiry) }

// Manager seals and opens session cookies and manages the OIDC state cookie.
type Manager struct {
	aead   cipher.AEAD
	ttl    time.Duration
	secure bool
}

// NewManager builds a Manager from a 32-byte key. secure controls the Secure
// cookie attribute (false only for local http development).
func NewManager(key []byte, ttl time.Duration, secure bool) (*Manager, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("session cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("session gcm: %w", err)
	}
	return &Manager{aead: aead, ttl: ttl, secure: secure}, nil
}

// seal encrypts v (JSON) into a base64 token.
func (m *Manager) seal(v any) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, m.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := m.aead.Seal(nonce, nonce, plain, nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// open decrypts a base64 token into v.
func (m *Manager) open(token string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return err
	}
	ns := m.aead.NonceSize()
	if len(raw) < ns {
		return errors.New("token too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	plain, err := m.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(plain, v)
}

// Issue writes a fresh session cookie for id (stamping iat/exp).
func (m *Manager) Issue(w http.ResponseWriter, id Identity) error {
	now := time.Now()
	id.Issued = now
	id.Expiry = now.Add(m.ttl)
	token, err := m.seal(id)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  id.Expiry,
		MaxAge:   int(m.ttl.Seconds()),
	})
	return nil
}

// Read returns the identity from the request's session cookie, or an error if
// absent, malformed, or expired.
func (m *Manager) Read(r *http.Request) (Identity, error) {
	c, err := r.Cookie(CookieName)
	if err != nil {
		return Identity{}, err
	}
	var id Identity
	if err := m.open(c.Value, &id); err != nil {
		return Identity{}, err
	}
	if id.Expired() {
		return Identity{}, errors.New("session expired")
	}
	return id, nil
}

// Clear removes the session cookie.
func (m *Manager) Clear(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// stateCookieName holds the short-lived OIDC state/nonce/PKCE cookie.
const stateCookieName = "stepshell_oidc"

// AuthState is the transient data threaded through the OIDC redirect.
type AuthState struct {
	State        string `json:"s"`
	Nonce        string `json:"n"`
	CodeVerifier string `json:"v"`
	Return       string `json:"r"`
}

// IssueState writes the encrypted OIDC state cookie (5-minute lifetime).
func (m *Manager) IssueState(w http.ResponseWriter, st AuthState) error {
	token, err := m.seal(st)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    token,
		Path:     "/auth",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   300,
	})
	return nil
}

// ReadState reads and clears the OIDC state cookie.
func (m *Manager) ReadState(w http.ResponseWriter, r *http.Request) (AuthState, error) {
	c, err := r.Cookie(stateCookieName)
	if err != nil {
		return AuthState{}, err
	}
	var st AuthState
	if err := m.open(c.Value, &st); err != nil {
		return AuthState{}, err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookieName,
		Value:    "",
		Path:     "/auth",
		HttpOnly: true,
		Secure:   m.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
	return st, nil
}
