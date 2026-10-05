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

package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"sync"
	"time"

	quicapi "github.com/jeremyhahn/go-qrdb/pkg/api/quic"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// inProcessForwarding holds the frame forwarder the node's own embedded
// transport forwards with (docs/replica-forwarding: embedded forwards over
// QUIC). It is built on first use, from the same TLS identity and forwarding
// configuration the QUIC listener forwards with, and closed with the server.
type inProcessForwarding struct {
	mu      sync.Mutex
	f       *quicapi.FrameForwarder
	stopped bool
}

// EmbeddedForwardingStoppedError reports a forward asked of a server that has
// stopped: its forwarder's connections are closed.
type EmbeddedForwardingStoppedError struct{}

// Error returns a human-readable description.
func (e *EmbeddedForwardingStoppedError) Error() string {
	return "server: replica forwarding stopped with the server"
}

// CallerCertificateError reports an in-process caller's certificate that does
// not verify to the CA pool every listener verifies a client against.
type CallerCertificateError struct {
	Reason string
	Err    error
}

// Error returns a human-readable description.
func (e *CallerCertificateError) Error() string {
	if e.Err == nil {
		return "server: caller certificate: " + e.Reason
	}
	return "server: caller certificate: " + e.Reason + ": " + e.Err.Error()
}

// Unwrap returns the verification failure.
func (e *CallerCertificateError) Unwrap() error { return e.Err }

// CallerAuthUnavailableError reports an in-process caller that cannot be
// authenticated because the server has no authentication chain.
type CallerAuthUnavailableError struct{}

// Error returns a human-readable description.
func (e *CallerAuthUnavailableError) Error() string {
	return "server: no authentication chain to authenticate the caller with (server.WithAuthMiddleware)"
}

// ReplicaForwarder returns the frame forwarder the node's embedded transport
// forwards a call it refused as a non-replica with, built on first use. It
// returns (nil, nil) when replica forwarding is off -- not composed, or no CA
// pool to verify a replica with -- and the embedded call is then answered the
// refusal.
func (s *Server) ReplicaForwarder() (*quicapi.FrameForwarder, error) {
	cfg, ok := s.replicaForwarding()
	if !ok {
		return nil, nil
	}
	s.inProcess.mu.Lock()
	defer s.inProcess.mu.Unlock()
	if s.inProcess.stopped {
		return nil, &EmbeddedForwardingStoppedError{}
	}
	if s.inProcess.f != nil {
		return s.inProcess.f, nil
	}
	tlsCfg, err := s.resolveTransportTLS("QUIC", s.config.Server.Host)
	if err != nil {
		return nil, err
	}
	s.inProcess.f = quicapi.NewFrameForwarder(cfg, tlsCfg)
	return s.inProcess.f, nil
}

// stopReplicaForwarder closes the embedded transport's forwarder, if it was
// built; a later ReplicaForwarder reports the server stopped.
func (s *Server) stopReplicaForwarder() {
	s.inProcess.mu.Lock()
	defer s.inProcess.mu.Unlock()
	s.inProcess.stopped = true
	if s.inProcess.f != nil {
		s.inProcess.f.Close()
		s.inProcess.f = nil
	}
}

// AuthenticateCertificate authenticates an in-process caller holding the
// client certificate chain (leaf first) exactly as the QUIC listener
// authenticates the certificate a client presents in its handshake: the chain
// must verify, for client authentication, to the CA pool every listener
// verifies a client against (WithCACertPool), and the identity is the one the
// server's authentication chain derives from it -- the mTLS authenticator, and
// every authenticator a composer added. It is how the embedded transport
// learns the identity its embedder configured to forward as.
func (s *Server) AuthenticateCertificate(ctx context.Context, chain []*x509.Certificate) (*auth.Identity, error) {
	if s.opts.authMW == nil {
		return nil, &CallerAuthUnavailableError{}
	}
	if len(chain) == 0 || chain[0] == nil {
		return nil, &CallerCertificateError{Reason: "no certificate"}
	}
	if s.opts.caCertPool == nil {
		return nil, &CallerCertificateError{Reason: "no CA pool to verify it with (server.WithCACertPool)"}
	}
	intermediates := x509.NewCertPool()
	for _, c := range chain[1:] {
		if c != nil {
			intermediates.AddCert(c)
		}
	}
	verified, err := chain[0].Verify(x509.VerifyOptions{
		Roots:         s.opts.caCertPool,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		CurrentTime:   time.Now(),
	})
	if err != nil {
		return nil, &CallerCertificateError{Reason: "does not verify", Err: err}
	}
	state := &tls.ConnectionState{
		Version:           tls.VersionTLS13,
		HandshakeComplete: true,
		PeerCertificates:  chain,
		VerifiedChains:    verified,
	}
	_, id, err := s.opts.authMW.Authenticate(auth.WithTLSState(ctx, state))
	if err != nil {
		return nil, err
	}
	return id, nil
}
