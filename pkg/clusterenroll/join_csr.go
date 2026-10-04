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
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/flynn/noise"
	"github.com/jeremyhahn/go-truststrap/pkg/noiseproto"
	noiseboot "github.com/jeremyhahn/go-truststrap/pkg/noiseproto/bootstrap"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterca"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
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

// MissingJoinTokenError is returned when a member-join operation (server or
// client) is configured with no join token. There is no default: a shared
// secret with a shipped default is not an admission control.
type MissingJoinTokenError struct{}

// Error returns a human-readable description.
func (e *MissingJoinTokenError) Error() string {
	return "clusterenroll: no join token configured; member-side CSR join requires an " +
		"operator-chosen admission secret on both the signing member and the joiner"
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

// JoinRequest is the join-admission request payload: the encrypted JSON
// body a Noise_NK joiner sends after the handshake completes, and the JSON
// request body the SPKI/Direct HTTPS join route (pkg/api/rest) reads
// directly -- the same shape, carried over two different transports.
// CSRDER is a PKCS#10 CertificateRequest (self-signed, so the signing
// member can verify the joiner holds the matching private key) encoding
// ONLY the joiner's own public key -- the joiner's private key, like the
// cluster CA's, never crosses this wire on any transport.
type JoinRequest struct {
	Token   string `json:"token"`
	CSRDER  []byte `json:"csr_der"`
	NodeURI string `json:"node_uri,omitempty"`
}

// JoinResponse is the join-admission response payload, over either
// transport.
type JoinResponse struct {
	CertPEM []byte `json:"cert_pem,omitempty"`
	Error   string `json:"error,omitempty"`
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
func HandleJoinRequest(ctx context.Context, provider cakey.Provider, store kvstore.AtomicKVStore, trustDomain string, req *JoinRequest) *JoinResponse {
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
	// some other way is still covered.
	if err := clusterca.CheckPublicKeyStrength(csr.PublicKey); err != nil {
		return &JoinResponse{Error: "certificate signing request: key is not strong enough for a cluster-member leaf"}
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

	reserved, pending, err := ReserveJoinIdentity(ctx, store, boundNodeURI, 0)
	if err != nil {
		return &JoinResponse{Error: "join request could not be processed"}
	}
	if !reserved {
		// Lost the race to a concurrent request for the SAME identity
		// (a second, independently valid token), or the identity is
		// already admitted or still within a live reservation. The token
		// this request presented is already consumed either way -- see
		// ConsumeJoinToken's own doc comment on why that is the correct
		// trade, not a bug to fix here.
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

	if err := PromoteJoinIdentityAdmitted(ctx, store, boundNodeURI, pending); err != nil {
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
	return &JoinResponse{CertPEM: certPEM}
}

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
	ln          net.Listener
	key         *noiseproto.StaticKey
	store       kvstore.AtomicKVStore
	trustDomain string
	provider    cakey.Provider
	logger      *slog.Logger
	stopOnce    sync.Once
	wg          sync.WaitGroup

	// conns bounds concurrent in-flight handshakes; see
	// joinServerMaxConcurrentConns.
	conns chan struct{}
}

// StartJoinServer starts the member-side CSR-sign listener. provider is the
// cluster CA (Key A); any cakey.Provider works, including an injected
// HSM/TPM/KMS one. Returns *NoiseFIPSUnavailableError in FIPS 140-3 mode:
// Noise_NK's X25519 handshake is not an approved key-agreement scheme, and
// unlike the anchor advertiser (which still has SPKI/DANE/Direct to fall
// back to for serving the public CA certificate), member-side CSR signing
// has no approved-method fallback of its own yet.
func StartJoinServer(provider cakey.Provider, opts JoinServerOptions, dataDir string, logger *slog.Logger) (*JoinServer, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if provider == nil {
		return nil, &NilProviderError{}
	}
	if !noiseAvailable() {
		logger.Warn(fipsNoiseAdvisory + "; member-side CSR join is unavailable")
		return nil, &NoiseFIPSUnavailableError{Operation: "member-side CSR join"}
	}

	key, err := loadOrGenerateJoinKey(filepath.Join(dataDir, JoinKeyFile), logger)
	if err != nil {
		return nil, err
	}

	addr := net.JoinHostPort(opts.Host, strconv.Itoa(opts.port()))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		key.Destroy()
		return nil, &ServerError{Protocol: "join", Err: err}
	}

	js := &JoinServer{
		ln:          ln,
		key:         key,
		store:       opts.Store,
		trustDomain: opts.TrustDomain,
		provider:    provider,
		logger:      logger,
		conns:       make(chan struct{}, joinServerMaxConcurrentConns),
	}
	js.wg.Add(1)
	go js.serve()

	logger.Info("member-join CSR listener started",
		"addr", ln.Addr().String(), "admin_port", opts.AdminPort)
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
// joiner configures as its dial target's identity. Returns nil on a nil
// JoinServer.
func (s *JoinServer) PublicKey() []byte {
	if s == nil || s.key == nil {
		return nil
	}
	return s.key.Public
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

// handle drives one connection's Noise_NK handshake (as responder) and one
// encrypted request/response exchange, then closes it. conn is any
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

	sess, err := noiseproto.NewSession(&noiseproto.SessionConfig{
		Pattern:        noise.HandshakeNK,
		LocalStaticKey: &s.key.DHKey,
		IsInitiator:    false,
	})
	if err != nil {
		s.logger.Error("join: session init failed", "error", err)
		return
	}
	if err := sess.InitHandshake(); err != nil {
		s.logger.Error("join: handshake init failed", "error", err)
		return
	}

	msg1, err := noiseboot.ReadFrame(conn, deadline)
	if err != nil {
		s.logger.Debug("join: read handshake message 1 failed", "error", err)
		return
	}
	msg2, done, err := sess.HandshakeMessage(msg1)
	if err != nil || !done {
		s.logger.Debug("join: handshake failed", "error", err)
		return
	}
	if err := noiseboot.WriteFrame(conn, msg2, deadline); err != nil {
		s.logger.Debug("join: write handshake message 2 failed", "error", err)
		return
	}

	ciphertext, err := noiseboot.ReadFrame(conn, deadline)
	if err != nil {
		s.logger.Debug("join: read request failed", "error", err)
		return
	}
	plaintext, err := sess.Decrypt(ciphertext)
	if err != nil {
		s.logger.Debug("join: decrypt request failed", "error", err)
		return
	}

	var req JoinRequest
	resp := &JoinResponse{}
	if err := json.Unmarshal(plaintext, &req); err != nil {
		resp.Error = "invalid request"
	} else {
		resp = HandleJoinRequest(context.Background(), s.provider, s.store, s.trustDomain, &req)
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
	respCiphertext, err := sess.Encrypt(respData)
	if err != nil {
		s.logger.Error("join: encrypt response failed", "error", err)
		return
	}
	if err := noiseboot.WriteFrame(conn, respCiphertext, deadline); err != nil {
		s.logger.Debug("join: write response failed", "error", err)
	}
}

// JoinClientOptions configures a joiner's CSR submission to an admitted
// member's join listener.
type JoinClientOptions struct {
	// ServerAddr is the host:port of an admitted member's join listener.
	ServerAddr string

	// ServerKey is the join listener's Curve25519 public key (JoinServer.PublicKey).
	ServerKey []byte

	// Token is the join-admission secret. Required.
	Token string

	// Timeout bounds the dial, handshake and exchange. Zero uses DefaultTimeout.
	Timeout time.Duration
}

func (o *JoinClientOptions) timeout() time.Duration {
	if o.Timeout <= 0 {
		return DefaultTimeout
	}
	return o.Timeout
}

// JoinCSR dials an admitted member's join listener, performs the Noise_NK
// handshake (as initiator, pinned to opts.ServerKey), and submits csrDER for
// signing. nodeURI, when non-empty, is carried to the signing member so the
// issued leaf carries it as a URI SAN (see clustertls.NodeLeafTemplate).
//
// Returns *JoinRefusedError when the member declines (bad token, invalid
// CSR, or a signing failure on its end) and *FetchError for a dial,
// handshake or framing failure. Neither this node's private key nor the
// cluster CA's ever appears on the wire in either direction.
func JoinCSR(ctx context.Context, opts *JoinClientOptions, csrDER []byte, nodeURI string) ([]byte, error) {
	if opts.Token == "" {
		return nil, &MissingJoinTokenError{}
	}

	dialer := net.Dialer{Timeout: opts.timeout()}
	conn, err := dialer.DialContext(ctx, "tcp", opts.ServerAddr)
	if err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}
	defer conn.Close()

	return joinClientExchange(conn, opts.ServerKey, opts.timeout(), opts.Token, csrDER, nodeURI)
}

// joinClientExchange drives the initiator side of one join exchange over an
// already-established conn: the Noise_NK handshake, then the encrypted
// request/response. Separated from JoinCSR so a test can drive it with an
// in-memory net.Conn fake that fails a specific Read or Write
// deterministically, rather than racing real socket timing to reach a
// frame-failure branch.
func joinClientExchange(conn net.Conn, serverKey []byte, timeout time.Duration, token string, csrDER []byte, nodeURI string) ([]byte, error) {
	deadline := time.Now().Add(timeout)

	sess, err := noiseproto.NewSession(&noiseproto.SessionConfig{
		Pattern:       noise.HandshakeNK,
		PeerStaticKey: serverKey,
		IsInitiator:   true,
	})
	if err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}
	if err := sess.InitHandshake(); err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}

	msg1, _, err := sess.HandshakeMessage(nil)
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
	if _, done, err := sess.HandshakeMessage(msg2); err != nil || !done {
		if err == nil {
			err = ErrHandshakeIncomplete
		}
		return nil, &FetchError{Method: "join", Err: err}
	}

	req := JoinRequest{Token: token, CSRDER: csrDER, NodeURI: nodeURI}
	reqData, err := json.Marshal(req)
	if err != nil {
		return nil, &FetchError{Method: "join", Err: err}
	}
	ciphertext, err := sess.Encrypt(reqData)
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
	plaintext, err := sess.Decrypt(respCiphertext)
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
	if err := clustertls.WriteFileAtomic(keyPath, hexEncoded, 0o600); err != nil {
		key.Destroy()
		return nil, &ServerError{Protocol: "join", Err: err}
	}
	logger.Info("generated and persisted join static key", "path", keyPath)
	return key, nil
}
