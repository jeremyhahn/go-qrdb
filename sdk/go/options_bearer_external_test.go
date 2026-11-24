// Copyright (c) 2025 Jeremy Hahn
// SPDX-License-Identifier: AGPL-3.0-or-later

package sdk_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
)

// connectWithRootOptions starts a TLS server that records the Authorization
// header of every request, connects a client built from root options plus
// opt through rest.New, and returns the recorded values.
func connectWithRootOptions(t *testing.T, opt sdk.ClientOption) []string {
	t.Helper()
	var (
		mu   sync.Mutex
		seen []string
	)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		mu.Lock()
		seen = append(seen, req.Header.Get(transport.AuthorizationHeader))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)

	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	tlsCfg := &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}

	tr, err := rest.New(sdk.TransportOptions(sdk.WithAddress(srv.URL), sdk.WithTLSConfig(tlsCfg), opt)...)
	if err != nil {
		t.Fatalf("rest.New: %v", err)
	}
	c, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	c.Healthy(ctx)

	mu.Lock()
	defer mu.Unlock()
	return append([]string(nil), seen...)
}

func assertEveryBearer(t *testing.T, seen []string, want string) {
	t.Helper()
	if len(seen) == 0 {
		t.Fatal("the server saw no request")
	}
	for i, got := range seen {
		if got != want {
			t.Fatalf("request %d: Authorization = %q, want %q", i, got, want)
		}
	}
}

func TestTransportOptions_BearerReachesServer(t *testing.T) {
	seen := connectWithRootOptions(t, sdk.WithBearerToken("root-option-token"))
	assertEveryBearer(t, seen, "Bearer root-option-token")
}

func TestTransportOptions_TokenSource(t *testing.T) {
	seen := connectWithRootOptions(t, sdk.WithTokenSource(transport.StaticTokenSource("from-source")))
	assertEveryBearer(t, seen, "Bearer from-source")
}
