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

// Integration-style tests for the member-side CSR join operator wiring: the
// --cluster-join-* and --bootstrap-join-* / --bootstrap-spki-pin /
// --bootstrap-addr cobra flags (and their YAML twins), exercised through the
// SAME cobra parse + resolveStartFlags merge path `qrdbd start` runs, driving
// a REAL join against a real cluster CA provider -- not a mock of either
// side. pkg/clusterenroll's own TestJoin_SurvivesFirstNodeLoss already covers
// the transports in isolation; this file's job is the flags->Options plumbing
// go-qrdb's "operator wiring" gap named: that a value set on the command line
// or in YAML is the value the join actually used, for both the signing
// member's listener and the joiner's client.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/internal/netport"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterca"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
)

// clusterJoinFlagsTestTrustDomain is the SPIFFE trust domain this file's
// tests pin on both the signing-member and joiner sides, matching
// --cluster-trust-domain so a minted token's bound domain always equals the
// domain the consuming side validates against.
const clusterJoinFlagsTestTrustDomain = "qrdb.cluster"

// clusterJoinFlagsTestBarrier is a trivial in-memory seal.EncryptionBarrier.
// This file tests the CLI/YAML operator-wiring surface, not
// cakey.SoftwareProvider's own encrypted round-trip, which pkg/cakey already
// covers.
type clusterJoinFlagsTestBarrier struct{}

func (clusterJoinFlagsTestBarrier) Encrypt(p []byte) ([]byte, error) { return p, nil }
func (clusterJoinFlagsTestBarrier) Decrypt(c []byte) ([]byte, error) { return c, nil }
func (clusterJoinFlagsTestBarrier) IsSealed() bool                   { return false }

// newClusterJoinFlagsTestCA returns an initialized cluster-CA provider.
func newClusterJoinFlagsTestCA(t *testing.T) *cakey.SoftwareProvider {
	t.Helper()
	p := clusterca.NewProvider(kvstoretest.NewGuardedMemoryStore(), clusterJoinFlagsTestBarrier{})
	require.NoError(t, p.Initialize(context.Background()))
	return p
}

// buildClusterJoinFlagsTestCSR builds a self-signed PKCS#10 request for a
// fresh ECDSA P-256 key, mirroring clusterenroll's own unexported
// buildJoinCSR closely enough to exercise the same signing path from this
// package, which cannot call it directly.
func buildClusterJoinFlagsTestCSR(t *testing.T, nodeURI string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	u, err := url.Parse(nodeURI)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "qrdb-node"},
		URIs:    []*url.URL{u},
	}, key)
	require.NoError(t, err)
	return der
}

// TestClusterJoinFlags_NoiseListenerSignsAValidCSR drives the signing
// member's side of operator wiring: --cluster-join-enabled,
// --cluster-join-listen-host, --cluster-join-listen-port and
// --cluster-trust-domain resolved through the real cobra parse + YAML merge
// (resolveStartFlags), then fed into clusterenroll.StartJoinServer exactly
// as runStart does, and dialed with a real Noise_NK join client -- presenting
// a single-use, identity-bound token minted into the replicated store, the
// SAME store the listener validates against -- to prove the resolved values
// describe a listener that actually signs. There is no token-file/-env flag
// on this side any more (see startFlags.clusterJoinEnabled's doc comment):
// an admin mints join tokens into the store out of band
// (clusterenroll.MintJoinToken), which is what this test does directly.
func TestClusterJoinFlags_NoiseListenerSignsAValidCSR(t *testing.T) {
	if !clusterenroll.NoiseAvailable() {
		// The member-side CSR join listener needs a real Noise NK
		// handshake (an X25519 static key), which GODEBUG=fips140=only
		// refuses to generate at all; permanently unreachable under
		// FIPS -- see docs/seal/fips140-mode.md.
		t.Skip("Noise NK is unavailable under FIPS 140 mode; the join listener cannot start")
	}
	provider := newClusterJoinFlagsTestCA(t)
	caCert, err := provider.Certificate(context.Background())
	require.NoError(t, err)
	store := kvstore.NewMemoryKVStore()

	port, err := netport.FreePortOn(testBindHost, netport.NetworkTCP)
	require.NoError(t, err)

	f := resolveWithArgs(t,
		"--cluster-join-enabled",
		"--cluster-join-listen-host", testBindHost,
		"--cluster-join-listen-port", strconv.Itoa(port),
	)
	require.True(t, f.clusterJoinEnabled, "--cluster-join-enabled must resolve true")
	require.Equal(t, testBindHost, f.clusterJoinListenHost)
	require.Equal(t, port, f.clusterJoinListenPort)

	token, _, err := clusterenroll.MintJoinToken(context.Background(), store, f.clusterTrustDomain, "member-1", time.Hour)
	require.NoError(t, err)

	js, err := clusterenroll.StartJoinServer(provider, clusterenroll.JoinServerOptions{
		Host:        f.clusterJoinListenHost,
		Port:        f.clusterJoinListenPort,
		Store:       store,
		TrustDomain: f.clusterTrustDomain,
	}, t.TempDir(), testLogger())
	require.NoError(t, err)
	t.Cleanup(js.Stop)

	nodeURI := spiffe.NodeURI(f.clusterTrustDomain, "member-1")
	csrDER := buildClusterJoinFlagsTestCSR(t, nodeURI)
	certPEM, err := clusterenroll.JoinCSR(context.Background(), &clusterenroll.JoinClientOptions{
		ServerAddr: js.Addr(),
		ServerKey:  js.PublicKey(),
		Token:      token,
	}, csrDER, nodeURI)
	require.NoError(t, err)

	block, _ := pem.Decode(certPEM)
	require.NotNil(t, block, "signed response has no leaf certificate PEM")
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	require.NoError(t, leaf.CheckSignatureFrom(caCert),
		"the listener started from resolved cluster-join-* flags signed with the wrong CA")
}

// TestClusterJoinFlags_WrongTokenRefused proves a joiner presenting a token
// it was never minted -- the operator-wiring failure mode that matters, since
// a mistaken or stale value is silent until the join is attempted -- is
// refused by the listener rather than silently signing.
func TestClusterJoinFlags_WrongTokenRefused(t *testing.T) {
	if !clusterenroll.NoiseAvailable() {
		// See TestClusterJoinFlags_NoiseListenerSignsAValidCSR.
		t.Skip("Noise NK is unavailable under FIPS 140 mode; the join listener cannot start")
	}
	provider := newClusterJoinFlagsTestCA(t)
	store := kvstore.NewMemoryKVStore()

	port, err := netport.FreePortOn(testBindHost, netport.NetworkTCP)
	require.NoError(t, err)

	f := resolveWithArgs(t,
		"--cluster-join-enabled",
		"--cluster-join-listen-host", testBindHost,
		"--cluster-join-listen-port", strconv.Itoa(port),
	)
	_, _, err = clusterenroll.MintJoinToken(context.Background(), store, f.clusterTrustDomain, "member-1", time.Hour)
	require.NoError(t, err)

	js, err := clusterenroll.StartJoinServer(provider, clusterenroll.JoinServerOptions{
		Host:        f.clusterJoinListenHost,
		Port:        f.clusterJoinListenPort,
		Store:       store,
		TrustDomain: f.clusterTrustDomain,
	}, t.TempDir(), testLogger())
	require.NoError(t, err)
	t.Cleanup(js.Stop)

	csrDER := buildClusterJoinFlagsTestCSR(t, "")
	_, err = clusterenroll.JoinCSR(context.Background(), &clusterenroll.JoinClientOptions{
		ServerAddr: js.Addr(),
		ServerKey:  js.PublicKey(),
		Token:      "never-minted-token",
	}, csrDER, "")
	var refused *clusterenroll.JoinRefusedError
	require.ErrorAs(t, err, &refused, "a wrong-token join must be refused, not signed")
}

// TestBootstrapJoinFlags_SPKIDirectEndToEnd drives the joiner's side of
// operator wiring end to end: --bootstrap-spki-pin, --bootstrap-addr (the
// anchor-fetch flags, pinned to the admitted member's own serving
// certificate), --bootstrap-join-url and --bootstrap-join-token-file,
// resolved through the real cobra parse + YAML merge and then carried by
// flagsToEnrollOptions into clusterenroll.Join -- the exact call bootstrapTLS
// makes during a real `qrdbd start`. The admitted member is a real
// httptest.Server serving /v1/ca/bootstrap and /v1/enroll/join through
// clusterenroll.HandleJoinRequest, the identical function pkg/api/rest's
// production route calls (see pkg/api/rest/cluster_join_test.go), so the only
// thing this test adds is the CLI/YAML flag layer in front of it.
func TestBootstrapJoinFlags_SPKIDirectEndToEnd(t *testing.T) {
	provider := newClusterJoinFlagsTestCA(t)
	caCert, err := provider.Certificate(context.Background())
	require.NoError(t, err)
	caCertPEM, err := clusterca.CACertPEM(context.Background(), provider)
	require.NoError(t, err)
	store := kvstore.NewMemoryKVStore()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/ca/bootstrap", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write(caCertPEM)
	})
	mux.HandleFunc(clusterenroll.JoinHTTPPath, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		var req clusterenroll.JoinRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		resp := clusterenroll.HandleJoinRequest(r.Context(), provider, store, clusterJoinFlagsTestTrustDomain, &req, nil)
		respBody, err := json.Marshal(resp)
		if err != nil {
			http.Error(w, "failed to marshal response", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if resp.Error != "" {
			w.WriteHeader(http.StatusForbidden)
		}
		_, _ = w.Write(respBody)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	sum := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
	pin := hex.EncodeToString(sum[:])
	serverAddr := strings.TrimPrefix(srv.URL, "https://")

	token, _, err := clusterenroll.MintJoinToken(context.Background(), store, clusterJoinFlagsTestTrustDomain, "joiner-1", time.Hour)
	require.NoError(t, err)
	tokenPath := filepath.Join(t.TempDir(), "join-token")
	require.NoError(t, os.WriteFile(tokenPath, []byte(token), 0o600))

	dataDir := t.TempDir()
	f := resolveWithArgs(t,
		"--data-dir", dataDir,
		"--host", testBindHost,
		"--cluster-trust-domain", clusterJoinFlagsTestTrustDomain,
		"--bootstrap-spki-pin", pin,
		"--bootstrap-addr", serverAddr,
		"--bootstrap-join-url", srv.URL,
		"--bootstrap-join-token-file", tokenPath,
	)
	require.Equal(t, pin, f.bootstrapSPKIPin)
	require.Equal(t, serverAddr, f.bootstrapAddr)
	require.Equal(t, srv.URL, f.bootstrapJoinHTTPURL)
	require.Equal(t, tokenPath, f.bootstrapJoinTokenFile)

	bundle, err := bootstrapTLS(f, testLogger())
	require.NoError(t, err)
	require.NotNil(t, bundle)
	require.Empty(t, bundle.CAKeyPEM, "a joined node must never receive the cluster CA private key")

	block, _ := pem.Decode(bundle.CertPEM)
	require.NotNil(t, block, "joined material has no leaf certificate PEM")
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	require.NoError(t, leaf.CheckSignatureFrom(caCert),
		"the node joined via resolved bootstrap-join-* flags does not chain to the anchor's CA")

	roleBytes, err := os.ReadFile(filepath.Join(dataDir, clusterenroll.RoleFile))
	require.NoError(t, err)
	require.Equal(t, string(clusterenroll.RoleJoined), string(roleBytes))
}

// TestBootstrapJoinFlags_WrongTokenFileRefused proves the joiner-side flags
// are refused end to end exactly as the signing-member side is: a joiner
// configured with the wrong join-token file never ends up with a leaf
// certificate or any persisted material.
func TestBootstrapJoinFlags_WrongTokenFileRefused(t *testing.T) {
	provider := newClusterJoinFlagsTestCA(t)
	store := kvstore.NewMemoryKVStore()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/ca/bootstrap", func(w http.ResponseWriter, _ *http.Request) {
		caCertPEM, err := clusterca.CACertPEM(context.Background(), provider)
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write(caCertPEM)
	})
	mux.HandleFunc(clusterenroll.JoinHTTPPath, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var req clusterenroll.JoinRequest
		require.NoError(t, json.Unmarshal(body, &req))
		resp := clusterenroll.HandleJoinRequest(r.Context(), provider, store, clusterJoinFlagsTestTrustDomain, &req, nil)
		respBody, err := json.Marshal(resp)
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		if resp.Error != "" {
			w.WriteHeader(http.StatusForbidden)
		}
		_, _ = w.Write(respBody)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	sum := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
	pin := hex.EncodeToString(sum[:])
	serverAddr := strings.TrimPrefix(srv.URL, "https://")

	// This token is never minted into store, so ConsumeJoinToken refuses it
	// -- the operator-wiring failure mode that matters, since a stale or
	// mistaken token-file value is silent until the join is attempted.
	tokenPath := filepath.Join(t.TempDir(), "join-token")
	require.NoError(t, os.WriteFile(tokenPath, []byte("never-minted-token-value"), 0o600))

	dataDir := t.TempDir()
	f := resolveWithArgs(t,
		"--data-dir", dataDir,
		"--host", testBindHost,
		"--cluster-trust-domain", clusterJoinFlagsTestTrustDomain,
		"--bootstrap-spki-pin", pin,
		"--bootstrap-addr", serverAddr,
		"--bootstrap-join-url", srv.URL,
		"--bootstrap-join-token-file", tokenPath,
	)

	_, err := bootstrapTLS(f, testLogger())
	var refused *clusterenroll.JoinRefusedError
	require.ErrorAs(t, err, &refused, "a wrong-token joiner must be refused, not issued a certificate")
	if _, statErr := os.Stat(filepath.Join(dataDir, "ca-key.pem")); !os.IsNotExist(statErr) {
		t.Fatalf("a refused join must leave no CA key file (stat err = %v)", statErr)
	}
}
