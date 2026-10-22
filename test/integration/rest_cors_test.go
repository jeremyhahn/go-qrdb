//go:build integration

package integration_test

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestRESTCORS_ConfigBlockReachesREST starts qrdbd with a cors: block and
// sends CORS preflights to its REST listener the way a browser does, with no
// credentials: a listed origin is answered with the CORS headers, an
// unlisted one gets none.
func TestRESTCORS_ConfigBlockReachesREST(t *testing.T) {
	const allowed = "https://app.example.com"
	node := startMultiProtocolNodeWithConfig(t, "",
		"cors:\n  allowed_origins: [\""+allowed+"\"]\n  allowed_headers: [\"Authorization\", \"Content-Type\"]\n  max_age: 600\n")
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: node.ca.Pool, MinVersion: tls.VersionTLS13},
	}}
	t.Cleanup(client.CloseIdleConnections)
	url := fmt.Sprintf("https://127.0.0.1:%d/health", node.restPort)

	for _, tc := range []struct {
		name, origin            string
		wantOrigin, wantHeaders string
		wantMaxAge              string
	}{
		{name: "listed origin", origin: allowed, wantOrigin: allowed, wantHeaders: "Authorization, Content-Type", wantMaxAge: "600"},
		{name: "unlisted origin", origin: "https://evil.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodOptions, url, nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Access-Control-Request-Method", http.MethodGet)
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("preflight: %v", err)
			}
			_ = resp.Body.Close()
			h := resp.Header
			if got := h.Get("Access-Control-Allow-Origin"); got != tc.wantOrigin {
				t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, tc.wantOrigin)
			}
			if got := h.Get("Access-Control-Allow-Headers"); got != tc.wantHeaders {
				t.Errorf("Access-Control-Allow-Headers = %q, want %q", got, tc.wantHeaders)
			}
			if got := h.Get("Access-Control-Max-Age"); got != tc.wantMaxAge {
				t.Errorf("Access-Control-Max-Age = %q, want %q", got, tc.wantMaxAge)
			}
		})
	}
}
