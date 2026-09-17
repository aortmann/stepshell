package config

import "testing"

func noEnv(string) string { return "" }

func TestLoadRequiresBaseURL(t *testing.T) {
	if _, err := Load([]string{"--auth.mode=none"}, noEnv); err == nil {
		t.Error("expected error when --base-url missing")
	}
}

func TestOIDCModeRequiresIssuerAndClient(t *testing.T) {
	_, err := Load([]string{"--base-url=https://s.example.com", "--auth.mode=oidc"}, noEnv)
	if err == nil {
		t.Error("expected oidc mode to require issuer and client-id")
	}
}

func TestNoneModeRejectedOnRemoteURL(t *testing.T) {
	_, err := Load([]string{"--base-url=https://s.example.com", "--auth.mode=none"}, noEnv)
	if err == nil {
		t.Error("auth mode none must be rejected on a non-localhost URL by default")
	}
}

func TestNoneModeAllowedOnLocalhost(t *testing.T) {
	c, err := Load([]string{"--base-url=http://localhost:8080", "--auth.mode=none"}, noEnv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.DevUser == "" {
		t.Error("dev user should default when unset")
	}
}

func TestEnvFallback(t *testing.T) {
	env := func(k string) string {
		switch k {
		case "STEPSHELL_OIDC_CLIENT_SECRET":
			return "s3cr3t"
		case "STEPSHELL_OIDC_ISSUER":
			return "https://issuer.example.com"
		case "STEPSHELL_OIDC_CLIENT_ID":
			return "stepshell"
		}
		return ""
	}
	c, err := Load([]string{"--base-url=https://s.example.com"}, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.OIDC.ClientSecret != "s3cr3t" || c.OIDC.Issuer != "https://issuer.example.com" {
		t.Errorf("env fallback did not populate OIDC config: %+v", c.OIDC)
	}
}

func TestExplicitFlagBeatsEnv(t *testing.T) {
	env := func(k string) string {
		if k == "STEPSHELL_LISTEN" {
			return ":9999"
		}
		return ""
	}
	c, err := Load([]string{"--base-url=http://localhost:8080", "--auth.mode=none", "--listen=:7777"}, env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Listen != ":7777" {
		t.Errorf("explicit flag should win over env: got %q", c.Listen)
	}
}

func TestBaseURLTrailingSlashTrimmed(t *testing.T) {
	c, err := Load([]string{"--base-url=http://localhost:8080/", "--auth.mode=none"}, noEnv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.RedirectURL() != "http://localhost:8080/auth/callback" {
		t.Errorf("RedirectURL = %q", c.RedirectURL())
	}
}
