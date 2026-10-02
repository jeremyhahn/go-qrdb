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
	"crypto/subtle"
	"crypto/x509"
	"encoding/json"
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

	// Token is the operator-chosen join-admission secret, checked inside
	// the Noise-encrypted channel before any CSR is parsed. Required --
	// there is no default, mirroring multicast discovery's PSK rule
	// (go-qrdb's "no default secret" convention).
	Token string

	// AdminPort is logged alongside the listener address for operator
	// convenience. Zero means unconfigured.
	AdminPort int
}

// validate checks the configuration before any I/O or key generation.
func (o JoinServerOptions) validate() error {
	if o.Host == "" {
		return &MissingBindHostError{Component: "member-join server"}
	}
	if o.Token == "" {
		return &MissingJoinTokenError{}
	}
	return nil
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

// joinRequest is the encrypted JSON payload a joiner sends after the
// Noise_NK handshake completes. CSRDER is a PKCS#10 CertificateRequest
// (self-signed, so the signing member can verify the joiner holds the
// matching private key) encoding ONLY the joiner's own public key -- the
// joiner's private key, like the cluster CA's, never crosses this wire.
type joinRequest struct {
	Token   string `json:"token"`
	CSRDER  []byte `json:"csr_der"`
	NodeURI string `json:"node_uri,omitempty"`
}

// joinResponse is the encrypted JSON payload a signing member sends back.
type joinResponse struct {
	CertPEM []byte `json:"cert_pem,omitempty"`
	Error   string `json:"error,omitempty"`
}

// handleJoinRequest is the transport-agnostic core of member-side CSR
// signing: constant-time token check, CSR parse and proof-of-possession
// check, then issuance through the cluster CA provider. It never reads a
// private key out of provider -- IssueLeafForKey signs with it, nothing
// more -- and the only error detail returned to the wire is a short,
// key-material-free reason.
func handleJoinRequest(ctx context.Context, provider cakey.Provider, token string, req *joinRequest) *joinResponse {
	if subtle.ConstantTimeCompare([]byte(token), []byte(req.Token)) != 1 {
		return &joinResponse{Error: "join token rejected"}
	}

	csr, err := x509.ParseCertificateRequest(req.CSRDER)
	if err != nil {
		return &joinResponse{Error: "invalid certificate signing request"}
	}
	if err := csr.CheckSignature(); err != nil {
		return &joinResponse{Error: "certificate signing request: proof of possession failed"}
	}

	var nodeURI []string
	if req.NodeURI != "" {
		nodeURI = []string{req.NodeURI}
	}
	certPEM, err := clusterca.IssueLeafForKey(ctx, provider, csr.PublicKey, nodeURI...)
	if err != nil {
		return &joinResponse{Error: "signing failed"}
	}
	return &joinResponse{CertPEM: certPEM}
}

// JoinServer is a running member-side CSR-sign listener. It must be stopped
// during graceful shutdown.
type JoinServer struct {
	ln       net.Listener
	key      *noiseproto.StaticKey
	token    string
	provider cakey.Provider
	logger   *slog.Logger
	stopOnce sync.Once
	wg       sync.WaitGroup
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

	js := &JoinServer{ln: ln, key: key, token: opts.Token, provider: provider, logger: logger}
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
			s.handle(conn)
		}()
	}
}

// handle drives one connection's Noise_NK handshake (as responder) and one
// encrypted request/response exchange, then closes it.
func (s *JoinServer) handle(conn net.Conn) {
	defer conn.Close()
	deadline := time.Now().Add(DefaultTimeout)

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

	var req joinRequest
	resp := &joinResponse{}
	if err := json.Unmarshal(plaintext, &req); err != nil {
		resp.Error = "invalid request"
	} else {
		resp = handleJoinRequest(context.Background(), s.provider, s.token, &req)
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

	deadline := time.Now().Add(opts.timeout())

	sess, err := noiseproto.NewSession(&noiseproto.SessionConfig{
		Pattern:       noise.HandshakeNK,
		PeerStaticKey: opts.ServerKey,
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

	req := joinRequest{Token: opts.Token, CSRDER: csrDER, NodeURI: nodeURI}
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

	var resp joinResponse
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
