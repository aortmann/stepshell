package auth

import "testing"

// TestSanitizeReturn guards against open redirects: only local absolute paths
// are allowed as the post-login destination.
func TestSanitizeReturn(t *testing.T) {
	cases := map[string]string{
		"":                     "/",
		"/wf/default/foo":      "/wf/default/foo",
		"/shell/ns/pod?c=main": "/shell/ns/pod?c=main",
		"//evil.com":           "/", // protocol-relative
		"https://evil.com":     "/", // absolute external
		"http://evil.com":      "/",
		"javascript:alert(1)":  "/",
		"evil.com":             "/", // no leading slash
	}
	for in, want := range cases {
		if got := sanitizeReturn(in); got != want {
			t.Errorf("sanitizeReturn(%q) = %q, want %q", in, got, want)
		}
	}
}
