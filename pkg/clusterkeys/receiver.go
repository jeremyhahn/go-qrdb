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

// Package clusterkeys implements the cluster-peer key-transport layer for
// Phase 4c-1c: the leader delivers a dedicated tenant's Raft root-key context
// to the follower nodes hosting its replicas over an mTLS HTTPS endpoint.
//
// Authorization is by node identity (peer cert SPIFFE SAN) plus the replicated
// placement table — NOT user RBAC. The cryptographic guarantee comes from
// HPKE recipient-binding in the keydelivery layer, which ensures only the node
// holding the private key paired to the cert the leader fetched can open the
// envelope.
//
// Receiver handles POST /v1/cluster/keydelivery and GET /v1/cluster/nodekey.
// Sender delivers keys to follower replicas.
package clusterkeys

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/jeremyhahn/go-quicraft/pkg/enclave"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// PlacementGetter retrieves a Placement for a (tenant, database) pair.
// placement.Store satisfies this interface directly.
type PlacementGetter interface {
	Get(ctx context.Context, tenantID, database string) (*placement.Placement, error)
}

// Receiver handles incoming cluster key-delivery requests for this node.
// It wraps a keydelivery.Receiver (which enforces HPKE recipient-binding,
// freshness, and replay defense) and adds the cluster transport layer: peer-cert
// authentication, placement-table authorisation, and barrier import.
// maxEnvelopeBytes bounds the delivery request body. A delivery envelope is a
// 32-byte root key (or a small sealed blob) plus HPKE encapsulation, base64 +
// JSON framing, and the metadata — well under 64 KiB. The cap prevents an
// unbounded-body OOM on this RBAC-exempt endpoint.
const maxEnvelopeBytes = 64 << 10

type Receiver struct {
	kd                *keydelivery.Receiver
	registry          *seal.BarrierRegistry
	placements        PlacementGetter
	pinnedTrustDomain string
	selfNodeID        string
	caPool            *x509.CertPool
	deliveryCertPEM   []byte
	logger            *slog.Logger
	downgradeLogged   atomic.Bool

	// revocation refuses a peer whose certificate has been revoked. This
	// route is on the REST public allowlist, so it never runs the
	// authenticator chain and inherits nothing from it: without this the
	// chain check and the node-SAN check are both satisfied by a revoked
	// certificate and a revoked node keeps taking part in key delivery.
	revocation auth.RevocationChecker
}

// ackResponse is the JSON body for a successful delivery response.
type ackResponse struct {
	Imported bool `json:"imported"`
}

// malformedOpenErrs are the structurally-bad-request Open errors that warrant a
// 400 (the caller sent garbage). Every OTHER Open failure — wrong target,
// decryption failure, replay, expiry — collapses to a single opaque 403 so the
// status code does not become a reconnaissance oracle (e.g. distinguishing "I
// am/­am not the intended target" or "this nonce was already seen").
var malformedOpenErrs = []error{
	keydelivery.ErrMalformedEnvelope,
	keydelivery.ErrInvalidMeta,
	keydelivery.ErrInvalidMode,
}

// modeImporter is the function type dispatched per delivery Mode.
type modeImporter func(ctx context.Context, r *Receiver, meta keydelivery.Meta, dataRoot string, plaintext []byte) error

// modeHandlers is the map-based dispatch for delivery modes. Map-based
// dispatch is preferred over switch per project conventions.
var modeHandlers = map[keydelivery.Mode]modeImporter{
	keydelivery.ModeSystemManaged: importSystemManaged,
	keydelivery.ModeSelfManaged:   importSelfManaged,
}

// NewReceiver constructs a Receiver bound to this follower node. caPool may be
// nil when the cluster runs without mutual TLS (dev mode); in that case the
// HPKE recipient-binding and placement gate are the sole authZ mechanisms and
// a SECURITY-DOWNGRADE warning is logged on the first unauthenticated request.
//
// revocation is REQUIRED as a parameter and may be nil as a value. Nil means
// revocation is not enforced here — the posture of a node with no certificate
// authority wired, and the same one auth.EnforceRevocation takes for a nil
// checker. It is a parameter rather than an option so that every construction
// site states which of the two it chose: this endpoint is one of the three
// places in go-qrdb where a peer certificate is authorized, and the other two
// are not reachable from here to compensate for an omission.
//
// Pass the checker the authentication path already holds
// (middleware.AuthMiddleware.RevocationChecker) rather than building a second
// one over the same provider, so both share one bounded-TTL cache and the
// window in which a freshly revoked certificate still passes is one number for
// the node.
func NewReceiver(
	kd *keydelivery.Receiver,
	registry *seal.BarrierRegistry,
	placements PlacementGetter,
	pinnedTrustDomain string,
	selfNodeID string,
	caPool *x509.CertPool,
	revocation auth.RevocationChecker,
	deliveryCertPEM []byte,
	logger *slog.Logger,
) (*Receiver, error) {
	if kd == nil {
		return nil, ErrNilReceiver
	}
	if registry == nil {
		return nil, ErrNilRegistry
	}
	if placements == nil {
		return nil, ErrNilPlacements
	}
	if selfNodeID == "" {
		return nil, ErrEmptySelfNodeID
	}
	if len(deliveryCertPEM) == 0 {
		return nil, ErrEmptyDeliveryCert
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Receiver{
		kd:                kd,
		registry:          registry,
		placements:        placements,
		pinnedTrustDomain: pinnedTrustDomain,
		selfNodeID:        selfNodeID,
		caPool:            caPool,
		revocation:        revocation,
		deliveryCertPEM:   deliveryCertPEM,
		logger:            logger,
	}, nil
}

// Handler returns an http.HandlerFunc for POST /v1/cluster/keydelivery.
// It enforces the following in order:
//
//	a. Peer-cert chain verification against the cluster CA (when cert is
//	   present); node SPIFFE SAN checked if present (best-effort); and the
//	   certificate refused if it has been REVOKED (401) or if its revocation
//	   status cannot be read (503) — see revocationRefusal.
//	b. JSON-decode the keydelivery.Envelope (400 on malformed body).
//	c. Placement-table gate: this node must appear as the replica assigned for
//	   the envelope's (tenant, database, shard, replica) (403 on mismatch).
//	d. keydelivery.Receiver.Open: HPKE decryption + freshness + replay guard.
//	e. Mode dispatch (map-based) to importSystemManaged or importSelfManaged.
func (r *Receiver) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		ctx := req.Context()

		// --- a: Peer-cert auth -------------------------------------------
		if req.TLS != nil && len(req.TLS.PeerCertificates) > 0 {
			leaf := req.TLS.PeerCertificates[0]
			if err := r.verifyCert(leaf); err != nil {
				writeJSONError(w, http.StatusUnauthorized, "client certificate verification failed")
				r.logger.Warn("clusterkeys: peer cert chain invalid",
					slog.String("error", err.Error()))
				return
			}
			if err := r.checkNodeSAN(leaf); err != nil {
				writeJSONError(w, http.StatusForbidden, "node identity mismatch")
				r.logger.Warn("clusterkeys: node SAN mismatch",
					slog.String("self", r.selfNodeID),
					slog.String("error", err.Error()))
				return
			}
			// A revoked peer must not take part in key delivery. Neither check
			// above sees revocation: a revoked certificate still chains to the
			// cluster CA and still carries its node SAN. This route is on the
			// REST public allowlist and never reaches the authenticator chain,
			// so the policy is applied here rather than inherited — from
			// auth.EnforceRevocation, its single definition, so this is not a
			// third copy of a three-way fail-open/fail-closed decision.
			if rerr := auth.EnforceRevocation(ctx, r.revocation, leaf); rerr != nil {
				code, msg := revocationRefusal(rerr)
				writeJSONError(w, code, msg)
				r.logger.Warn("clusterkeys: peer certificate refused by revocation policy",
					slog.Int("status", code),
					slog.String("error", rerr.Error()))
				return
			}
		} else if r.downgradeLogged.CompareAndSwap(false, true) {
			// No peer cert — SECURITY-DOWNGRADE: logged once.
			r.logger.Warn("clusterkeys: SECURITY-DOWNGRADE: no peer certificate present; " +
				"authorization relies on HPKE recipient-binding and placement gate only")
		}

		// --- b: decode envelope ------------------------------------------
		// Bound the body: the endpoint is RBAC-exempt and (in no-mTLS mode)
		// unauthenticated before this point, so an unbounded body is an OOM
		// DoS vector. A delivery is a 32-byte key plus HPKE overhead or a small
		// sealed blob — maxEnvelopeBytes is generous.
		req.Body = http.MaxBytesReader(w, req.Body, maxEnvelopeBytes)
		var env keydelivery.Envelope
		if err := json.NewDecoder(req.Body).Decode(&env); err != nil {
			writeJSONError(w, http.StatusBadRequest, "malformed envelope")
			return
		}

		// --- c: placement gate -------------------------------------------
		p, err := r.placements.Get(ctx, env.TenantID, env.Database)
		if err != nil {
			writeJSONError(w, http.StatusForbidden, "placement gate rejected")
			r.logger.Warn("clusterkeys: placement lookup failed",
				slog.String("tenant", env.TenantID),
				slog.String("database", env.Database),
				slog.String("error", err.Error()))
			return
		}
		replica, ok := p.ReplicaForNode(r.selfNodeID)
		if !ok || replica.ReplicaID != env.ReplicaID || p.ShardID != env.ShardID {
			writeJSONError(w, http.StatusForbidden, "placement gate rejected")
			r.logger.Warn("clusterkeys: placement gate denied: shard/replica mismatch",
				slog.String("tenant", env.TenantID),
				slog.String("database", env.Database),
				slog.Uint64("envelope_replica", env.ReplicaID),
				slog.Uint64("envelope_shard", env.ShardID))
			return
		}

		// --- d: HPKE open ------------------------------------------------
		plaintext, openErr := r.kd.Open(&env)
		if openErr != nil {
			code := httpCodeForOpenErr(openErr)
			writeJSONError(w, code, "key delivery rejected")
			r.logger.Warn("clusterkeys: envelope open failed",
				slog.String("tenant", env.TenantID),
				slog.Int("status", code))
			return
		}
		defer enclave.WipeBytes(plaintext)

		// --- e: mode dispatch (map-based) --------------------------------
		handler, known := modeHandlers[env.Mode]
		if !known {
			writeJSONError(w, http.StatusBadRequest, "unknown delivery mode")
			return
		}
		if importErr := handler(ctx, r, env.Meta, replica.DataRoot, plaintext); importErr != nil {
			if errors.Is(importErr, seal.ErrAlreadyInitialized) ||
				errors.Is(importErr, seal.ErrTenantAlreadyExists) {
				// Idempotent: key already installed by a prior delivery.
				writeJSON(w, http.StatusOK, ackResponse{Imported: true})
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "key import failed")
			r.logger.Error("clusterkeys: barrier import failed",
				slog.String("tenant", env.TenantID),
				slog.String("mode", string(env.Mode)),
				slog.String("error", importErr.Error()))
			return
		}

		writeJSON(w, http.StatusOK, ackResponse{Imported: true})
	}
}

// NodeKeyHandler returns an http.HandlerFunc for GET /v1/cluster/nodekey.
// It returns this node's delivery certificate PEM so senders can extract
// the ECDSA P-256 public key for HPKE-sealing.
func (r *Receiver) NodeKeyHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/x-pem-file")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(r.deliveryCertPEM)
	}
}

// verifyCert checks that cert chains to the cluster CA pool. When no CA pool
// is configured (caPool == nil), the check is skipped and the caller relies
// on HPKE recipient-binding and the placement gate.
func (r *Receiver) verifyCert(cert *x509.Certificate) error {
	if r.caPool == nil {
		return nil
	}
	opts := x509.VerifyOptions{
		Roots:     r.caPool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	_, err := cert.Verify(opts)
	return err
}

// checkNodeSAN verifies the peer cert carries a valid cluster-node SPIFFE SAN
// under r.pinnedTrustDomain. It does NOT check which node the sender is:
// the sender is the leader delivering a key TO this follower, so its node ID
// will always differ from r.selfNodeID. The HPKE recipient-binding and the
// placement gate already enforce that only the correct target node can consume
// the envelope; this check adds defence-in-depth by ensuring the sender is at
// minimum a cluster peer (holds a CA-signed cert with a node SAN). When no
// node SAN is present (ErrNoNodeSPIFFEID) the check is skipped for backward
// compat with certs predating node-SAN issuance; HPKE + placement still hold.
func (r *Receiver) checkNodeSAN(cert *x509.Certificate) error {
	_, err := spiffe.NodeFromCert(cert, r.pinnedTrustDomain)
	if errors.Is(err, spiffe.ErrNoNodeSPIFFEID) {
		return nil // no node SAN — best-effort, proceed
	}
	// Any other error means the SAN is present but malformed or wrong trust
	// domain. Return the error so the delivery is rejected.
	return err
}

// revocationRefusal maps an auth.EnforceRevocation refusal to the status and
// body this endpoint answers with.
//
// The two outcomes get DIFFERENT codes on purpose, and neither is the opaque
// 403 the Open failures collapse to. Revocation status is not a
// reconnaissance oracle: the answer is about the certificate the caller
// itself presented, so it tells a caller nothing it could not read off its
// own credential.
//
//   - revoked -> 401, the same code the mTLS authenticator's cert_revoked
//     produces. Retrying is pointless and the sender should say so in its log
//     rather than treat a withdrawn identity as a transient delivery failure.
//   - status unknown -> 503. FAIL CLOSED, but as a refusal rather than a
//     denial: the peer certificate may be perfectly good and this node could
//     not read replicated revocation state, so the honest answer is "retry",
//     not "your credential is bad".
//
// Any other shape lands on 503 as well. EnforceRevocation produces only these
// two today; a future third has to be a refusal-with-retry until someone
// decides otherwise here, which is the safe default for an unclassified
// authorization failure.
func revocationRefusal(err error) (int, string) {
	if errors.Is(err, auth.ErrCertificateRevoked) {
		return http.StatusUnauthorized, "client certificate has been revoked"
	}
	return http.StatusServiceUnavailable, "certificate revocation status unavailable"
}

// httpCodeForOpenErr maps a keydelivery Open error to an HTTP status code.
// Structurally-malformed requests get 400; all authz/crypto rejections collapse
// to a single opaque 403 to avoid a reconnaissance oracle.
func httpCodeForOpenErr(err error) int {
	for _, sentinel := range malformedOpenErrs {
		if errors.Is(err, sentinel) {
			return http.StatusBadRequest
		}
	}
	return http.StatusForbidden
}

// importSystemManaged handles ModeSystemManaged: unmarshal the plaintext as
// a keyPayload and call BarrierRegistry.ImportTenant with autoUnseal=true.
// The caller wipes plaintext, but unmarshal base64-decodes the root key into a
// FRESH allocation (imported.RootKey) that the caller's defer never touches, so
// it is wiped here explicitly regardless of import outcome.
func importSystemManaged(ctx context.Context, r *Receiver, meta keydelivery.Meta, dataRoot string, plaintext []byte) error {
	imported, err := unmarshalKeyPayload(plaintext)
	if err != nil {
		return errors.Join(ErrMalformedBody, err)
	}
	defer enclave.WipeBytes(imported.RootKey)
	_, err = r.registry.ImportTenant(ctx, meta.TenantID, dataRoot, true, imported, nil)
	return err
}

// importSelfManaged handles ModeSelfManaged: treat the plaintext as the
// already-sealed blob and call BarrierRegistry.ImportTenant with
// autoUnseal=false. The barrier remains sealed; the tenant unseals later.
func importSelfManaged(ctx context.Context, r *Receiver, meta keydelivery.Meta, dataRoot string, plaintext []byte) error {
	_, err := r.registry.ImportTenant(ctx, meta.TenantID, dataRoot, false, nil, plaintext)
	return err
}

// writeJSON writes a JSON-encoded value with the given status code.
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONError writes a JSON error response body.
func writeJSONError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
