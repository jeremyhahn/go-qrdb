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

package clusterenroll

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/flynn/noise"
	"github.com/jeremyhahn/go-truststrap/pkg/noiseproto"
	noiseboot "github.com/jeremyhahn/go-truststrap/pkg/noiseproto/bootstrap"
	"github.com/jeremyhahn/go-truststrap/pkg/p256proto"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterca"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/util/securefile"
)

// DefaultJoinPort is the default TCP port for the member-side CSR-sign
// listener (see JoinServerOptions).
const DefaultJoinPort = 63007

// JoinKeyFile is the filename used to persist the join listener's Noise
// static key in the data directory, independent of the anchor advertiser's
// own noise-key.hex: the two listeners have independent lifecycles (a member
// that is not the first node still runs a join listener, but never an
// anchor advertiser).
const JoinKeyFile = "join-key.hex"

// JoinServerOptions configures the member-side CSR-sign listener: the
// server half of member-side cluster join. Any admitted, unsealed member
// may run one, not only the first node -- it needs the cluster CA (Key A)
// reachable through a cakey.Provider, which every admitted member has once
// Key A is replicated (see docs/certificate-authority/architecture.md).
type JoinServerOptions struct {
	// Host is the bind host. Required, for the same reason
	// AdvertiseOptions.NoiseHost is: a containment decision this package
	// will not make on the operator's behalf.
	Host string

	// Port is the TCP port. Zero defaults to DefaultJoinPort.
	Port int

	// Store is the replicated kvstore join tokens are minted into
	// (MintJoinToken) and consumed from (ConsumeJoinToken) -- the same
	// store cakey.SoftwareProvider persists Key A through. Required: a
	// join listener with no token store to check against can admit
	// nothing and would not fail closed on its own.
	Store kvstore.AtomicKVStore

	// TrustDomain is this member's own pinned cluster trust domain. A
	// presented token bound to any other trust domain is refused. Required.
	TrustDomain string

	// AdminPort is logged alongside the listener address for operator
	// convenience. Zero means unconfigured.
	AdminPort int

	// Advertiser publishes a pre-membership feature advertisement for a
	// joiner once its leaf is issued, so the operator's member add can admit
	// it after a cluster feature has activated. Nil means this signing
	// member cannot (no lease-capable store): the join still succeeds, and
	// member add refuses until the node itself starts and advertises.
	Advertiser FeatureAdvertiser
}

// validate checks the configuration before any I/O or key generation.
func (o JoinServerOptions) validate() error {
	if o.Host == "" {
		return &MissingBindHostError{Component: "member-join server"}
	}
	if o.Store == nil {
		return &MissingJoinTokenStoreError{}
	}
	if o.TrustDomain == "" {
		return &MissingTrustDomainError{Component: "member-join server"}
	}
	return nil
}

// MissingJoinTokenStoreError is returned when a member-join server is
// configured with no token store to validate presented tokens against.
type MissingJoinTokenStoreError struct{}

// Error returns a human-readable description.
func (e *MissingJoinTokenStoreError) Error() string {
	return "clusterenroll: no join token store configured; member-side CSR join requires " +
		"the replicated kvstore join tokens are minted into and consumed from"
}

// MissingTrustDomainError is returned when a component that must pin a
// cluster trust domain is configured with none.
type MissingTrustDomainError struct {
	Component string
}

// Error returns a human-readable description.
func (e *MissingTrustDomainError) Error() string {
	return "clusterenroll: no trust domain configured for the " + e.Component
}

// port returns the configured port or DefaultJoinPort.
func (o JoinServerOptions) port() int {
	if o.Port == 0 {
		return DefaultJoinPort
	}
	return o.Port
}

// JoinRefusedError is returned when an admitted member refuses a join
// request: a bad token, a malformed or unsigned CSR, or a signing failure.
// The Reason is the member's own report and is safe to log; it carries no
// key material.
type JoinRefusedError struct {
	Reason string
}

// Error returns a human-readable description.
func (e *JoinRefusedError) Error() string {
	return "clusterenroll: member-side join refused: " + e.Reason
}

// JoinPendingError is returned by JoinCSR and JoinCSRHTTP when a tokenless
// CSR was accepted into the signing member's replicated approval queue
// rather than refused or signed. Join treats it as retryable -- not a
// failure -- and keeps polling with the identical CSR until an operator
// decides (see Join's doc comment).
type JoinPendingError struct {
	RequestID string
}

// Error returns a human-readable description.
func (e *JoinPendingError) Error() string {
	return "clusterenroll: join request " + e.RequestID + " is awaiting operator approval"
}

// JoinRequest is the join-admission request payload: the encrypted JSON
// body a Noise_NK joiner sends after the handshake completes, and the JSON
// request body the SPKI/Direct HTTPS join route (pkg/api/rest) reads
// directly -- the same shape, carried over two different transports.
// CSRDER is a PKCS#10 CertificateRequest (self-signed, so the signing
// member can verify the joiner holds the matching private key) encoding
// ONLY the joiner's own public key -- the joiner's private key, like the
// cluster CA's, never crosses this wire on any transport.
type JoinRequest struct {
	// Token is the join-admission secret. Empty submits the CSR into the
	// replicated approval queue instead of presenting a token -- see
	// pending_join.go and docs/clustering/architecture.md's "Approval
	// queue" section -- rather than being refused outright.
	Token   string `json:"token"`
	CSRDER  []byte `json:"csr_der"`
	NodeURI string `json:"node_uri,omitempty"`

	// ReplicaID is the joiner's own Raft replica ID, self-reported. It plays
	// no part in authorization or in the identity the leaf is issued for --
	// that is still the token's bound identity, derived from the address
	// alone -- it only keys the pre-membership feature advertisement
	// HandleJoinRequest publishes on a successful join (see FeatureAdvertiser).
	// Zero skips advertising.
	ReplicaID uint64 `json:"replica_id,omitempty"`

	// Features names the cluster features the joiner's binary implements
	// (featuregate.SupportedNames). The signing member filters this to the
	// names its own binary knows before publishing anything -- an unknown
	// name is never trusted onto the wire of the advertisement record.
	Features []string `json:"features,omitempty"`
}

// JoinResponse is the join-admission response payload, over either
// transport.
type JoinResponse struct {
	CertPEM []byte `json:"cert_pem,omitempty"`
	Error   string `json:"error,omitempty"`

	// Pending reports that the CSR was accepted into the replicated
	// approval queue (req.Token == "") rather than refused or signed.
	// RequestID names its queue entry -- logged for the operator to find
	// it with ListPendingJoinRequests -- never required of the joiner,
	// whose own retry re-presents the identical CSR rather than needing to
	// remember or supply it.
	Pending   bool   `json:"pending,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

// FeatureAdvertiser publishes a lease-backed feature advertisement for a
// node that is not a cluster member yet, under the SAME record
// pkg/cluster/featuregate's Gate reads to decide admission
// (featuregate.AdmitReplica). Without one, a brand-new CSR-joined node has
// no live advertisement, so once any feature has activated, the operator's
// member add refuses it with *featuregate.UnsupportedReplicaError until the
// node itself starts, forms its shard and advertises for itself -- which,
// not yet being a member, it generally cannot do first.
//
// It is a narrow interface, not a dependency on pkg/cluster/featuregate or
// pkg/service directly, so this package stays free of the lease and MVCC
// machinery the implementation (pkg/server/lifecycle) needs: HandleJoinRequest
// only ever calls the one method.
type FeatureAdvertiser interface {
	// AdvertiseJoiner publishes replicaID's address and claimed features.
	// Implementations filter features to names they know before writing
	// anything -- an unknown name is never trusted onto the wire of the
	// advertisement record -- and bound how many they accept.
	AdvertiseJoiner(ctx context.Context, replicaID uint64, raftAddress string, features []string) error
}

// HandleJoinRequest is the transport-agnostic core of member-side CSR
// signing, shared by every transport binding (Noise_NK's JoinServer, the
// SPKI/Direct HTTPS route in pkg/api/rest): CSR parse and
// proof-of-possession check, THEN single-use token consumption
// (ConsumeJoinToken), THEN a reservation of the bound identity
// (ReserveJoinIdentity) BEFORE issuance through the cluster CA provider
// FOR THE TOKEN'S BOUND IDENTITY -- req.NodeURI is never consulted; a
// token binds the identity, the joiner does not get to assert one of its
// own choosing. It never reads a private key out of provider --
// IssueLeafForKey signs with it, nothing more -- and the only error
// detail returned to the wire is the single, cause-independent reason
// ConsumeJoinToken reports (no token-validity oracle).
//
// The CSR and its proof of possession are checked BEFORE the token is
// touched: a malformed CSR or a failed signature check is a defect in THIS
// attempt, not evidence the token is bad, and the joiner can retry with a
// corrected CSR and the SAME token. Burning the token on a CSR-shaped
// problem would turn a client-side bug into a forced re-mint.
//
// The full sequence once the CSR is known-good:
//
//  1. ConsumeJoinToken burns the token and resolves its bound identity.
//  2. ReserveJoinIdentity atomically claims that identity, BEFORE signing.
//     A second, independently-and-validly minted token for the SAME
//     identity -- two concurrent join attempts racing each other -- is
//     refused HERE, not by both reaching IssueLeafForKey and racing to
//     mark admission afterward: exactly one reservation can succeed.
//  3. IssueLeafForKey signs the leaf.
//  4. On a SIGNING failure, ReleaseJoinIdentityReservation frees the
//     reservation: the identity was never actually admitted, so a fresh
//     token minted for it can retry. A transient device/network fault,
//     or the barrier sealing mid-request, must not retire the node ID --
//     the reservation step is what makes that safe to promise even with
//     two concurrent attempts, which an unordered consume-then-mark could
//     not.
//  5. On a signing SUCCESS, PromoteJoinIdentityAdmitted swaps the
//     reservation for a durable admitted record. If THAT fails (the
//     promotion's own outcome is unknown, or the reservation was not
//     there in the state it expected), the issue is refused -- the leaf
//     already signed is discarded, never returned -- rather than handing
//     out a certificate this package cannot confirm is correctly
//     reflected in the ledger MintJoinToken's own already-admitted check
//     reads.
//
// store is the replicated kvstore the signing member's join tokens are
// minted into (see MintJoinToken) -- the SAME store cakey.SoftwareProvider
// persists Key A through, reached via the same kvstore.AtomicKVStore seam.
// trustDomain is this member's own pinned cluster trust domain: a token
// bound to any other trust domain is refused by ConsumeJoinToken.
//
// advertiser, when non-nil, publishes a pre-membership feature advertisement
// for the joiner once its leaf is issued (see FeatureAdvertiser): without
// one, a brand-new node has no live advertisement for featuregate.Gate to
// find, so member add refuses it with *featuregate.UnsupportedReplicaError
// the moment any cluster feature has activated -- the node's own binary
// cannot advertise for itself before it is a member. A failure to advertise
// is logged, not returned: the certificate this call already signed is
// still good, and the operator's member add simply surfaces the same
// refusal it would have without this parameter at all.
func HandleJoinRequest(ctx context.Context, provider cakey.Provider, store kvstore.AtomicKVStore, trustDomain string, req *JoinRequest, advertiser FeatureAdvertiser) *JoinResponse {
	// Checked BEFORE the token is consumed, and on failure reported with
	// the SAME generic message the infrastructure-failure branch below
	// uses: a sealed node (or any other reason provider cannot currently
	// sign) must refuse identically whether the presented token is valid
	// or not. Consuming a token first and discovering only afterward that
	// signing is unavailable would both burn a legitimately minted
	// single-use token for nothing, and -- the actual oracle -- let a
	// prober learn "my token was accepted" from a response distinguishable
	// from "token rejected," with no join to show for it either way.
	if _, err := provider.Signer(ctx); err != nil {
		return &JoinResponse{Error: "join request could not be processed"}
	}

	csr, err := x509.ParseCertificateRequest(req.CSRDER)
	if err != nil {
		return &JoinResponse{Error: "invalid certificate signing request"}
	}
	if err := csr.CheckSignature(); err != nil {
		return &JoinResponse{Error: "certificate signing request: proof of possession failed"}
	}
	// Checked here, BEFORE the token is consumed, for the same reason the
	// Signer availability check above runs first: a weak CSR key is a
	// property of the REQUEST, not of the token, and discovering it only
	// inside IssueLeafForKey (after ConsumeJoinToken already burned a
	// single-use token) would cost the joiner its token for a request
	// that was never going to be signed. IssueLeafForKey runs the
	// identical check again before it signs, so a caller that reaches it
	// some other way is still covered. The same check protects the
	// tokenless (approval-queue) path below, before anything is queued.
	if err := clusterca.CheckPublicKeyStrength(csr.PublicKey); err != nil {
		return &JoinResponse{Error: "certificate signing request: key is not strong enough for a cluster-member leaf"}
	}
	// Checked here, before the token is touched, for the same reason: the
	// CSR's own subject alternative names are a property of THIS request,
	// not of the token. See validateJoinCSRIdentity.
	if err := validateJoinCSRIdentity(csr, req.NodeURI); err != nil {
		return &JoinResponse{Error: err.Error()}
	}

	// No token presented: this is the approval-queue path (see
	// pending_join.go), not a refusal -- architecture.md's "the join token
	// (or a future approval workflow)" decides admission here instead.
	if req.Token == "" {
		return handleTokenlessJoin(ctx, provider, store, csr, req, advertiser)
	}

	boundNodeURI, err := ConsumeJoinToken(ctx, store, trustDomain, req.Token)
	if err != nil {
		var refused *JoinRefusedError
		if errors.As(err, &refused) {
			return &JoinResponse{Error: refused.Reason}
		}
		// A JoinTokenStoreError (or anything else ConsumeJoinToken might
		// return): a genuine infrastructure failure, not a refusal -- it
		// carries no information about the presented token, so reporting
		// it distinctly from "join token rejected" is not an oracle.
		return &JoinResponse{Error: "join request could not be processed"}
	}

	return issueAndAdmit(ctx, provider, store, boundNodeURI, csr, req.ReplicaID, req.Features, advertiser)
}

// issueAndAdmit runs the reserve -> sign -> promote -> advertise sequence
// shared by the token path and the approved tokenless path (see
// handleTokenlessJoin/deliverApprovedJoin in pending_join.go), once each has
// independently decided which identity (boundNodeURI) the leaf must carry.
// See ReserveJoinIdentity, PromoteJoinIdentityAdmitted and
// ReleaseJoinIdentityReservation's own doc comments for why each step runs
// in this exact order.
func issueAndAdmit(ctx context.Context, provider cakey.Provider, store kvstore.AtomicKVStore, boundNodeURI string, csr *x509.CertificateRequest, replicaID uint64, features []string, advertiser FeatureAdvertiser) *JoinResponse {
	reserved, pending, err := ReserveJoinIdentity(ctx, store, boundNodeURI, 0)
	if err != nil {
		return &JoinResponse{Error: "join request could not be processed"}
	}
	if !reserved {
		// Lost the race to a concurrent request for the SAME identity
		// (a second, independently valid token or approval), or the
		// identity is already admitted or still within a live
		// reservation. The token this request presented, if any, is
		// already consumed either way -- see ConsumeJoinToken's own doc
		// comment on why that is the correct trade, not a bug to fix here.
		return &JoinResponse{Error: "join request could not be processed"}
	}

	certPEM, err := clusterca.IssueLeafForKey(ctx, provider, csr.PublicKey, boundNodeURI)
	if err != nil {
		if _, relErr := ReleaseJoinIdentityReservation(ctx, store, boundNodeURI, pending); relErr != nil {
			slog.ErrorContext(ctx, "member-side CSR join: signing failed and the reservation could not be released",
				"node_uri", boundNodeURI, "sign_error", err, "release_error", relErr)
		}
		return &JoinResponse{Error: "signing failed"}
	}

	leafNotAfter, leafSerial, err := parseLeafExpiry(certPEM)
	if err != nil {
		if _, relErr := ReleaseJoinIdentityReservation(ctx, store, boundNodeURI, pending); relErr != nil {
			slog.ErrorContext(ctx, "member-side CSR join: could not read back the issued leaf's own expiry, and the reservation could not be released",
				"node_uri", boundNodeURI, "parse_error", err, "release_error", relErr)
		}
		slog.ErrorContext(ctx, "member-side CSR join: signed a leaf but could not parse its own expiry back; refusing the issue",
			"node_uri", boundNodeURI, "error", err)
		return &JoinResponse{Error: "join request could not be processed"}
	}

	if err := PromoteJoinIdentityAdmitted(ctx, store, boundNodeURI, pending, leafNotAfter, leafSerial); err != nil {
		// The leaf was signed, but this package cannot confirm the ledger
		// correctly reflects it -- refuse the issue rather than hand out a
		// certificate MintJoinToken's own already-admitted check might not
		// see. The reservation (or its absence) is left exactly as
		// PromoteJoinIdentityAdmitted left it; this is a genuine
		// infrastructure failure for an operator to investigate, not a
		// state this request can safely paper over by returning the leaf
		// anyway.
		slog.ErrorContext(ctx, "member-side CSR join: signed a leaf but could not promote the reservation to admitted; refusing the issue",
			"node_uri", boundNodeURI, "error", err)
		return &JoinResponse{Error: "join request could not be processed"}
	}

	if advertiser != nil && replicaID != 0 {
		raftAddress := boundNodeURI
		if u, perr := url.Parse(boundNodeURI); perr == nil {
			if identity, ok, idErr := spiffe.ParseNodeID(u); idErr == nil && ok {
				raftAddress = identity.NodeID
			}
		}
		if aerr := advertiser.AdvertiseJoiner(ctx, replicaID, raftAddress, features); aerr != nil {
			slog.ErrorContext(ctx, "member-side CSR join: signed and admitted the leaf, but could not publish its pre-membership feature advertisement; member add will refuse it until it advertises on its own",
				"node_uri", boundNodeURI, "replica_id", replicaID, "error", aerr)
		}
	}
	return &JoinResponse{CertPEM: certPEM}
}

// validateJoinCSRIdentity requires csr to carry the claimed node identity
// as its SOLE subject alternative name: exactly one URI SAN, equal to
// claimedNodeURI, and no DNS, IP or email SANs at all -- the shape every
// legitimate joiner's own buildJoinCSR produces (the CSR's URI and
// JoinRequest.NodeURI are always the SAME value there, both derived from
// spiffe.NodeURI). This runs BEFORE the token is touched, for the same
// reason the proof-of-possession and key-strength checks above it do: a
// CSR/envelope mismatch is a defect in THIS request, not evidence the
// token is bad, so a corrected resubmission with the SAME token must not
// be blocked by having burned it here.
//
// This does NOT make the bound identity trustworthy by itself --
// IssueLeafForKey still signs for the TOKEN's bound identity (resolved by
// ConsumeJoinToken), never for claimedNodeURI. It only catches a confused
// or tampered client, or a relay that altered one copy of the identity but
// not the other, before either reaches the token at all.
func validateJoinCSRIdentity(csr *x509.CertificateRequest, claimedNodeURI string) error {
	if claimedNodeURI == "" {
		return &JoinCSRIdentityError{Reason: "join request did not name a claimed node identity"}
	}
	if len(csr.DNSNames) > 0 || len(csr.IPAddresses) > 0 || len(csr.EmailAddresses) > 0 {
		return &JoinCSRIdentityError{Reason: "carries a DNS, IP or email subject alternative name; only a single URI SAN naming the claimed node identity is accepted"}
	}
	if len(csr.URIs) != 1 {
		return &JoinCSRIdentityError{Reason: "must carry exactly one URI subject alternative name"}
	}
	if csr.URIs[0].String() != claimedNodeURI {
		return &JoinCSRIdentityError{Reason: "subject alternative name does not match the join request's claimed node identity"}
	}
	return nil
}

// parseLeafExpiry extracts the serial number and NotAfter of a freshly
// issued leaf certificate, to record alongside its identity's admission
// (PromoteJoinIdentityAdmitted) so a LATER join attempt for the SAME
// identity can tell an active admission from a lapsed one without a
// second signing-time lookup.
func parseLeafExpiry(certPEM []byte) (notAfter time.Time, serial string, err error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return time.Time{}, "", &LeafExpiryParseError{Err: errors.New("issued leaf is not valid PEM")}
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, "", &LeafExpiryParseError{Err: err}
	}
	return cert.NotAfter, cert.SerialNumber.String(), nil
}

// JoinCSRIdentityError reports that a joiner's CSR does not carry the
// claimed node identity as its sole URI subject alternative name, or
// carries a SAN of a kind HandleJoinRequest never trusts. See
// validateJoinCSRIdentity.
type JoinCSRIdentityError struct {
	Reason string
}

// Error returns a human-readable description.
func (e *JoinCSRIdentityError) Error() string {
	return "clusterenroll: certificate signing request: " + e.Reason
}

// LeafExpiryParseError reports that HandleJoinRequest could not parse the
// NotAfter/serial of the leaf it had just itself issued (IssueLeafForKey)
// -- an internal invariant violation, never caused by anything the joiner
// submitted.
type LeafExpiryParseError struct {
	Err error
}

// Error returns a human-readable description.
func (e *LeafExpiryParseError) Error() string {
	return "clusterenroll: could not parse the issued leaf's own expiry: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *LeafExpiryParseError) Unwrap() error { return e.Err }

// joinServerMaxConcurrentConns bounds how many Noise_NK handshakes a
// JoinServer services at once. An attacker that can open connections
// faster than they are serviced would otherwise grow one goroutine per
// connection without limit; this caps that at a number generously above any
// legitimate join burst (a fleet bringing up many nodes at once) while
// still bounding worst-case resource use.
const joinServerMaxConcurrentConns = 64

// JoinServer is a running member-side CSR-sign listener. It must be stopped
// during graceful shutdown.
type JoinServer struct {
	ln net.Listener

	// key is the Noise_NK suite's static key, nil where the suite is
	// unavailable (see noiseAvailable). p256Key is the approved suite's
	// responder key and is always set.
	key         *noiseproto.StaticKey
	p256Key     *p256proto.StaticKey
	store       kvstore.AtomicKVStore
	trustDomain string
	provider    cakey.Provider
	advertiser  FeatureAdvertiser
	logger      *slog.Logger
	stopOnce    sync.Once
	wg          sync.WaitGroup

	// conns bounds concurrent in-flight handshakes; see
	// joinServerMaxConcurrentConns.
	conns chan struct{}
}

// StartJoinServer starts the member-side CSR-sign listener. provider is the
// cluster CA (Key A); any cakey.Provider works, including an injected
// HSM/TPM/KMS one.
//
// The listener serves both suites of the join exchange, chosen per connection
// by the first frame's version||suite header: the FIPS-approved P-256 suite is
// always served, and the Noise_NK suite -- whose X25519 handshake is not an
// approved key-agreement scheme -- only where noiseAvailable allows it. In
// FIPS 140-3 mode the listener therefore runs P-256 only and generates no
// Noise key.
func StartJoinServer(provider cakey.Provider, opts JoinServerOptions, dataDir string, logger *slog.Logger) (*JoinServer, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, &NilProviderError{}
	}

	var key *noiseproto.StaticKey
	if noiseAvailable() {
		var err error
		key, err = loadOrGenerateJoinKey(filepath.Join(dataDir, JoinKeyFile), logger)
		if err != nil {
			return nil, err
		}
	} else {
		logger.Warn(fipsNoiseAdvisory + "; the member-side CSR join listener serves the " +
			"FIPS-approved P-256 suite only")
	}

	p256Key, err := loadOrGenerateP256Key(filepath.Join(dataDir, JoinP256KeyFile), "join", "join", logger)
	if err != nil {
		key.Destroy()
		return nil, err
	}

	addr := net.JoinHostPort(opts.Host, strconv.Itoa(opts.port()))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		key.Destroy()
		p256Key.Destroy()
		return nil, &ServerError{Protocol: "join", Err: err}
	}

	js := &JoinServer{
		ln:          ln,
		key:         key,
		p256Key:     p256Key,
		store:       opts.Store,
		trustDomain: opts.TrustDomain,
		provider:    provider,
		advertiser:  opts.Advertiser,
		logger:      logger,
		conns:       make(chan struct{}, joinServerMaxConcurrentConns),
	}
	js.wg.Add(1)
	go js.serve()

	attrs := []any{
		"addr", ln.Addr().String(),
		"admin_port", opts.AdminPort,
		"join_p256_public_key", hex.EncodeToString(js.P256PublicKey()),
	}
	if pub := js.PublicKey(); pub != nil {
		attrs = append(attrs, "join_noise_public_key", hex.EncodeToString(pub))
	}
	logger.Info("member-join CSR listener started", attrs...)
	return js, nil
}

// NilProviderError is returned when a nil cakey.Provider is supplied to
// StartJoinServer. A nil seam refuses rather than starting a listener that
// can never sign anything.
type NilProviderError struct{}

// Error returns a human-readable description.
func (e *NilProviderError) Error() string {
	return "clusterenroll: nil cakey.Provider"
}

// Addr returns the listener's bound address, or empty string on a nil
// JoinServer.
func (s *JoinServer) Addr() string {
	if s == nil || s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// PublicKey returns the join listener's Curve25519 public key, the value a
// joiner configures as its dial target's Noise identity. Returns nil on a nil
// JoinServer and where the Noise suite is unavailable (FIPS 140-3 mode).
func (s *JoinServer) PublicKey() []byte {
	if s == nil || s.key == nil {
		return nil
	}
	return s.key.PublicBytes()
}

// P256PublicKey returns the join listener's P-256 responder public key as the
// 65-byte uncompressed point a joiner configures as its dial target's
// FIPS-approved identity. Returns nil on a nil JoinServer.
func (s *JoinServer) P256PublicKey() []byte {
	if s == nil || s.p256Key == nil {
		return nil
	}
	return s.p256Key.PublicBytes()
}

// Stop gracefully shuts down the listener and releases the static key's
// protected pages. Safe to call on a nil receiver and more than once.
func (s *JoinServer) Stop() {
	if s == nil || s.ln == nil {
		return
	}
	s.stopOnce.Do(func() {
		_ = s.ln.Close()
		s.wg.Wait()
		s.key.Destroy()
		s.p256Key.Destroy()
	})
}

// sourceAddr extracts the host portion of addr for attempt-guard keying,
// so a rate limit or lockout scopes to a source IP rather than to one
// ephemeral port of a many-port attacker. addr may be nil (an already-
// closed or unusual connection); the zero value groups those together
// rather than panicking.
func sourceAddr(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

// serve accepts connections until the listener is closed.
func (s *JoinServer) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			select {
			case s.conns <- struct{}{}:
				defer func() { <-s.conns }()
			default:
				// At joinServerMaxConcurrentConns already: refuse rather
				// than queue behind a potentially unbounded flood, or stall
				// Accept for every other caller.
				s.logger.Debug("join: refused, at max concurrent connections",
					"max", joinServerMaxConcurrentConns)
				_ = conn.Close()
				return
			}
			s.handle(conn)
		}()
	}
}

// handle drives one connection's handshake (as responder, in whichever suite the
// first frame's header selects) and one encrypted request/response exchange,
// then closes it. conn is any
// net.Conn, which is what lets a test drive this with an in-memory fake
// that fails a specific Read or Write deterministically, rather than racing
// real socket timing to reach these branches.
func (s *JoinServer) handle(conn net.Conn) {
	defer conn.Close()
	deadline := time.Now().Add(DefaultTimeout)
	source := sourceAddr(conn.RemoteAddr())

	if !AllowJoinAttempt(source) {
		s.logger.Debug("join: refused, source is rate-limited or locked out", "source", source)
		return
	}

	// The first frame opens with the version||suite header; it selects the
	// handshake, and both suites bind it into their transcript so a tampered
	// header breaks the handshake rather than downgrading it.
	first, err := noiseboot.ReadFrame(conn, deadline)
	if err != nil {
		s.logger.Debug("join: read handshake message 1 failed", "error", err)
		return
	}
	if len(first) < joinSuiteHeaderSize || first[0] != p256proto.Version {
		s.logger.Debug("join: refused, unsupported protocol version or short first frame")
		return
	}
	handshake, ok := joinServerSuites[first[1]]
	if !ok {
		s.logger.Debug("join: refused, unsupported suite", "suite", first[1])
		return
	}
	ch, msg2, release, err := handshake(s, first)
	if err != nil {
		s.logger.Debug("join: handshake failed", "suite", first[1], "error", err)
		return
	}
	defer release()
	if err := noiseboot.WriteFrame(conn, msg2, deadline); err != nil {
		s.logger.Debug("join: write handshake message 2 failed", "error", err)
		return
	}

	ciphertext, err := noiseboot.ReadFrame(conn, deadline)
	if err != nil {
		s.logger.Debug("join: read request failed", "error", err)
		return
	}
	plaintext, err := ch.Decrypt(ciphertext)
	if err != nil {
		s.logger.Debug("join: decrypt request failed", "error", err)
		return
	}

	var req JoinRequest
	resp := &JoinResponse{}
	if err := json.Unmarshal(plaintext, &req); err != nil {
		resp.Error = "invalid request"
	} else {
		resp = HandleJoinRequest(context.Background(), s.provider, s.store, s.trustDomain, &req, s.advertiser)
	}
	if resp.Error != "" {
		RecordJoinAttemptFailure(source)
	} else {
		RecordJoinAttemptSuccess(source)
	}

	respData, err := json.Marshal(resp)
	if err != nil {
		s.logger.Error("join: marshal response failed", "error", err)
		return
	}
	respCiphertext, err := ch.Encrypt(respData)
	if err != nil {
		s.logger.Error("join: encrypt response failed", "error", err)
		return
	}
	if err := noiseboot.WriteFrame(conn, respCiphertext, deadline); err != nil {
		s.logger.Debug("join: write response failed", "error", err)
	}
}

// joinSuiteHeaderSize is the length of the version||suite header that opens a
// join exchange's first frame.
const joinSuiteHeaderSize = 2

// joinChannel is the encrypted request/response channel a completed handshake
// yields, abstracting the two suites so the exchange is written once. Each
// direction carries exactly one message, which both backings satisfy.
type joinChannel interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}

// p256Channel adapts a p256proto one-shot Transport to joinChannel.
type p256Channel struct {
	t *p256proto.Transport
}

func (c *p256Channel) Encrypt(plaintext []byte) ([]byte, error) { return c.t.Seal(plaintext, nil) }

func (c *p256Channel) Decrypt(ciphertext []byte) ([]byte, error) { return c.t.Open(ciphertext, nil) }

// joinServerSuites dispatches a first frame's suite byte to the responder
// handshake for that suite. A byte absent from the map is a suite this listener
// does not speak.
var joinServerSuites = map[byte]func(*JoinServer, []byte) (joinChannel, []byte, func(), error){
	p256proto.SuiteNoiseNK:    (*JoinServer).handshakeNoise,
	p256proto.SuiteP256AESGCM: (*JoinServer).handshakeP256,
}

// JoinSuiteUnavailableError is returned when a join exchange selects a suite
// this process cannot run: the Noise suite on a listener that holds no Noise
// key because FIPS 140-3 mode is enabled.
type JoinSuiteUnavailableError struct {
	Suite byte
}

// Error returns a human-readable description.
func (e *JoinSuiteUnavailableError) Error() string {
	return "clusterenroll: join suite 0x" + strconv.FormatUint(uint64(e.Suite), 16) +
		" is unavailable in this process"
}

// handshakeNoise answers a Noise_NK first frame. The version||suite header is
// the Noise prologue, so the two ends agree on it or the handshake fails.
func (s *JoinServer) handshakeNoise(first []byte) (joinChannel, []byte, func(), error) {
	if s.key == nil || !noiseAvailable() {
		return nil, nil, nil, &JoinSuiteUnavailableError{Suite: p256proto.SuiteNoiseNK}
	}
	sess, err := noiseproto.NewSession(&noiseproto.SessionConfig{
		Pattern:        noise.HandshakeNK,
		LocalStaticKey: s.key,
		IsInitiator:    false,
		Prologue:       first[:joinSuiteHeaderSize:joinSuiteHeaderSize],
	})
	if err != nil {
		return nil, nil, nil, err
	}
	if err := sess.InitHandshake(); err != nil {
		sess.Destroy()
		return nil, nil, nil, err
	}
	msg2, done, err := sess.HandshakeMessage(first[joinSuiteHeaderSize:])
	if err != nil || !done {
		sess.Destroy()
		if err == nil {
			err = ErrHandshakeIncomplete
		}
		return nil, nil, nil, err
	}
	return sess, msg2, sess.Destroy, nil
}

// handshakeP256 answers a P-256 first frame. Message 1 carries the
// version||suite header itself.
func (s *JoinServer) handshakeP256(first []byte) (joinChannel, []byte, func(), error) {
	r, err := p256proto.NewResponder(s.p256Key)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := r.ReadMessage1(first); err != nil {
		r.Destroy()
		return nil, nil, nil, err
	}
	msg2, transport, err := r.WriteMessage2()
	if err != nil {
		r.Destroy()
		return nil, nil, nil, err
	}
	return &p256Channel{t: transport}, msg2, r.Destroy, nil
}

// JoinClientOptions configures a joiner's CSR submission to an admitted
// member's join listener.
type JoinClientOptions struct {
	// ServerAddr is the host:port of an admitted member's join listener.
	ServerAddr string

	// ServerKey is the join listener's Curve25519 public key
	// (JoinServer.PublicKey), for the Noise_NK suite. May be empty when
	// ServerP256Key is set.
	ServerKey []byte

	// ServerP256Key is the join listener's uncompressed P-256 public key
	// (JoinServer.P256PublicKey), for the FIPS-approved suite. May be empty when
	// ServerKey is set. With both set, Noise is used where this process may use
	// it and P-256 otherwise (FIPS 140-3 mode).
	ServerP256Key []byte

	// Token is the join-admission secret. Empty submits the CSR into the
	// signing member's replicated approval queue instead (see JoinCSR).
	Token string

	// ReplicaID is this node's own Raft replica ID, carried so the signing
	// member can publish a pre-membership feature advertisement for it (see
	// FeatureAdvertiser). Zero skips advertising.
	ReplicaID uint64

	// Features names the cluster features this node's binary implements
	// (featuregate.SupportedNames()). The signing member filters this to
	// names it knows before advertising anything.
	Features []string

	// Timeout bounds the dial, handshake and exchange. Zero uses DefaultTimeout.
	Timeout time.Duration
}

func (o *JoinClientOptions) timeout() time.Duration {
	if o.Timeout <= 0 {
		return DefaultTimeout
	}
	return o.Timeout
}

// JoinCSR dials an admitted member's join listener, performs the handshake (as
// initiator, pinned to opts.ServerKey for Noise_NK or opts.ServerP256Key for the
// FIPS-approved P-256 suite; see selectJoinSuite), and submits csrDER for
// signing. nodeURI, when non-empty, is carried to the signing member so the
// issued leaf carries it as a URI SAN (see clustertls.NodeLeafTemplate).
//
// Returns *JoinRefusedError when the member declines (bad token, invalid
// CSR, or a signing failure on its end) and *FetchError for a dial,
// handshake or framing failure. Neither this node's private key nor the
// cluster CA's ever appears on the wire in either direction.
func JoinCSR(ctx context.Context, opts *JoinClientOptions, csrDER []byte, nodeURI string) ([]byte, error) {
	suite, serverKey, err := selectJoinSuite(opts)
	if err != nil {
		return nil, err
	}

	dialer := net.Dialer{Timeout: opts.timeout()}
	conn, err := dialer.DialContext(ctx, "tcp", opts.ServerAddr)
	if err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}
	defer conn.Close()

	return joinClientSuites[suite](conn, serverKey, opts.timeout(), opts.Token, csrDER, nodeURI, opts.ReplicaID, opts.Features)
}

// joinClientSuites dispatches a selected suite to its initiator exchange.
var joinClientSuites = map[byte]func(net.Conn, []byte, time.Duration, string, []byte, string, uint64, []string) ([]byte, error){
	p256proto.SuiteNoiseNK:    joinClientExchange,
	p256proto.SuiteP256AESGCM: joinClientExchangeP256,
}

// MissingJoinServerKeyError is returned when a join client is configured with
// neither a Noise nor a P-256 server key to pin the listener's identity by.
type MissingJoinServerKeyError struct{}

// Error returns a human-readable description.
func (e *MissingJoinServerKeyError) Error() string {
	return "clusterenroll: no join listener key configured; member-side CSR join requires " +
		"the listener's P-256 public key (--bootstrap-join-p256-key) or its Noise public key (--bootstrap-join-key)"
}

// selectJoinSuite chooses the suite a join client offers and the server key
// that suite pins. Noise is used when its key is configured and this process
// may use it; otherwise P-256 when its key is configured. A client that holds
// only a Noise key in FIPS 140-3 mode is refused, before any connection, with
// *NoiseFIPSUnavailableError rather than being downgraded or handed an
// X25519 failure.
func selectJoinSuite(opts *JoinClientOptions) (byte, []byte, error) {
	switch {
	case len(opts.ServerKey) > 0 && noiseAvailable():
		return p256proto.SuiteNoiseNK, opts.ServerKey, nil
	case len(opts.ServerP256Key) > 0:
		return p256proto.SuiteP256AESGCM, opts.ServerP256Key, nil
	case len(opts.ServerKey) > 0:
		return 0, nil, &NoiseFIPSUnavailableError{Operation: "member-side CSR join"}
	default:
		return 0, nil, &MissingJoinServerKeyError{}
	}
}

// joinClientExchange drives the initiator side of one join exchange over an
// already-established conn: the Noise_NK handshake, then the encrypted
// request/response. Separated from JoinCSR so a test can drive it with an
// in-memory net.Conn fake that fails a specific Read or Write
// deterministically, rather than racing real socket timing to reach a
// frame-failure branch. replicaID and features are carried to the signing
// member so it can publish a pre-membership feature advertisement (see
// FeatureAdvertiser); replicaID zero skips it.
func joinClientExchange(conn net.Conn, serverKey []byte, timeout time.Duration, token string, csrDER []byte, nodeURI string, replicaID uint64, features []string) ([]byte, error) {
	deadline := time.Now().Add(timeout)

	// The version||suite header is both the first bytes of the first frame and
	// the Noise prologue, so a header altered in flight breaks the handshake.
	header := noiseboot.NoiseSuiteHeader()
	sess, err := noiseproto.NewSession(&noiseproto.SessionConfig{
		Pattern:       noise.HandshakeNK,
		PeerStaticKey: serverKey,
		IsInitiator:   true,
		Prologue:      header,
	})
	if err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}
	defer sess.Destroy()
	if err := sess.InitHandshake(); err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}

	msg1, _, err := sess.HandshakeMessage(nil)
	if err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}
	if err := noiseboot.WriteFrame(conn, append(header, msg1...), deadline); err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}

	msg2, err := noiseboot.ReadFrame(conn, deadline)
	if err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}
	if _, done, err := sess.HandshakeMessage(msg2); err != nil || !done {
		if err == nil {
			err = ErrHandshakeIncomplete
		}
		return nil, &FetchError{Method: "join", Err: err}
	}

	return joinClientRequest(conn, sess, deadline, token, csrDER, nodeURI, replicaID, features)
}

// joinClientExchangeP256 drives the initiator side of one join exchange over an
// already-established conn using the FIPS-approved P-256 suite
// (TSBOOT1_P256_AESGCM_SHA256), pinned to serverKey, the listener's
// uncompressed P-256 public key. It has the same shape as joinClientExchange
// so the two dispatch from one table.
func joinClientExchangeP256(conn net.Conn, serverKey []byte, timeout time.Duration, token string, csrDER []byte, nodeURI string, replicaID uint64, features []string) ([]byte, error) {
	deadline := time.Now().Add(timeout)

	initiator, err := p256proto.NewInitiator(serverKey)
	if err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}
	defer initiator.Destroy()

	msg1, err := initiator.WriteMessage1()
	if err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}
	if err := noiseboot.WriteFrame(conn, msg1, deadline); err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}

	msg2, err := noiseboot.ReadFrame(conn, deadline)
	if err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}
	transport, err := initiator.ReadMessage2(msg2)
	if err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}

	return joinClientRequest(conn, &p256Channel{t: transport}, deadline, token, csrDER, nodeURI, replicaID, features)
}

// joinClientRequest sends the join request over a completed handshake's
// channel and decodes the member's answer. It is the suite-independent half of
// the initiator exchange.
func joinClientRequest(conn net.Conn, ch joinChannel, deadline time.Time, token string, csrDER []byte, nodeURI string, replicaID uint64, features []string) ([]byte, error) {
	req := JoinRequest{Token: token, CSRDER: csrDER, NodeURI: nodeURI, ReplicaID: replicaID, Features: features}
	reqData, err := json.Marshal(req)
	if err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}
	ciphertext, err := ch.Encrypt(reqData)
	if err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}
	if err := noiseboot.WriteFrame(conn, ciphertext, deadline); err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}

	respCiphertext, err := noiseboot.ReadFrame(conn, deadline)
	if err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}
	plaintext, err := ch.Decrypt(respCiphertext)
	if err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}

	var resp JoinResponse
	if err := json.Unmarshal(plaintext, &resp); err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}
	if resp.Error != "" {
		return nil, &JoinRefusedError{Reason: resp.Error}
	}
	if resp.Pending {
		return nil, &JoinPendingError{RequestID: resp.RequestID}
	}
	return resp.CertPEM, nil
}

// ErrHandshakeIncomplete is returned when a Noise_NK handshake's final
// message did not complete the session.
var ErrHandshakeIncomplete = &handshakeIncompleteError{}

type handshakeIncompleteError struct{}

func (e *handshakeIncompleteError) Error() string {
	return "clusterenroll: Noise_NK handshake did not complete"
}

// loadOrGenerateJoinKey loads the join listener's static key from keyPath,
// generating and persisting a new one if absent. It mirrors
// startNoiseAdvertiser's key handling exactly, under its own file so the
// join listener's identity is independent of the anchor advertiser's.
func loadOrGenerateJoinKey(keyPath string, logger *slog.Logger) (*noiseproto.StaticKey, error) {
	if fileExists(keyPath) {
		hexData, err := readFileFn(keyPath)
		if err != nil {
			return nil, &ServerError{Protocol: "join", Err: err}
		}
		trimmed := bytes.TrimSpace(hexData)
		key, err := noiseproto.DecodeStaticKey(trimmed)
		noiseproto.WipeBytes(hexData)
		if err != nil {
			return nil, &ServerError{Protocol: "join", Err: err}
		}
		logger.Info("loaded existing join static key", "path", keyPath)
		return key, nil
	}

	key, err := noiseproto.GenerateStaticKey()
	if err != nil {
		return nil, &ServerError{Protocol: "join", Err: err}
	}
	hexEncoded := make([]byte, noiseproto.HexKeySize)
	defer noiseproto.WipeBytes(hexEncoded)
	if err := noiseproto.EncodeStaticKey(hexEncoded, key); err != nil {
		key.Destroy()
		return nil, &ServerError{Protocol: "join", Err: err}
	}
	dir := filepath.Dir(keyPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		key.Destroy()
		return nil, &ServerError{Protocol: "join", Err: err}
	}
	if err := securefile.WriteFileAtomic(keyPath, hexEncoded, 0o600); err != nil {
		key.Destroy()
		return nil, &ServerError{Protocol: "join", Err: err}
	}
	logger.Info("generated and persisted join static key", "path", keyPath)
	return key, nil
}
