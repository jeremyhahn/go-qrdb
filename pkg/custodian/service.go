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
	store  kvstore.KVStore
	claims kvstore.AtomicKVStore
	audit  audit.AuditLogger
	logger *slog.Logger
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
	return &service{store: store, claims: claims, audit: auditLogger, logger: logger}, nil
}

// RegisterCustodian validates the request, derives the custodian's
// deterministic ID from tenantID+username, seals the Shamir share into an
// ECDH envelope, persists the custodian metadata, and returns the sealed
// envelope. The share plaintext bytes are zeroed by SealShareTo before this
// method returns.
func (s *service) RegisterCustodian(ctx context.Context, req RegisterRequest) (*RegisterResponse, error) {
	if err := validateRegisterRequest(req); err != nil {
		s.audit.Emit(ctx, audit.AuditEvent{
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
		s.audit.Emit(ctx, audit.AuditEvent{
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
		s.audit.Emit(ctx, audit.AuditEvent{
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
	claim := newClaim(req.TenantID, req.Username)
	claimed, err := s.claims.PutIfAbsent(ctx, claim.key, claim.value)
	if err != nil {
		storeErr := &StoreError{Op: claim.op, Err: err}
		s.audit.Emit(ctx, audit.AuditEvent{
			Resource:   "custodian:" + id,
			Action:     "register",
			Outcome:    audit.OutcomeFailure + ":claim",
			Attributes: map[string]any{"username": req.Username, "tenantID": req.TenantID, "error": storeErr.Error()},
		})
		return nil, storeErr
	}
	if !claimed {
		// The username was claimed between the duplicate check and here, so a
		// concurrent registration owns this identity.
		s.audit.Emit(ctx, audit.AuditEvent{
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
		s.audit.Emit(ctx, audit.AuditEvent{
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
		s.audit.Emit(ctx, audit.AuditEvent{
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

	s.audit.Emit(ctx, audit.AuditEvent{
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
		s.audit.Emit(ctx, audit.AuditEvent{
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
		s.audit.Emit(ctx, audit.AuditEvent{
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

	s.audit.Emit(ctx, audit.AuditEvent{
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
func newClaim(tenantID, username string) custodianWrite {
	return custodianWrite{
		op:    "claim-username",
		key:   custodianUsernameKey(tenantID, username),
		value: []byte(claimPrefix + rand.Text()),
	}
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

	written := make([]custodianWrite, 0, len(writes))
	for i, w := range writes {
		if putErr := s.store.Put(ctx, w.key, w.value); putErr != nil {
			storeErr := &StoreError{Op: w.op, Err: putErr}
			stranded := s.rollback(ctx, claim, s.commitProof(ctx, writes, i), written)
			if len(stranded) > 0 {
				return &OrphanedEnrolmentError{ID: c.ID, Keys: stranded, Cause: storeErr}
			}
			return storeErr
		}
		written = append(written, w)
	}
	return nil
}

// commitProof returns the value rollback may additionally accept as proof that
// the claim is still this registration's, or nil when there is none.
//
// It is non-nil only when the write that failed was the commit — the last one,
// which replaces the claim token on the username index with the custodian
// identifier. A store can apply a write and still report a failure, so after
// that one the claim may legitimately read as the identifier rather than as
// the token.
//
// The identifier is derived, so every registration for this identity commits
// the same value and reading it back does not by itself prove who wrote it.
// The primary record settles that: it carries this call's marshalled custodian,
// which no other registration would produce. Without that corroboration the
// proof is withheld and rollback simply finds the claim unowned, which is the
// conservative answer.
func (s *service) commitProof(ctx context.Context, writes []custodianWrite, failed int) []byte {
	if failed != len(writes)-1 {
		return nil
	}
	ours, err := s.ownsPrimary(ctx, writes[0])
	if err != nil {
		s.logger.Error("custodian registration rollback: cannot establish record ownership",
			slog.String("key", writes[0].key),
			slog.String("error", err.Error()))
		return nil
	}
	if !ours {
		return nil
	}
	return writes[len(writes)-1].value
}

// ownsPrimary reports whether the primary record still holds exactly the bytes
// this registration wrote. An absent record is not this call's: it wrote one,
// so something else has been at these keys.
func (s *service) ownsPrimary(ctx context.Context, primary custodianWrite) (bool, error) {
	current, err := s.store.Get(ctx, primary.key)
	if err != nil {
		return false, err
	}
	return bytes.Equal(current, primary.value), nil
}

// rollback removes what this registration wrote and releases its username
// claim, in reverse order of writing. It returns the keys it could not remove.
// Every failure is logged as it happens: the caller reports the set, but only
// the individual delete error explains why each one survived.
//
// It compensates ONLY while this registration still owns the claim, and
// ownership is exact rather than inferred. PutIfAbsent reported that this call
// created the claim key, no other registration can create it again until this
// one releases it, and the token it holds was generated for this attempt
// alone. So the claim key carrying either that token or the identifier this
// call commits it to means every key below it is this call's to remove.
//
// If it carries anything else the claim was taken away — an operator clearing
// what looked like debris, and another enrolment then acquiring the identity —
// and what is in the store is that enrolment's, written under the same
// deterministic keys. Nothing is deleted and nothing is reported: this call has
// no debris left, because the other registration overwrote it.
func (s *service) rollback(ctx context.Context, claim custodianWrite, committed []byte, written []custodianWrite) []string {
	owned, err := s.ownsClaim(ctx, claim, committed)
	if err != nil {
		s.logger.Error("custodian registration rollback: cannot establish claim ownership",
			slog.String("key", claim.key),
			slog.String("error", err.Error()))
		return keysOf(written, claim)
	}
	if !owned {
		s.logger.Warn("custodian registration rollback skipped: the username claim is no longer this registration's",
			slog.String("key", claim.key))
		return nil
	}

	// written never holds the claim key: the write that would put the
	// identifier there is the last one, and succeeding at it ends the
	// registration rather than bringing it here.
	var stranded []string
	for i := len(written) - 1; i >= 0; i-- {
		if err := s.store.Delete(ctx, written[i].key); err != nil {
			s.logger.Error("custodian registration rollback failed",
				slog.String("key", written[i].key),
				slog.String("error", err.Error()))
			stranded = append(stranded, written[i].key)
		}
	}

	// The claim goes last: while it stands no other registration for this
	// identity can start, so the debris is cleared before the username is
	// available again.
	if err := s.store.Delete(ctx, claim.key); err != nil {
		s.logger.Error("custodian registration rollback failed to release the username claim",
			slog.String("key", claim.key),
			slog.String("error", err.Error()))
		stranded = append(stranded, claim.key)
	}
	return stranded
}

// releaseClaim removes a username claim whose registration never got as far as
// writing a record. It is best-effort by construction — there is nothing left
// to undo if the delete fails — so a surviving key is logged for an operator
// rather than reported to the caller, whose error is the failure that made the
// release necessary.
func (s *service) releaseClaim(ctx context.Context, claim custodianWrite) {
	owned, err := s.ownsClaim(ctx, claim, nil)
	if err != nil {
		s.logger.Error("custodian registration: cannot establish claim ownership to release it",
			slog.String("key", claim.key),
			slog.String("error", err.Error()))
		return
	}
	if !owned {
		s.logger.Warn("custodian registration: username claim release skipped, it is no longer this registration's",
			slog.String("key", claim.key))
		return
	}
	if err := s.store.Delete(ctx, claim.key); err != nil {
		s.logger.Error("custodian registration: failed to release the username claim",
			slog.String("key", claim.key),
			slog.String("error", err.Error()))
	}
}

// ownsClaim reports whether the username claim key still carries a value only
// this registration could have written: the token it claimed with, or — when
// the commit write may have landed — the identifier that token is replaced
// with. Pass a nil committed value when the registration never reached the
// commit.
//
// An absent claim is not owned. Something removed it, so another registration
// may have taken the identity since.
func (s *service) ownsClaim(ctx context.Context, claim custodianWrite, committed []byte) (bool, error) {
	current, err := s.store.Get(ctx, claim.key)
	if err != nil {
		return false, err
	}
	if current == nil {
		return false, nil
	}
	if bytes.Equal(current, claim.value) {
		return true, nil
	}
	return committed != nil && bytes.Equal(current, committed), nil
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
		if bytes.HasPrefix(indexed, []byte(claimPrefix)) {
			return "", &IncompleteEnrolmentError{ID: derivedID, Key: usernameKey}
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
