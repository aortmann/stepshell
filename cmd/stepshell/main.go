// Command stepshell is an authenticated web shell for Kubernetes pods with
// Argo Workflows debug-pause awareness.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/strikesecurity/stepshell/internal/auth"
	"github.com/strikesecurity/stepshell/internal/config"
	"github.com/strikesecurity/stepshell/internal/kube"
	"github.com/strikesecurity/stepshell/internal/server"
	"github.com/strikesecurity/stepshell/internal/session"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(os.Args[1:], os.Getenv)
	if err != nil {
		return err
	}

	log := newLogger(cfg.LogLevel)
	slog.SetDefault(log)

	secure := strings.HasPrefix(cfg.BaseURL, "https://")
	sm, err := session.NewManager(cfg.Session.Key, cfg.Session.TTL, secure)
	if err != nil {
		return err
	}

	factory, err := kube.NewFactory(cfg)
	if err != nil {
		return err
	}

	ctx := context.Background()
	var authn *auth.Authenticator
	if cfg.AuthMode == "oidc" {
		// OIDC discovery can fail transiently at startup: DNS for the public
		// host may not have propagated and, with a bundled Dex behind the same
		// ingress, the provider may not be reachable yet. Retry with backoff
		// instead of crash-looping.
		authn, err = newAuthWithRetry(ctx, cfg, sm, log)
		if err != nil {
			return err
		}
	} else {
		log.Warn("running with auth mode NONE — every request acts as the dev identity", "user", cfg.DevUser)
	}

	srv := server.New(cfg, log, sm, authn, factory)

	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: exec streams are long-lived WebSockets.
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Listen, "baseURL", cfg.BaseURL, "authMode", cfg.AuthMode, "argo", cfg.Argo.Enabled)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return err
	case <-stop:
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}

// newAuthWithRetry builds the OIDC authenticator, retrying discovery with a
// capped backoff so a not-yet-ready issuer (DNS/ingress/Dex still coming up)
// does not crash-loop the pod. It gives up after ~5 minutes.
func newAuthWithRetry(ctx context.Context, cfg *config.Config, sm *session.Manager, log *slog.Logger) (*auth.Authenticator, error) {
	const maxWait = 5 * time.Minute
	deadline := time.Now().Add(maxWait)
	delay := 2 * time.Second
	attempt := 0
	for {
		attempt++
		a, err := auth.New(ctx, cfg, sm)
		if err == nil {
			if attempt > 1 {
				log.Info("oidc discovery succeeded", "attempts", attempt)
			}
			return a, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("oidc discovery failed after %s: %w", maxWait, err)
		}
		log.Warn("oidc discovery not ready, retrying", "attempt", attempt, "retryIn", delay.String(), "err", err.Error())
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if delay < 30*time.Second {
			delay *= 2
		}
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
