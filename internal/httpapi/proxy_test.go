package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyHeadersTrustBoundary(t *testing.T) {
	tests := []struct {
		name       string
		trusted    bool
		remoteAddr string
		realIP     string
		wantIP     string
		wantProto  string
	}{
		{name: "untrusted headers are stripped", remoteAddr: "192.0.2.10:1234", realIP: "203.0.113.8", wantIP: "192.0.2.10"},
		{name: "trusted proxy supplies client address", trusted: true, remoteAddr: "10.0.0.2:1234", realIP: "203.0.113.8", wantIP: "203.0.113.8", wantProto: "https"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var gotIP, gotProto string
			handler := ProxyHeaders(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				gotIP, gotProto = clientIP(r), r.Header.Get("X-Forwarded-Proto")
			}), test.trusted)
			request := httptest.NewRequest(http.MethodGet, "/", nil)
			request.RemoteAddr = test.remoteAddr
			request.Header.Set("X-Real-IP", test.realIP)
			request.Header.Set("X-Forwarded-For", "198.51.100.9")
			request.Header.Set("X-Forwarded-Proto", "https")
			handler.ServeHTTP(httptest.NewRecorder(), request)
			if gotIP != test.wantIP || gotProto != test.wantProto {
				t.Fatalf("ip=%q proto=%q", gotIP, gotProto)
			}
		})
	}
}
