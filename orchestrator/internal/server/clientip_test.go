package server

import (
	"net/http/httptest"
	"testing"

	"harness-platform/orchestrator/internal/config"
)

func TestClientIPTrustProxyHeaders(t *testing.T) {
	cases := []struct {
		name       string
		trust      bool
		remoteAddr string
		xRealIP    string
		xff        string
		want       string
	}{
		{
			name:       "trusted uses X-Real-IP over spoofed XFF",
			trust:      true,
			remoteAddr: "127.0.0.1:5555",
			xRealIP:    "203.0.113.7",
			xff:        "1.2.3.4, 203.0.113.7", // leftmost is attacker-controlled
			want:       "203.0.113.7",
		},
		{
			name:       "trusted falls back to rightmost XFF hop",
			trust:      true,
			remoteAddr: "127.0.0.1:5555",
			xff:        "1.2.3.4, 203.0.113.7",
			want:       "203.0.113.7",
		},
		{
			name:       "untrusted ignores client headers, uses RemoteAddr",
			trust:      false,
			remoteAddr: "198.51.100.9:443",
			xRealIP:    "203.0.113.7",
			xff:        "1.2.3.4",
			want:       "198.51.100.9",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: config.Config{TrustProxyHeaders: tc.trust}}
			r := httptest.NewRequest("POST", "/api/login", nil)
			r.RemoteAddr = tc.remoteAddr
			if tc.xRealIP != "" {
				r.Header.Set("X-Real-IP", tc.xRealIP)
			}
			if tc.xff != "" {
				r.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := s.clientIP(r); got != tc.want {
				t.Fatalf("clientIP() = %q, want %q", got, tc.want)
			}
		})
	}
}
