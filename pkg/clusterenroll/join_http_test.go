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

// Tests for the SPKI/Direct member-side CSR join HTTP client (join_http.go).
// pkg/api/rest/cluster_join_test.go covers the server side
// (HandleJoinRequest wired through the REST route); this file covers the
// client half, which -- before this file -- had no coverage of its own at
// all in this package's profile (cmd/qrdbd's operator-wiring test exercises
// this code too, but attributes the coverage to cmd/qrdbd's profile, not
// pkg/clusterenroll's).
package clusterenroll

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
)

// generateTestCSR returns a fresh ECDSA P-256 key and a self-signed CSR for
// it, for submitting to a join listener over either transport.
func generateTestCSR(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	csrDER, err := buildJoinCSR(key, "")
	if err != nil {
		t.Fatalf("buildJoinCSR: %v", err)
	}
	return key, csrDER
}

// assertLeafChainsToCA parses certPEM and fails the test unless it chains to
// provider's CA certificate and carries nodeKey's own public key.
func assertLeafChainsToCA(t *testing.T, provider *cakey.SoftwareProvider, certPEM []byte, nodeKey *ecdsa.PrivateKey) {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no leaf certificate PEM in response")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	caCert, err := provider.Certificate(context.Background())
	if err != nil {
		t.Fatalf("provider.Certificate: %v", err)
	}
	if err := leaf.CheckSignatureFrom(caCert); err != nil {
		t.Fatalf("issued leaf does not chain to the cluster CA: %v", err)
	}
	gotPub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	if !ok || !gotPub.Equal(&nodeKey.PublicKey) {
		t.Fatal("issued leaf does not carry the joiner's own public key")
	}
}

// joinHTTPServer starts an httptest TLS server serving JoinHTTPPath through
// HandleJoinRequest -- the exact core pkg/api/rest's production route calls
// -- and returns its base URL and the hex SHA-256 SPKI pin of its own
// serving certificate.
func joinHTTPServer(t *testing.T, provider *cakey.SoftwareProvider, token string) (serverURL, spkiPin string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(JoinHTTPPath, func(w http.ResponseWriter, r *http.Request) {
		var req JoinRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		resp := HandleJoinRequest(r.Context(), provider, token, &req)
		respBody, err := json.Marshal(resp)
		if err != nil {
			http.Error(w, "marshal failed", http.StatusInternalServerError)
			return
		}
		if resp.Error != "" {
			w.WriteHeader(http.StatusForbidden)
		}
		_, _ = w.Write(respBody)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
	return srv.URL, hex.EncodeToString(sum[:])
}

func TestJoinCSRHTTP_RequiresToken(t *testing.T) {
	_, err := JoinCSRHTTP(context.Background(), &JoinHTTPClientOptions{ServerURL: "https://127.0.0.1"}, nil, "")
	var missing *MissingJoinTokenError
	if !errors.As(err, &missing) {
		t.Fatalf("JoinCSRHTTP with no token = %v, want *MissingJoinTokenError", err)
	}
}

func TestJoinHTTPClientOptions_TimeoutDefault(t *testing.T) {
	o := &JoinHTTPClientOptions{}
	if got := o.timeout(); got != DefaultTimeout {
		t.Fatalf("timeout() = %v, want DefaultTimeout (%v)", got, DefaultTimeout)
	}
	o2 := &JoinHTTPClientOptions{Timeout: 5 * time.Second}
	if got := o2.timeout(); got != 5*time.Second {
		t.Fatalf("timeout() = %v, want 5s", got)
	}
}

func TestBuildJoinHTTPClient_Direct(t *testing.T) {
	client, err := buildJoinHTTPClient(&JoinHTTPClientOptions{})
	if err != nil {
		t.Fatalf("buildJoinHTTPClient (direct): %v", err)
	}
	if client.Transport != nil {
		t.Fatal("direct mode must not install a custom Transport (relies on the system trust store)")
	}
}

func TestBuildJoinHTTPClient_InvalidPin(t *testing.T) {
	_, err := buildJoinHTTPClient(&JoinHTTPClientOptions{SPKIPin: "not-hex"})
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("buildJoinHTTPClient with an invalid pin = %v, want *FetchError", err)
	}
}

// TestJoinCSRHTTP_SignsAValidCSR drives the full SPKI-pinned success path.
func TestJoinCSRHTTP_SignsAValidCSR(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	const token = "s3cret-token"
	serverURL, pin := joinHTTPServer(t, provider, token)

	nodeKey, csrDER := generateTestCSR(t)
	certPEM, err := JoinCSRHTTP(context.Background(), &JoinHTTPClientOptions{
		ServerURL: serverURL,
		SPKIPin:   pin,
		Token:     token,
	}, csrDER, "spiffe://test.cluster/node/127.0.0.1:9100")
	if err != nil {
		t.Fatalf("JoinCSRHTTP: %v", err)
	}
	assertLeafChainsToCA(t, provider, certPEM, nodeKey)
}

// TestJoinCSRHTTP_WrongTokenRefused proves a bad token is refused, not
// signed, over the HTTP transport -- the twin of
// TestJoinServer_WrongTokenRefused for Noise_NK.
func TestJoinCSRHTTP_WrongTokenRefused(t *testing.T) {
	provider := newTestClusterCAProvider(t)
	const token = "correct-token"
	serverURL, pin := joinHTTPServer(t, provider, token)

	_, csrDER := generateTestCSR(t)
	_, err := JoinCSRHTTP(context.Background(), &JoinHTTPClientOptions{
		ServerURL: serverURL,
		SPKIPin:   pin,
		Token:     "wrong-token",
	}, csrDER, "")
	var refused *JoinRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("JoinCSRHTTP with wrong token = %v, want *JoinRefusedError", err)
	}
}

// TestJoinCSRHTTP_MalformedResponseIsFetchError proves a response body that
// is not valid JoinResponse JSON surfaces as *FetchError rather than being
// silently treated as success.
func TestJoinCSRHTTP_MalformedResponseIsFetchError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(JoinHTTPPath, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
	pin := hex.EncodeToString(sum[:])

	_, csrDER := generateTestCSR(t)
	_, err := JoinCSRHTTP(context.Background(), &JoinHTTPClientOptions{
		ServerURL: srv.URL,
		SPKIPin:   pin,
		Token:     "tok",
	}, csrDER, "")
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("JoinCSRHTTP against a non-JSON response = %v, want *FetchError", err)
	}
}

// TestJoinCSRHTTP_ConnectionRefusedIsFetchError proves a dial/transport
// failure (nothing listening) surfaces as *FetchError, exercising the
// client.Do failure branch.
func TestJoinCSRHTTP_ConnectionRefusedIsFetchError(t *testing.T) {
	port := freePort(t)
	_, csrDER := generateTestCSR(t)
	_, err := JoinCSRHTTP(context.Background(), &JoinHTTPClientOptions{
		ServerURL: "https://127.0.0.1:" + strconv.Itoa(port),
		Token:     "tok",
		Timeout:   500 * time.Millisecond,
	}, csrDER, "")
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("JoinCSRHTTP against nothing listening = %v, want *FetchError", err)
	}
}

// TestJoinCSRHTTP_MalformedServerURLIsFetchError proves an unparseable
// ServerURL fails building the HTTP request, not at the Do call, exercising
// the http.NewRequestWithContext failure branch.
func TestJoinCSRHTTP_MalformedServerURLIsFetchError(t *testing.T) {
	_, csrDER := generateTestCSR(t)
	_, err := JoinCSRHTTP(context.Background(), &JoinHTTPClientOptions{
		ServerURL: "://not-a-url",
		Token:     "tok",
	}, csrDER, "")
	var fetchErr *FetchError
	if !errors.As(err, &fetchErr) {
		t.Fatalf("JoinCSRHTTP with an unparseable server URL = %v, want *FetchError", err)
	}
}
