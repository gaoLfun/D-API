package httpapi

import "net/http"

// SecurityHeaders applies headers that are safe for both the admin SPA and
// the API. A strict CSP is intentionally left to the reverse proxy because
// the SPA uses a small number of runtime style attributes.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		if len(r.URL.Path) >= 11 && r.URL.Path[:11] == "/api/admin/" {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
