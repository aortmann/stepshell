// Package config holds stepshell's runtime configuration and flag/env parsing.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	Listen  string
	BaseURL string

	AuthMode string // "oidc" | "none"

	OIDC        OIDCConfig
	Impersonate ImpersonateConfig
	Session     SessionConfig

	Namespaces []string // fallback when the user cannot list namespaces

	DebugImage         string
	ExecDefaultCommand string

	Argo ArgoConfig

	Kubeconfig string

	DevUser   string
	DevGroups []string

	AuditEvents bool
	LogLevel    string

	AllowInsecureNone bool
}

// OIDCConfig configures the OpenID Connect client.
type OIDCConfig struct {
	Issuer        string
	ClientID      string
	ClientSecret  string
	Scopes        []string
	UsernameClaim string
	GroupsClaim   string
}

// ImpersonateConfig maps OIDC claims to Kubernetes impersonation subjects.
type ImpersonateConfig struct {
	UserPrefix  string
	GroupPrefix string
}

// SessionConfig configures the encrypted session cookie.
type SessionConfig struct {
	Key []byte
	TTL time.Duration
}

// ArgoConfig configures Argo Workflows awareness.
type ArgoConfig struct {
	Enabled  bool
	UIURL    string
	DebugTTL int64
}

// DefaultExecCommand launches bash if present, otherwise sh, with a sane TERM.
const DefaultExecCommand = `export TERM=xterm-256color; command -v bash >/dev/null 2>&1 && exec bash || exec sh`

// Load parses flags and STEPSHELL_* environment variables into a Config and
// validates it. Every flag "--foo.bar" also reads from env "STEPSHELL_FOO_BAR"
// when the flag is not passed explicitly. It stays on the standard library flag
// package so the binary carries no config framework.
func Load(args []string, getenv func(string) string) (*Config, error) {
	fs := flag.NewFlagSet("stepshell", flag.ContinueOnError)
	c := &Config{}

	var (
		scopes        commaList
		nsList        commaList
		devGroups     commaList
		sessionKeyB64 string
		sessionTTL    string
	)

	fs.StringVar(&c.Listen, "listen", ":8080", "address to listen on")
	fs.StringVar(&c.BaseURL, "base-url", "", "public base URL (required)")
	fs.StringVar(&c.AuthMode, "auth.mode", "oidc", "auth mode: oidc | none")
	fs.StringVar(&c.OIDC.Issuer, "oidc.issuer", "", "OIDC issuer URL")
	fs.StringVar(&c.OIDC.ClientID, "oidc.client-id", "", "OIDC client ID")
	fs.StringVar(&c.OIDC.ClientSecret, "oidc.client-secret", "", "OIDC client secret (prefer env)")
	fs.Var(&scopes, "oidc.scopes", "OIDC scopes (comma-separated)")
	fs.StringVar(&c.OIDC.UsernameClaim, "oidc.username-claim", "email", "claim to use as username")
	fs.StringVar(&c.OIDC.GroupsClaim, "oidc.groups-claim", "groups", "claim to use as groups")
	fs.StringVar(&c.Impersonate.UserPrefix, "impersonate.user-prefix", "", "prefix added to impersonated username")
	fs.StringVar(&c.Impersonate.GroupPrefix, "impersonate.group-prefix", "", "prefix added to impersonated groups")
	fs.StringVar(&sessionKeyB64, "session.key", "", "base64 32-byte session key (random if empty)")
	fs.StringVar(&sessionTTL, "session.ttl", "8h", "session lifetime")
	fs.Var(&nsList, "namespaces", "fallback namespaces (comma-separated)")
	fs.StringVar(&c.DebugImage, "debug.image", "nicolaka/netshoot:latest", "ephemeral debug container image")
	fs.StringVar(&c.ExecDefaultCommand, "exec.default-command", DefaultExecCommand, "default shell command")
	fs.BoolVar(&c.Argo.Enabled, "argo.enabled", true, "enable Argo Workflows features")
	fs.StringVar(&c.Argo.UIURL, "argo.ui-url", "", "Argo Workflows UI base URL")
	fs.Int64Var(&c.Argo.DebugTTL, "argo.debug-ttl", 3600, "ttlStrategy seconds for debug re-runs")
	fs.StringVar(&c.Kubeconfig, "kubeconfig", "", "kubeconfig path (empty = in-cluster)")
	fs.StringVar(&c.DevUser, "dev.user", "", "dev identity username (auth mode none)")
	fs.Var(&devGroups, "dev.groups", "dev identity groups (comma-separated)")
	fs.BoolVar(&c.AuditEvents, "audit.events", true, "record a Kubernetes Event per exec")
	fs.StringVar(&c.LogLevel, "log.level", "info", "log level")
	fs.BoolVar(&c.AllowInsecureNone, "auth.allow-insecure-none", false, "allow auth mode none on a non-localhost URL")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	applyEnvDefaults(fs, getenv)

	c.OIDC.Scopes = scopes.orDefault("openid", "profile", "email", "groups")
	c.Namespaces = nsList
	c.DevGroups = devGroups

	ttl, err := time.ParseDuration(sessionTTL)
	if err != nil {
		return nil, fmt.Errorf("--session.ttl: %w", err)
	}
	c.Session.TTL = ttl

	if sessionKeyB64 != "" {
		key, err := base64.StdEncoding.DecodeString(sessionKeyB64)
		if err != nil {
			return nil, fmt.Errorf("session.key: not valid base64: %w", err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("session.key: must decode to 32 bytes, got %d", len(key))
		}
		c.Session.Key = key
	} else {
		c.Session.Key = make([]byte, 32)
		if _, err := rand.Read(c.Session.Key); err != nil {
			return nil, fmt.Errorf("generating session key: %w", err)
		}
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// applyEnvDefaults fills any flag not set on the command line from
// STEPSHELL_<UPPER_SNAKE>, e.g. --oidc.client-secret <- STEPSHELL_OIDC_CLIENT_SECRET.
func applyEnvDefaults(fs *flag.FlagSet, getenv func(string) string) {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	fs.VisitAll(func(f *flag.Flag) {
		if set[f.Name] {
			return
		}
		envName := "STEPSHELL_" + strings.NewReplacer(".", "_", "-", "_").Replace(strings.ToUpper(f.Name))
		if v := getenv(envName); v != "" {
			_ = f.Value.Set(v)
		}
	})
}

func (c *Config) validate() error {
	if c.BaseURL == "" {
		return fmt.Errorf("--base-url is required")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("--base-url must be an absolute URL, got %q", c.BaseURL)
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")

	switch c.AuthMode {
	case "oidc":
		if c.OIDC.Issuer == "" || c.OIDC.ClientID == "" {
			return fmt.Errorf("auth mode oidc requires --oidc.issuer and --oidc.client-id")
		}
	case "none":
		isLocal := strings.Contains(u.Host, "localhost") || strings.HasPrefix(u.Host, "127.0.0.1")
		if !isLocal && !c.AllowInsecureNone {
			return fmt.Errorf("auth mode none is only allowed with a localhost --base-url unless --auth.allow-insecure-none is set")
		}
		if c.DevUser == "" {
			c.DevUser = "dev@stepshell.local"
		}
	default:
		return fmt.Errorf("--auth.mode must be oidc or none, got %q", c.AuthMode)
	}

	if c.Session.TTL <= 0 {
		return fmt.Errorf("--session.ttl must be positive")
	}
	return nil
}

// RedirectURL is the OIDC callback URL derived from BaseURL.
func (c *Config) RedirectURL() string { return c.BaseURL + "/auth/callback" }

// commaList is a flag.Value that parses a comma-separated list.
type commaList []string

func (l *commaList) String() string { return strings.Join(*l, ",") }

func (l *commaList) Set(v string) error {
	*l = nil
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			*l = append(*l, p)
		}
	}
	return nil
}

func (l commaList) orDefault(def ...string) []string {
	if len(l) == 0 {
		return def
	}
	return l
}
