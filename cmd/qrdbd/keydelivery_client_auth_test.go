// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// TestDefaultListenerClientAuth_AuthOff_RequestsNodeCert drives a real TLS
// listener configured exactly as runStart configures the shared listener
// with auth.enabled=false and no explicit client-auth mode. The handler
// stands in for the key-delivery receiver's mandatory check: it answers 401
// when the request carries no client certificate. A node presenting its
// cluster certificate must get through (the certificate reaches the
// handler), and a client presenting none must still complete the handshake
// and reach the handler (public requests keep working with auth off), where
// it is refused 401.
func TestDefaultListenerClientAuth_AuthOff_RequestsNodeCert(t *testing.T) {
	bundle, err := generateCATLS("spiffe://cluster.local/node/node-1")
	if err != nil {
		t.Fatalf("generateCATLS: %v", err)
	}
	bundle.TLSConfig.ClientCAs = bundle.ClientCACertPool
	bundle.wireClientTrustPool()
	bundle.SetClientAuthMode(defaultListenerClientAuth(false, bundle))

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "node client certificate required", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, strconv.Itoa(len(r.TLS.PeerCertificates)))
	}))
	srv.TLS = bundle.TLSConfig
	srv.StartTLS()
	t.Cleanup(srv.Close)

	nodeCert, err := tls.X509KeyPair(bundle.CertPEM, bundle.KeyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	post := func(certs []tls.Certificate) int {
		t.Helper()
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:      bundle.CACertPool,
			ServerName:   "localhost",
			Certificates: certs,
			MinVersion:   tls.VersionTLS13,
		}}}
		defer client.CloseIdleConnections()
		resp, err := client.Post(srv.URL+"/v1/cluster/keydelivery", "application/json", nil)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	if got := post([]tls.Certificate{nodeCert}); got != http.StatusOK {
		t.Fatalf("delivery with node certificate: status %d, want 200 (the listener must request a client certificate with auth off)", got)
	}
	if got := post(nil); got != http.StatusUnauthorized {
		t.Fatalf("delivery without certificate: status %d, want 401", got)
	}
}

// TestDefaultListenerClientAuth_Modes pins the mode table.
func TestDefaultListenerClientAuth_Modes(t *testing.T) {
	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS: %v", err)
	}
	for _, authEnabled := range []bool{true, false} {
		mode, pool := defaultListenerClientAuth(authEnabled, bundle)
		if mode != tls.VerifyClientCertIfGiven || pool != bundle.ClientCACertPool {
			t.Errorf("auth=%v: mode %v, want VerifyClientCertIfGiven against the client CA pool", authEnabled, mode)
		}
	}
	bare := &TLSBundle{TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13}}
	if mode, _ := defaultListenerClientAuth(false, bare); mode != tls.NoClientCert {
		t.Errorf("auth off without cluster identity: mode %v, want NoClientCert", mode)
	}
}
