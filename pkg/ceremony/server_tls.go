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

package ceremony

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
)

// ServerTLSValidityDays is the validity, in days, of a server TLS
// certificate auto-provisioned by ServerTLSConfig. It matches the leaf
// validity the ceremony uses for its own TLS/SO certificates.
const ServerTLSValidityDays = 365

// baseServerSANs are the Subject Alternative Names every auto-provisioned
// server certificate carries so that local clients (loopback dials, the CLI
// over the unix socket, embedded transports) verify the leaf without extra
// configuration. The machine hostname and any caller-supplied SANs are added
// on top of these by ServerTLSConfig.
var baseServerSANs = []string{"localhost", "127.0.0.1", "::1"}

// ServerTLSConfig auto-provisions a server TLS configuration from the node's
// CeremonyCA. It issues a fresh leaf certificate — signed by the same CA that
// signs the node's own TLS/SO certificates, so clients that already trust the
// cluster CA verify it without further configuration — and returns a
// *tls.Config pinned to TLS 1.3.
//
// The leaf carries SANs for localhost, 127.0.0.1, ::1, the machine hostname,
// and any extraSANs (e.g. a configured advertise host). Duplicate SANs are
// collapsed. The certificate is issued with ServerAuth + ClientAuth extended
// key usage so a node may also present it as a client identity on mutual-TLS
// cluster links, mirroring the cluster node leaf profile.
//
// The returned config sets only the server certificate and MinVersion; it does
// NOT set ClientAuth. Requiring and authenticating a client certificate is the
// job of the auth middleware layered on top of the transport, so the transport
// itself never widens its stated client-verification posture here.
//
// A nil ca is a caller error and returns ErrNilCA — callers must fail closed
// rather than start a plaintext listener. Certificate issuance and key-pair
// assembly failures are wrapped in a *ServerTLSError.
func ServerTLSConfig(ctx context.Context, ca CeremonyCA, extraSANs ...string) (*tls.Config, error) {
	if ca == nil {
		return nil, ErrNilCA
	}

	sans := serverSANs(extraSANs)
	commonName := "localhost"
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		commonName = hostname
	}

	issued, err := ca.IssueCertificate(ctx, &IssueCertRequest{
		CommonName:  commonName,
		SANs:        sans,
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		// ValidityDays is intentionally short-lived relative to the CA so that
		// a long-running node re-provisions its leaf on restart.
		ValidityDays: ServerTLSValidityDays,
	})
	if err != nil {
		return nil, &ServerTLSError{Err: err}
	}

	cert, err := tls.X509KeyPair(issued.CertPEM, issued.KeyPEM)
	if err != nil {
		return nil, &ServerTLSError{Err: err}
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// serverSANs returns the deduplicated union of the base server SANs, the
// machine hostname, and the caller-supplied extra SANs, preserving first-seen
// order for stable certificate contents.
func serverSANs(extra []string) []string {
	seen := make(map[string]struct{}, len(baseServerSANs)+len(extra)+1)
	out := make([]string, 0, len(baseServerSANs)+len(extra)+1)
	add := func(san string) {
		if san == "" {
			return
		}
		if _, ok := seen[san]; ok {
			return
		}
		seen[san] = struct{}{}
		out = append(out, san)
	}

	for _, san := range baseServerSANs {
		add(san)
	}
	if hostname, err := os.Hostname(); err == nil {
		add(hostname)
	}
	for _, san := range extra {
		add(san)
	}
	return out
}
