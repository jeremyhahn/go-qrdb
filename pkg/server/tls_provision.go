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

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
)

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
	s.logger.Info("auto-provisioned server TLS certificate from CeremonyCA",
		"transport", transport)
	return cfg, nil
}
