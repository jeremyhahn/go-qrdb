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

package custodian

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

const (
	// sysCustodianPrefix is the primary KV namespace for custodian records.
	sysCustodianPrefix = "__sys:custodian/"

	// sysCustodianIdxTenant is the tenant secondary index prefix.
	// Key: __sys:custodian-idx/tenant/{tenantID}/{custodianID}.
	sysCustodianIdxTenant = "__sys:custodian-idx/tenant/"

	// sysCustodianIdxUsername is the username secondary index prefix.
	// Key: __sys:custodian-idx/username/{tenantID}/{username}.
	sysCustodianIdxUsername = "__sys:custodian-idx/username/"
)

// Servicer manages the lifecycle of custodians: registration, retrieval,
// listing, and revocation. Share material is never stored; only the public
// metadata record is persisted.
type Servicer interface {
	// RegisterCustodian creates a custodian metadata record, seals the
	// provided Shamir share into an ECDH envelope, and returns the
	// RegisterResponse. The share plaintext is zeroed after sealing.
	// Returns ErrCustodianAlreadyExists if the username already exists
	// for the tenant.
	RegisterCustodian(ctx context.Context, req RegisterRequest) (*RegisterResponse, error)

	// GetCustodian retrieves a custodian by its unique ID.
	// Returns ErrCustodianNotFound when absent.
	GetCustodian(ctx context.Context, id string) (*Custodian, error)

	// ListCustodians returns all custodians for the given tenant.
	ListCustodians(ctx context.Context, tenantID string) ([]*Custodian, error)

	// RevokeCustodian marks the custodian as revoked. Idempotent: revoking
	// an already-revoked custodian returns nil.
	RevokeCustodian(ctx context.Context, id string) error
}

// service is the default Servicer implementation backed by a kvstore.KVStore.
//
// claims is the same store narrowed to its atomic conditional write. It is
// held separately, and required at construction, because registration's
// exactly-once guarantee rests on it: see NewService.
type service struct {
	store   kvstore.KVStore
	claims  kvstore.AtomicKVStore
	guarded kvstore.GuardedBatchKVStore
	audit   audit.AuditLogger
	logger  *slog.Logger
}

// Compile-time interface check.
var _ Servicer = (*service)(nil)

// NewService constructs a Servicer backed by the provided KV store.
// Both store and logger must be non-nil.
//
// The store must also implement kvstore.AtomicKVStore. Registration claims a
// tenant's username with PutIfAbsent so that exactly one of two concurrent
// enrolments for the same identity proceeds; without that primitive the claim
// degrades to a Get followed by a Put, which is the very check-then-act window
// it exists to close. Refusing here surfaces the missing capability at startup
// rather than at the first ceremony, and matches how every other caller in this
// repository treats the capability — see kvstore.AtomicKVStore and
// invite.NewDAOStore.
//
// Every production store satisfies it: service.KVServicer requires PutIfAbsent
// of all engines, and the Raft-backed adapter evaluates the condition inside
// the state machine so replicas cannot diverge.
func NewService(store kvstore.KVStore, auditLogger audit.AuditLogger, logger *slog.Logger) (Servicer, error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if auditLogger == nil {
		return nil, ErrNilAuditLogger
	}
	if logger == nil {
		return nil, ErrNilLogger
	}
	claims, ok := store.(kvstore.AtomicKVStore)
	if !ok {
		return nil, ErrAtomicWriteUnsupported
	}
	// The enrolment's three keys are written as ONE guarded transaction, so the
	// capability is required rather than optional. Refusing here surfaces a
	// store that cannot fence an enrolment at startup instead of at the first
	// ceremony — the same reason AtomicKVStore is checked above — and refusing
	// is the only honest answer: without it the writes can only be fenced one
	// at a time, which narrows the takeover race without closing it and is
	// exactly the data loss go-qrdb#21 exists to prevent.
	guarded, ok := store.(kvstore.GuardedBatchKVStore)
	if !ok {
		return nil, ErrGuardedBatchUnsupported
	}
	return &service{store: store, claims: claims, guarded: guarded, audit: auditLogger, logger: logger}, nil
}

// RegisterCustodian validates the request, derives the custodian's
// deterministic ID from tenantID+username, seals the Shamir share into an
// ECDH envelope, persists the custodian metadata, and returns the sealed
// envelope. The share plaintext bytes are zeroed by SealShareTo before this
// method returns.
func (s *service) RegisterCustodian(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	if err := validateRegisterRequest(req); err != nil {
		audit.EmitBestEffort(ctx, s.audit, "custodian", audit.AuditEvent{
			Resource:   "custodian",
			Action:     "register",
			Outcome:    audit.OutcomeFailure + ":" + err.Error(),
			Attributes: map[string]any{"username": req.Username, "tenantID": req.TenantID},
		})
		return nil, err
	}

	id := custodianID(req.TenantID, req.Username)

	// Duplicate registration must be refused however the existing record is
	// reachable — overwriting one replaces a share holder's enrolment with a
	// freshly sealed share, destroying the original. See findExistingID for
	// why one lookup is not enough.
	existingID, err := s.findExistingID(ctx, req.TenantID, req.Username, id)
	if err != nil {
		// A refusal that cannot be explained afterwards is a weak refusal:
		// this path rejects an enrolment because the store could not be read
		// or holds a corrupt record, both of which an operator must act on.
		audit.EmitBestEffort(ctx, s.audit, "custodian", audit.AuditEvent{
			Resource:   "custodian:" + id,
			Action:     "register",
			Outcome:    audit.OutcomeFailure + ":duplicate-check",
			Attributes: map[string]any{"username": req.Username, "tenantID": req.TenantID, "error": err.Error()},
		})
		return nil, err
	}
	if existingID != "" {
		// Name the record that actually exists rather than the one this call
		// would have created; on a legacy enrolment they differ.
		audit.EmitBestEffort(ctx, s.audit, "custodian", audit.AuditEvent{
			Resource:   "custodian:" + existingID,
			Action:     "register",
			Outcome:    audit.OutcomeFailure + ":already-exists",
			Attributes: map[string]any{"username": req.Username, "tenantID": req.TenantID},
		})
		return nil, ErrCustodianAlreadyExists
	}

	// Claim the username before sealing anything. findExistingID above is a
	// read, so two registrations for one tenant and username can both pass it;
	// they derive the SAME identifier and would then write the same three keys,
	// both reporting success while the later write decides which enrolment
	// survives. The loser's holder would keep an envelope for share material
	// the store no longer describes. PutIfAbsent settles that in one operation:
	// the key is created by exactly one caller.
	//
	// The claim occupies the username index key with a token unique to this
	// attempt. The registration's final write replaces that token with the
	// custodian identifier, so committing the enrolment and releasing the
	// claim are one operation and there is no window in which the identity is
	// unclaimed but the record exists.
	//
	// It is taken before SealShareTo so a refused registration has not yet
	// consumed the caller's share (SealShareTo zeroes req.ShareBytes) and so an
	// enrolment interrupted between the claim and the record has produced no
	// envelope material to lose.
	claim := newClaim(req.TenantID, req.Username, time.Now().UTC())
	claimed, err := s.acquireClaim(ctx, id, claim)
	if err != nil {
		outcome := audit.OutcomeFailure + ":claim"
		var incomplete *IncompleteEnrolmentError
		if errors.As(err, &incomplete) {
			outcome = audit.OutcomeFailure + ":already-claimed"
		}
		audit.EmitBestEffort(ctx, s.audit, "custodian", audit.AuditEvent{
			Resource:   "custodian:" + id,
			Action:     "register",
			Outcome:    outcome,
			Attributes: map[string]any{"username": req.Username, "tenantID": req.TenantID, "error": err.Error()},
		})
		return nil, err
	}
	if !claimed {
		// The username was committed to a custodian between the duplicate
		// check and here.
		audit.EmitBestEffort(ctx, s.audit, "custodian", audit.AuditEvent{
			Resource:   "custodian:" + id,
			Action:     "register",
			Outcome:    audit.OutcomeFailure + ":already-claimed",
			Attributes: map[string]any{"username": req.Username, "tenantID": req.TenantID},
		})
		return nil, ErrCustodianAlreadyExists
	}

	// Seal the share into an ECDH envelope. SealShareTo zeroes req.ShareBytes.
	envelope, err := SealShareTo(req.ShareBytes, req.PublicKeyPEM)
	if err != nil {
		s.releaseClaim(ctx, claim)
		audit.EmitBestEffort(ctx, s.audit, "custodian", audit.AuditEvent{
			Resource:   "custodian:" + id,
			Action:     "register",
			Outcome:    audit.OutcomeFailure + ":envelope-seal",
			Attributes: map[string]any{"username": req.Username, "tenantID": req.TenantID},
		})
		return nil, err
	}

	now := time.Now().UTC()
	c := &Custodian{
		ID:           id,
		Username:     req.Username,
		TenantID:     req.TenantID,
		Method:       req.Method,
		EnrollmentID: req.EnrollmentID,
		PublicKeyPEM: req.PublicKeyPEM,
		ShareIndex:   req.ShareIndex,
		EnrolledAt:   now,
	}

	if err := s.persistNew(ctx, c, claim); err != nil {
		audit.EmitBestEffort(ctx, s.audit, "custodian", audit.AuditEvent{
			Resource:   "custodian:" + id,
			Action:     "register",
			Outcome:    audit.OutcomeFailure + ":persist",
			Attributes: map[string]any{"username": req.Username, "tenantID": req.TenantID, "error": err.Error()},
		})
		return nil, err
	}

	s.logger.Info("custodian registered",
		slog.String("id", id),
		slog.String("username", req.Username),
		slog.String("tenantID", req.TenantID),
		slog.Int("shareIndex", req.ShareIndex))

	audit.EmitBestEffort(ctx, s.audit, "custodian", audit.AuditEvent{
		Resource: "custodian:" + id,
		Action:   "register",
		Outcome:  audit.OutcomeSuccess,
		Attributes: map[string]any{
			"username":   req.Username,
			"tenantID":   req.TenantID,
			"method":     string(req.Method),
			"shareIndex": req.ShareIndex,
		},
	})

	return &RegisterResponse{
		Custodian:      c,
		SealedEnvelope: envelope,
	}, nil
}

// GetCustodian retrieves a custodian by its unique ID.
func (s *service) GetCustodian(ctx context.Context, id string) (*Custodian, error) {
	data, err := s.store.Get(ctx, sysCustodianPrefix+id)
	if err != nil {
		return nil, &StoreError{Op: "get", Err: err}
	}
	if data == nil {
		return nil, ErrCustodianNotFound
	}
	var c Custodian
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, &StoreError{Op: "unmarshal", Err: err}
	}
	return &c, nil
}

// ListCustodians returns all custodian records for the given tenant by
// scanning the tenant secondary index.
func (s *service) ListCustodians(ctx context.Context, tenantID string) ([]*Custodian, error) {
	prefix := sysCustodianIdxTenant + tenantID + "/"
	keys, err := s.store.List(ctx, prefix)
	if err != nil {
		return nil, &StoreError{Op: "list", Err: err}
	}

	custodians := make([]*Custodian, 0, len(keys))
	for _, key := range keys {
		// Index keys end with the custodian ID.
		custodianID := key[len(prefix):]
		c, err := s.GetCustodian(ctx, custodianID)
		if err != nil {
			s.logger.Warn("custodian list: skipping missing record",
				slog.String("key", key),
				slog.String("error", err.Error()))
			continue
		}
		custodians = append(custodians, c)
	}
	return custodians, nil
}

// RevokeCustodian marks the custodian as revoked. Idempotent.
func (s *service) RevokeCustodian(ctx context.Context, id string) error {
	c, err := s.GetCustodian(ctx, id)
	if err != nil {
		audit.EmitBestEffort(ctx, s.audit, "custodian", audit.AuditEvent{
			Resource:   "custodian:" + id,
			Action:     "revoke",
			Outcome:    audit.OutcomeFailure + ":not-found",
			Attributes: map[string]any{"id": id},
		})
		return err
	}

	// Idempotent: already revoked is success.
	if c.RevokedAt != nil {
		return nil
	}

	now := time.Now().UTC()
	c.RevokedAt = &now

	if err := s.persist(ctx, c); err != nil {
		audit.EmitBestEffort(ctx, s.audit, "custodian", audit.AuditEvent{
			Resource:   "custodian:" + id,
			Action:     "revoke",
			Outcome:    audit.OutcomeFailure + ":store",
			Attributes: map[string]any{"id": id, "username": c.Username},
		})
		return err
	}

	s.logger.Info("custodian revoked",
		slog.String("id", id),
		slog.String("username", c.Username))

	audit.EmitBestEffort(ctx, s.audit, "custodian", audit.AuditEvent{
		Resource: "custodian:" + id,
		Action:   "revoke",
		Outcome:  audit.OutcomeSuccess,
		Attributes: map[string]any{
			"id":       id,
			"username": c.Username,
			"tenantID": c.TenantID,
		},
	})
	return nil
}

// custodianWrite is one key a custodian record occupies, with the store
// operation name reported when writing it fails.
type custodianWrite struct {
	op    string
	key   string
	value []byte
}

// custodianWrites returns the three keys a custodian record occupies, in the
// order they must be written: the primary record first, then the indexes that
// point at it, so no index is ever visible before its record.
//
// For a new enrolment the last of them — the username index — doubles as the
// commit: it replaces the claim token that registration put there with the
// custodian identifier, which is what makes the enrolment visible as an
// existing custodian rather than one in flight. See persistNew.
func custodianWrites(c *Custodian) ([]custodianWrite, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return nil, &StoreError{Op: "marshal", Err: err}
	}
	return []custodianWrite{
		{op: "put", key: sysCustodianPrefix + c.ID, value: data},
		{op: "put-idx-tenant", key: sysCustodianIdxTenant + c.TenantID + "/" + c.ID, value: []byte(c.ID)},
		{op: "put-idx-username", key: custodianUsernameKey(c.TenantID, c.Username), value: []byte(c.ID)},
	}, nil
}

// claimPrefix marks a username index entry as an uncommitted registration
// claim rather than a completed enrolment. A custodian identifier is a
// 36-character UUID, so it never carries this prefix and the two are never
// confused — including for a custodian enrolled under the old UUIDv5
// identifiers, whose index entry is likewise a bare UUID.
const claimPrefix = "claim:"

// claimTTL is how long a username claim stays authoritative before another
// registration may take it over (owner ruling 2026-08-23, go-qrdb#21).
//
// It bounds a CRASH, not a slow caller. The work a registration does while
// holding the claim is one ECDH seal and three KV writes; on a Raft-backed
// store that is milliseconds, and even a badly degraded cluster is orders of
// magnitude inside this. The generous margin is deliberate: expiring a claim
// whose registration is still running would let a second enrolment start for
// the same identity, which is the concurrent duplicate the claim exists to
// prevent. Waiting five minutes to recover a username a crash stranded is the
// cheaper mistake by far.
const claimTTL = 5 * time.Minute

// newClaim builds the username claim a registration takes before it seals
// anything.
//
// The value is unique to this attempt, not the custodian identifier the commit
// will replace it with. The identifier is derived from the tenant and username,
// so every registration for one identity computes the same one; a claim carrying
// it could not tell "the entry I created" from "an entry another attempt created
// after mine was removed", and compensation would then delete a stranger's
// enrolment. A per-attempt random token can only have been written by the
// attempt that generated it — rand.Text is 128+ bits, so a collision between
// two attempts is unreachable and the ownership test in rollback is exact.
//
// The value also carries an EXPIRY, as "claim:<unix seconds>:<token>". A
// process that dies between taking the claim and committing or compensating
// used to strand the username until an operator deleted the key by hand; the
// expiry makes that state self-healing. Unix seconds rather than RFC 3339
// because the format has to survive a split on ":" unambiguously, and
// IncompleteEnrolmentError renders the instant for operators.
func newClaim(tenantID, username string, now time.Time) custodianWrite {
	return custodianWrite{
		op:  "claim-username",
		key: custodianUsernameKey(tenantID, username),
		value: []byte(claimPrefix +
			strconv.FormatInt(now.Add(claimTTL).Unix(), 10) + ":" +
			rand.Text()),
	}
}

// acquireClaim takes the username claim for a registration, taking over an
// EXPIRED claim left behind by an interrupted one (owner ruling 2026-08-23,
// go-qrdb#21).
//
// It reports claimed=true when this call owns the claim. claimed=false with a
// nil error means the username belongs to a committed custodian. A live claim
// held by another registration is an IncompleteEnrolmentError, not a false:
// the remedy differs, and the error names when the claim expires.
//
// Two steps, and the second is the whole point:
//
//  1. PutIfAbsent creates the claim when the key is free. Unchanged.
//  2. When the key is taken, the value decides. A committed identifier means
//     the custodian exists. A LIVE claim means a registration is running or
//     has just been interrupted, and this call must refuse. An EXPIRED claim
//     is debris, and is taken over with CompareAndSwap.
//
// The take-over is a compare-and-swap on the exact stale bytes, not a delete
// followed by a create. Two registrations meeting one expired claim would
// otherwise both delete-then-create and both believe they own it: the window
// between the read and the write is exactly what a guarded write exists to
// close. With CAS the loser sees swapped=false, because the value it compared
// against is no longer there.
//
// A CAS that loses is reported as a live claim rather than retried. The winner
// holds a claim that is by definition fresh, so a retry would only refuse one
// round later, and refusing here keeps this function free of a loop whose
// bound would have to be justified.
func (s *service) acquireClaim(ctx context.Context, id string, claim custodianWrite) (bool, error) {
	created, err := s.claims.PutIfAbsent(ctx, claim.key, claim.value)
	if err != nil {
		return false, &StoreError{Op: claim.op, Err: err}
	}
	if created {
		return true, nil
	}

	existing, err := s.store.Get(ctx, claim.key)
	if err != nil {
		return false, &StoreError{Op: claim.op, Err: err}
	}
	if existing == nil {
		// The key was released between the PutIfAbsent and this read. Refuse
		// rather than loop: a retry by the caller is correct and cheap, and a
		// registration that silently re-raced here would be harder to reason
		// about than one that says the identity was contended.
		return false, &IncompleteEnrolmentError{ID: id, Key: claim.key}
	}
	if !isClaim(existing) {
		// A committed custodian identifier.
		return false, nil
	}

	expiry, parsed := claimExpiry(existing)
	if !claimIsExpired(existing, time.Now().UTC()) {
		return false, &IncompleteEnrolmentError{
			ID:        id,
			Key:       claim.key,
			ExpiresAt: expiry,
			Readable:  parsed,
		}
	}

	swapped, err := s.claims.CompareAndSwap(ctx, claim.key, existing, claim.value)
	if err != nil {
		return false, &StoreError{Op: "take-over-expired-claim", Err: err}
	}
	if !swapped {
		return false, &IncompleteEnrolmentError{ID: id, Key: claim.key}
	}

	s.logger.Warn("custodian registration: took over an expired username claim",
		slog.String("key", claim.key),
		slog.Time("expired_at", expiry))
	return true, nil
}

// isClaim reports whether a username index entry holds a registration claim
// rather than a committed custodian identifier.
func isClaim(value []byte) bool {
	return bytes.HasPrefix(value, []byte(claimPrefix))
}

// claimExpiry reports when a claim value stops being authoritative.
//
// ok is false for a claim this build cannot read — one written before the
// expiry existed, or a corrupt value. Such a claim is treated as NEVER
// expiring, which is the fail-closed direction: an unreadable claim might
// belong to a registration that is still running, and taking it over would
// admit the concurrent duplicate the claim exists to prevent. The operator
// recovery procedure in docs/seal/custodians.md covers clearing one by hand.
func claimExpiry(value []byte) (expiry time.Time, ok bool) {
	if !isClaim(value) {
		return time.Time{}, false
	}
	rest := value[len(claimPrefix):]
	sep := bytes.IndexByte(rest, ':')
	if sep <= 0 {
		return time.Time{}, false
	}
	secs, err := strconv.ParseInt(string(rest[:sep]), 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(secs, 0).UTC(), true
}

// claimIsExpired reports whether a claim may be taken over by another
// registration as of now.
func claimIsExpired(value []byte, now time.Time) bool {
	expiry, ok := claimExpiry(value)
	if !ok {
		return false
	}
	return now.After(expiry)
}

// persist serialises the custodian record and writes it together with its
// secondary indexes to the KV store.
//
// It is the UPDATE path — RevokeCustodian rewrites an existing record — so a
// failure leaves what is already there alone. Creating a record instead goes
// through persistNew, which compensates for a torn write.
func (s *service) persist(ctx context.Context, c *Custodian) error {
	writes, err := custodianWrites(c)
	if err != nil {
		return err
	}
	for _, w := range writes {
		if err := s.store.Put(ctx, w.key, w.value); err != nil {
			return &StoreError{Op: w.op, Err: err}
		}
	}
	return nil
}

// persistNew writes a brand-new enrolment and compensates for a torn write by
// removing whatever it managed to write and releasing the username claim the
// caller acquired for it.
//
// The caller already holds that claim, so this is the only registration that
// can be writing these keys. The last write commits: it replaces the claim
// token on the username index with the custodian identifier, which is what
// makes the enrolment visible to the duplicate check as an existing custodian.
//
// The KV store has no multi-key transaction — kvstore.AtomicKVStore offers
// single-key PutIfAbsent and nothing wider — so the three writes can tear.
// Without compensation the primary record survives a failed registration while
// the sealed envelope does not: the envelope is returned to the caller, never
// stored, so a registration that fails after writing the primary would leave a
// record that the duplicate check honours forever and an envelope that no
// longer exists. The custodian could then neither receive their share nor
// re-enrol.
//
// Compensating deletes run in reverse order, so an index is never left
// pointing at a record that has already gone, and the claim is released LAST:
// while it stands no other registration for that identity can start, so the
// debris is cleared before the username is available again. A delete that
// itself fails is reported as OrphanedEnrolmentError naming the keys that
// survived, rather than being folded into the write error: at that point an
// operator has something to clean up and must be told which keys.
//
// One limit remains, and it is a crash rather than a race: a process that dies
// between the claim and the compensation strands the claim, and the username
// cannot enrol until an operator removes that key. Registration reports the
// state distinctly — see IncompleteEnrolmentError — and nothing is lost with
// it, because the claim precedes the seal. Closing it entirely needs a
// multi-key atomic write, or a two-phase enrolment that persists the sealed
// envelope, and the latter would overturn this package's standing property
// that no share material is ever stored.
func (s *service) persistNew(ctx context.Context, c *Custodian, claim custodianWrite) error {
	writes, err := custodianWrites(c)
	if err != nil {
		s.releaseClaim(ctx, claim)
		return err
	}

	// ONE guarded multi-key write, fenced on this attempt's exact claim token.
	//
	// This is the whole enrolment: the primary record, the tenant index, and
	// the username-index commit that replaces the claim token with the
	// custodian identifier and so publishes the enrolment. The store evaluates
	// the guard and all three writes together, in one revision.
	//
	// It replaces a per-key fence — check ownership, write, check, write —
	// which narrowed the race without closing it. Claims EXPIRE (go-qrdb#21),
	// so a registration that stalls past the TTL loses its claim to another
	// caller that may already have committed; under the per-key fence the
	// stale owner could still lose the claim in the gap between one check and
	// its write, land that write on top of the winner's, and leave the
	// winner's published index pointing at the loser's record. Two callers
	// return success with different sealed envelopes and one record survives,
	// which is the duplicate enrolment the claim protocol exists to prevent
	// (Codex review on PR #111; owner ruling: multi-key CAS).
	//
	// Nothing to compensate on the losing branch, and nothing to roll back on
	// the winning one: a transaction whose compare fails writes nothing, so
	// there is no torn state to clean up and no claim of ours to release —
	// the claim now belongs to whoever took it over.
	kvWrites := make([]kvstore.KV, 0, len(writes))
	for _, w := range writes {
		kvWrites = append(kvWrites, kvstore.KV{Key: w.key, Value: w.value})
	}

	applied, err := s.guarded.PutAllIfUnchanged(ctx, claim.key, claim.value, kvWrites)
	if err != nil {
		// The outcome is UNKNOWN, and it must be RECONCILED rather than
		// reported. A store can apply a transaction and still return an error
		// — the proposal commits and the response misses the caller's deadline
		// — and by this point SealShareTo has zeroed the caller's share and the
		// envelope exists only in the response this function is about to
		// abandon. Reporting a failure there consumes the share: the records
		// are committed, so every retry answers ErrCustodianAlreadyExists,
		// while no custodian ever received the envelope. That is the enrolment
		// whose sealed envelope does not exist — the exact loss go-qrdb#21
		// exists to prevent, arrived at from the other direction.
		//
		// Reading the keys back settles it. They carry this attempt's exact
		// bytes, which no other registration would produce: the marshalled
		// custodian is this call's, and the identifier is derived, so all three
		// matching means THIS transaction applied.
		return s.reconcileAmbiguousPersist(ctx, c, claim, writes, err)
	}
	if !applied {
		s.logger.Warn("custodian registration: claim lost before commit, abandoning",
			slog.String("key", claim.key),
			slog.String("id", c.ID))
		return &ClaimLostError{ID: c.ID, Key: claim.key}
	}
	return nil
}

// releaseClaim removes a username claim whose registration never got as far as
// writing a record. It is best-effort by construction — there is nothing left
// to undo if the delete fails — so a surviving key is logged for an operator
// rather than reported to the caller, whose error is the failure that made the
// release necessary.
func (s *service) releaseClaim(ctx context.Context, claim custodianWrite) {
	// ONE compare-and-delete on the exact claim token, not a read followed by
	// an unconditional delete.
	//
	// Since claims EXPIRE (go-qrdb#21) the read-then-delete is a live TOCTOU,
	// not a theoretical one: this registration reads its own token, and before
	// the delete lands another registration may legitimately take the expired
	// claim over with a compare-and-swap. The unconditional delete then removes
	// the SUCCESSOR's claim, and a valid enrolment silently loses the identity
	// it had just acquired.
	//
	// That is precisely the window CompareAndDelete exists to close, and the
	// same one go-qrdb#41 closed for the guarded create: no amount of
	// re-reading helps, because the window is between the read and the delete.
	deleted, err := s.claims.CompareAndDelete(ctx, claim.key, claim.value)
	if err != nil {
		s.logger.Error("custodian registration: failed to release the username claim",
			slog.String("key", claim.key),
			slog.String("error", err.Error()))
		return
	}
	if !deleted {
		s.logger.Warn("custodian registration: username claim release skipped, it is no longer this registration's",
			slog.String("key", claim.key))
	}
}

// reconcileAmbiguousPersist decides what an errored guarded write actually did.
//
// Three outcomes, and only the last is a genuine unknown:
//
//   - Every key holds this attempt's exact bytes: the transaction APPLIED and
//     the error was on the way back. The enrolment succeeded, so this reports
//     success and the caller gets its envelope.
//   - The claim key no longer holds this attempt's token: another registration
//     took the identity over. Nothing of this attempt's is published, and the
//     caller is told it lost rather than that the store broke.
//   - Anything else, including a failure to read: still unknown. Deleting on a
//     guess could remove the winner's keys, so this names every key an operator
//     must inspect.
//
// The read is the only way to answer this. A transaction cannot tear, so there
// is no partial state to compensate — but "did it apply?" is a question the
// error alone cannot answer, and answering it wrongly costs a custodian their
// share.
func (s *service) reconcileAmbiguousPersist(ctx context.Context, c *Custodian, claim custodianWrite, writes []custodianWrite, cause error) error {
	orphaned := func() error {
		return &OrphanedEnrolmentError{
			ID:    c.ID,
			Keys:  keysOf(writes),
			Cause: &StoreError{Op: "persist", Err: cause},
		}
	}

	allOurs := true
	for _, w := range writes {
		current, readErr := s.store.Get(ctx, w.key)
		if readErr != nil {
			s.logger.Error("custodian registration: cannot reconcile an ambiguous persist",
				slog.String("key", w.key),
				slog.String("error", readErr.Error()))
			return orphaned()
		}
		if !bytes.Equal(current, w.value) {
			allOurs = false
			break
		}
	}
	if allOurs {
		s.logger.Warn("custodian registration: the guarded write reported an error but applied; "+
			"treating the enrolment as committed so the envelope is not lost",
			slog.String("id", c.ID),
			slog.String("error", cause.Error()))
		return nil
	}

	// Not ours. If the claim itself has moved on, another registration owns the
	// identity and this one simply lost — which is a result, not a fault.
	current, readErr := s.store.Get(ctx, claim.key)
	if readErr != nil {
		return orphaned()
	}
	if !bytes.Equal(current, claim.value) {
		return &ClaimLostError{ID: c.ID, Key: claim.key}
	}
	return orphaned()
}

// keysOf projects the key of each write, for reporting what was left behind.
func keysOf(writes []custodianWrite, more ...custodianWrite) []string {
	keys := make([]string, 0, len(writes)+len(more))
	for _, w := range writes {
		keys = append(keys, w.key)
	}
	for _, w := range more {
		keys = append(keys, w.key)
	}
	return keys
}

// validateRegisterRequest checks all required fields in a RegisterRequest.
func validateRegisterRequest(req RegisterRequest) error {
	if req.Username == "" {
		return ErrMissingUsername
	}
	if req.TenantID == "" {
		return ErrMissingTenantID
	}
	if req.EnrollmentID == "" {
		return ErrMissingEnrollmentID
	}
	if req.PublicKeyPEM == "" {
		return ErrMissingPublicKeyPEM
	}
	if req.ShareIndex < 1 {
		return ErrInvalidShareIndex
	}
	if len(req.ShareBytes) == 0 {
		return ErrEmptyShareBytes
	}
	switch req.Method {
	case FactorMethodPKCS11, FactorMethodPIV, FactorMethodFIDO2, FactorMethodSoftware, FactorMethodMTLS:
	default:
		return ErrInvalidMethod
	}
	return nil
}

// custodianUsernameKey builds the username secondary index key.
func custodianUsernameKey(tenantID, username string) string {
	return sysCustodianIdxUsername + tenantID + "/" + username
}

// findExistingID returns the ID of the custodian already enrolled for
// tenantID/username, or "" when there is none. derivedID is the identifier a
// new registration would take.
//
// It consults three sources because no single one is sufficient, and the
// consequence of missing an existing record is that RegisterCustodian
// overwrites a share holder's enrolment with a newly sealed share:
//
//  1. The username index. Catches the ordinary case, and is the only source
//     that finds a custodian enrolled before custodian IDs moved off
//     UUIDv5/SHA-1 — that record lives under an identifier this code can no
//     longer derive, since deriving it would call SHA-1. It is also where a
//     registration's username claim lives, so an entry still holding a claim
//     token is an enrolment in flight or interrupted rather than an existing
//     custodian; that is refused as IncompleteEnrolmentError, which says so
//     instead of reporting a custodian that does not exist.
//  2. The primary record at derivedID. persist writes the primary first and
//     the indexes after, and the KV store offers no transaction, so a failed
//     index write leaves a valid primary with no index. Without this check a
//     retry would read the missing index as "absent" and overwrite it.
//  3. A scan of the tenant index matching Username. Covers a record whose
//     username index is missing or damaged AND whose ID this code cannot
//     derive — the torn-write case for a legacy enrolment, which neither of
//     the cheaper lookups can see.
//
// The scan is last and runs only when the first two find nothing, so the
// ordinary path costs two Gets. Registration is a ceremony-time operation
// performed once per custodian, so the fallback's cost is not on any hot
// path.
func (s *service) findExistingID(ctx context.Context, tenantID, username, derivedID string) (string, error) {
	usernameKey := custodianUsernameKey(tenantID, username)
	indexed, err := s.store.Get(ctx, usernameKey)
	if err != nil {
		return "", &StoreError{Op: "check-exists", Err: err}
	}
	if indexed != nil {
		// A claim token means a registration for this identity holds the
		// username but has not committed: in flight on another caller, or
		// interrupted. Either way this call must not proceed, and neither is
		// an existing custodian.
		//
		// Unless the claim has EXPIRED (go-qrdb#21). Then it is debris from an
		// interrupted registration and this call reports the username as free,
		// so registration proceeds to acquireClaim — which takes the stale
		// claim over with a compare-and-swap. Deciding it here would be wrong:
		// this is a plain read, and between it and the write another caller
		// can take the same claim. The refusal that matters is the guarded
		// one.
		if isClaim(indexed) {
			if claimIsExpired(indexed, time.Now().UTC()) {
				return "", nil
			}
			expiry, parsed := claimExpiry(indexed)
			return "", &IncompleteEnrolmentError{
				ID:        derivedID,
				Key:       usernameKey,
				ExpiresAt: expiry,
				Readable:  parsed,
			}
		}
		return string(indexed), nil
	}

	primary, err := s.store.Get(ctx, sysCustodianPrefix+derivedID)
	if err != nil {
		return "", &StoreError{Op: "check-exists", Err: err}
	}
	if primary != nil {
		return derivedID, nil
	}

	return s.findByUsernameScan(ctx, tenantID, username)
}

// findByUsernameScan walks the tenant index looking for a custodian record
// with the given username. It reports "" when the tenant has no such record.
//
// An undecodable record fails the scan rather than being skipped. Decoding is
// precisely how a record's username would be learned, so a record that cannot
// be decoded cannot be ruled OUT as this username's enrolment — and treating
// it as absence would let the caller enrol a second custodian, under a second
// identifier, holding a second sealed share for a username that may already
// have one. Refusing costs an operator an explicit repair of a corrupt record
// at ceremony time; the alternative silently splits a share holder's
// enrolment in two.
func (s *service) findByUsernameScan(ctx context.Context, tenantID, username string) (string, error) {
	prefix := sysCustodianIdxTenant + tenantID + "/"
	keys, err := s.store.List(ctx, prefix)
	if err != nil {
		return "", &StoreError{Op: "check-exists", Err: err}
	}
	for _, key := range keys {
		candidateID := key[len(prefix):]
		data, err := s.store.Get(ctx, sysCustodianPrefix+candidateID)
		if err != nil {
			return "", &StoreError{Op: "check-exists", Err: err}
		}
		if data == nil {
			continue
		}
		var c Custodian
		if err := json.Unmarshal(data, &c); err != nil {
			s.logger.Error("custodian duplicate check: undecodable record",
				slog.String("id", candidateID),
				slog.String("error", err.Error()))
			return "", &CorruptRecordError{ID: candidateID, Err: err}
		}
		if c.Username == username {
			return c.ID, nil
		}
	}
	return "", nil
}

// custodianNamespace domain-separates custodian identifiers from every other
// SHA-256 derivation in the system. UUIDv8 leaves the derivation entirely to
// the application (RFC 9562 §5.8), so unlike a v3/v5 namespace this is
// hashed as a literal prefix rather than as sixteen namespace bytes.
const custodianNamespace = "go-qrdb:custodian-namespace/v8/"

// custodianID derives a custodian's stable identifier from its tenant and
// username as an RFC 9562 §5.8 UUIDv8 over SHA-256.
//
// It replaces a UUIDv5 (name-based, SHA-1) derivation: SHA-1 does not exist
// under FIPS 140-3, so that call panicked outright in a binary built with
// GODEBUG=fips140=only. RFC 4122 fixes v3 to MD5 and v5 to SHA-1, so v8 —
// the version reserved for application-defined derivations — is the only
// name-based form an approved hash can take. The 36-character UUID text
// shape is unchanged, so no caller, key, or wire format reformats.
//
// The value differs from what the SHA-1 derivation produced for the same
// inputs. Custodians enrolled earlier keep resolving because every read
// addresses them by their stored identifier: GetCustodian and
// RevokeCustodian take the ID as given, ListCustodians reads it out of the
// tenant index, and RegisterCustodian's duplicate check goes through the
// username index rather than re-deriving.
//
// tenantID and username are length-prefixed rather than merely joined so
// that the encoding is injective: without framing, tenant "a:b" with user
// "c" and tenant "a" with user "b:c" would hash identically and two
// distinct custodians would collide on one identifier.
func custodianID(tenantID, username string) string {
	buf := make([]byte, 0, len(custodianNamespace)+len(tenantID)+len(username)+16)
	buf = append(buf, custodianNamespace...)
	buf = strconv.AppendInt(buf, int64(len(tenantID)), 10)
	buf = append(buf, ':')
	buf = append(buf, tenantID...)
	buf = strconv.AppendInt(buf, int64(len(username)), 10)
	buf = append(buf, ':')
	buf = append(buf, username...)

	sum := sha256.Sum256(buf)

	var u uuid.UUID
	copy(u[:], sum[:len(u)])
	u[6] = (u[6] & 0x0f) | 0x80 // version 8
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 9562 variant (10xx)
	return u.String()
}
