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

// The approval queue: the "or a future approval workflow" architecture.md
// left open beside the join token. A joiner that presents NO token
// (req.Token == "") is not refused outright -- its CSR is queued, replicated,
// for an operator to list and decide (ApproveJoinRequest / RejectJoinRequest),
// and HandleJoinRequest reports *JoinResponse.Pending* rather than an error so
// the joiner's own retry loop (see Join) keeps presenting the identical CSR
// until a decision lands. Approving does NOT sign anything itself -- it only
// records which identity the CSR is bound to, exactly as a minted join token
// does; the next poll that finds the decision is what actually calls
// IssueLeafForKey, through the SAME issueAndAdmit sequence the token path
// uses. This keeps the admission decision (store-only, made by Approve) and
// the signing step (provider-only, made by HandleJoinRequest) as separate as
// the token path already keeps them.
package clusterenroll

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// pendingJoinPrefix is the replicated-store namespace for CSRs awaiting
// operator approval, keyed by the hex SHA-256 digest of the submitted CSR
// DER -- a value the JOINER's own generated keypair determines, not an
// admin choice, so the same joiner retrying (the shape Join's poll loop
// takes) always addresses the same queue entry rather than minting a new
// one on every attempt.
// #nosec G101 -- replicated-store key-space prefix, not a credential.
const pendingJoinPrefix = "__sys:cluster_join_pending/"

// DefaultPendingJoinRequestTTL bounds how long an undecided ("pending")
// request waits in the queue before it is treated as absent and a retry
// starts a fresh one. Generous compared to
// DefaultJoinIdentityReservationTTL -- an operator reviews a queue on their
// own schedule, not in the middle of a signing handshake -- but still
// finite: a joiner that gave up, or was reconfigured with a token instead,
// must not occupy a queue slot forever.
const DefaultPendingJoinRequestTTL = 72 * time.Hour

// MaxPendingJoinRequests bounds the queue's total size. A minted join
// token is scarce because an admin had to mint it; a tokenless CSR
// submission proves nothing about its sender at all -- the queue is the
// ONLY admission control on this path -- so without a cap, a source with no
// credential whatsoever could grow the replicated store without bound
// simply by generating a fresh keypair per attempt. A submission past the
// cap is refused the same cause-independent way an expired reservation's
// slot already being retaken is: logged, not distinguished from "try again
// later".
const MaxPendingJoinRequests = 256

// pendingJoinStatus is the lifecycle state of a pendingJoinRecord.
type pendingJoinStatus string

const (
	// pendingStatusPending is a queued request awaiting a decision.
	pendingStatusPending pendingJoinStatus = "pending"
	// pendingStatusApproved is a decided request whose leaf has not yet
	// been signed -- HandleJoinRequest signs it on the next poll that
	// observes this state.
	pendingStatusApproved pendingJoinStatus = "approved"
	// pendingStatusRejected is a decided, terminal refusal.
	pendingStatusRejected pendingJoinStatus = "rejected"
	// pendingStatusDelivered is an approved request whose leaf has been
	// signed and cached, so a retried poll (the response to the FIRST
	// delivery never reached the joiner) is answered from the cache
	// rather than re-running IssueLeafForKey.
	pendingStatusDelivered pendingJoinStatus = "delivered"
)

// pendingJoinRecord is the JSON envelope stored under pendingJoinPrefix+ID.
type pendingJoinRecord struct {
	ID        string            `json:"id"`
	CSRDER    []byte            `json:"csr_der"`
	NodeURI   string            `json:"node_uri,omitempty"`
	ReplicaID uint64            `json:"replica_id,omitempty"`
	Features  []string          `json:"features,omitempty"`
	Status    pendingJoinStatus `json:"status"`

	SubmittedAt time.Time `json:"submitted_at"`
	ExpiresAt   time.Time `json:"expires_at"`

	// BoundNodeURI, DecidedBy and DecidedAt are set by ApproveJoinRequest /
	// RejectJoinRequest. BoundNodeURI is the identity an approval binds the
	// eventual leaf to -- an operator choice, exactly like a minted join
	// token's bound identity, never the joiner's self-reported NodeURI.
	BoundNodeURI string    `json:"bound_node_uri,omitempty"`
	DecidedBy    string    `json:"decided_by,omitempty"`
	DecidedAt    time.Time `json:"decided_at,omitempty"`
	Reason       string    `json:"reason,omitempty"`

	// CertPEM is the leaf issued for an approved request, cached on first
	// delivery (Status transitions to pendingStatusDelivered) so a retry
	// after a lost response is answered idempotently instead of re-signing.
	// A certificate is a public artifact, unlike a key, so caching it here
	// carries no additional exposure.
	CertPEM []byte `json:"cert_pem,omitempty"`
}

// expired reports whether r's queue slot should be treated as absent.
// Only an UNDECIDED ("pending") record expires here: a decision already
// made (approved, rejected, delivered) is never undone by the clock, the
// same split joinIdentityPrefix draws between a live reservation and a
// durable admitted timestamp.
func (r *pendingJoinRecord) expired(now time.Time) bool {
	return r.Status == pendingStatusPending && now.After(r.ExpiresAt)
}

// PendingJoinRequest is the operator-facing view of a queued CSR returned
// by ListPendingJoinRequests: every field an operator needs to decide,
// never the CSR's raw DER or a delivered leaf's certificate, neither of
// which bears on the decision.
type PendingJoinRequest struct {
	ID          string
	NodeURI     string
	ReplicaID   uint64
	Features    []string
	SubmittedAt time.Time
	ExpiresAt   time.Time
}

// csrDigestHex is the pending-queue entry ID for csrDER: a stable key the
// SAME joiner's retry reproduces without needing to remember or re-learn an
// ID the first response may never have reached it with.
func csrDigestHex(csrDER []byte) string {
	sum := sha256.Sum256(csrDER)
	return hex.EncodeToString(sum[:])
}

func pendingJoinKey(id string) string { return pendingJoinPrefix + id }

func marshalPendingJoin(r *pendingJoinRecord) ([]byte, error) { return json.Marshal(r) }

func unmarshalPendingJoin(data []byte) (*pendingJoinRecord, error) {
	var r pendingJoinRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// SubmitPendingJoinRequest records a tokenless CSR as awaiting operator
// approval, or returns the record ALREADY there for the identical CSR
// (matched by its own digest) when this is a retry of an earlier
// submission -- see HandleJoinRequest's poll loop. A PENDING record past
// its ExpiresAt is reclaimed exactly as ReserveJoinIdentity reclaims a
// lapsed reservation: read the stale value, then CompareAndSwap from
// exactly that value, never a blind overwrite, so a concurrent reclaimer
// is not clobbered. A decided record (approved, rejected, delivered) is
// always returned as-is, however old.
//
// A brand-new entry is refused with *PendingJoinQueueFullError once the
// queue already holds MaxPendingJoinRequests undecided-or-decided records
// -- see that constant's own doc comment; a RETRY of an existing entry is
// never refused by the cap, since it grows nothing.
func SubmitPendingJoinRequest(ctx context.Context, store kvstore.AtomicKVStore, req *JoinRequest, ttl time.Duration) (*pendingJoinRecord, error) {
	if ttl <= 0 {
		ttl = DefaultPendingJoinRequestTTL
	}
	id := csrDigestHex(req.CSRDER)
	key := pendingJoinKey(id)
	now := time.Now().UTC()
	fresh := &pendingJoinRecord{
		ID:          id,
		CSRDER:      req.CSRDER,
		NodeURI:     req.NodeURI,
		ReplicaID:   req.ReplicaID,
		Features:    req.Features,
		Status:      pendingStatusPending,
		SubmittedAt: now,
		ExpiresAt:   now.Add(ttl),
	}
	freshData, err := marshalPendingJoin(fresh)
	if err != nil {
		return nil, &PendingJoinStoreError{Op: "marshal", Detail: id, Err: err}
	}

	existingData, err := store.Get(ctx, key)
	if err != nil {
		return nil, &PendingJoinStoreError{Op: "get", Detail: key, Err: err}
	}
	if existingData != nil {
		existing, err := unmarshalPendingJoin(existingData)
		if err != nil {
			return nil, &PendingJoinStoreError{Op: "unmarshal", Detail: key, Err: err}
		}
		if !existing.expired(now) {
			return existing, nil
		}
		swapped, err := store.CompareAndSwap(ctx, key, existingData, freshData)
		if err != nil {
			return nil, &PendingJoinStoreError{Op: "compareandswap", Detail: key, Err: err}
		}
		if swapped {
			return fresh, nil
		}
		// Lost the reclaim race to a concurrent submitter or an operator
		// decision that landed in between; report whichever won rather
		// than guessing.
		again, err := store.Get(ctx, key)
		if err != nil {
			return nil, &PendingJoinStoreError{Op: "get", Detail: key, Err: err}
		}
		return unmarshalPendingJoin(again)
	}

	// A brand-new entry: this submission's only gate IS the queue, unlike
	// a reclaim above (always the same joiner retrying), so it must not
	// grow the queue without bound.
	keys, err := store.List(ctx, pendingJoinPrefix)
	if err != nil {
		return nil, &PendingJoinStoreError{Op: "list", Detail: pendingJoinPrefix, Err: err}
	}
	if len(keys) >= MaxPendingJoinRequests {
		return nil, &PendingJoinQueueFullError{}
	}
	stored, err := store.PutIfAbsent(ctx, key, freshData)
	if err != nil {
		return nil, &PendingJoinStoreError{Op: "putifabsent", Detail: key, Err: err}
	}
	if stored {
		return fresh, nil
	}
	// Lost a concurrent race to queue the SAME new CSR.
	again, err := store.Get(ctx, key)
	if err != nil {
		return nil, &PendingJoinStoreError{Op: "get", Detail: key, Err: err}
	}
	return unmarshalPendingJoin(again)
}

// ListPendingJoinRequests returns every undecided, unexpired queue entry
// for an operator to review. Decided entries (approved, rejected,
// delivered) are never returned here -- there is no "list decisions"
// read path, the same write/read split MintJoinToken's own "no list minted
// tokens" precedent follows -- an operator who needs that history reads it
// from wherever this node's audit log is configured to land.
func ListPendingJoinRequests(ctx context.Context, store kvstore.AtomicKVStore) ([]*PendingJoinRequest, error) {
	var out []*PendingJoinRequest
	now := time.Now().UTC()
	err := store.Scan(ctx, pendingJoinPrefix, func(key string, value []byte) error {
		r, err := unmarshalPendingJoin(value)
		if err != nil {
			slog.Warn("member-side CSR join: skipping an unparsable pending-queue record",
				"key", key, "error", err)
			return nil
		}
		if r.Status != pendingStatusPending || r.expired(now) {
			return nil
		}
		out = append(out, &PendingJoinRequest{
			ID:          r.ID,
			NodeURI:     r.NodeURI,
			ReplicaID:   r.ReplicaID,
			Features:    r.Features,
			SubmittedAt: r.SubmittedAt,
			ExpiresAt:   r.ExpiresAt,
		})
		return nil
	})
	if err != nil {
		return nil, &PendingJoinStoreError{Op: "scan", Detail: pendingJoinPrefix, Err: err}
	}
	return out, nil
}

// loadDecidable reads a record that ApproveJoinRequest or RejectJoinRequest
// may act on: present, unexpired, and still in the pendingStatusPending
// state -- an already-decided or expired entry is reported exactly like an
// absent one (*PendingJoinRequestNotFoundError), not re-decided.
func loadDecidable(data []byte) (*pendingJoinRecord, bool) {
	if data == nil {
		return nil, false
	}
	r, err := unmarshalPendingJoin(data)
	if err != nil {
		return nil, false
	}
	if r.Status != pendingStatusPending || r.expired(time.Now().UTC()) {
		return nil, false
	}
	return r, true
}

// ApproveJoinRequest binds id's queued CSR to
// spiffe://trustDomain/node/<nodeID> -- the same operator-chosen-identity
// invariant MintJoinToken enforces for the token path: the leaf eventually
// issued carries THIS identity, never the joiner's self-reported NodeURI.
// It does not sign anything itself: the next poll that presents the same
// CSR to HandleJoinRequest is what calls IssueLeafForKey. approver is the
// deciding operator's identity, recorded for audit, never re-verified here.
//
// Refuses with *ExistingMemberError when nodeID's identity is already
// admitted or reserved for an in-flight issuance -- the identical check
// MintJoinToken runs before minting -- and with
// *PendingJoinRequestNotFoundError when id names no live, undecided queue
// entry (absent, expired, or already decided).
func ApproveJoinRequest(ctx context.Context, store kvstore.AtomicKVStore, trustDomain, id, nodeID, approver string) (*pendingJoinRecord, error) {
	identity := spiffe.NodeIdentity{TrustDomain: trustDomain, NodeID: nodeID}
	if err := identity.Validate(); err != nil {
		return nil, &InvalidJoinIdentityError{Err: err}
	}
	boundNodeURI := identity.String()

	state, err := classifyIdentity(ctx, store, joinIdentityPrefix+boundNodeURI)
	if err != nil {
		return nil, err
	}
	if identityBlocksAdmission(state) {
		return nil, &ExistingMemberError{NodeURI: boundNodeURI}
	}

	key := pendingJoinKey(id)
	data, err := store.Get(ctx, key)
	if err != nil {
		return nil, &PendingJoinStoreError{Op: "get", Detail: key, Err: err}
	}
	record, ok := loadDecidable(data)
	if !ok {
		return nil, &PendingJoinRequestNotFoundError{ID: id}
	}

	updated := *record
	updated.Status = pendingStatusApproved
	updated.BoundNodeURI = boundNodeURI
	updated.DecidedBy = approver
	updated.DecidedAt = time.Now().UTC()
	updatedData, err := marshalPendingJoin(&updated)
	if err != nil {
		return nil, &PendingJoinStoreError{Op: "marshal", Detail: id, Err: err}
	}
	swapped, err := store.CompareAndSwap(ctx, key, data, updatedData)
	if err != nil {
		return nil, &PendingJoinStoreError{Op: "compareandswap", Detail: key, Err: err}
	}
	if !swapped {
		return nil, &PendingJoinRequestNotFoundError{ID: id}
	}
	return &updated, nil
}

// RejectJoinRequest records a terminal refusal of id's queued CSR. The
// joiner's next poll is answered with a refusal (never retried again by
// Join, which treats a rejection as terminal exactly as a bad token is).
// approver is the deciding operator's identity, recorded for audit.
//
// Refuses with *PendingJoinRequestNotFoundError when id names no live,
// undecided queue entry (absent, expired, or already decided).
func RejectJoinRequest(ctx context.Context, store kvstore.AtomicKVStore, id, reason, approver string) (*pendingJoinRecord, error) {
	key := pendingJoinKey(id)
	data, err := store.Get(ctx, key)
	if err != nil {
		return nil, &PendingJoinStoreError{Op: "get", Detail: key, Err: err}
	}
	record, ok := loadDecidable(data)
	if !ok {
		return nil, &PendingJoinRequestNotFoundError{ID: id}
	}

	updated := *record
	updated.Status = pendingStatusRejected
	updated.Reason = reason
	updated.DecidedBy = approver
	updated.DecidedAt = time.Now().UTC()
	updatedData, err := marshalPendingJoin(&updated)
	if err != nil {
		return nil, &PendingJoinStoreError{Op: "marshal", Detail: id, Err: err}
	}
	swapped, err := store.CompareAndSwap(ctx, key, data, updatedData)
	if err != nil {
		return nil, &PendingJoinStoreError{Op: "compareandswap", Detail: key, Err: err}
	}
	if !swapped {
		return nil, &PendingJoinRequestNotFoundError{ID: id}
	}
	return &updated, nil
}

// handleTokenlessJoin is HandleJoinRequest's branch for req.Token == "":
// the CSR already passed the same provider-readiness, signature and
// key-strength checks a token presentation would have. It queues (or
// re-reads) the pending request and reports its current state -- still
// pending, a terminal rejection, or an approval this call signs and
// delivers (caching the result for an idempotent re-poll).
func handleTokenlessJoin(ctx context.Context, provider cakey.Provider, store kvstore.AtomicKVStore, csr *x509.CertificateRequest, req *JoinRequest, advertiser FeatureAdvertiser) *JoinResponse {
	record, err := SubmitPendingJoinRequest(ctx, store, req, 0)
	if err != nil {
		slog.ErrorContext(ctx, "member-side CSR join: could not queue a tokenless join request for approval", "error", err)
		return &JoinResponse{Error: "join request could not be processed"}
	}

	switch record.Status {
	case pendingStatusRejected:
		return &JoinResponse{Error: "join request rejected by operator"}
	case pendingStatusDelivered:
		return &JoinResponse{CertPEM: record.CertPEM}
	case pendingStatusApproved:
		return deliverApprovedJoin(ctx, provider, store, record, csr, advertiser)
	default: // pendingStatusPending
		slog.InfoContext(ctx, "member-side CSR join: queued for operator approval", "request_id", record.ID)
		return &JoinResponse{Pending: true, RequestID: record.ID}
	}
}

// deliverApprovedJoin signs and admits an approved pending request through
// the same issueAndAdmit sequence the token path uses, then caches the
// resulting leaf on the queue record (pendingStatusDelivered) so a retried
// poll -- the response to THIS delivery never reaching the joiner -- is
// answered from the cache instead of re-signing. A concurrent poll of the
// SAME approved request that loses the ReserveJoinIdentity race inside
// issueAndAdmit is not left with a bare failure: it re-reads the queue
// record, and a sibling poll that already delivered it is reported as a
// cached success instead.
func deliverApprovedJoin(ctx context.Context, provider cakey.Provider, store kvstore.AtomicKVStore, record *pendingJoinRecord, csr *x509.CertificateRequest, advertiser FeatureAdvertiser) *JoinResponse {
	resp := issueAndAdmit(ctx, provider, store, record.BoundNodeURI, csr, record.ReplicaID, record.Features, advertiser)
	key := pendingJoinKey(record.ID)
	if resp.Error != "" {
		if data, gerr := store.Get(ctx, key); gerr == nil {
			if cur, uerr := unmarshalPendingJoin(data); uerr == nil && cur.Status == pendingStatusDelivered {
				return &JoinResponse{CertPEM: cur.CertPEM}
			}
		}
		return resp
	}

	data, err := store.Get(ctx, key)
	if err != nil {
		slog.ErrorContext(ctx, "member-side CSR join: signed an approved request but could not re-read its queue record to cache delivery",
			"request_id", record.ID, "error", err)
		return resp
	}
	cur, err := unmarshalPendingJoin(data)
	if err != nil {
		slog.ErrorContext(ctx, "member-side CSR join: signed an approved request but its queue record is unparsable",
			"request_id", record.ID, "error", err)
		return resp
	}
	updated := *cur
	updated.Status = pendingStatusDelivered
	updated.CertPEM = resp.CertPEM
	updatedData, err := marshalPendingJoin(&updated)
	if err != nil {
		slog.ErrorContext(ctx, "member-side CSR join: signed an approved request but could not encode it for caching",
			"request_id", record.ID, "error", err)
		return resp
	}
	if _, err := store.CompareAndSwap(ctx, key, data, updatedData); err != nil {
		slog.ErrorContext(ctx, "member-side CSR join: signed an approved request but could not cache delivery for idempotent re-polling",
			"request_id", record.ID, "error", err)
	}
	return resp
}

// PendingJoinStoreError wraps a failure reading or writing the replicated
// pending-join-request queue.
type PendingJoinStoreError struct {
	Op     string
	Detail string
	Err    error
}

// Error returns a human-readable description.
func (e *PendingJoinStoreError) Error() string {
	return "clusterenroll: pending join request store " + e.Op + " failed (" + e.Detail + "): " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *PendingJoinStoreError) Unwrap() error { return e.Err }

// PendingJoinQueueFullError is returned by SubmitPendingJoinRequest when a
// brand-new tokenless CSR would exceed MaxPendingJoinRequests.
type PendingJoinQueueFullError struct{}

// Error returns a human-readable description.
func (e *PendingJoinQueueFullError) Error() string {
	return "clusterenroll: pending join request queue is full"
}

// PendingJoinRequestNotFoundError is returned by ApproveJoinRequest and
// RejectJoinRequest when id names no live, undecided queue entry: absent,
// expired, or already decided.
type PendingJoinRequestNotFoundError struct {
	ID string
}

// Error returns a human-readable description.
func (e *PendingJoinRequestNotFoundError) Error() string {
	return "clusterenroll: no pending join request " + e.ID + " awaiting a decision"
}
