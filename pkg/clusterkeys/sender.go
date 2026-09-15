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

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/jeremyhahn/go-enclave"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// AdminURLResolver resolves the admin HTTPS base URL for a given Raft address.
// rest.LeaderAdminResolver satisfies this interface (it has a superset of
// methods including Register, but Go duck-typing matches the single method).
type AdminURLResolver interface {
	// ResolveAdminURL returns the admin HTTPS base URL for raftAddr, or ""
	// if the address cannot be resolved.
	ResolveAdminURL(raftAddr string) string
}

// Sender delivers tenant barrier keys from a leader to its follower replicas.
// It resolves each follower's admin URL, fetches its delivery certificate,
// AUTHENTICATES that certificate against the cluster CA and the target node's
// SPIFFE identity, seals the payload with HPKE to the verified key, and POSTs
// the envelope to the receiver endpoint.
//
// The recipient authentication is mandatory and independent of whether the
// transport channel is mTLS: HPKE recipient-binding only protects the key if
// the public key sealed to is the genuine target node's key. Without this the
// no-mTLS fallback would permit an on-path attacker to substitute its own key
// and harvest the root key, so the verification fails closed.
type Sender struct {
	resolver          AdminURLResolver
	client            *http.Client
	caPool            *x509.CertPool
	pinnedTrustDomain string
	logger            *slog.Logger

	// revocation refuses to seal a tenant root key to a recipient whose
	// delivery certificate has been revoked. This is the disclosure half of
	// the same gap the Receiver closes: chain validity and node-SAN match are
	// both satisfied by a revoked certificate, so without this an operator
	// who revokes a compromised node still watches the leader HPKE-seal that
	// node's next tenant root key to it.
	revocation auth.RevocationChecker
}

// NewSender constructs a Sender. The provided http.Client should be configured
// with an mTLS transport presenting the leader node's certificate and trusting
// the cluster CA pool. caPool (the cluster CA roots) and pinnedTrustDomain are
// REQUIRED: the sender uses them to authenticate each recipient's delivery
// certificate before sealing the root key to it. A cluster always has a CA, so
// requiring them is not a dev-mode burden; it closes the MITM disclosure path.
//
// revocation is REQUIRED as a parameter and may be nil as a value, with the
// same meaning and for the same reason as on NewReceiver: nil is "not
// enforced", and it is a parameter so the choice is stated rather than
// defaulted. A leader that skips it will HPKE-seal a tenant root key to a node
// whose certificate the operator has already withdrawn.
func NewSender(resolver AdminURLResolver, client *http.Client, caPool *x509.CertPool, pinnedTrustDomain string, revocation auth.RevocationChecker, logger *slog.Logger) (*Sender, error) {
	if resolver == nil {
		return nil, ErrNilResolver
	}
	if client == nil {
		return nil, ErrNilClient
	}
	if caPool == nil {
		return nil, ErrNilCAPool
	}
	if pinnedTrustDomain == "" {
		return nil, ErrEmptyTrustDomain
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Sender{
		resolver:          resolver,
		client:            client,
		caPool:            caPool,
		pinnedTrustDomain: pinnedTrustDomain,
		revocation:        revocation,
		logger:            logger,
	}, nil
}

// fetchNodeKey GETs /v1/cluster/nodekey from adminURL, parses the PEM
// certificate, AUTHENTICATES it (chains to the cluster CA AND its node SPIFFE
// SAN equals expectedNodeID, AND it has not been revoked), and only then
// returns the node's ECDSA P-256 delivery public key. This is the trust anchor for HPKE recipient-binding:
// without it the leader would seal the root key to whatever key the network
// returned (an on-path attacker's key under no-mTLS). Fails closed — unlike the
// receiver's best-effort SAN skip, the sender has a concrete expected identity.
func (s *Sender) fetchNodeKey(ctx context.Context, adminURL, expectedNodeID string) (*ecdsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, adminURL+"/v1/cluster/nodekey", nil)
	if err != nil {
		return nil, errors.Join(ErrNodeKeyFetch, err)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, errors.Join(ErrNodeKeyFetch, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrNodeKeyFetch
	}
	pemData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Join(ErrNodeKeyFetch, err)
	}
	block, _ := pem.Decode(pemData)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, ErrDeliveryCert
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, ErrDeliveryCert
	}

	// Authenticate the recipient certificate against the cluster CA. A self-
	// signed or foreign cert (MITM) fails here regardless of channel mTLS.
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     s.caPool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		return nil, errors.Join(ErrRecipientUntrusted, err)
	}

	// Bind the recipient to the placement-assigned NodeID via its SPIFFE SAN.
	// Fail closed when the SAN is absent or mismatched — the sender knows
	// exactly which node it intends to seal to.
	nodeID, err := spiffe.NodeFromCert(cert, s.pinnedTrustDomain)
	if err != nil {
		return nil, errors.Join(ErrRecipientUntrusted, err)
	}
	if nodeID.NodeID != expectedNodeID {
		return nil, ErrRecipientNodeMismatch
	}

	// The recipient is the RIGHT node; ask whether it is still a permitted
	// one. A revoked certificate satisfies both checks above, so this is the
	// only place the leader can decline to hand a tenant root key to a node
	// the operator has withdrawn. Policy comes from auth.EnforceRevocation
	// so the sender, the receiver and the authenticator cannot disagree about
	// what "revoked" and "cannot tell" mean.
	if rerr := auth.EnforceRevocation(ctx, s.revocation, cert); rerr != nil {
		if errors.Is(rerr, auth.ErrCertificateRevoked) {
			return nil, errors.Join(ErrRecipientRevoked, rerr)
		}
		return nil, errors.Join(ErrRecipientRevocationUnknown, rerr)
	}

	ecPub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	if !ok || ecPub.Curve != elliptic.P256() {
		return nil, ErrNonP256NodeKey
	}
	return ecPub, nil
}

// SealFor authenticates the target replica's delivery certificate (chains to the
// cluster CA AND its SPIFFE SAN matches target.NodeID) and HPKE-seals plaintext
// to its verified public key, returning the envelope WITHOUT delivering it. It is
// for callers that persist the per-recipient envelope themselves — e.g. a
// cluster-migration coordinator embedding it in a replicated record — rather than
// POSTing it. The same recipient-authentication that protects Deliver applies, so
// the sealed credential is bound to the genuine target node. The caller owns
// plaintext and must wipe it after SealFor returns.
func (s *Sender) SealFor(ctx context.Context, target placement.Replica, meta keydelivery.Meta, plaintext []byte) (*keydelivery.Envelope, error) {
	adminURL := s.resolver.ResolveAdminURL(target.RaftAddr)
	if adminURL == "" {
		return nil, ErrResolveURL
	}
	pub, err := s.fetchNodeKey(ctx, adminURL, target.NodeID)
	if err != nil {
		return nil, err
	}
	env, err := keydelivery.Seal(pub, meta, plaintext)
	if err != nil {
		return nil, errors.Join(ErrDelivery, err)
	}
	return env, nil
}

// Deliver seals plaintext to the given replica's delivery key and POSTs the
// HPKE envelope to the replica's /v1/cluster/keydelivery endpoint.
// The caller owns plaintext and must wipe it after Deliver returns.
func (s *Sender) Deliver(ctx context.Context, target placement.Replica, meta keydelivery.Meta, plaintext []byte) error {
	adminURL := s.resolver.ResolveAdminURL(target.RaftAddr)
	if adminURL == "" {
		return ErrResolveURL
	}
	env, err := s.SealFor(ctx, target, meta, plaintext)
	if err != nil {
		return err
	}
	body, err := json.Marshal(env)
	if err != nil {
		return errors.Join(ErrDelivery, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		adminURL+"/v1/cluster/keydelivery", bytes.NewReader(body))
	if err != nil {
		return errors.Join(ErrDelivery, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return errors.Join(ErrDelivery, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ErrDelivery
	}
	return nil
}

// DistributeTenantKey ships the tenant barrier key to every follower replica in
// p (skipping selfNodeID). For system-managed mode (autoUnseal=true) it exports
// the leader's ImportedKey context; for self-managed mode (autoUnseal=false) it
// exports the sealed blob verbatim. All replicas are attempted; errors are
// accumulated via errors.Join.
func (s *Sender) DistributeTenantKey(
	ctx context.Context,
	p *placement.Placement,
	leaderTenantBarrier *seal.StorageBarrier,
	selfNodeID string,
	autoUnseal bool,
) error {
	var errs []error
	for _, replica := range p.Replicas {
		if replica.NodeID == selfNodeID {
			continue // skip the leader itself
		}
		meta := keydelivery.Meta{
			TenantID:     p.TenantID,
			Database:     p.Database,
			ShardID:      p.ShardID,
			ReplicaID:    replica.ReplicaID,
			TargetNodeID: replica.NodeID,
		}
		var deliverErr error
		if autoUnseal {
			meta.Mode = keydelivery.ModeSystemManaged
			deliverErr = s.deliverSystemManaged(ctx, replica, meta, leaderTenantBarrier)
		} else {
			meta.Mode = keydelivery.ModeSelfManaged
			deliverErr = s.deliverSelfManaged(ctx, replica, meta, leaderTenantBarrier)
		}
		if deliverErr != nil {
			errs = append(errs, deliverErr)
			s.logger.Error("clusterkeys: delivery failed",
				slog.String("node", replica.NodeID),
				slog.String("mode", string(meta.Mode)),
				slog.String("error", deliverErr.Error()))
		}
	}
	return errors.Join(errs...)
}

// deliverSystemManaged exports the leader's ImportedKey context, marshals it as
// JSON, and delivers it via Deliver. The key material is wiped immediately after
// Deliver returns.
func (s *Sender) deliverSystemManaged(ctx context.Context, replica placement.Replica, meta keydelivery.Meta, b *seal.StorageBarrier) error {
	imported, err := b.ExportRootKeyContext()
	if err != nil {
		return err
	}
	plaintext, err := marshalKeyPayload(imported)
	enclave.Wipe(imported.RootKey) // wipe the exported root key immediately
	if err != nil {
		return errors.Join(ErrDelivery, err)
	}
	defer enclave.Wipe(plaintext)
	return s.Deliver(ctx, replica, meta, plaintext)
}

// deliverSelfManaged exports the sealed root-key blob and delivers it verbatim.
// The blob is at-rest ciphertext sealed by the tenant's strategy; it is wiped
// from the transient buffer after delivery.
func (s *Sender) deliverSelfManaged(ctx context.Context, replica placement.Replica, meta keydelivery.Meta, b *seal.StorageBarrier) error {
	blob, err := b.ExportSealedBlob(ctx)
	if err != nil {
		return err
	}
	defer enclave.Wipe(blob)
	return s.Deliver(ctx, replica, meta, blob)
}
