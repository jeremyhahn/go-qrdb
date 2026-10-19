// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

//go:build integration

// join_token_cli_test.go drives `qrdb admin join-token mint` through the
// real CLI binary against every transport, against the one harness in this
// package that can actually sign a join (startSelfIssuedCAMultiProtocolNode,
// multi_protocol_helpers_test.go) -- and, on a BYO-TLS node, proves the
// typed refusal every other harness in this package gets instead.
//
// Redemption (the CSR submission a real joining node's --bootstrap-join-url
// + --bootstrap-join-token-file configuration performs) is driven directly
// through pkg/clusterenroll.JoinCSRHTTP, the exact client-side function that
// code path calls into -- see redeemJoinToken's doc comment for why that is
// a faithful stand-in for a second qrdbd process rather than a shortcut.
package integration_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-truststrap/pkg/spkipin"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
)

// mintJoinTokenCLIResponse mirrors transport.MintJoinTokenResponse's wire
// JSON shape, decoded from `admin join-token mint --output json`.
type mintJoinTokenCLIResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// generateJoinCSR creates a fresh ECDSA P-256 key and a self-signed PKCS#10
// certificate signing request for it, carrying nodeURI as its sole URI
// SAN -- proof of possession, plus the identity-match shape
// HandleJoinRequest's validateJoinCSRIdentity now requires between the CSR
// and the JoinRequest envelope's own NodeURI. The signed LEAF's identity
// still comes entirely from the TOKEN's own binding, never from this CSR's
// Subject or SAN -- see pkg/clusterenroll/join_csr.go's HandleJoinRequest
// doc comment ("FOR THE TOKEN'S BOUND IDENTITY -- req.NodeURI is never
// consulted" for SIGNING purposes). It returns the CSR's DER encoding,
// ready for clusterenroll.JoinCSRHTTP.
func generateJoinCSR(t *testing.T, nodeURI string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generateJoinCSR: generate key: %v", err)
	}
	u, err := url.Parse(nodeURI)
	if err != nil {
		t.Fatalf("generateJoinCSR: url.Parse(%q): %v", nodeURI, err)
	}
	template := &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: "joining-node"},
		SignatureAlgorithm: x509.ECDSAWithSHA256,
		URIs:               []*url.URL{u},
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		t.Fatalf("generateJoinCSR: create CSR: %v", err)
	}
	return der
}

// redeemJoinToken submits a fresh CSR -- claiming nodeURI, the token's own
// bound identity -- to node's SPKI/Direct join route (/v1/enroll/join on
// its admin HTTPS listener) presenting token, via clusterenroll.JoinCSRHTTP
// -- the SAME client-side function a real joining node's
// --bootstrap-join-url + --bootstrap-join-token-file configuration calls
// into (cmd/qrdbd/bootstrap.go's clusterenroll.Resolve path 4). Trust in
// node's listener is established by pinning its own leaf certificate's
// SPKI hash, exactly as --bootstrap-spki-pin would, so this never depends on
// node's self-issued CA being in any system trust store. It returns the
// signed leaf PEM on success, or the typed error JoinCSRHTTP reports (in
// particular *clusterenroll.JoinRefusedError for a bad or already-consumed
// token, or a CSR/NodeURI mismatch).
func redeemJoinToken(t *testing.T, node *selfIssuedCANode, token, nodeURI string) ([]byte, error) {
	t.Helper()
	leafPEM, err := os.ReadFile(node.leafCertPath)
	if err != nil {
		t.Fatalf("redeemJoinToken: read leaf cert: %v", err)
	}
	block, _ := pem.Decode(leafPEM)
	if block == nil {
		t.Fatalf("redeemJoinToken: no PEM block in %s", node.leafCertPath)
	}
	leafCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("redeemJoinToken: parse leaf cert: %v", err)
	}
	pin := spkipin.ComputeSPKIPin(leafCert)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return clusterenroll.JoinCSRHTTP(ctx, &clusterenroll.JoinHTTPClientOptions{
		ServerURL: node.adminURL,
		SPKIPin:   pin,
		Token:     token,
	}, generateJoinCSR(t, nodeURI), nodeURI)
}

// leafURISANs returns the URI SANs of a PEM-encoded certificate.
func leafURISANs(t *testing.T, certPEM []byte) []string {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatalf("leafURISANs: no PEM block in signed leaf")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("leafURISANs: parse signed leaf: %v", err)
	}
	uris := make([]string, len(cert.URIs))
	for i, u := range cert.URIs {
		uris[i] = u.String()
	}
	return uris
}

// TestAdminJoinTokenMint_CLI_PerProtocol drives `qrdb admin join-token mint`
// against a self-issued-CA node over every transport, sharing one node
// across all 5 protocol subtests (each mints its own, never-reused node
// identity, so a token minted by one subtest cannot collide with another's).
//
// Per protocol: mint a token, assert its shape (a non-trivial opaque token
// string and an ExpiresAt in the future), redeem it once against the node's
// SPKI/Direct join route exactly as a real joining node would (proving the
// mint is not merely accepted but genuinely usable, and that the signed
// leaf carries the token's bound SPIFFE identity, never a joiner-asserted
// one), then redeem the SAME token again and confirm it is refused --
// single-use, consumed atomically on first use, per docs/clustering/cli.md.
func TestAdminJoinTokenMint_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startSelfIssuedCAMultiProtocolNode(t)

	// redeemableToken/redeemableNodeID carry the one token minted below
	// (over REST, the first protocol in multiProtocols) into the dedicated
	// "redemption" subtest that runs after the mint loop -- see that
	// subtest's doc comment for why redemption is proven once, globally,
	// rather than once per transport.
	var redeemableToken, redeemableNodeID string

	for i, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			nodeID := fmt.Sprintf("joiner-%s", proto.name)

			out, err := runCLIProto(t, node.multiProtoNode, proto, "--output", "json",
				"admin", "join-token", "mint", nodeID, "--ttl", "1h")
			if err != nil {
				t.Fatalf("[%s] admin join-token mint: %v\noutput: %s", proto.name, err, out)
			}
			var resp mintJoinTokenCLIResponse
			if jerr := json.Unmarshal([]byte(out), &resp); jerr != nil {
				t.Fatalf("[%s] decode `admin join-token mint` JSON: %v\noutput: %s", proto.name, jerr, out)
			}
			if len(resp.Token) < 16 {
				t.Fatalf("[%s] admin join-token mint: token too short to be a real admission secret: %q", proto.name, resp.Token)
			}
			if !resp.ExpiresAt.After(time.Now()) {
				t.Fatalf("[%s] admin join-token mint: ExpiresAt %s is not in the future", proto.name, resp.ExpiresAt)
			}
			if want := time.Now().Add(55 * time.Minute); resp.ExpiresAt.Before(want) {
				t.Fatalf("[%s] admin join-token mint: ExpiresAt %s does not reflect --ttl 1h (want after %s)",
					proto.name, resp.ExpiresAt, want)
			}

			if i == 0 {
				redeemableToken, redeemableNodeID = resp.Token, nodeID
			}

			// Table-format sanity. Minting carries no admission secret of
			// its own to burn and is not behind the redemption rate
			// limiter (only /v1/enroll/join is), so a second mint per
			// protocol costs nothing beyond an unused, harmless token.
			tableOut, err := runCLIProto(t, node.multiProtoNode, proto, "admin", "join-token", "mint", nodeID+"-table", "--ttl", "1h")
			if err != nil {
				t.Fatalf("[%s] admin join-token mint (table): %v\noutput: %s", proto.name, err, tableOut)
			}
			if !strings.Contains(tableOut, "Token:") || !strings.Contains(tableOut, "ExpiresAt:") {
				t.Fatalf("[%s] admin join-token mint (table): missing Token:/ExpiresAt: in output: %s", proto.name, tableOut)
			}
		})
	}

	// Redemption -- the CSR submission a real joining node's
	// --bootstrap-join-url + --bootstrap-join-token-file configuration
	// performs -- always goes over the ONE SPKI/Direct HTTPS route
	// regardless of which transport minted the token, and
	// pkg/clusterenroll's own join-attempt guard rate-limits it per SOURCE
	// ADDRESS (a token-bucket: burst 5, refilling at 1 per 5s, shared
	// across every attempt this test process makes, since every attempt
	// comes from the same loopback address) -- so proving it once here,
	// not once per protocol, is not merely sufficient but the only shape
	// that does not spuriously trip that limiter. The mint loop above
	// already proves every transport can mint; this proves a minted token
	// is genuinely usable and single-use.
	t.Run("redemption", func(t *testing.T) {
		wantURI := spiffe.NodeURI(clusterTestTrustDomain, redeemableNodeID)
		certPEM, err := redeemJoinToken(t, node, redeemableToken, wantURI)
		if err != nil {
			t.Fatalf("redeem minted token: %v", err)
		}
		uris := leafURISANs(t, certPEM)
		if len(uris) != 1 || uris[0] != wantURI {
			t.Fatalf("signed leaf URI SANs = %v, want exactly [%s] (the token's bound identity, never a joiner-asserted one)",
				uris, wantURI)
		}

		// Single-use: the SAME token presented again is refused, not
		// silently re-honored.
		if _, err := redeemJoinToken(t, node, redeemableToken, wantURI); err == nil {
			t.Fatal("second redemption of the same token unexpectedly succeeded")
		} else {
			var refused *clusterenroll.JoinRefusedError
			if !errors.As(err, &refused) {
				t.Fatalf("second redemption: got %v (%T), want *clusterenroll.JoinRefusedError", err, err)
			}
			if refused.Reason != "join token rejected" {
				t.Fatalf("second redemption: refusal reason = %q, want %q", refused.Reason, "join token rejected")
			}
		}
	})
}

// TestAdminJoinTokenMint_CLI_BYOTLSRefusal proves `admin join-token mint` is
// refused with the typed *service.ClusterJoinTokenNotConfiguredError on
// every transport of a BYO-TLS node. cmd/qrdbd/start.go only ever calls
// AdminService.SetClusterJoinTokenStore inside its
// `svcNode.ClusterCAProv != nil` branch (self-issued CA); a BYO-TLS node
// (bundle.ExplicitTLS, startMultiProtocolNode's --tls-ca-file/--tls-cert-file/
// --tls-key-file harness) never takes that branch, so the refusal is
// identical whether or not --cluster-join-enabled was also passed -- there
// is simply no join-token store to mint into, confirmed directly from that
// source rather than assumed.
func TestAdminJoinTokenMint_CLI_BYOTLSRefusal(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			out, err := runCLIProto(t, node, proto, "admin", "join-token", "mint", "node-x", "--ttl", "1h")
			if err == nil {
				t.Fatalf("[%s] admin join-token mint on a BYO-TLS node unexpectedly succeeded, output: %s", proto.name, out)
			}
			if !strings.Contains(out, "no cluster join-token store configured") {
				t.Fatalf("[%s] admin join-token mint: expected the typed ClusterJoinTokenNotConfiguredError, got: %s", proto.name, out)
			}
		})
	}
}

// TestAdminJoinTokenRetire_CLI_PerProtocol drives `qrdb admin join-token
// retire` against a self-issued-CA node over every transport: each
// protocol mints a token for its own never-reused node identity, retires
// that SAME identity, and then proves a FRESH mint for it is permanently
// refused with the typed *clusterenroll.RetiredJoinIdentityError -- not
// merely that the retire call itself returned success.
func TestAdminJoinTokenRetire_CLI_PerProtocol(t *testing.T) {
	t.Parallel()
	node := startSelfIssuedCAMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			nodeID := fmt.Sprintf("retiree-%s", proto.name)

			mintOut, err := runCLIProto(t, node.multiProtoNode, proto, "admin", "join-token", "mint", nodeID, "--ttl", "1h")
			if err != nil {
				t.Fatalf("[%s] admin join-token mint (setup): %v\noutput: %s", proto.name, err, mintOut)
			}

			out, err := runCLIProto(t, node.multiProtoNode, proto, "admin", "join-token", "retire", nodeID, "--reason", "integration test")
			if err != nil {
				t.Fatalf("[%s] admin join-token retire: %v\noutput: %s", proto.name, err, out)
			}
			if !strings.Contains(out, "OK") {
				t.Fatalf("[%s] admin join-token retire: expected OK, got: %s", proto.name, out)
			}

			// A FRESH mint for the SAME identity is now permanently refused
			// -- retirement, not merely the active-admission check an
			// unconsumed token would already trip (retiring writes to the
			// join-IDENTITY ledger, a different key namespace than the
			// join-TOKEN the setup mint above left unconsumed).
			retryOut, err := runCLIProto(t, node.multiProtoNode, proto, "admin", "join-token", "mint", nodeID, "--ttl", "1h")
			if err == nil {
				t.Fatalf("[%s] admin join-token mint after retire unexpectedly succeeded, output: %s", proto.name, retryOut)
			}
			if !strings.Contains(retryOut, "retired") {
				t.Fatalf("[%s] admin join-token mint after retire: expected a retirement refusal, got: %s", proto.name, retryOut)
			}
		})
	}
}

// TestAdminJoinTokenRetire_CLI_BYOTLSRefusal mirrors
// TestAdminJoinTokenMint_CLI_BYOTLSRefusal: RetireJoinIdentity reads and
// writes the SAME join-identity ledger MintJoinToken does (see
// pkg/service.AdminService's shared joinToken state), so a BYO-TLS node --
// no join-token store configured at all -- refuses it identically.
func TestAdminJoinTokenRetire_CLI_BYOTLSRefusal(t *testing.T) {
	t.Parallel()
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			out, err := runCLIProto(t, node, proto, "admin", "join-token", "retire", "node-x")
			if err == nil {
				t.Fatalf("[%s] admin join-token retire on a BYO-TLS node unexpectedly succeeded, output: %s", proto.name, out)
			}
			if !strings.Contains(out, "no cluster join-token store configured") {
				t.Fatalf("[%s] admin join-token retire: expected the typed ClusterJoinTokenNotConfiguredError, got: %s", proto.name, out)
			}
		})
	}
}
