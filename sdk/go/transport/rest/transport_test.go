// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newTestServer creates an httptest.Server that simulates the QRDB REST API.
// It returns the server and a cleanup function.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	kvStore := make(map[string][]byte)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Health endpoint.
		if r.URL.Path == "/health" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{
				Status:   "ok",
				IsLeader: true,
			})
			return
		}

		// Status endpoint.
		if r.URL.Path == "/status" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.StatusResponse{
				NodeID:      "test-node",
				RaftAddress: "127.0.0.1:63001",
				IsLeader:    true,
				LeaderID:    1,
				ShardCount:  1,
			})
			return
		}

		// Peers endpoint.
		if r.URL.Path == "/peers" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.PeersResponse{
				Shards: []transport.ShardPeers{
					{
						ShardID:  0,
						LeaderID: 1,
						Peers: []transport.PeerInfo{
							{ReplicaID: 1, Address: "127.0.0.1:63001", IsLeader: true},
						},
					},
				},
			})
			return
		}

		// Capabilities endpoint.
		if r.URL.Path == "/capabilities" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.CapabilitiesResponse{
				KV:          true,
				MVCC:        true,
				Transaction: true,
				Batch:       true,
				Watch:       true,
				Lease:       true,
				Index:       true,
			})
			return
		}

		// Drain endpoint.
		if r.URL.Path == "/drain" && r.Method == http.MethodPost {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.DrainResponse{Status: "drained"})
			return
		}

		// Membership add endpoint.
		if r.URL.Path == "/membership/add" && r.Method == http.MethodPost {
			var req transport.AddMemberRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.OKResponse{Status: "ok"})
			return
		}

		// Membership remove endpoint.
		if r.URL.Path == "/membership/remove" && r.Method == http.MethodPost {
			var req transport.RemoveMemberRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.OKResponse{Status: "ok"})
			return
		}

		// Snapshot endpoint.
		if r.URL.Path == "/snapshot" && r.Method == http.MethodPost {
			var req transport.SnapshotRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.OKResponse{Status: "ok"})
			return
		}

		// Cluster join-token mint endpoint.
		if r.URL.Path == "/v1/cluster/join-tokens" && r.Method == http.MethodPost {
			var req transport.MintJoinTokenRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(transport.MintJoinTokenResponse{
				Token:     "test-token",
				ExpiresAt: time.Now().Add(time.Hour),
			})
			return
		}

		// KV prefix listing: GET /kv or GET /kv?prefix=X
		if r.URL.Path == "/kv" && r.Method == http.MethodGet {
			prefix := r.URL.Query().Get("prefix")
			var keys []string
			for k := range kvStore {
				if prefix == "" || strings.HasPrefix(k, prefix) {
					keys = append(keys, k)
				}
			}
			if keys == nil {
				keys = []string{}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.KVListResponse{Keys: keys})
			return
		}

		// KV operations: /kv/{key}
		if strings.HasPrefix(r.URL.Path, "/kv/") {
			key := strings.TrimPrefix(r.URL.Path, "/kv/")

			switch r.Method {
			case http.MethodPut:
				body, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				kvStore[key] = body
				w.WriteHeader(http.StatusNoContent)
				return

			case http.MethodGet:
				val, ok := kvStore[key]
				if !ok {
					http.Error(w, "key not found", http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write(val)
				return

			case http.MethodDelete:
				delete(kvStore, key)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		// StartShard endpoint: POST /admin/shards
		if r.URL.Path == "/admin/shards" && r.Method == http.MethodPost {
			var req transport.StartShardRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.OKResponse{Status: "ok"})
			return
		}

		// StopShard endpoint: DELETE /admin/shards/{shardID}
		if strings.HasPrefix(r.URL.Path, "/admin/shards/") && !strings.HasSuffix(r.URL.Path, "/state") && r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// GetShardState endpoint: GET /admin/shards/{shardID}/state
		if strings.HasSuffix(r.URL.Path, "/state") && strings.HasPrefix(r.URL.Path, "/admin/shards/") && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.ShardStateResponse{
				ShardID:   1,
				LeaderID:  1,
				Term:      5,
				HasLeader: true,
			})
			return
		}

		// GetTopology endpoint: GET /admin/topology
		if r.URL.Path == "/admin/topology" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.TopologyResponse{
				NodeID:      "test-node",
				RaftAddress: "127.0.0.1:63001",
				ShardCount:  1,
				Shards: []transport.ShardInfo{
					{
						ShardID:   1,
						ReplicaID: 1,
						IsLeader:  true,
						LeaderID:  1,
						Nodes:     map[uint64]string{1: "127.0.0.1:63001"},
					},
				},
			})
			return
		}

		http.NotFound(w, r)
	})

	return httptest.NewServer(mux)
}

// newTestTransport creates a REST transport connected to the test server.
func newTestTransport(t *testing.T, serverURL string) *Transport {
	t.Helper()

	rt, err := New(transport.WithAddress(serverURL))
	if err != nil {
		t.Fatalf("failed to create REST transport: %v", err)
	}

	if err := rt.Connect(context.Background()); err != nil {
		t.Fatalf("failed to connect: %v", err)
	}

	return rt
}

// newWiredTransport creates a Transport wired directly to the given httptest.Server
// without invoking Connect (bypassing the health check). This is useful for
// testing error paths where the server intentionally returns bad responses.
func newWiredTransport(srv *httptest.Server) *Transport {
	rt := &Transport{
		config:  &transport.Config{Address: srv.URL},
		baseURL: srv.URL,
		client:  srv.Client(),
		// baseRT is what Connect sets and what scopedRoundTripper wraps, so a
		// transport wired with a client but no baseRT panics the moment
		// WithDatabase clones it — a nil RoundTripper inside a live
		// http.Client. Setting it here keeps this helper's transports
		// interchangeable with connected ones (go-qrdb#126).
		baseRT: srv.Client().Transport,
	}
	return rt
}

// generateSelfSignedCert writes a self-signed ECDSA P-256 certificate and
// private key in PEM format to the given directory. It returns the paths to
// the cert file, key file, and CA (same cert, self-signed).
func generateSelfSignedCert(t *testing.T, dir string) (certFile, keyFile, caFile string) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	caFile = filepath.Join(dir, "ca.pem")

	// Write certificate PEM.
	cf, err := os.Create(certFile)
	if err != nil {
		t.Fatalf("create cert file: %v", err)
	}
	if err := pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		t.Fatalf("encode cert pem: %v", err)
	}
	cf.Close()

	// Write private key PEM.
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal ec key: %v", err)
	}
	kf, err := os.Create(keyFile)
	if err != nil {
		t.Fatalf("create key file: %v", err)
	}
	if err := pem.Encode(kf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		t.Fatalf("encode key pem: %v", err)
	}
	kf.Close()

	// CA file is the same self-signed cert.
	if err := os.WriteFile(caFile, func() []byte {
		var buf strings.Builder
		_ = pem.Encode(io.Writer(&nopWriteCloser{&buf}), &pem.Block{Type: "CERTIFICATE", Bytes: certDER})
		return []byte(buf.String())
	}(), 0o600); err != nil {
		t.Fatalf("write ca file: %v", err)
	}

	return certFile, keyFile, caFile
}

// nopWriteCloser wraps a strings.Builder to satisfy io.Writer via pem.Encode.
type nopWriteCloser struct{ *strings.Builder }

func (n *nopWriteCloser) Write(p []byte) (int, error) { return n.Builder.Write(p) }

// writePEMFile writes a PEM block to a temp file and returns its path.
func writePEMFile(t *testing.T, dir, name string, block *pem.Block) string {
	t.Helper()
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	defer f.Close()
	if err := pem.Encode(f, block); err != nil {
		t.Fatalf("encode %s: %v", name, err)
	}
	return path
}

// ---------------------------------------------------------------------------
// Construction and lifecycle tests
// ---------------------------------------------------------------------------

func TestNew_ValidConfig(t *testing.T) {
	rt, err := New(transport.WithAddress("http://localhost:63002"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt == nil {
		t.Fatal("expected non-nil transport")
	}
}

func TestNew_InvalidConfig(t *testing.T) {
	_, err := New() // no address
	if err == nil {
		t.Fatal("expected error for empty address")
	}
	var cfgErr *transport.ConfigError
	if !errors.As(err, &cfgErr) {
		t.Errorf("expected ConfigError, got %T: %v", err, err)
	}
}

func TestConnect_HealthCheckSuccess(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()
}

func TestConnect_HealthCheckFailure(t *testing.T) {
	// Server that always returns 500 for health.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unhealthy", http.StatusInternalServerError)
	}))
	defer srv.Close()

	rt, err := New(transport.WithAddress(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = rt.Connect(context.Background())
	if err == nil {
		t.Fatal("expected connection error from health check failure")
	}
	var connErr *transport.ConnectionError
	if !errors.As(err, &connErr) {
		t.Errorf("expected ConnectionError, got %T: %v", err, err)
	}
}

// TestConnect_SealedServerIsReachable verifies that a server returning 503
// from /health (a sealed barrier / not-ready node) is still connectable, so
// the CLI can run allowlisted barrier/seal commands (status, unseal,
// shamir/*) over REST. Gating Connect on 200 would lock operators out of a
// sealed node entirely.
func TestConnect_SealedServerIsReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mimic the sealed-window middleware: /health -> 503.
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"barrier sealed","code":"SEALED"}`))
	}))
	defer srv.Close()

	rt, err := New(transport.WithAddress(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer rt.Close()

	if err := rt.Connect(context.Background()); err != nil {
		t.Fatalf("Connect to a sealed (503) server should succeed, got: %v", err)
	}
	// Healthy() must still report false for a sealed server.
	if rt.Healthy(context.Background()) {
		t.Error("Healthy() should be false for a sealed (503) server")
	}
}

func TestConnect_AfterClose(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt, err := New(transport.WithAddress(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	_ = rt.Close()

	err = rt.Connect(context.Background())
	if err == nil {
		t.Fatal("expected error connecting after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestConnect_BuildTLSConfigError(t *testing.T) {
	// TLSEnabled with a nonexistent CA file causes buildTLSConfig to fail,
	// which is propagated from Connect before the health check.
	rt := &Transport{
		config: &transport.Config{
			Address:    "http://127.0.0.1:1",
			TLSEnabled: true,
			TLSCAFile:  "/nonexistent/ca.pem",
		},
		baseURL: "http://127.0.0.1:1",
	}

	err := rt.Connect(context.Background())
	if err == nil {
		t.Fatal("expected TLS error from nonexistent CA file")
	}
	var tlsErr *transport.TLSError
	if !errors.As(err, &tlsErr) {
		t.Errorf("expected TLSError, got %T: %v", err, err)
	}
}

func TestClose_Idempotent(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)

	if err := rt.Close(); err != nil {
		t.Errorf("first close failed: %v", err)
	}
	if err := rt.Close(); err != nil {
		t.Errorf("second close failed: %v", err)
	}
}

func TestHealthy_Connected(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	if !rt.Healthy(context.Background()) {
		t.Error("expected healthy")
	}
}

func TestHealthy_AfterClose(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	if rt.Healthy(context.Background()) {
		t.Error("expected unhealthy after close")
	}
}

func TestHealthy_NilClient(t *testing.T) {
	// Transport with no client (Connect not called) returns false.
	rt := &Transport{
		config:  &transport.Config{Address: "http://127.0.0.1:1"},
		baseURL: "http://127.0.0.1:1",
	}

	if rt.Healthy(context.Background()) {
		t.Error("expected unhealthy when client is nil")
	}
}

// ---------------------------------------------------------------------------
// buildTLSConfig tests
// ---------------------------------------------------------------------------

func TestBuildTLSConfig_TLSDisabled(t *testing.T) {
	rt := &Transport{
		config: &transport.Config{TLSEnabled: false},
	}
	cfg, err := rt.buildTLSConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Error("expected nil config when TLS is disabled")
	}
}

func TestBuildTLSConfig_CustomTLSConfig(t *testing.T) {
	custom := &tls.Config{MinVersion: tls.VersionTLS12}
	rt := &Transport{
		config: &transport.Config{
			TLSEnabled: true,
			TLSConfig:  custom,
		},
	}
	cfg, err := rt.buildTLSConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == custom {
		t.Error("expected a clone of the custom TLSConfig, got the caller's pointer")
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %#x, want %#x", cfg.MinVersion, tls.VersionTLS12)
	}
}

func TestBuildTLSConfig_WithCAFile(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, caFile := generateSelfSignedCert(t, dir)
	_ = certFile
	_ = keyFile

	rt := &Transport{
		config: &transport.Config{
			TLSEnabled: true,
			TLSCAFile:  caFile,
		},
	}
	cfg, err := rt.buildTLSConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil TLS config")
	}
	if cfg.RootCAs == nil {
		t.Error("expected RootCAs to be populated")
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("expected TLS 1.3 minimum, got %v", cfg.MinVersion)
	}
}

func TestBuildTLSConfig_WithMTLS(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile, caFile := generateSelfSignedCert(t, dir)

	rt := &Transport{
		config: &transport.Config{
			TLSEnabled:  true,
			TLSCertFile: certFile,
			TLSKeyFile:  keyFile,
			TLSCAFile:   caFile,
		},
	}
	cfg, err := rt.buildTLSConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil TLS config")
	}
	// The static Certificates field stays empty: the client certificate
	// is carried via GetClientCertificate instead, so it is presented on
	// every handshake regardless of the server's advertised
	// AcceptableCAs (transport.BuildTLSConfig's own doc comment).
	if len(cfg.Certificates) != 0 {
		t.Errorf("expected the static Certificates field to stay empty, got %d entries", len(cfg.Certificates))
	}
	if cfg.GetClientCertificate == nil {
		t.Error("expected GetClientCertificate to be set")
	}
	if cfg.RootCAs == nil {
		t.Error("expected RootCAs to be populated")
	}
}

func TestBuildTLSConfig_NonexistentCAFile(t *testing.T) {
	rt := &Transport{
		config: &transport.Config{
			TLSEnabled: true,
			TLSCAFile:  "/nonexistent/path/ca.pem",
		},
	}
	_, err := rt.buildTLSConfig()
	if err == nil {
		t.Fatal("expected error for nonexistent CA file")
	}
	var tlsErr *transport.TLSError
	if !errors.As(err, &tlsErr) {
		t.Errorf("expected TLSError, got %T: %v", err, err)
	}
}

func TestBuildTLSConfig_InvalidCAFile(t *testing.T) {
	// File exists but contains no valid PEM certificate data.
	dir := t.TempDir()
	caFile := filepath.Join(dir, "bad-ca.pem")
	if err := os.WriteFile(caFile, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("write bad ca file: %v", err)
	}

	rt := &Transport{
		config: &transport.Config{
			TLSEnabled: true,
			TLSCAFile:  caFile,
		},
	}
	_, err := rt.buildTLSConfig()
	if err == nil {
		t.Fatal("expected error for invalid CA PEM content")
	}
	var tlsErr *transport.TLSError
	if !errors.As(err, &tlsErr) {
		t.Errorf("expected TLSError, got %T: %v", err, err)
	}
}

func TestBuildTLSConfig_InvalidCertPair(t *testing.T) {
	dir := t.TempDir()
	_, _, caFile := generateSelfSignedCert(t, dir)

	// Generate a second key whose cert doesn't match the first.
	priv2, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate second key: %v", err)
	}
	keyDER2, err := x509.MarshalECPrivateKey(priv2)
	if err != nil {
		t.Fatalf("marshal second key: %v", err)
	}

	// Write the mismatched key.
	mismatchKeyFile := writePEMFile(t, dir, "mismatch-key.pem",
		&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER2})

	// Read the cert from the first pair (mismatched with the second key).
	certData, err := os.ReadFile(filepath.Join(dir, "cert.pem"))
	if err != nil {
		t.Fatalf("read cert file: %v", err)
	}
	mismatchCertFile := filepath.Join(dir, "mismatch-cert.pem")
	// #nosec G703 -- mismatchCertFile is filepath.Join(t.TempDir(), "mismatch-cert.pem"); the only non-literal component is the temp root the test runtime chose.
	if err := os.WriteFile(mismatchCertFile, certData, 0o600); err != nil {
		t.Fatalf("write mismatch cert file: %v", err)
	}

	rt := &Transport{
		config: &transport.Config{
			TLSEnabled:  true,
			TLSCertFile: mismatchCertFile,
			TLSKeyFile:  mismatchKeyFile,
			TLSCAFile:   caFile,
		},
	}
	_, err = rt.buildTLSConfig()
	if err == nil {
		t.Fatal("expected error for mismatched cert/key pair")
	}
	var tlsErr *transport.TLSError
	if !errors.As(err, &tlsErr) {
		t.Errorf("expected TLSError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// KV operation tests
// ---------------------------------------------------------------------------

func TestPut_Success(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.Put(context.Background(), "test-key", []byte("test-value"))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
}

func TestPut_AfterClose(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	err := rt.Put(context.Background(), "key", []byte("val"))
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestPut_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(transport.ErrorResponse{Error: "write failed"})
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.Put(context.Background(), "key", []byte("val"))
	if err == nil {
		t.Fatal("expected error from server error response")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", reqErr.StatusCode)
	}
}

func TestGet_Success(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	ctx := context.Background()

	if err := rt.Put(ctx, "hello", []byte("world")); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	val, err := rt.Get(ctx, "hello")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(val) != "world" {
		t.Errorf("expected 'world', got %q", string(val))
	}
}

func TestGet_KeyNotFound(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.Get(context.Background(), "nonexistent")
	if err == nil {
		t.Fatal("expected KeyNotFoundError")
	}
	var knf *transport.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Errorf("expected KeyNotFoundError, got %T: %v", err, err)
	}
	if knf.Key != "nonexistent" {
		t.Errorf("expected key 'nonexistent', got %q", knf.Key)
	}
}

func TestGet_AfterClose(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	_, err := rt.Get(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestGet_NonOKServerError(t *testing.T) {
	// Server returns 503 (not 200 and not 404), which triggers decodeErrorResponse.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("service unavailable"))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.Get(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error from 503 response")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", reqErr.StatusCode)
	}
}

func TestDelete_Success(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	ctx := context.Background()

	if err := rt.Put(ctx, "to-delete", []byte("data")); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	if err := rt.Delete(ctx, "to-delete"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err := rt.Get(ctx, "to-delete")
	if err == nil {
		t.Fatal("expected KeyNotFoundError after delete")
	}
	var knf *transport.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Errorf("expected KeyNotFoundError, got %T", err)
	}
}

func TestDelete_AfterClose(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	err := rt.Delete(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestDelete_ServerError(t *testing.T) {
	// Server returns 500 for delete, which triggers decodeErrorResponse
	// through the non-204 branch.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(transport.ErrorResponse{Error: "delete failed"})
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.Delete(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error from server error response")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", reqErr.StatusCode)
	}
}

func TestList_Empty(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	keys, err := rt.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected empty list, got %v", keys)
	}
}

func TestList_WithPrefix(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	ctx := context.Background()

	if err := rt.Put(ctx, "app/config", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := rt.Put(ctx, "app/data", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	if err := rt.Put(ctx, "other/key", []byte("v3")); err != nil {
		t.Fatal(err)
	}

	keys, err := rt.List(ctx, "app/")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 keys with prefix 'app/', got %d: %v", len(keys), keys)
	}
}

func TestList_AfterClose(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	_, err := rt.List(context.Background(), "")
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestList_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("list error"))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.List(context.Background(), "")
	if err == nil {
		t.Fatal("expected error from server error response")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestList_InvalidJSON(t *testing.T) {
	// Server returns 200 but with an invalid JSON body, triggering the
	// JSON decode error path in List.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{invalid json"))
	}))
	defer srv.Close()

	rt := newWiredTransport(srv)

	_, err := rt.List(context.Background(), "")
	if err == nil {
		t.Fatal("expected error from invalid JSON response")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Admin operation tests
// ---------------------------------------------------------------------------

func TestHealth_Success(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	resp, err := rt.Health(context.Background())
	if err != nil {
		t.Fatalf("Health failed: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", resp.Status)
	}
	if !resp.IsLeader {
		t.Error("expected IsLeader true")
	}
}

func TestHealth_AfterClose(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	_, err := rt.Health(context.Background())
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestHealth_InvalidJSON(t *testing.T) {
	// Server returns 200 with invalid JSON for the health endpoint, hitting
	// the JSON decode error path in Health.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{bad json"))
	}))
	defer srv.Close()

	rt := newWiredTransport(srv)

	_, err := rt.Health(context.Background())
	if err == nil {
		t.Fatal("expected error from invalid JSON response")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestStatus_Success(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	resp, err := rt.Status(context.Background())
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if resp.NodeID != "test-node" {
		t.Errorf("expected node_id 'test-node', got %q", resp.NodeID)
	}
	if resp.ShardCount != 1 {
		t.Errorf("expected shard_count 1, got %d", resp.ShardCount)
	}
}

func TestStatus_AfterClose(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	_, err := rt.Status(context.Background())
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestStatus_InvalidJSON(t *testing.T) {
	// Server returns 200 with invalid JSON for the status endpoint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{bad json"))
	}))
	defer srv.Close()

	rt := newWiredTransport(srv)

	_, err := rt.Status(context.Background())
	if err == nil {
		t.Fatal("expected error from invalid JSON response")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestPeers_Success(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	resp, err := rt.Peers(context.Background())
	if err != nil {
		t.Fatalf("Peers failed: %v", err)
	}
	if len(resp.Shards) != 1 {
		t.Fatalf("expected 1 shard, got %d", len(resp.Shards))
	}
	if len(resp.Shards[0].Peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(resp.Shards[0].Peers))
	}
	if resp.Shards[0].Peers[0].Address != "127.0.0.1:63001" {
		t.Errorf("unexpected peer address: %q", resp.Shards[0].Peers[0].Address)
	}
}

func TestPeers_AfterClose(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	_, err := rt.Peers(context.Background())
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestPeers_InvalidJSON(t *testing.T) {
	// Server returns 200 with invalid JSON for the peers endpoint.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{bad json"))
	}))
	defer srv.Close()

	rt := newWiredTransport(srv)

	_, err := rt.Peers(context.Background())
	if err == nil {
		t.Fatal("expected error from invalid JSON response")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestAddMember_Success(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.AddMember(context.Background(), 0, 2, "10.0.0.2:63001", "")
	if err != nil {
		t.Fatalf("AddMember failed: %v", err)
	}
}

func TestAddMember_AfterClose(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	err := rt.AddMember(context.Background(), 0, 2, "10.0.0.2:63001", "")
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestMintJoinToken_Success(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	resp, err := rt.MintJoinToken(context.Background(), &transport.MintJoinTokenRequest{NodeID: "node-1"})
	if err != nil {
		t.Fatalf("MintJoinToken failed: %v", err)
	}
	if resp.Token != "test-token" {
		t.Errorf("Token = %q, want test-token", resp.Token)
	}
	if !resp.ExpiresAt.After(time.Now()) {
		t.Errorf("ExpiresAt = %v, want a future time", resp.ExpiresAt)
	}
}

func TestMintJoinToken_AfterClose(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	_, err := rt.MintJoinToken(context.Background(), &transport.MintJoinTokenRequest{NodeID: "node-1"})
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T", err)
	}
}

func TestMintJoinToken_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(transport.ErrorResponse{Error: "invalid identity"})
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.MintJoinToken(context.Background(), &transport.MintJoinTokenRequest{NodeID: "bad id"})
	if err == nil {
		t.Fatal("expected error from server error response")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestAddMember_ServerError(t *testing.T) {
	// Server returns 500 for membership/add, exercising the postJSON
	// non-200 path.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(transport.ErrorResponse{Error: "membership error"})
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.AddMember(context.Background(), 0, 2, "10.0.0.2:63001", "")
	if err == nil {
		t.Fatal("expected error from server error response")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", reqErr.StatusCode)
	}
}

func TestRemoveMember_Success(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.RemoveMember(context.Background(), 0, 2)
	if err != nil {
		t.Fatalf("RemoveMember failed: %v", err)
	}
}

func TestRemoveMember_AfterClose(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	err := rt.RemoveMember(context.Background(), 0, 2)
	if err == nil {
		t.Fatal("expected error after close")
	}
}

func TestRemoveMember_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("remove failed"))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.RemoveMember(context.Background(), 0, 2)
	if err == nil {
		t.Fatal("expected error from server error response")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestRequestSnapshot_Success(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.RequestSnapshot(context.Background(), 0)
	if err != nil {
		t.Fatalf("RequestSnapshot failed: %v", err)
	}
}

func TestRequestSnapshot_AfterClose(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	err := rt.RequestSnapshot(context.Background(), 0)
	if err == nil {
		t.Fatal("expected error after close")
	}
}

func TestRequestSnapshot_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("snapshot failed"))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.RequestSnapshot(context.Background(), 0)
	if err == nil {
		t.Fatal("expected error from server error response")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestDrain_Success(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain failed: %v", err)
	}
}

func TestDrain_AfterClose(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	err := rt.Drain(context.Background())
	if err == nil {
		t.Fatal("expected error after close")
	}
}

func TestDrain_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		if r.URL.Path == "/drain" {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.DrainResponse{Status: "drain_timeout", Error: "timed out"})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.Drain(context.Background())
	if err == nil {
		t.Fatal("expected error from failed drain")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T", err)
	}
}

func TestDrain_NotDrainedStatus(t *testing.T) {
	// Server returns 200 OK but with status != "drained", hitting the
	// drainFailedError path for the non-drained case.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(transport.DrainResponse{Status: "partial", Error: "some shards busy"})
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.Drain(context.Background())
	if err == nil {
		t.Fatal("expected error when drain status is not 'drained'")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestDrain_InvalidJSON(t *testing.T) {
	// Server returns 200 with invalid JSON, hitting the JSON decode error
	// path in Drain before the status check.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{bad json"))
	}))
	defer srv.Close()

	rt := newWiredTransport(srv)

	err := rt.Drain(context.Background())
	if err == nil {
		t.Fatal("expected error from invalid JSON response")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Generic Request method tests
// ---------------------------------------------------------------------------

func TestRequest_Success(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	var resp transport.OKResponse
	err := rt.Request(context.Background(), "membership/add", &transport.AddMemberRequest{
		ShardID:   0,
		ReplicaID: 3,
		Address:   "10.0.0.3:63001",
	}, &resp)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
}

func TestRequest_AfterClose(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	err := rt.Request(context.Background(), "health", nil, nil)
	if err == nil {
		t.Fatal("expected error after close")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Errorf("expected ClosedError, got %T: %v", err, err)
	}
}

func TestRequest_ServerError(t *testing.T) {
	// Server returns 400 for unknown method, exercising the >= 400 branch
	// in Request.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(transport.ErrorResponse{Error: "unknown method"})
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.Request(context.Background(), "unknown/method", nil, nil)
	if err == nil {
		t.Fatal("expected error from server error response")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.StatusCode != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", reqErr.StatusCode)
	}
}

func TestRequest_InvalidJSONResponse(t *testing.T) {
	// Server returns 200 with invalid JSON, hitting the JSON decode error
	// in the resp != nil branch of Request.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{bad json"))
	}))
	defer srv.Close()

	rt := newWiredTransport(srv)

	var resp transport.OKResponse
	err := rt.Request(context.Background(), "some/method", nil, &resp)
	if err == nil {
		t.Fatal("expected error from invalid JSON response")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestRequest_NilResponse(t *testing.T) {
	// When resp is nil, a 200 OK with any body should succeed (no decode).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("irrelevant body"))
	}))
	defer srv.Close()

	rt := newWiredTransport(srv)

	err := rt.Request(context.Background(), "some/method", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error for nil response with 200 OK: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Server error handling tests
// ---------------------------------------------------------------------------

func TestGet_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(transport.ErrorResponse{Error: "internal failure"})
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.Get(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error from server error")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T", err)
	}
	if reqErr.StatusCode != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", reqErr.StatusCode)
	}
}

func TestStatus_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal server error"))
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.Status(context.Background())
	if err == nil {
		t.Fatal("expected error from status endpoint")
	}
}

func TestPeers_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.Peers(context.Background())
	if err == nil {
		t.Fatal("expected error from peers endpoint")
	}
}

// ---------------------------------------------------------------------------
// decodeErrorResponse: empty body path
// ---------------------------------------------------------------------------

func TestDecodeErrorResponse_EmptyBody(t *testing.T) {
	// Server returns a non-2xx status with an empty body. decodeErrorResponse
	// should fall back to the HTTP status text.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		// Write nothing — empty body.
	}))
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.Status(context.Background())
	if err == nil {
		t.Fatal("expected error from 503 with empty body")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
	if reqErr.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", reqErr.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Error type tests
// ---------------------------------------------------------------------------

func TestErrors_Messages(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		contains string
	}{
		{"healthCheck", &healthCheckError{}, "health check failed"},
		{"server", &serverError{Message: "boom"}, "boom"},
		{"jsonEncode", &jsonEncodeError{Err: errors.New("bad")}, "json encode failed: bad"},
		{"jsonDecode", &jsonDecodeError{Err: errors.New("bad")}, "json decode failed: bad"},
		{"readBody", &readBodyError{Err: errors.New("eof")}, "read body failed: eof"},
		{"drainFailed", &drainFailedError{Status: "timeout", Detail: "5s"}, "drain failed with status timeout: 5s"},
		{"drainFailedNoDetail", &drainFailedError{Status: "timeout"}, "drain failed with status timeout"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(tt.err.Error(), tt.contains) {
				t.Errorf("expected error to contain %q, got %q", tt.contains, tt.err.Error())
			}
		})
	}
}

func TestErrors_Unwrap(t *testing.T) {
	inner := errors.New("inner")

	tests := []struct {
		name string
		err  error
	}{
		{"jsonEncode", &jsonEncodeError{Err: inner}},
		{"jsonDecode", &jsonDecodeError{Err: inner}},
		{"readBody", &readBodyError{Err: inner}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !errors.Is(tt.err, inner) {
				t.Error("expected Unwrap to return inner error")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// KV roundtrip integration test
// ---------------------------------------------------------------------------

func TestKV_PutGetDeleteRoundtrip(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	ctx := context.Background()

	// Put multiple keys.
	keys := map[string]string{
		"key1": "value1",
		"key2": "value2",
		"key3": "value3",
	}
	for k, v := range keys {
		if err := rt.Put(ctx, k, []byte(v)); err != nil {
			t.Fatalf("Put(%s) failed: %v", k, err)
		}
	}

	// Get and verify all keys.
	for k, expected := range keys {
		val, err := rt.Get(ctx, k)
		if err != nil {
			t.Fatalf("Get(%s) failed: %v", k, err)
		}
		if string(val) != expected {
			t.Errorf("Get(%s) = %q, expected %q", k, string(val), expected)
		}
	}

	// List all keys.
	allKeys, err := rt.List(ctx, "")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(allKeys) != 3 {
		t.Errorf("expected 3 keys, got %d", len(allKeys))
	}

	// Delete one key.
	if err := rt.Delete(ctx, "key2"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify deleted.
	_, err = rt.Get(ctx, "key2")
	if err == nil {
		t.Fatal("expected KeyNotFoundError after delete")
	}
	var knf *transport.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Errorf("expected KeyNotFoundError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// NewRequestWithContext error paths (invalid baseURL containing null byte)
// ---------------------------------------------------------------------------

// newInvalidURLTransport creates a Transport whose baseURL contains a null
// byte, causing http.NewRequestWithContext to fail for any method that
// constructs a request URL from baseURL. The client field is set to a
// no-op client so the closed/nil checks pass.
func newInvalidURLTransport() *Transport {
	return &Transport{
		config:  &transport.Config{Address: "http://invalid"},
		baseURL: "http://host\x00", // null byte causes url.Parse to fail
		client:  &http.Client{},
	}
}

func TestPut_InvalidURL(t *testing.T) {
	rt := newInvalidURLTransport()
	err := rt.Put(context.Background(), "key", []byte("val"))
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestGet_InvalidURL(t *testing.T) {
	rt := newInvalidURLTransport()
	_, err := rt.Get(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestDelete_InvalidURL(t *testing.T) {
	rt := newInvalidURLTransport()
	err := rt.Delete(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestList_InvalidURL(t *testing.T) {
	rt := newInvalidURLTransport()
	_, err := rt.List(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestHealth_InvalidURL(t *testing.T) {
	rt := newInvalidURLTransport()
	_, err := rt.Health(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestStatus_InvalidURL(t *testing.T) {
	rt := newInvalidURLTransport()
	_, err := rt.Status(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestPeers_InvalidURL(t *testing.T) {
	rt := newInvalidURLTransport()
	_, err := rt.Peers(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestDrain_InvalidURL(t *testing.T) {
	rt := newInvalidURLTransport()
	err := rt.Drain(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestHealthy_InvalidURL(t *testing.T) {
	rt := newInvalidURLTransport()
	// Healthy returns bool, not error — the NewRequestWithContext failure
	// returns false.
	if rt.Healthy(context.Background()) {
		t.Error("expected Healthy to return false for invalid URL")
	}
}

// ---------------------------------------------------------------------------
// client.Do error paths (server closed before call)
// ---------------------------------------------------------------------------

// newRefusedTransport creates a Transport pointed at a server that has been
// closed, so client.Do will fail with a connection refused error.
func newRefusedTransport(t *testing.T) *Transport {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	client := srv.Client()
	srv.Close() // close immediately so all future requests fail
	return &Transport{
		config:  &transport.Config{Address: url},
		baseURL: url,
		client:  client,
	}
}

func TestPut_ClientDoError(t *testing.T) {
	rt := newRefusedTransport(t)
	err := rt.Put(context.Background(), "key", []byte("val"))
	if err == nil {
		t.Fatal("expected error when server is closed")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestGet_ClientDoError(t *testing.T) {
	rt := newRefusedTransport(t)
	_, err := rt.Get(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error when server is closed")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestDelete_ClientDoError(t *testing.T) {
	rt := newRefusedTransport(t)
	err := rt.Delete(context.Background(), "key")
	if err == nil {
		t.Fatal("expected error when server is closed")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestList_ClientDoError(t *testing.T) {
	rt := newRefusedTransport(t)
	_, err := rt.List(context.Background(), "")
	if err == nil {
		t.Fatal("expected error when server is closed")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestHealth_ClientDoError(t *testing.T) {
	rt := newRefusedTransport(t)
	_, err := rt.Health(context.Background())
	if err == nil {
		t.Fatal("expected error when server is closed")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestStatus_ClientDoError(t *testing.T) {
	rt := newRefusedTransport(t)
	_, err := rt.Status(context.Background())
	if err == nil {
		t.Fatal("expected error when server is closed")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestPeers_ClientDoError(t *testing.T) {
	rt := newRefusedTransport(t)
	_, err := rt.Peers(context.Background())
	if err == nil {
		t.Fatal("expected error when server is closed")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestDrain_ClientDoError(t *testing.T) {
	rt := newRefusedTransport(t)
	err := rt.Drain(context.Background())
	if err == nil {
		t.Fatal("expected error when server is closed")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestHealthy_ClientDoError(t *testing.T) {
	rt := newRefusedTransport(t)
	if rt.Healthy(context.Background()) {
		t.Error("expected Healthy to return false when server is closed")
	}
}

func TestRequest_ClientDoError(t *testing.T) {
	rt := newRefusedTransport(t)
	err := rt.Request(context.Background(), "health", nil, nil)
	if err == nil {
		t.Fatal("expected error when server is closed")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Connect: TLS config applied when non-nil (httptest.NewTLSServer path)
// ---------------------------------------------------------------------------

func TestConnect_WithTLSConfig(t *testing.T) {
	// Use httptest.NewTLSServer which provides its own TLS config.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
		}
	}))
	defer srv.Close()

	// Use the server's own TLS client config so the certificate is trusted.
	rt, err := New(
		transport.WithAddress(srv.URL),
		transport.WithTLSConfig(srv.Client().Transport.(*http.Transport).TLSClientConfig),
	)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if err := rt.Connect(context.Background()); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer rt.Close()

	if !rt.Healthy(context.Background()) {
		t.Error("expected healthy TLS connection")
	}
}

// TestConnect_WithTLSConfigLeavesCallerNextProtos pins #949: the REST
// transport attempts HTTP/2, and net/http appends "h2" to the NextProtos
// of the TLS config it is handed. That config must be a copy, so the
// caller's own config, possibly shared with an HTTP/1.1-only client, still
// offers only what the caller put in it.
func TestConnect_WithTLSConfigLeavesCallerNextProtos(t *testing.T) {
	protos := make(chan int, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			select {
			case protos <- r.ProtoMajor:
			default:
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
		}
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	callerCfg := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}

	rt, err := New(
		transport.WithAddress(srv.URL),
		transport.WithTLSConfig(callerCfg),
	)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if err := rt.Connect(context.Background()); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer rt.Close()

	if !rt.Healthy(context.Background()) {
		t.Fatal("expected healthy TLS connection")
	}
	select {
	case major := <-protos:
		if major != 2 {
			t.Fatalf("request ProtoMajor = %d, want 2 (the transport must attempt HTTP/2)", major)
		}
	default:
		t.Fatal("the server saw no /health request")
	}
	if len(callerCfg.NextProtos) != 0 {
		t.Errorf("caller NextProtos = %v, want empty: the transport mutated the caller's config", callerCfg.NextProtos)
	}
}

// ---------------------------------------------------------------------------
// Additional coverage: Connect zero timeout, Request invalid URL,
// postJSON invalid URL and json.Marshal error, Get io.ReadAll error
// ---------------------------------------------------------------------------

func TestConnect_ZeroTimeout(t *testing.T) {
	// When Timeout is explicitly set to 0, Connect uses the 30s default.
	srv := newTestServer(t)
	defer srv.Close()

	rt, err := New(
		transport.WithAddress(srv.URL),
		transport.WithTimeout(0),
	)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if err := rt.Connect(context.Background()); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer rt.Close()
}

func TestRequest_InvalidURL(t *testing.T) {
	// baseURL with a null byte causes http.NewRequestWithContext to fail
	// inside Request.
	rt := newInvalidURLTransport()
	err := rt.Request(context.Background(), "health", nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid URL in Request")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

// unmarshalableValue is a type that cannot be marshaled to JSON.
type unmarshalableValue struct {
	Ch chan struct{}
}

func TestPostJSON_MarshalError(t *testing.T) {
	// Trigger the json.Marshal error path in postJSON by passing a value
	// containing a channel (which is not JSON-serializable).
	// AddMember, RemoveMember, and RequestSnapshot all call postJSON.
	// We access postJSON directly since the test is in the same package.
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	// Call postJSON directly with an unmarshalable payload.
	err := rt.postJSON(context.Background(), "test", "/membership/add", unmarshalableValue{Ch: make(chan struct{})})
	if err == nil {
		t.Fatal("expected json.Marshal error for unmarshalable value")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestPostJSON_InvalidURL(t *testing.T) {
	// baseURL with a null byte causes http.NewRequestWithContext to fail
	// inside postJSON.
	rt := newInvalidURLTransport()
	err := rt.postJSON(context.Background(), "test", "/membership/add", struct{}{})
	if err == nil {
		t.Fatal("expected error for invalid URL in postJSON")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestRequest_MarshalError(t *testing.T) {
	// Trigger the json.Marshal error path in Request by passing an
	// unmarshalable value (channel). The transport needs a valid client
	// so the closed check passes.
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.Request(context.Background(), "test", unmarshalableValue{Ch: make(chan struct{})}, nil)
	if err == nil {
		t.Fatal("expected json.Marshal error for unmarshalable request payload")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

func TestPostJSON_ClientDoError(t *testing.T) {
	// Trigger the client.Do error path in postJSON by using a refused server.
	rt := newRefusedTransport(t)
	err := rt.postJSON(context.Background(), "test", "/membership/add", struct{}{})
	if err == nil {
		t.Fatal("expected error when server is closed")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Errorf("expected RequestError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Shard management tests
// ---------------------------------------------------------------------------

func TestStartShard(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.StartShard(context.Background(), &transport.StartShardRequest{
		ShardID:   1,
		ReplicaID: 1,
		Members:   map[uint64]string{1: "127.0.0.1:63001"},
		Join:      false,
	})
	if err != nil {
		t.Fatalf("StartShard: %v", err)
	}
}

func TestStartShard_OnClosedTransport(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	err := rt.StartShard(context.Background(), &transport.StartShardRequest{
		ShardID:   1,
		ReplicaID: 1,
	})
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestStopShard(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.StopShard(context.Background(), 1)
	if err != nil {
		t.Fatalf("StopShard: %v", err)
	}
}

func TestStopShard_OnClosedTransport(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	err := rt.StopShard(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestGetShardState(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	resp, err := rt.GetShardState(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetShardState: %v", err)
	}
	if resp.ShardID != 1 {
		t.Fatalf("expected ShardID=1, got %d", resp.ShardID)
	}
	if resp.LeaderID != 1 {
		t.Fatalf("expected LeaderID=1, got %d", resp.LeaderID)
	}
	if resp.Term != 5 {
		t.Fatalf("expected Term=5, got %d", resp.Term)
	}
	if !resp.HasLeader {
		t.Fatal("expected HasLeader=true")
	}
}

func TestGetShardState_OnClosedTransport(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	_, err := rt.GetShardState(context.Background(), 1)
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestGetTopology(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	resp, err := rt.GetTopology(context.Background())
	if err != nil {
		t.Fatalf("GetTopology: %v", err)
	}
	if resp.NodeID != "test-node" {
		t.Fatalf("expected NodeID %q, got %q", "test-node", resp.NodeID)
	}
	if resp.RaftAddress != "127.0.0.1:63001" {
		t.Fatalf("expected RaftAddress %q, got %q", "127.0.0.1:63001", resp.RaftAddress)
	}
	if resp.ShardCount != 1 {
		t.Fatalf("expected ShardCount=1, got %d", resp.ShardCount)
	}
	if len(resp.Shards) != 1 {
		t.Fatalf("expected 1 shard, got %d", len(resp.Shards))
	}
	if resp.Shards[0].ShardID != 1 {
		t.Fatalf("expected shard[0].ShardID=1, got %d", resp.Shards[0].ShardID)
	}
}

func TestGetTopology_OnClosedTransport(t *testing.T) {
	srv := newTestServer(t)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	_ = rt.Close()

	_, err := rt.GetTopology(context.Background())
	if err == nil {
		t.Fatal("expected error on closed transport")
	}
	var closedErr *transport.ClosedError
	if !errors.As(err, &closedErr) {
		t.Fatalf("expected *transport.ClosedError, got %T: %v", err, err)
	}
}

func TestGetShardState_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/state") && strings.HasPrefix(r.URL.Path, "/admin/shards/") {
			http.Error(w, `{"error":"shard not found"}`, http.StatusNotFound)
			return
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.GetShardState(context.Background(), 999)
	if err == nil {
		t.Fatal("expected error for nonexistent shard")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestGetTopology_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		if r.URL.Path == "/admin/topology" {
			http.Error(w, `{"error":"internal"}`, http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	_, err := rt.GetTopology(context.Background())
	if err == nil {
		t.Fatal("expected error for server error")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

func TestStopShard_ServerError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(transport.HealthResponse{Status: "ok"})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/admin/shards/") && r.Method == http.MethodDelete {
			http.Error(w, `{"error":"shard not found"}`, http.StatusNotFound)
			return
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	err := rt.StopShard(context.Background(), 999)
	if err == nil {
		t.Fatal("expected error for nonexistent shard")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
}

// TestAddMember_CarriesIdentity pins that the operator-supplied member
// identity is in the JSON body REST sends.
func TestAddMember_CarriesIdentity(t *testing.T) {
	var got transport.AddMemberRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(transport.OKResponse{Status: "ok"})
	}))
	defer srv.Close()
	rt := newTestTransport(t, srv.URL)
	defer rt.Close()

	identity := "spki:" + strings.Repeat("ab", 32)
	if err := rt.AddMember(context.Background(), 0, 4, "10.0.0.4:63001", identity); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	if got.Identity != identity {
		t.Fatalf("request identity %q, want %q", got.Identity, identity)
	}
}
