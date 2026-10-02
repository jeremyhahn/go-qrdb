// Copyright (c) 2026 Jeremy Hahn
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

// Package forward implements signed on-behalf-of caller-identity propagation
// for cross-node write forwarding.
//
// When a REST write lands on a follower it is proxied to the Raft leader. The
// leader authenticates the mTLS peer, which on a forwarded request is the
// FORWARDING NODE, not the original caller. A node identity carries no user
// roles, so RBAC would deny the write. To preserve the original caller's
// authorization across the forward, the forwarding node -- which has ALREADY
// authenticated and authorized the caller at ingress -- emits a signed
// CallerAssertion that carries the caller's IDENTITY (Subject + TenantID),
// bound to the specific request (method + path + body digest) and time-boxed by
// a short TTL. The leader verifies the assertion was signed by the mTLS peer's
// own key, that the peer is a trusted cluster node, and that the binding
// matches the actual request; it then INDEPENDENTLY re-resolves the caller's
// roles from its own authorization data (the asserted identity is never trusted
// to carry roles) and authorizes the request AS THE CALLER.
//
// Trust model. The assertion is signed with the forwarding node's private key
// and verified against the mTLS peer certificate's public key, cryptographically
// binding the signer to the presented cluster-peer certificate. The peer
// certificate must carry a node-scoped SPIFFE SAN (verified against the
// pinned cluster trust domain when configured); a user/service certificate
// presenting an assertion is rejected. Because the mTLS handshake has already
// verified the peer certificate chains to the cluster CA, a non-cluster caller
// cannot present a node certificate, and a cluster node cannot forge the
// signature of a different node.
//
// Replay window. An assertion is valid only for its TTL (default
// DefaultTTL) and only for the exact request it is bound to (method, path,
// and body SHA-256). Within that window a captured assertion is replayable
// solely for the identical operation and body; the window is bounded by the
// TTL. A per-node nonce cache keyed on the assertion digest would eliminate
// even that residual window and is a natural future hardening step; it is not
// required for correctness given the tight binding and short TTL and is
// intentionally not built here.
package forward

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
)

// HTTP header names carrying the on-behalf assertion and its signature. The
// forwarding node sets both; the leader honors them only when the mTLS peer is
// a verified cluster node.
const (
	// HeaderAssertion carries the base64-encoded canonical CallerAssertion.
	HeaderAssertion = "X-Qrdb-On-Behalf-Assertion"

	// HeaderSignature carries the base64-encoded ECDSA signature over the
	// canonical assertion bytes.
	HeaderSignature = "X-Qrdb-On-Behalf-Sig"
)

// DefaultTTL is the assertion validity window. It is deliberately short: the
// assertion only needs to survive a single follower->leader hop, so a tight TTL
// minimizes the replay window while tolerating modest inter-node clock skew.
const DefaultTTL = 30 * time.Second

// MaxBodyBytes bounds the body a node reads from a forwarded request that
// carries an on-behalf assertion. The body must be read before its digest can
// be checked against the assertion's binding, so this is the most a cluster
// peer can make the node buffer ahead of that check.
//
// It must admit the largest body any REST handler forwards to the leader,
// which is the rebalance plan (16 MiB + 4 KiB) and a guarded write (twice
// the 8 MiB Raft entry ceiling). pkg/api/rest asserts every forwarding
// handler's bound stays within it. The 64 KiB above those bounds is
// headroom for their envelopes, not a limit a caller can use: each handler
// re-applies its own bound to the bytes it is handed.
const MaxBodyBytes = int64(16<<20) + 64<<10

// clockSkewTolerance is the allowance applied to both the not-before and
// not-after checks so that small clock differences between cluster nodes do not
// reject an otherwise-valid assertion.
const clockSkewTolerance = 5 * time.Second

// CallerAssertion is a signed statement, made by a cluster node, that a named
// caller (Subject within TenantID) issued a specific request. It carries
// IDENTITY ONLY: the leader re-resolves the caller's roles independently and
// never trusts any role claim. The Method/Path/BodySHA256 fields bind the
// assertion to a single request so it cannot be replayed for a different
// operation or body.
//
// The struct field order is the canonical serialization order (json.Marshal of
// a struct emits fields in declaration order), giving a deterministic byte
// sequence to sign and verify without a separate canonicalization pass.
type CallerAssertion struct {
	// Subject is the authenticated principal of the original caller (the CN
	// from the caller's client certificate).
	Subject string `json:"subject"`

	// TenantID is the original caller's tenant namespace.
	TenantID string `json:"tenant_id"`

	// Method is the HTTP method of the forwarded request.
	Method string `json:"method"`

	// Path is the request URI (path + raw query) of the forwarded request.
	Path string `json:"path"`

	// BodySHA256 is the lowercase hex SHA-256 digest of the request body (the
	// digest of an empty body for bodyless requests).
	BodySHA256 string `json:"body_sha256"`

	// ForwarderNodeID is the SPIFFE node id of the forwarding node. It must
	// match the node id in the mTLS peer certificate that presents the
	// assertion (defense in depth on top of the signature check).
	ForwarderNodeID string `json:"forwarder_node_id"`

	// IssuedAtUnix is the assertion issue time (Unix seconds).
	IssuedAtUnix int64 `json:"issued_at_unix"`

	// ExpiresAtUnix is the assertion expiry time (Unix seconds).
	ExpiresAtUnix int64 `json:"expires_at_unix"`

	// HolderProfile is the holder-certificate profile (auth.HolderProfile)
	// the original caller authenticated with, and empty for a user. A holder
	// certificate is never resolved to a user account; carrying the profile
	// is what keeps that true when the request is forwarded, since the leader
	// never sees the certificate. omitempty keeps a user's assertion
	// byte-identical to the form before the field existed.
	HolderProfile string `json:"holder_profile,omitempty"`
}

// NewCallerAssertion builds a request-bound assertion for the given caller and
// request, stamped now and expiring after ttl. When ttl <= 0, DefaultTTL is
// used. bodySHA256 must be the hex SHA-256 digest of the request body as
// produced by BodyDigest.
func NewCallerAssertion(subject, tenantID, method, path, bodySHA256, forwarderNodeID string, now time.Time, ttl time.Duration) CallerAssertion {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return CallerAssertion{
		Subject:         subject,
		TenantID:        tenantID,
		Method:          method,
		Path:            path,
		BodySHA256:      bodySHA256,
		ForwarderNodeID: forwarderNodeID,
		IssuedAtUnix:    now.Unix(),
		ExpiresAtUnix:   now.Add(ttl).Unix(),
	}
}

// BodyDigest returns the lowercase hex SHA-256 digest of body. A nil or empty
// body yields the digest of the empty string, so bodyless requests bind to a
// stable, well-known value.
func BodyDigest(body []byte) string {
	sum := sha256.Sum256(body)
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(sum)*2)
	for i, b := range sum {
		out[i*2] = hexdigits[b>>4]
		out[i*2+1] = hexdigits[b&0x0f]
	}
	return string(out)
}

// ClusterPeer returns the node identity of peerCert, or
// *ForwardAssertionNotClusterPeerError when peerCert is nil or carries no
// node-scoped SPIFFE SAN (under pinnedTrustDomain when non-empty). It is
// Verify's first check, exposed so a receiver can refuse a presenter that
// is not a cluster node BEFORE reading anything the request carries: only a
// cluster node can present an assertion that will be honored.
func ClusterPeer(peerCert *x509.Certificate, pinnedTrustDomain string) (spiffe.NodeIdentity, error) {
	if peerCert == nil {
		return spiffe.NodeIdentity{}, &ForwardAssertionNotClusterPeerError{Err: spiffe.ErrNoNodeSPIFFEID}
	}
	nodeID, err := spiffe.NodeFromCert(peerCert, pinnedTrustDomain)
	if err != nil {
		return spiffe.NodeIdentity{}, &ForwardAssertionNotClusterPeerError{Err: err}
	}
	return nodeID, nil
}

// Sign serializes the assertion canonically and signs the SHA-256 digest of the
// serialized bytes with key (an ECDSA private key). It returns the base64
// assertion and base64 signature to place in HeaderAssertion / HeaderSignature.
func Sign(a CallerAssertion, key crypto.Signer) (assertionB64, sigB64 string, err error) {
	raw, mErr := json.Marshal(a)
	if mErr != nil {
		return "", "", &ForwardSigningError{Err: mErr}
	}
	digest := sha256.Sum256(raw)
	sig, sErr := key.Sign(rand.Reader, digest[:], crypto.SHA256)
	if sErr != nil {
		return "", "", &ForwardSigningError{Err: sErr}
	}
	return base64.StdEncoding.EncodeToString(raw), base64.StdEncoding.EncodeToString(sig), nil
}

// Verify validates an on-behalf assertion against the presenting mTLS peer
// certificate and the actual request, returning the verified CallerAssertion on
// success. It enforces, in order:
//
//  1. peerCert is a trusted cluster node (node-scoped SPIFFE SAN present, under
//     pinnedTrustDomain when non-empty);
//  2. the assertion and signature decode and the signature verifies against
//     peerCert's ECDSA public key over the canonical assertion bytes;
//  3. the ForwarderNodeID matches the peer certificate's node id;
//  4. the assertion is within its validity window (with clock-skew tolerance);
//  5. the assertion is bound to this request (method, path, bodySHA256).
//
// Every failure returns a distinct typed error; the caller collapses them to an
// opaque 401/403 to avoid a reconnaissance oracle. Verify NEVER returns role
// information: the caller re-resolves roles from its own authorization data.
func Verify(assertionB64, sigB64 string, peerCert *x509.Certificate, pinnedTrustDomain string, now time.Time, method, path, bodySHA256 string) (*CallerAssertion, error) {
	// (1) The presenter must be a cluster node.
	nodeID, nErr := ClusterPeer(peerCert, pinnedTrustDomain)
	if nErr != nil {
		return nil, nErr
	}

	// Decode the canonical assertion bytes and the signature.
	raw, dErr := base64.StdEncoding.DecodeString(assertionB64)
	if dErr != nil {
		return nil, &ForwardAssertionEncodingError{Field: "assertion", Err: dErr}
	}
	sig, sErr := base64.StdEncoding.DecodeString(sigB64)
	if sErr != nil {
		return nil, &ForwardAssertionEncodingError{Field: "signature", Err: sErr}
	}

	// (2) The signature must verify against the peer certificate's key over the
	// exact bytes that were signed (no re-serialization, so canonicalization is
	// never ambiguous).
	pub, ok := peerCert.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, &ForwardAssertionSignatureError{Reason: "peer certificate key is not ECDSA"}
	}
	digest := sha256.Sum256(raw)
	if !ecdsa.VerifyASN1(pub, digest[:], sig) {
		return nil, &ForwardAssertionSignatureError{Reason: "verification failed"}
	}

	var a CallerAssertion
	if uErr := json.Unmarshal(raw, &a); uErr != nil {
		return nil, &ForwardAssertionEncodingError{Field: "assertion", Err: uErr}
	}

	if strings.TrimSpace(a.Subject) == "" {
		return nil, &ForwardAssertionFieldError{Field: "subject"}
	}
	if a.IssuedAtUnix <= 0 || a.ExpiresAtUnix <= 0 || a.ExpiresAtUnix < a.IssuedAtUnix {
		return nil, &ForwardAssertionFieldError{Field: "validity"}
	}

	// (3) The asserted forwarder must be the presenting node (defense in depth
	// on top of the signature binding).
	if a.ForwarderNodeID != nodeID.NodeID {
		return nil, &ForwardAssertionNotClusterPeerError{Err: spiffe.ErrNoNodeSPIFFEID}
	}

	// (4) Validity window with clock-skew tolerance.
	nowUnix := now.Unix()
	if nowUnix > a.ExpiresAtUnix+int64(clockSkewTolerance/time.Second) {
		return nil, &ForwardAssertionExpiredError{Reason: "expired"}
	}
	if a.IssuedAtUnix-int64(clockSkewTolerance/time.Second) > nowUnix {
		return nil, &ForwardAssertionExpiredError{Reason: "issued in the future"}
	}

	// (5) Request binding.
	if a.Method != method {
		return nil, &ForwardAssertionBindingMismatchError{Field: "method"}
	}
	if a.Path != path {
		return nil, &ForwardAssertionBindingMismatchError{Field: "path"}
	}
	if a.BodySHA256 != bodySHA256 {
		return nil, &ForwardAssertionBindingMismatchError{Field: "body"}
	}

	return &a, nil
}
