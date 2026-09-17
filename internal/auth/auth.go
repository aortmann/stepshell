// Package auth implements the OIDC authorization-code (with PKCE) login flow
// and turns ID-token claims into a session.Identity.
package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/strikesecurity/stepshell/internal/config"
	"github.com/strikesecurity/stepshell/internal/session"
)

// Authenticator drives the OIDC flow.
type Authenticator struct {
	cfg      *config.Config
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config
	sessions *session.Manager
}

// New builds an Authenticator (dialing the OIDC discovery endpoint).
func New(ctx context.Context, cfg *config.Config, sm *session.Manager) (*Authenticator, error) {
	provider, err := oidc.NewProvider(ctx, cfg.OIDC.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.OIDC.ClientID,
		ClientSecret: cfg.OIDC.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  cfg.RedirectURL(),
		Scopes:       cfg.OIDC.Scopes,
	}
	return &Authenticator{
		cfg:      cfg,
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.OIDC.ClientID}),
		oauth:    oauthCfg,
		sessions: sm,
	}, nil
}

// Login starts the flow: it stores state/nonce/PKCE in a cookie and redirects
// to the IdP.
func (a *Authenticator) Login(w http.ResponseWriter, r *http.Request) {
	rd := sanitizeReturn(r.URL.Query().Get("rd"))
	st := session.AuthState{
		State:        randToken(),
		Nonce:        randToken(),
		CodeVerifier: oauth2.GenerateVerifier(),
		Return:       rd,
	}
	if err := a.sessions.IssueState(w, st); err != nil {
		http.Error(w, "failed to start login", http.StatusInternalServerError)
		return
	}
	url := a.oauth.AuthCodeURL(st.State,
		oidc.Nonce(st.Nonce),
		oauth2.S256ChallengeOption(st.CodeVerifier),
	)
	http.Redirect(w, r, url, http.StatusFound)
}

// Callback completes the flow: it validates state, exchanges the code, verifies
// the ID token and nonce, extracts the identity and issues a session.
func (a *Authenticator) Callback(w http.ResponseWriter, r *http.Request) {
	st, err := a.sessions.ReadState(w, r)
	if err != nil {
		http.Error(w, "login session expired, please retry", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("state") != st.State {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		return
	}
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		http.Error(w, "identity provider error: "+errMsg, http.StatusUnauthorized)
		return
	}

	ctx := r.Context()
	tok, err := a.oauth.Exchange(ctx, r.URL.Query().Get("code"),
		oauth2.VerifierOption(st.CodeVerifier),
	)
	if err != nil {
		http.Error(w, "token exchange failed", http.StatusUnauthorized)
		return
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok {
		http.Error(w, "no id_token in response", http.StatusUnauthorized)
		return
	}
	idToken, err := a.verifier.Verify(ctx, rawID)
	if err != nil {
		http.Error(w, "id_token verification failed", http.StatusUnauthorized)
		return
	}
	if idToken.Nonce != st.Nonce {
		http.Error(w, "nonce mismatch", http.StatusUnauthorized)
		return
	}

	id, err := a.identityFromClaims(idToken)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	if err := a.sessions.Issue(w, id); err != nil {
		http.Error(w, "failed to create session", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, st.Return, http.StatusFound)
}

// Logout clears the session.
func (a *Authenticator) Logout(w http.ResponseWriter, r *http.Request) {
	a.sessions.Clear(w)
	w.WriteHeader(http.StatusNoContent)
}

// identityFromClaims maps configured claims to a session.Identity.
func (a *Authenticator) identityFromClaims(idToken *oidc.IDToken) (session.Identity, error) {
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return session.Identity{}, fmt.Errorf("parsing claims: %w", err)
	}

	user, _ := claims[a.cfg.OIDC.UsernameClaim].(string)
	if user == "" {
		return session.Identity{}, fmt.Errorf("username claim %q is empty", a.cfg.OIDC.UsernameClaim)
	}
	email, _ := claims["email"].(string)
	groups := extractStringSlice(claims[a.cfg.OIDC.GroupsClaim])

	return session.Identity{User: user, Groups: groups, Email: email}, nil
}

func extractStringSlice(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return t
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case json.RawMessage:
		var s []string
		_ = json.Unmarshal(t, &s)
		return s
	}
	return nil
}

// sanitizeReturn ensures the post-login redirect is a local absolute path,
// preventing open redirects. It rejects protocol-relative ("//host") targets.
func sanitizeReturn(rd string) string {
	if rd == "" || !strings.HasPrefix(rd, "/") || strings.HasPrefix(rd, "//") {
		return "/"
	}
	return rd
}

func randToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
