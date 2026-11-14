package server

import (
	"crypto/tls"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/config"
)

func TestRestCORS_ConfiguredOriginYieldsPolicy(t *testing.T) {
	cfg := testConfig(t, true, false)
	cfg.CORS = config.CORSConfig{
		AllowedOrigins:   []string{"https://app.example.com"},
		AllowedHeaders:   []string{"Authorization"},
		MaxAge:           600,
		AllowCredentials: true,
	}
	srv := New(cfg, newMockKVService(), &mockAdminService{})
	got, ok := srv.restCORS()
	if !ok {
		t.Fatal("restCORS: want a policy for a configured origin")
	}
	if !slices.Equal(got.AllowedOrigins, cfg.CORS.AllowedOrigins) ||
		!slices.Equal(got.AllowedHeaders, cfg.CORS.AllowedHeaders) || got.MaxAge != 600 ||
		!got.AllowCredentials {
		t.Fatalf("restCORS = %+v, want the config's fields", got)
	}
	// The policy owns its slices: a later edit of the config does not reach it.
	cfg.CORS.AllowedOrigins[0] = "https://other.example.com"
	if got.AllowedOrigins[0] != "https://app.example.com" {
		t.Fatalf("restCORS aliases the config's origins: %v", got.AllowedOrigins)
	}
}

func TestRestCORS_NoOriginNoPolicy(t *testing.T) {
	for name, cfg := range map[string]config.CORSConfig{
		"default":            config.DefaultConfig().CORS,
		"headers, no origin": {AllowedHeaders: []string{"Authorization"}, MaxAge: 60},
	} {
		t.Run(name, func(t *testing.T) {
			c := testConfig(t, true, false)
			c.CORS = cfg
			if got, ok := New(c, newMockKVService(), &mockAdminService{}).restCORS(); ok {
				t.Fatalf("restCORS = %+v, want no policy without an origin", got)
			}
		})
	}
}

// corsPreflight sends a CORS preflight for /health from origin to srv's REST
// listener and returns the response's Access-Control-Allow-Origin.
func corsPreflight(t *testing.T, srv *Server, origin string) string {
	t.Helper()
	hc := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs:    testServerCAPool(t),
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	}}}
	t.Cleanup(hc.CloseIdleConnections)
	req, err := http.NewRequest(http.MethodOptions, "https://"+srv.RESTAddr()+"/health", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	_ = resp.Body.Close()
	return resp.Header.Get("Access-Control-Allow-Origin")
}

func TestStart_CORSConfigReachesREST(t *testing.T) {
	cfg := testConfig(t, true, false)
	cfg.CORS.AllowedOrigins = []string{"https://app.example.com"}
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	if got := corsPreflight(t, srv, "https://app.example.com"); got != "https://app.example.com" {
		t.Fatalf("allowed origin: Access-Control-Allow-Origin = %q, want the origin", got)
	}
	if got := corsPreflight(t, srv, "https://evil.example.com"); got != "" {
		t.Fatalf("unlisted origin: Access-Control-Allow-Origin = %q, want none", got)
	}
}

func TestStart_DefaultConfigSendsNoCORS(t *testing.T) {
	srv := New(testConfig(t, true, false), newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	if got := corsPreflight(t, srv, "https://app.example.com"); got != "" {
		t.Fatalf("default config: Access-Control-Allow-Origin = %q, want none", got)
	}
}
