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

package clusterkeys

import "errors"

// Sentinel errors for the cluster-peer key transport layer.
// Callers match these with errors.Is.
var (
	// ErrNilReceiver indicates a nil keydelivery.Receiver was provided.
	ErrNilReceiver = errors.New("clusterkeys: keydelivery receiver must not be nil")

	// ErrNilPlacements indicates a nil PlacementGetter was provided.
	ErrNilPlacements = errors.New("clusterkeys: placement getter must not be nil")

	// ErrEmptySelfNodeID indicates an empty self-node ID was provided.
	ErrEmptySelfNodeID = errors.New("clusterkeys: self node ID must not be empty")

	// ErrEmptyDeliveryCert indicates no delivery certificate PEM was provided.
	ErrEmptyDeliveryCert = errors.New("clusterkeys: delivery certificate PEM must not be empty")

	// ErrNilResolver indicates a nil AdminURLResolver was provided.
	ErrNilResolver = errors.New("clusterkeys: admin URL resolver must not be nil")

	// ErrNilClient indicates a nil HTTP client was provided.
	ErrNilClient = errors.New("clusterkeys: HTTP client must not be nil")

	// ErrPlacementGate indicates the placement table has no entry assigning
	// a replica of the requested tenant/database to this node.
	ErrPlacementGate = errors.New("clusterkeys: no placement entry for this node")

	// ErrMalformedBody indicates the POST body could not be decoded as a valid
	// keydelivery.Envelope.
	ErrMalformedBody = errors.New("clusterkeys: malformed request body")

	// ErrUnknownMode indicates the delivery envelope carries a mode value
	// not handled by the mode dispatch table.
	ErrUnknownMode = errors.New("clusterkeys: unknown delivery mode")

	// ErrNonP256NodeKey indicates the delivery certificate's public key is
	// not an ECDSA P-256 key as required by the DHKEM(P-256) KEM.
	ErrNonP256NodeKey = errors.New("clusterkeys: node key is not ECDSA P-256")

	// ErrDeliveryCert indicates the PEM response from a node's
	// /v1/cluster/nodekey endpoint could not be parsed as a certificate.
	ErrDeliveryCert = errors.New("clusterkeys: cannot parse node delivery certificate")

	// ErrNodeKeyFetch indicates the GET to /v1/cluster/nodekey failed.
	ErrNodeKeyFetch = errors.New("clusterkeys: failed to fetch node delivery key")

	// ErrDelivery indicates the POST to /v1/cluster/keydelivery failed.
	ErrDelivery = errors.New("clusterkeys: key delivery request failed")

	// ErrResolveURL indicates the admin URL for a replica could not be resolved.
	ErrResolveURL = errors.New("clusterkeys: cannot resolve admin URL for replica")

	// ErrNilCAPool indicates no cluster CA pool was provided to the Sender.
	// The sender requires it to authenticate each recipient's delivery cert.
	ErrNilCAPool = errors.New("clusterkeys: cluster CA pool must not be nil")

	// ErrEmptyTrustDomain indicates no pinned trust domain was provided to the
	// Sender, which it needs to bind a recipient cert's node SPIFFE identity.
	ErrEmptyTrustDomain = errors.New("clusterkeys: pinned trust domain must not be empty")

	// ErrRecipientUntrusted indicates a fetched delivery certificate did not
	// chain to the cluster CA or lacked a valid node SPIFFE SAN. The sender
	// refuses to seal the root key to an unauthenticated recipient key.
	ErrRecipientUntrusted = errors.New("clusterkeys: recipient delivery certificate is untrusted")

	// ErrRecipientNodeMismatch indicates a fetched delivery certificate's node
	// SPIFFE SAN does not match the placement-assigned target NodeID.
	ErrRecipientNodeMismatch = errors.New("clusterkeys: recipient node identity does not match target")

	// ErrRecipientRevoked indicates the recipient's delivery certificate
	// chains to the cluster CA and carries the expected node identity, but its
	// serial has been REVOKED. The leader refuses to seal a tenant root key to
	// a withdrawn node: HPKE recipient-binding would otherwise make the
	// delivery a correct, verified disclosure to precisely the node the
	// operator removed.
	ErrRecipientRevoked = errors.New("clusterkeys: recipient delivery certificate has been revoked")

	// ErrRecipientRevocationUnknown indicates the recipient's revocation
	// status could not be determined. It is a REFUSAL, not a rejection of the
	// certificate: the recipient may be perfectly good and this node could not
	// read replicated revocation state. Delivery fails closed and the caller
	// may retry, because sealing a root key to a node whose standing is
	// unknown is the outcome this check exists to prevent.
	ErrRecipientRevocationUnknown = errors.New("clusterkeys: recipient revocation status could not be determined")
)
