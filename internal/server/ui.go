package server

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/strikesecurity/stepshell/web"
)

// uiHandler serves the embedded SPA. Real asset paths are served with a strong
// Content-Type; everything else falls back to index.html so client-side routes
// (/wf/..., /shell/...) resolve. index.html is never long-cached; hashed
// assets under /assets/ are.
func (s *Server) uiHandler() http.Handler {
	dist, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		// web/dist missing at build time: serve a helpful message.
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "UI assets not built; run `make ui`", http.StatusInternalServerError)
		})
	}
	fileServer := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.securityHeaders(w)

		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean == "" {
			serveIndex(w, r, dist)
			return
		}
		if f, err := dist.Open(clean); err == nil {
			f.Close()
			if strings.HasPrefix(clean, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		// Unknown path that is not an API/asset: SPA fallback.
		serveIndex(w, r, dist)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, dist fs.FS) {
	w.Header().Set("Cache-Control", "no-cache")
	b, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		http.Error(w, "index.html missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

// securityHeaders sets a conservative CSP and the usual hardening headers. The
// CSP allows inline styles (xterm needs them) but no inline scripts.
func (s *Server) securityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "same-origin")
}
