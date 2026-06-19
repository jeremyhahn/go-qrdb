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

package spiffe

import (
	"crypto/x509"
	"errors"
	"net/url"
	"strings"
)

// Node-identity SPIFFE form. A cluster node carries an INFRASTRUCTURE identity
// (distinct from the tenant-workload identities handled by ID):
//
//	spiffe://<trust-domain>/node/<nodeID>
//
// where nodeID is the go-quicraft host id. The cluster-peer control plane
// (Phase 4c-1c tenant-key transport) pins this against the placement table's
// assigned NodeID: a node may receive a tenant key only if its certificate's
// node SPIFFE SAN matches the node the placement table assigned that replica
// to. The trust domain is the operator-pinned cluster trust domain (fail-closed
// on mismatch), exactly as for tenant identities.

// nodePathSegment is the first path segment marking a node-identity SPIFFE URI.
const nodePathSegment = "node"

// Node identity errors.
var (
	// ErrNodeIDEmpty indicates an empty node id was parsed.
	ErrNodeIDEmpty = errors.New("spiffe: node id must not be empty")

	// ErrNoNodeSPIFFEID indicates no node-scoped SPIFFE SAN was found.
	ErrNoNodeSPIFFEID = errors.New("spiffe: no node spiffe identity present")
)

// NodeIdentity describes a cluster-node SPIFFE identity extracted from a
// certificate URI SAN.
type NodeIdentity struct {
	TrustDomain string
	NodeID      string
}

// Validate verifies the trust domain and node id are present.
func (n NodeIdentity) Validate() error {
	if strings.TrimSpace(n.TrustDomain) == "" {
		return ErrTrustDomainEmpty
	}
	if strings.TrimSpace(n.NodeID) == "" {
		return ErrNodeIDEmpty
	}
	return nil
}

// String renders the canonical node SPIFFE string form, or "" if invalid.
func (n NodeIdentity) String() string {
	if n.Validate() != nil {
		return ""
	}
	u := &url.URL{
		Scheme: scheme,
		Host:   n.TrustDomain,
		Path:   "/" + nodePathSegment + "/" + n.NodeID,
	}
	return u.String()
}

// NodeURI builds the canonical node SPIFFE URI for cert issuance. It returns ""
// for empty inputs so callers fail closed rather than emit a malformed SAN.
func NodeURI(trustDomain, nodeID string) string {
	return NodeIdentity{TrustDomain: trustDomain, NodeID: nodeID}.String()
}

// ParseNodeID extracts a node SPIFFE identity from a URI. Like ParseID it
// returns false (not an error) when the URI is not a node SPIFFE path so
// callers can skip non-matching SANs; a malformed node path returns an error.
func ParseNodeID(u *url.URL) (NodeIdentity, bool, error) {
	if u == nil || u.Scheme != scheme {
		return NodeIdentity{}, false, nil
	}
	if u.Host == "" {
		return NodeIdentity{}, false, ErrTrustDomainEmpty
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != nodePathSegment {
		return NodeIdentity{}, false, nil
	}
	id := NodeIdentity{TrustDomain: u.Host, NodeID: parts[1]}
	if err := id.Validate(); err != nil {
		return NodeIdentity{}, false, err
	}
	return id, true, nil
}

// NodeFromCert scans a certificate's URI SANs for a node-scoped SPIFFE
// identity, enforcing the pinned trust domain (fail-closed).
//
//   - When pinnedTrustDomain is non-empty, any node SPIFFE SAN whose trust
//     domain differs returns ErrTrustDomainMismatch (reject foreign certs).
//   - When no node SPIFFE SAN is present, returns ErrNoNodeSPIFFEID so the
//     caller can fall back (e.g. to HPKE recipient-binding + placement gate
//     when inter-node mTLS is not in force).
//   - A malformed node SPIFFE SAN returns the parse error (fail-closed).
func NodeFromCert(cert *x509.Certificate, pinnedTrustDomain string) (NodeIdentity, error) {
	if cert == nil {
		return NodeIdentity{}, ErrNoNodeSPIFFEID
	}
	for _, u := range cert.URIs {
		id, ok, err := ParseNodeID(u)
		if err != nil {
			return NodeIdentity{}, err
		}
		if !ok {
			continue
		}
		if pinnedTrustDomain != "" && id.TrustDomain != pinnedTrustDomain {
			return NodeIdentity{}, ErrTrustDomainMismatch
		}
		return id, nil
	}
	return NodeIdentity{}, ErrNoNodeSPIFFEID
}
