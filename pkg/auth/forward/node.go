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

package forward

import (
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
)

// SignCaller signs the assertion that caller made the request b binds, as the
// node nodeID holding key. A holder caller's assertion carries its holder
// profile, and a delegated caller's its delegation certificate (Carry). With no
// authenticated caller, or no node signing identity, it returns
// *ForwardUnsignedError: a forward must never be authorized as the forwarding
// node instead of its caller.
func SignCaller(caller *auth.Identity, b Binding, key crypto.Signer, nodeID string, now time.Time) (assertionB64, sigB64 string, err error) {
	if caller == nil || caller.Subject == "" {
		return "", "", &ForwardUnsignedError{Reason: "no authenticated caller"}
	}
	if key == nil || nodeID == "" {
		return "", "", &ForwardUnsignedError{Reason: "this node has no signing identity"}
	}
	a := b.Assertion(caller.Subject, caller.TenantID, nodeID, now)
	a.Carry(caller)
	return Sign(a, key)
}

// Carry stamps a with what the receiving node needs to rebuild caller as the
// identity it is, beyond its name: a holder's profile, so it stays a holder,
// and a delegated caller's delegation certificate, which the receiving node
// verifies again rather than taking this node's word for.
func (a *CallerAssertion) Carry(caller *auth.Identity) {
	if caller == nil {
		return
	}
	a.HolderProfile = caller.Metadata[auth.HolderProfileMetadataKey]
	if d, ok := auth.DelegationOf(caller); ok {
		a.Delegation = base64.StdEncoding.EncodeToString(d.Certificate)
	}
}

// NodeIdentity extracts the on-behalf signing key and this node's
// SPIFFE node id from the node's own TLS identity certificate. It returns
// (nil, "") when no usable identity is present, and a signer with an empty
// node id when the certificate carries no node SAN: such a node cannot sign an
// assertion a peer would honor. Both
// the static Certificates slice and a GetClientCertificate callback are probed
// so the derivation works regardless of how the transport TLS was provisioned.
func NodeIdentity(tlsCfg *tls.Config) (crypto.Signer, string) {
	if tlsCfg == nil {
		return nil, ""
	}

	leaf := leafCertificate(tlsCfg)
	if leaf == nil {
		return nil, ""
	}

	signer, ok := leaf.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, ""
	}

	parsed := leaf.Leaf
	if parsed == nil {
		if len(leaf.Certificate) == 0 {
			return nil, ""
		}
		var perr error
		parsed, perr = x509.ParseCertificate(leaf.Certificate[0])
		if perr != nil {
			return nil, ""
		}
	}

	// The node id is not trust-domain-pinned here: it merely stamps the
	// assertion's ForwarderNodeID, which the leader re-checks against the
	// mTLS peer certificate it actually verified against the cluster CA.
	nodeID, err := spiffe.NodeFromCert(parsed, "")
	if err != nil {
		return signer, ""
	}
	return signer, nodeID.NodeID
}

// leafCertificate returns the node's own TLS identity certificate from cfg,
// preferring the static Certificates slice and falling back to a
// GetClientCertificate callback. It returns nil when neither yields a
// certificate with a usable private key.
func leafCertificate(cfg *tls.Config) *tls.Certificate {
	if len(cfg.Certificates) > 0 && cfg.Certificates[0].PrivateKey != nil {
		return &cfg.Certificates[0]
	}
	if cfg.GetClientCertificate != nil {
		if cert, err := cfg.GetClientCertificate(&tls.CertificateRequestInfo{}); err == nil &&
			cert != nil && cert.PrivateKey != nil {
			return cert
		}
	}
	return nil
}
