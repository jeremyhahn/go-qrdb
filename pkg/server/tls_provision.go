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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
)

// autoTLSCATimeout bounds resolving the ceremony CA's certificate for an
// auto-provisioned listener's ClientCAs.
const autoTLSCATimeout = 10 * time.Second

// TLSUnavailableError is returned by a transport start path when it cannot
// obtain a TLS configuration: neither an explicit *tls.Config (WithTLS) nor a
// CeremonyCA (WithCeremonyCA) to auto-provision one from is available. qrdb
// transports are TLS-only, so the orchestrator fails closed rather than bind a
// plaintext listener — a sealed barrier or an uninitialized node (whose CA key
// is not yet accessible) surfaces here instead of silently downgrading to
// cleartext.
type TLSUnavailableError struct {
	// Transport names the protocol server that could not be secured
	// (e.g. "gRPC", "unix", "REST", "QUIC", "MCP").
	Transport string
}

// Error returns a human-readable description.
func (e *TLSUnavailableError) Error() string {
	return "server: cannot start " + e.Transport +
		" transport: no TLS configured and no CeremonyCA available to auto-provision one; " +
		"qrdb transports are TLS-only"
}

// resolveTransportTLS returns the TLS configuration a transport listener must
// use. qrdb transports never accept a plaintext connection, so this method
// always returns either a non-nil *tls.Config or an error:
//
//   - When the operator (or the daemon boot path) supplied an explicit TLS
//     config via WithTLS, that config is used verbatim. The receiving
//     transport's own WithTLS enforces the TLS 1.3 floor and rejects an
//     explicitly weaker MinVersion.
//   - Otherwise, when a CeremonyCA is available, a server certificate is
//     auto-provisioned from it (chained to the node's own CA) so the operator
//     never has to configure TLS by hand. The leaf binds the WithServerTLSKey
//     signer when one is composed in, else a fresh in-memory P-256 key.
//   - When neither is available the method fails closed with a
//     *TLSUnavailableError; the caller must NOT fall back to a plaintext
//     listener.
//
// transport names the protocol for diagnostics. extraSANs are appended to the
// auto-provisioned certificate's SANs (e.g. the configured advertise host) so
// remote clients that address the node by that name still verify the leaf.
func (s *Server) resolveTransportTLS(transport string, extraSANs ...string) (*tls.Config, error) {
	if s.opts.tlsConfig != nil {
		return s.opts.tlsConfig, nil
	}
	if s.opts.ceremonyCA == nil {
		return nil, &TLSUnavailableError{Transport: transport}
	}
	key := s.opts.serverTLSKey
	if key == nil {
		generated, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, &ceremony.ServerTLSError{Err: err}
		}
		key = generated
	}
	cfg, err := ceremony.ServerTLSConfig(context.Background(), s.opts.ceremonyCA, key, extraSANs...)
	if err != nil {
		return nil, err
	}
	// The auto-provisioned leaf is this node's own identity, so the
	// listener takes the node posture of ListenerClientAuth: it requests a
	// client certificate and verifies one if given, which a key delivery
	// (POST /v1/cluster/keydelivery) needs whatever the user-auth posture.
	clientCAs := s.autoTLSClientCAs()
	cfg.ClientAuth = ListenerClientAuth(false, clientCAs != nil)
	cfg.ClientCAs = clientCAs
	if s.opts.autoTLSClientTrust != nil {
		s.opts.autoTLSClientTrust.Wire(cfg)
	}
	s.logger.Info("auto-provisioned server TLS certificate from CeremonyCA",
		"transport", transport)
	return cfg, nil
}

// ListenerClientAuth is the client-auth mode of a node listener whose
// operator chose none, shared by cmd/qrdbd's TLS bundle and the embedded
// server's auto-provisioned configuration.
//
// The listener also serves POST /v1/cluster/keydelivery, whose receiver
// authenticates the sending node by its client certificate whatever the
// user-auth posture. So whenever the node has a client CA pool and a node
// certificate of its own (nodeIdentity), the listener requests a client
// certificate and verifies one if given (VerifyClientCertIfGiven),
// independent of authEnabled: certificate-less requests still work when
// auth is off, and a key delivery still carries its sender's certificate.
// Only a node without that identity falls back to NoClientCert when auth
// is off.
func ListenerClientAuth(authEnabled, nodeIdentity bool) tls.ClientAuthType {
	if authEnabled || nodeIdentity {
		return tls.VerifyClientCertIfGiven
	}
	return tls.NoClientCert
}

// autoTLSClientCAs is the ClientCAs pool of every auto-provisioned listener:
// the ceremony CA's certificate alone, built once so every transport's
// configuration shares one pool (a TLSClientTrust keys its widening on that
// pool). WithCACertPool is deliberately absent: it only verifies peer servers
// for the REST forwarding client, and its CAs must not issue client
// identities here. Nil when the ceremony CA yields no certificate.
func (s *Server) autoTLSClientCAs() *x509.CertPool {
	s.autoClientCAs.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), autoTLSCATimeout)
		defer cancel()
		caCert, err := s.opts.ceremonyCA.CACertificate(ctx)
		if err != nil {
			s.logger.Warn("auto-provisioned TLS: ceremony CA certificate unavailable for client verification",
				"error", err)
			return
		}
		if caCert == nil {
			return
		}
		pool := x509.NewCertPool()
		pool.AddCert(caCert)
		s.autoClientCAs.pool = pool
	})
	return s.autoClientCAs.pool
}
