// Copyright 2026 Jeremy Hahn
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

package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// ---------------------------------------------------------------------------
// Storage layout
// ---------------------------------------------------------------------------
//
// The user auth-factor service persists three coordinated records per
// factor in the store it is constructed over:
//
//	__sys:auth/factors/{factorID}                         -> AuthFactor JSON
//	__sys:auth/users/{userID}/factors/{factorID}          -> factorID (bytes)
//	__sys:auth/lookup/{type}/{credKeyHex}                 -> factorID (bytes)
//
// Those keys carry NO tenant segment and are not meant to. This service is a
// PER-TENANT store: the tenant dimension is the STORE it is built over, never
// the key. routingUserAuthFactorService resolves that store from the
// request's tenant through a tenant.StoreResolver, exactly as pkg/user's
// routingUserService does for the User/Role IAM surface, so the physical keys
// are "{tenant}/__sys:auth/..." and a credential enrolled under one tenant
// cannot resolve a login begun under another (go-qrdb#393).
//
// Putting the tenant INTO the key was the rejected alternative. It is less
// code, and it would have inaugurated a second, divergent tenancy mechanism
// for the same class of data — per-tenant IAM — beside the one this
// repository already has. Two mechanisms drift, and the drift is where the
// next isolation bug is written.
//
// The first record is the canonical AuthFactor. The second is a per-user
// secondary index used by List. The third is a (type, credential lookup
// key) index used by login resolution to map an incoming auth event to a
// user.
//
// Factors that carry an SPKICredential (mtls / piv) additionally write a
// fourth record under the same lookup namespace:
//
//	__sys:auth/lookup/{type}/{spkiCredKeyHex}             -> factorID (bytes)
//
// keyed by the SHA-256 of the certificate's SubjectPublicKeyInfo, so a
// certificate renewed with the same keypair still resolves to the factor.

const (
	userFactorPrimaryPrefix = "__sys:auth/factors/"
	userFactorByUserPrefix  = "__sys:auth/users/"
	userFactorByUserSuffix  = "/factors/"
	userFactorLookupPrefix  = "__sys:auth/lookup/"
)

// UserAuthFactorServicer is the M14 user-side auth-factor service. It is
// distinct from AuthFactorServicer (which handles ceremony enrollment with
// challenge / verify nonces). The user service models a 1-to-many
// relationship between users and credentials and exposes the operations
// needed for login resolution.
type UserAuthFactorServicer interface {
	// Add persists a new factor for the given user. The factor's ID is
	// generated when empty and RegisteredAt is set when zero.
	Add(ctx context.Context, userID uint64, factor *auth.AuthFactor) error

	// AddConditional persists a new factor for the given user only when cond
	// holds against the user's factor set at the moment the add commits, and
	// returns the factor-set version it committed. The decision is one
	// guarded write on replicated state, so of N callers racing the same
	// condition exactly one wins; every other caller receives
	// *auth.FactorConditionFailedError and nothing is enrolled for it.
	AddConditional(ctx context.Context, userID uint64, factor *auth.AuthFactor, cond auth.FactorAddCondition) (uint64, error)

	// FactorSetVersion returns the version of the user's factor set, the
	// value a FactorAddCondition.ExpectedVersion is compared to. It is 0
	// until the first membership change commits and moves on every Add,
	// AddConditional, Remove and Rotate.
	FactorSetVersion(ctx context.Context, userID uint64) (uint64, error)

	// List returns every factor (active AND revoked) belonging to the user
	// in registration order.
	List(ctx context.Context, userID uint64) ([]*auth.AuthFactor, error)

	// Get returns the factor identified by factorID or an
	// auth.FactorNotFoundError.
	Get(ctx context.Context, factorID string) (*auth.AuthFactor, error)

	// LookupByCredentialKey returns the active factor whose (Type, key)
	// tuple matches. Used by login resolution. Returns
	// auth.FactorRevokedError when the matching factor has been revoked.
	LookupByCredentialKey(ctx context.Context, t auth.AuthFactorType, key []byte) (*auth.AuthFactor, error)

	// Remove soft-deletes the factor by setting RevokedAt to now. When
	// force is false the call returns auth.LastFactorError if it would
	// remove the user's last active factor. When force is true the user's
	// last active factor may be removed (admin override).
	Remove(ctx context.Context, factorID string, force bool) error

	// Delete hard-deletes an active factor: its record and the user,
	// credential and SPKI index entries that point at it are dropped in one
	// guarded batch, so the same credential can be bound again. It exists to
	// roll back an add; a revoked factor is refused with
	// auth.FactorRevokedError, so a revocation is never undone by deleting
	// and re-adding. A factor that does not exist returns
	// auth.FactorNotFoundError. force has Remove's meaning. A store without
	// a multi-guarded batch is refused with
	// ErrUserAuthFactorBatchStoreRequired before anything is written.
	Delete(ctx context.Context, factorID string, force bool) error

	// Rotate atomically adds the new factor and revokes the old factor in
	// the same logical operation. The new factor inherits UserID from the
	// old factor when its UserID is zero.
	Rotate(ctx context.Context, oldID string, newFactor *auth.AuthFactor) error

	// Relabel updates the human-readable label of the factor.
	Relabel(ctx context.Context, factorID string, label string) error

	// MarkUsed updates LastUsedAt for the factor. Implementations may run
	// this asynchronously; callers should ignore errors.
	MarkUsed(ctx context.Context, factorID string, ts time.Time) error
}

// ActiveFactorChecker is the FAIL-CLOSED active-factor query. Unlike List —
// which serves display paths, walks only the per-user secondary index, and
// deliberately skips entries it cannot read — an implementation MUST answer
// from a complete, authoritative read of the canonical factor records and
// MUST return an error whenever that read cannot be completed: any record
// read failure is an error, never a success over a partial factor set, and
// a factor a secondary index fails to name must still be found. Security
// gates depend on this contract (deferred tenant-admin provisioning in
// pkg/bootstrap consults it before handing back an invitation target). The
// production user auth-factor service implements it alongside
// UserAuthFactorServicer.
type ActiveFactorChecker interface {
	// HasActiveFactor reports whether the user owns at least one active
	// (non-revoked) auth factor.
	HasActiveFactor(ctx context.Context, userID uint64) (bool, error)
}

// AttestationRecorder is the trusted-server path for recording what an
// enrollment ceremony PROVED about a factor. It is the ONE way a value inside
// the server-owned attest_* metadata namespace is written (go-qrdb#342).
//
// It is deliberately NOT part of UserAuthFactorServicer. Every network
// transport -- REST, gRPC, MCP, QUIC, the Unix socket -- reaches the service
// through a handler that calls Add or Rotate, both of which refuse a
// caller-supplied attest_* key and stamp auth.Unattested. No handler maps to
// this interface, so nothing on any wire can reach it; only code running in
// the process that hosts the service can, and that process is the composer
// that ran the ceremony and verified the attestation chain. The production
// service implements it alongside UserAuthFactorServicer; a host holding the
// servicer reaches it by type assertion, exactly as ActiveFactorChecker is
// reached.
//
// Both methods refuse caller-supplied attest_* keys in the factor's Metadata
// just as Add and Rotate do: the typed attestation is the only input, so
// there is never a second place the same claim could arrive from.
type AttestationRecorder interface {
	// AddAttested persists a new factor for the user, recording attestation
	// as what the ceremony proved. Returns *auth.InvalidAttestationError when
	// the attestation does not name a defined level; otherwise the contract
	// is Add's.
	AddAttested(ctx context.Context, userID uint64, factor *auth.AuthFactor, attestation auth.VerifiedAttestation) error

	// RotateAttested atomically adds newFactor, recorded with attestation,
	// and revokes the old factor. Returns *auth.InvalidAttestationError when
	// the attestation does not name a defined level; otherwise the contract
	// is Rotate's.
	RotateAttested(ctx context.Context, oldID string, newFactor *auth.AuthFactor, attestation auth.VerifiedAttestation) error
}

// UserStateUpdater optionally receives a callback whenever a user's last
// active factor is force-removed, so the embedding system can mark the
// user inactive. The interface is satisfied by any user store that
// supports DisableUser(ctx, id string) error semantics.
type UserStateUpdater interface {
	// SetUserActive flips the user's active flag. Implementations MUST be
	// idempotent.
	SetUserActive(ctx context.Context, userID uint64, active bool) error
}

// AuthFactorKV is the whole key-value surface the user auth-factor service
// uses: three verbs over a flat keyspace.
//
// It is declared rather than reusing KVServicer because the service must be
// constructible over EITHER of the two stores this repository hands out — the
// replicated KVServicer a composition root holds, and the per-tenant
// kvstore.KVStore a tenant.StoreResolver returns — and both satisfy these
// three signatures verbatim. Narrowing the dependency is what lets the routed
// service build one inner service per tenant without an adapter standing
// between them.
//
// The two stores disagree about one thing, and the service is written to
// tolerate both: a missing key reads back as a typed KeyNotFoundError from
// KVServicer and as (nil, nil) from kvstore.KVStore. Every read here treats
// "error or empty" as absent, so neither convention is privileged.
type AuthFactorKV interface {
	// Put stores value under key.
	Put(ctx context.Context, key string, value []byte) error

	// Get returns the value stored under key. An absent key may be reported
	// either as an error or as an empty value; see above.
	Get(ctx context.Context, key string) ([]byte, error)

	// List returns the keys carrying prefix.
	List(ctx context.Context, prefix string) ([]string, error)
}

// UserAuthFactorServiceConfig holds the dependencies for the service.
type UserAuthFactorServiceConfig struct {
	// KV is the durable store used to persist factor records. It is ONE
	// TENANT's store: see the storage-layout comment at the top of this file.
	// Composition roots that serve more than one tenant build the routed
	// service (NewRoutingUserAuthFactorService) instead of passing a shared
	// store here.
	KV AuthFactorKV

	// UserStateUpdater is invoked when a force=true Remove or Delete takes
	// away the last active factor. May be nil to skip the user-state hook.
	UserStateUpdater UserStateUpdater

	// AuditLogger receives the event of every hard Delete. May be nil, in
	// which case nothing is emitted.
	AuditLogger audit.AuditLogger
}

// userAuthFactorService is the default implementation. It serialises
// mutations through a process-local mutex so multi-record updates (Add,
// Rotate, Remove) are atomic with respect to other mutators sharing the
// same process. Cross-process atomicity is provided by the underlying
// KVServicer (Raft-replicated puts).
// The production service satisfies both the servicer and the fail-closed
// checker; consumers holding a UserAuthFactorServicer reach the checker via
// type assertion.
var _ ActiveFactorChecker = (*userAuthFactorService)(nil)
var _ AttestationRecorder = (*userAuthFactorService)(nil)

type userAuthFactorService struct {
	kv      AuthFactorKV
	users   UserStateUpdater
	audit   audit.AuditLogger
	idGen   func() string
	nowFunc func() time.Time
	mu      sync.Mutex
}

// Compile-time assertion that the service implements the public interface
// AND auth.FactorStore (so it can be passed to LoginResolver directly).
var (
	_ UserAuthFactorServicer = (*userAuthFactorService)(nil)
	_ auth.FactorStore       = (*userAuthFactorService)(nil)
)

// NewUserAuthFactorService constructs the M14 user auth-factor service over
// ONE tenant's store. KV must be non-nil.
//
// A composition that serves several tenants builds
// NewRoutingUserAuthFactorService instead; this constructor is what that
// router builds per tenant, and what a genuinely single-tenant composition
// builds directly over a store already confined to its tenant's identity
// namespace (tenant.IdentityNamespace).
func NewUserAuthFactorService(cfg UserAuthFactorServiceConfig) (UserAuthFactorServicer, error) {
	return newUserAuthFactorService(cfg)
}

// newUserAuthFactorService is NewUserAuthFactorService returning the concrete
// type. The router builds inner services through it so it reaches
// ActiveFactorChecker and AttestationRecorder directly rather than asserting
// for interfaces it just constructed.
func newUserAuthFactorService(cfg UserAuthFactorServiceConfig) (*userAuthFactorService, error) {
	if cfg.KV == nil {
		return nil, &auth.InvalidFactorError{Reason: "kv store is required"}
	}
	return &userAuthFactorService{
		kv:      cfg.KV,
		users:   cfg.UserStateUpdater,
		audit:   cfg.AuditLogger,
		idGen:   uuid.NewString,
		nowFunc: func() time.Time { return time.Now().UTC() },
	}, nil
}

// Add persists the factor along with its secondary indexes, recorded as
// auth.Unattested: this is the path every transport handler reaches, and a
// factor written through it proved nothing about where its key lives. The
// server stamps that fact rather than leaving the key absent, so a record
// that proved nothing is distinguishable from one written before the field
// existed (go-qrdb#342).
func (s *userAuthFactorService) Add(ctx context.Context, userID uint64, factor *auth.AuthFactor) error {
	_, err := s.add(ctx, userID, factor, auth.Unattested(), nil)
	return err
}

// AddConditional implements UserAuthFactorServicer.
func (s *userAuthFactorService) AddConditional(ctx context.Context, userID uint64, factor *auth.AuthFactor, cond auth.FactorAddCondition) (uint64, error) {
	return s.add(ctx, userID, factor, auth.Unattested(), &cond)
}

// AddAttested implements AttestationRecorder.
func (s *userAuthFactorService) AddAttested(ctx context.Context, userID uint64, factor *auth.AuthFactor, attestation auth.VerifiedAttestation) error {
	if err := attestation.Validate(); err != nil {
		return err
	}
	_, err := s.add(ctx, userID, factor, attestation, nil)
	return err
}

// add is the single write path behind Add and AddAttested. The only
// difference between the two is the attestation that is stamped; every
// validation, including the refusal of caller-supplied attest_* keys, is
// shared so the trusted path cannot drift looser than the wire path.
func (s *userAuthFactorService) add(ctx context.Context, userID uint64, factor *auth.AuthFactor, attestation auth.VerifiedAttestation, cond *auth.FactorAddCondition) (uint64, error) {
	if factor == nil {
		return 0, &auth.InvalidFactorError{Reason: "factor is nil"}
	}
	if userID == 0 {
		return 0, &auth.InvalidFactorError{Reason: "userID must be non-zero"}
	}
	if !factor.Type.IsValid() {
		return 0, &auth.FactorTypeUnsupportedError{Type: string(factor.Type)}
	}
	if len(factor.Credential) == 0 {
		return 0, &auth.InvalidFactorError{Reason: "credential blob is empty"}
	}
	// The server-owned metadata namespace is refused here, at the service,
	// rather than in each handler: REST, gRPC, MCP, QUIC, the Unix socket
	// (which registers the gRPC servicer) and the embedded SDK transport all
	// reach this method, so one check covers every way in and a new transport
	// inherits it by construction. Refusing rather than stripping is the
	// point -- a caller who sets an attestation key and receives a success
	// would reasonably believe it was recorded (go-qrdb#234).
	if err := auth.ValidateMetadata(factor.Metadata); err != nil {
		return 0, err
	}
	if cond != nil {
		if err := cond.Validate(); err != nil {
			return 0, err
		}
	}
	// A conditional add is decided by the guarded write; a store without one
	// is refused before anything is written rather than decided by a read.
	store := s.atomicStore()
	if cond != nil && store == nil {
		return 0, ErrUserAuthFactorAtomicStoreRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Normalise.
	factor.UserID = userID
	if factor.ID == "" {
		factor.ID = s.idGen()
	}
	if factor.RegisteredAt.IsZero() {
		factor.RegisteredAt = s.nowFunc()
	}

	lookupKey := lookupKeyFor(factor.Type, factor.Credential)
	if existingID, err := s.lookupFactorID(ctx, factor.Type, factor.Credential); err == nil && existingID != "" {
		return 0, &auth.FactorAlreadyExistsError{Type: factor.Type, FactorID: existingID}
	}
	if len(factor.SPKICredential) > 0 {
		if existingID, err := s.lookupFactorID(ctx, factor.Type, factor.SPKICredential); err == nil && existingID != "" {
			return 0, &auth.FactorAlreadyExistsError{Type: factor.Type, FactorID: existingID}
		}
	}

	// The server, not the caller, says what was proved. ValidateMetadata has
	// already established the caller supplied nothing in the namespace, so
	// this never overwrites anything.
	factor.Metadata = attestation.Stamp(factor.Metadata)

	data, err := json.Marshal(factor)
	if err != nil {
		return 0, &auth.InvalidFactorError{Reason: "marshal: " + err.Error()}
	}

	// Primary record, reserved: an ID already in use is refused here, before
	// anything is written. Nothing points at the record until the factor set
	// commits, so a refused add leaves no record a login or a List can reach.
	if err := s.reservePrimary(ctx, factor, data); err != nil {
		return 0, err
	}
	batch := s.batchStore()
	var version uint64
	switch {
	case batch != nil:
		version, err = s.commitEnrollment(ctx, batch, factor, data, cond)
	case store != nil:
		version, err = s.commitFactorSet(ctx, store, userID, func(set *factorSet) error {
			if cond != nil {
				if err := checkFactorCondition(set, userID, factor.Type, *cond); err != nil {
					return err
				}
			}
			set.Active[factor.ID] = factor.Type
			return nil
		})
	}
	if err != nil {
		// Refused outright -- nothing committed -- so the primary this add
		// reserved is released, guarded on the bytes written above so a
		// record anything else has since written is left alone. Any other
		// error leaves the outcome unknown and the record in place.
		if store != nil && (errors.Is(err, auth.ErrFactorConditionFailed) || errors.Is(err, ErrFactorSetContended) || errors.Is(err, auth.ErrFactorAlreadyExists)) {
			_, _ = store.CompareAndDelete(ctx, primaryKey(factor.ID), data)
		}
		return 0, err
	}
	if batch != nil {
		// commitEnrollment published the indexes with the factor set.
		return version, nil
	}
	// Secondary index by user.
	if err := s.kv.Put(ctx, userIndexKey(userID, factor.ID), []byte(factor.ID)); err != nil {
		return 0, err
	}
	// Lookup index by (type, credential).
	if err := s.kv.Put(ctx, lookupKey, []byte(factor.ID)); err != nil {
		return 0, err
	}
	// Secondary lookup index by (type, SPKI credential). Renewal-stable:
	// a certificate renewed with the same keypair resolves through this
	// entry even though its SHA-256(cert) credential changed.
	if len(factor.SPKICredential) > 0 {
		if err := s.kv.Put(ctx, lookupKeyFor(factor.Type, factor.SPKICredential), []byte(factor.ID)); err != nil {
			return 0, err
		}
	}
	return version, nil
}

// reservePrimary writes factor's primary record only where no record holds
// its ID, and refuses with *auth.FactorAlreadyExistsError where one does. A
// caller may name the ID, so an unguarded write would overwrite another
// factor -- another user's included -- and a refused add's release would then
// delete it; reserved, the record is one this call created, the only record
// a release may remove. The guard is the store's own: PutIfAbsent, else an
// absence guard on the batch. A store with neither offers no atomic test, so
// it is read first: that narrows the overwrite to a race with a concurrent
// add, and such a store is never released from, as add's release needs
// CompareAndDelete.
func (s *userAuthFactorService) reservePrimary(ctx context.Context, factor *auth.AuthFactor, data []byte) error {
	key := primaryKey(factor.ID)
	var (
		stored bool
		err    error
	)
	if store := s.atomicStore(); store != nil {
		stored, err = store.PutIfAbsent(ctx, key, data)
	} else if batch := s.batchStore(); batch != nil {
		stored, err = batch.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: key}}, []kvstore.KV{{Key: key, Value: data}})
	} else {
		var existing []byte
		if existing, err = s.getRecord(ctx, key); err == nil && existing == nil {
			stored, err = true, s.kv.Put(ctx, key, data)
		}
	}
	if err != nil {
		return err
	}
	if !stored {
		return &auth.FactorAlreadyExistsError{Type: factor.Type, FactorID: factor.ID}
	}
	return nil
}

// List returns the user's factors sorted by registration time.
func (s *userAuthFactorService) List(ctx context.Context, userID uint64) ([]*auth.AuthFactor, error) {
	if userID == 0 {
		return nil, &auth.InvalidFactorError{Reason: "userID must be non-zero"}
	}
	keys, err := s.kv.List(ctx, userIndexPrefix(userID))
	if err != nil {
		return nil, err
	}
	out := make([]*auth.AuthFactor, 0, len(keys))
	for _, k := range keys {
		raw, err := s.kv.Get(ctx, k)
		if err != nil || len(raw) == 0 {
			continue
		}
		factorID := string(raw)
		f, err := s.Get(ctx, factorID)
		if err != nil {
			continue
		}
		out = append(out, f)
	}
	return out, nil
}

// HasActiveFactor implements ActiveFactorChecker with strict reads over the
// canonical PRIMARY factor records, not the per-user secondary index: Add
// writes the primary before the index, so a partially indexed enrollment
// (index write failed) leaves an orphaned primary that no index entry
// names, and index corruption could point at another user's factor. For a
// fail-closed security gate the primaries are the only authoritative set.
// Any unreadable record errors the call rather than being skipped. The
// full-prefix scan is acceptable because this query guards rare
// control-plane operations (tenant provisioning), never login resolution.
func (s *userAuthFactorService) HasActiveFactor(ctx context.Context, userID uint64) (bool, error) {
	if userID == 0 {
		return false, &auth.InvalidFactorError{Reason: "userID must be non-zero"}
	}
	// Serialized with every factor mutation (Add / Remove / Rotate /
	// Relabel / MarkUsed all hold s.mu), so the scan cannot interleave
	// with a mid-flight enrollment's partial writes: an Add that completed
	// before this call is always seen, and one that starts during it
	// lands entirely after the answer is computed.
	s.mu.Lock()
	defer s.mu.Unlock()
	keys, err := s.kv.List(ctx, userFactorPrimaryPrefix)
	if err != nil {
		return false, err
	}
	for _, k := range keys {
		f, err := s.Get(ctx, strings.TrimPrefix(k, userFactorPrimaryPrefix))
		if err != nil {
			return false, err
		}
		if f.UserID == userID && f.IsActive() {
			return true, nil
		}
	}
	return false, nil
}

// Get loads the canonical factor by ID.
func (s *userAuthFactorService) Get(ctx context.Context, factorID string) (*auth.AuthFactor, error) {
	if factorID == "" {
		return nil, &auth.InvalidFactorError{Reason: "factorID is empty"}
	}
	raw, err := s.kv.Get(ctx, primaryKey(factorID))
	if err != nil || len(raw) == 0 {
		return nil, &auth.FactorNotFoundError{FactorID: factorID}
	}
	var f auth.AuthFactor
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, &auth.InvalidFactorError{Reason: "unmarshal: " + err.Error()}
	}
	return &f, nil
}

// LookupByCredentialKey resolves an active factor by its (type, key) tuple.
func (s *userAuthFactorService) LookupByCredentialKey(ctx context.Context, t auth.AuthFactorType, key []byte) (*auth.AuthFactor, error) {
	if !t.IsValid() {
		return nil, &auth.FactorTypeUnsupportedError{Type: string(t)}
	}
	if len(key) == 0 {
		return nil, &auth.InvalidFactorError{Reason: "lookup key is empty"}
	}
	id, err := s.lookupFactorID(ctx, t, key)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, &auth.FactorNotFoundError{}
	}
	f, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if f.RevokedAt != nil {
		return nil, &auth.FactorRevokedError{FactorID: f.ID}
	}
	return f, nil
}

// Remove soft-deletes the factor. When force=false and the factor is the
// last active factor of the user, returns auth.LastFactorError.
//
// On a store with multi-guarded batches the revoked record and the factor
// set, one version later, commit in ONE batch guarded on both as read, as a
// hard Delete's do: a failed write leaves neither, so the factor is never
// revoked under the version a conditional add was decided against, and a
// Delete or another change committed in between refuses the batch instead of
// having the revoked record written back over it. A store with only a
// single-key guard writes the revocation and then commits the version.
func (s *userAuthFactorService) Remove(ctx context.Context, factorID string, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if store := s.batchStore(); store != nil {
		_, err := s.commitRemoval(ctx, store, func() (*factorRemovalPlan, error) {
			return s.planRevoke(ctx, factorID, force)
		})
		return err
	}

	factor, err := s.Get(ctx, factorID)
	if err != nil {
		return err
	}
	if factor.RevokedAt != nil {
		return &auth.FactorRevokedError{FactorID: factor.ID}
	}

	// Count active factors for this user.
	active, err := s.countActive(ctx, factor.UserID)
	if err != nil {
		return err
	}
	if active <= 1 && !force {
		return &auth.LastFactorError{UserID: factor.UserID, FactorID: factor.ID}
	}

	now := s.nowFunc()
	factor.RevokedAt = &now
	data, err := json.Marshal(factor)
	if err != nil {
		return &auth.InvalidFactorError{Reason: "marshal: " + err.Error()}
	}
	if err := s.kv.Put(ctx, primaryKey(factor.ID), data); err != nil {
		return err
	}
	// The revocation above already takes the factor out of the live set; the
	// commit records the change as a new factor-set version.
	if err := s.commitMembership(ctx, factor.UserID); err != nil {
		return err
	}

	// If we just removed the last active factor under force, mark user inactive.
	if active <= 1 && force && s.users != nil {
		if err := s.users.SetUserActive(ctx, factor.UserID, false); err != nil {
			return err
		}
	}
	return nil
}

// Delete implements UserAuthFactorServicer.
//
// The record, its index entries and the factor-set record change in ONE
// multi-guarded batch: every key the batch touches is guarded on the value
// read, so a concurrent writer refuses the batch instead of interleaving with
// it, and a refused batch is re-read and retried up to
// factorSetCommitAttempts times. Writing them one at a time, as add does,
// would leave a window in which a crash keeps an index pointing at a record
// that is gone, and the credential would stay reserved: the defect a hard
// delete exists to remove.
//
// An index entry is deleted only while it still points at this factor. A
// Rotate to a renewed certificate on the same keypair moves the SPKI entry to
// the new factor, and deleting that entry would strand the new factor's
// renewal-stable login.
func (s *userAuthFactorService) Delete(ctx context.Context, factorID string, force bool) error {
	if factorID == "" {
		return &auth.InvalidFactorError{Reason: "factorID is empty"}
	}
	store := s.batchStore()
	if store == nil {
		return ErrUserAuthFactorBatchStoreRequired
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	plan, err := s.commitRemoval(ctx, store, func() (*factorRemovalPlan, error) {
		return s.planDelete(ctx, factorID, force)
	})
	if err != nil {
		return err
	}
	s.emitDeleted(ctx, plan.factor, force)
	return nil
}

// factorRemovalPlan is one attempt of Remove or Delete: the batch to propose
// and what the caller does once it applies.
type factorRemovalPlan struct {
	factor *auth.AuthFactor
	guards []kvstore.Guard
	writes []kvstore.KV
	// last reports that the factor was the user's last active one, which a
	// non-force call has already refused.
	last bool
}

// commitRemoval proposes the batch plan builds until one applies, building it
// again from fresh reads after every refusal, and marks the user inactive once
// a batch taking out the user's last active factor has applied. It returns the
// plan that applied.
func (s *userAuthFactorService) commitRemoval(ctx context.Context, store factorBatchStore, plan func() (*factorRemovalPlan, error)) (*factorRemovalPlan, error) {
	var userID uint64
	for range factorSetCommitAttempts {
		p, err := plan()
		if err != nil {
			return nil, err
		}
		userID = p.factor.UserID
		applied, err := store.PutAllIfAllUnchanged(ctx, p.guards, p.writes)
		if err != nil {
			return nil, err
		}
		if !applied {
			continue
		}
		if p.last && s.users != nil {
			if err := s.users.SetUserActive(ctx, userID, false); err != nil {
				return nil, err
			}
		}
		return p, nil
	}
	return nil, &FactorSetContendedError{UserID: userID, Attempts: factorSetCommitAttempts}
}

// planRemoval reads the factor and the user's factor set and builds the part
// of the batch Remove and Delete share: guarded on both records as read, and
// writing the set one version later without the factor. It refuses a factor
// that is absent, revoked, or the user's last active one when force is false.
// The caller adds the write that retires the factor's own record.
func (s *userAuthFactorService) planRemoval(ctx context.Context, factorID string, force bool) (*factorRemovalPlan, error) {
	key := primaryKey(factorID)
	raw, err := s.getRecord(ctx, key)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, &auth.FactorNotFoundError{FactorID: factorID}
	}
	var factor auth.AuthFactor
	if err := json.Unmarshal(raw, &factor); err != nil {
		return nil, &auth.InvalidFactorError{Reason: "unmarshal: " + err.Error()}
	}
	if factor.RevokedAt != nil {
		return nil, &auth.FactorRevokedError{FactorID: factorID}
	}

	set, setRaw, err := s.readFactorSet(ctx, factor.UserID)
	if err != nil {
		return nil, err
	}
	if err := s.liveFactorSet(ctx, factor.UserID, set); err != nil {
		return nil, err
	}
	_, counted := set.Active[factorID]
	last := counted && len(set.Active) <= 1
	if last && !force {
		return nil, &auth.LastFactorError{UserID: factor.UserID, FactorID: factorID}
	}
	delete(set.Active, factorID)
	set.Version++
	next, err := json.Marshal(set)
	if err != nil {
		return nil, &auth.InvalidFactorError{Reason: "marshal factor set: " + err.Error()}
	}
	return &factorRemovalPlan{
		factor: &factor,
		guards: []kvstore.Guard{
			{Key: key, Value: raw},
			{Key: factorSetKey(factor.UserID), Value: setRaw},
		},
		writes: []kvstore.KV{{Key: factorSetKey(factor.UserID), Value: next}},
		last:   last,
	}, nil
}

// planRevoke builds the batch that revokes the factor: planRemoval's, writing
// the record back with RevokedAt set. Its index entries stay, so a revoked
// factor keeps its credential reserved.
func (s *userAuthFactorService) planRevoke(ctx context.Context, factorID string, force bool) (*factorRemovalPlan, error) {
	plan, err := s.planRemoval(ctx, factorID, force)
	if err != nil {
		return nil, err
	}
	now := s.nowFunc()
	plan.factor.RevokedAt = &now
	data, err := json.Marshal(plan.factor)
	if err != nil {
		return nil, &auth.InvalidFactorError{Reason: "marshal: " + err.Error()}
	}
	plan.writes = append(plan.writes, kvstore.KV{Key: primaryKey(factorID), Value: data})
	return plan, nil
}

// planDelete builds the batch that deletes the factor: planRemoval's, deleting
// the record and every index entry that still names it, each guarded on the
// value read.
func (s *userAuthFactorService) planDelete(ctx context.Context, factorID string, force bool) (*factorRemovalPlan, error) {
	plan, err := s.planRemoval(ctx, factorID, force)
	if err != nil {
		return nil, err
	}
	plan.writes = append(plan.writes, kvstore.KV{Key: primaryKey(factorID), Delete: true})
	for _, indexKey := range factorIndexKeys(plan.factor) {
		value, err := s.getRecord(ctx, indexKey)
		if err != nil {
			return nil, err
		}
		if len(value) == 0 {
			value = nil
		}
		plan.guards = append(plan.guards, kvstore.Guard{Key: indexKey, Value: value})
		if string(value) == factorID {
			plan.writes = append(plan.writes, kvstore.KV{Key: indexKey, Delete: true})
		}
	}
	return plan, nil
}

// factorIndexKeys returns the distinct index keys add writes for f: the user
// index, the credential lookup and, when f has one, the SPKI lookup.
func factorIndexKeys(f *auth.AuthFactor) []string {
	keys := []string{userIndexKey(f.UserID, f.ID), lookupKeyFor(f.Type, f.Credential)}
	if len(f.SPKICredential) > 0 {
		if spki := lookupKeyFor(f.Type, f.SPKICredential); spki != keys[1] {
			keys = append(keys, spki)
		}
	}
	return keys
}

// emitDeleted records a committed hard Delete on the audit trail.
func (s *userAuthFactorService) emitDeleted(ctx context.Context, f *auth.AuthFactor, force bool) {
	if s.audit == nil {
		return
	}
	var actor, tenantID string
	if id := auth.IdentityFromContext(ctx); id != nil {
		actor = id.Subject
		tenantID = id.TenantID
	}
	audit.EmitBestEffort(ctx, s.audit, "user_authfactor", audit.AuditEvent{
		Actor:    actor,
		TenantID: tenantID,
		Resource: "authfactor:" + f.ID,
		Action:   "delete",
		Outcome:  audit.OutcomeSuccess,
		Attributes: map[string]any{
			"user_id":   f.UserID,
			"factor_id": f.ID,
			"type":      string(f.Type),
			"force":     force,
		},
	})
}

// Rotate atomically adds newFactor and revokes the old factor. The new
// factor is recorded as auth.Unattested for the same reason Add's is: a
// rotation is the other way a factor the server never verified is written.
func (s *userAuthFactorService) Rotate(ctx context.Context, oldID string, newFactor *auth.AuthFactor) error {
	return s.rotate(ctx, oldID, newFactor, auth.Unattested())
}

// RotateAttested implements AttestationRecorder.
func (s *userAuthFactorService) RotateAttested(ctx context.Context, oldID string, newFactor *auth.AuthFactor, attestation auth.VerifiedAttestation) error {
	if err := attestation.Validate(); err != nil {
		return err
	}
	return s.rotate(ctx, oldID, newFactor, attestation)
}

// rotate is the single write path behind Rotate and RotateAttested; see add.
func (s *userAuthFactorService) rotate(ctx context.Context, oldID string, newFactor *auth.AuthFactor, attestation auth.VerifiedAttestation) error {
	if newFactor == nil {
		return &auth.InvalidFactorError{Reason: "newFactor is nil"}
	}
	if oldID == "" {
		return &auth.InvalidFactorError{Reason: "oldID is empty"}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	old, err := s.Get(ctx, oldID)
	if err != nil {
		return err
	}
	if old.RevokedAt != nil {
		return &auth.FactorRevokedError{FactorID: oldID}
	}

	if newFactor.UserID == 0 {
		newFactor.UserID = old.UserID
	}
	if newFactor.UserID != old.UserID {
		return &auth.InvalidFactorError{Reason: "rotated factor must belong to same user"}
	}
	if !newFactor.Type.IsValid() {
		return &auth.FactorTypeUnsupportedError{Type: string(newFactor.Type)}
	}
	if len(newFactor.Credential) == 0 {
		return &auth.InvalidFactorError{Reason: "credential blob is empty"}
	}
	// Same rule as Add: rotation is the other way a caller supplies metadata
	// for a factor the server never verified.
	if err := auth.ValidateMetadata(newFactor.Metadata); err != nil {
		return err
	}
	if newFactor.ID == "" {
		newFactor.ID = s.idGen()
	}
	if newFactor.RegisteredAt.IsZero() {
		newFactor.RegisteredAt = s.nowFunc()
	}

	// Reject collision against the new credential. A hit on the old
	// factor's own index entries is allowed — rotating to a renewed cert
	// with the same keypair legitimately reuses the SPKI lookup key.
	if existingID, err := s.lookupFactorID(ctx, newFactor.Type, newFactor.Credential); err == nil && existingID != "" && existingID != oldID {
		return &auth.FactorAlreadyExistsError{Type: newFactor.Type, FactorID: existingID}
	}
	if len(newFactor.SPKICredential) > 0 {
		if existingID, err := s.lookupFactorID(ctx, newFactor.Type, newFactor.SPKICredential); err == nil && existingID != "" && existingID != oldID {
			return &auth.FactorAlreadyExistsError{Type: newFactor.Type, FactorID: existingID}
		}
	}

	// The server, not the caller, says what was proved; see add.
	newFactor.Metadata = attestation.Stamp(newFactor.Metadata)

	newData, err := json.Marshal(newFactor)
	if err != nil {
		return &auth.InvalidFactorError{Reason: "marshal new: " + err.Error()}
	}
	if store := s.batchStore(); store != nil {
		return s.commitRotation(ctx, store, oldID, newFactor, newData)
	}

	// A store with only a single-key guard writes the rotation one record at
	// a time. The new factor goes first so a partial failure leaves the user
	// with at least one active factor (graceful degradation). It is reserved
	// as add's is, so an ID in use -- the old factor's own included -- is
	// refused before anything is written.
	if err := s.reservePrimary(ctx, newFactor, newData); err != nil {
		return err
	}
	if err := s.kv.Put(ctx, userIndexKey(newFactor.UserID, newFactor.ID), []byte(newFactor.ID)); err != nil {
		return err
	}
	if err := s.kv.Put(ctx, lookupKeyFor(newFactor.Type, newFactor.Credential), []byte(newFactor.ID)); err != nil {
		return err
	}
	// SPKI lookup index. When the rotation reuses the old keypair this
	// overwrites the old factor's SPKI entry to point at the new factor —
	// exactly the renewal-survives-login behaviour the index exists for.
	if len(newFactor.SPKICredential) > 0 {
		if err := s.kv.Put(ctx, lookupKeyFor(newFactor.Type, newFactor.SPKICredential), []byte(newFactor.ID)); err != nil {
			return err
		}
	}

	// Revoke the old factor.
	now := s.nowFunc()
	old.RevokedAt = &now
	oldData, err := json.Marshal(old)
	if err != nil {
		return &auth.InvalidFactorError{Reason: "marshal old: " + err.Error()}
	}
	if err := s.kv.Put(ctx, primaryKey(old.ID), oldData); err != nil {
		return err
	}

	// One version for the whole rotation: the new factor, already indexed,
	// joins the live set and the old one, now revoked, leaves it. Both are
	// re-derived from the records, so a crash before this commit still
	// counts the new factor and not the old.
	return s.commitMembership(ctx, newFactor.UserID)
}

// commitRotation commits a rotation in ONE multi-guarded batch: the new
// factor's record and index entries, the old factor's record revoked, and the
// factor set one version later with the new factor in it and the old one out.
// The batch is guarded on the old record and the factor set as read, on the
// new record being absent and on each new index entry as read, so a hard
// Delete of the old factor, an enrollment claiming the new credential or any
// other change to the user's set committed in between refuses it, and the
// rotation is decided again from fresh reads. An index entry may already name
// the old factor -- a renewal on the same keypair reuses the SPKI lookup --
// and the batch repoints it at the new one.
func (s *userAuthFactorService) commitRotation(ctx context.Context, store factorBatchStore, oldID string, newFactor *auth.AuthFactor, newData []byte) error {
	oldKey := primaryKey(oldID)
	newKey := primaryKey(newFactor.ID)
	setKey := factorSetKey(newFactor.UserID)
	for range factorSetCommitAttempts {
		oldRaw, err := s.getRecord(ctx, oldKey)
		if err != nil {
			return err
		}
		if len(oldRaw) == 0 {
			return &auth.FactorNotFoundError{FactorID: oldID}
		}
		var old auth.AuthFactor
		if err := json.Unmarshal(oldRaw, &old); err != nil {
			return &auth.InvalidFactorError{Reason: "unmarshal old: " + err.Error()}
		}
		if old.RevokedAt != nil {
			return &auth.FactorRevokedError{FactorID: oldID}
		}
		taken, err := s.getRecord(ctx, newKey)
		if err != nil {
			return err
		}
		if len(taken) > 0 {
			return &auth.FactorAlreadyExistsError{Type: newFactor.Type, FactorID: newFactor.ID}
		}
		now := s.nowFunc()
		old.RevokedAt = &now
		oldData, err := json.Marshal(&old)
		if err != nil {
			return &auth.InvalidFactorError{Reason: "marshal old: " + err.Error()}
		}

		set, setRaw, err := s.readFactorSet(ctx, newFactor.UserID)
		if err != nil {
			return err
		}
		if err := s.liveFactorSet(ctx, newFactor.UserID, set); err != nil {
			return err
		}
		delete(set.Active, oldID)
		set.Active[newFactor.ID] = newFactor.Type
		set.Version++
		next, err := json.Marshal(set)
		if err != nil {
			return &auth.InvalidFactorError{Reason: "marshal factor set: " + err.Error()}
		}

		guards := []kvstore.Guard{{Key: oldKey, Value: oldRaw}, {Key: newKey}, {Key: setKey, Value: setRaw}}
		writes := []kvstore.KV{{Key: newKey, Value: newData}, {Key: oldKey, Value: oldData}, {Key: setKey, Value: next}}
		for _, key := range factorIndexKeys(newFactor) {
			owner, err := s.getRecord(ctx, key)
			if err != nil {
				return err
			}
			if len(owner) == 0 {
				owner = nil
			} else if string(owner) != oldID {
				return &auth.FactorAlreadyExistsError{Type: newFactor.Type, FactorID: string(owner)}
			}
			guards = append(guards, kvstore.Guard{Key: key, Value: owner})
			writes = append(writes, kvstore.KV{Key: key, Value: []byte(newFactor.ID)})
		}
		applied, err := store.PutAllIfAllUnchanged(ctx, guards, writes)
		if err != nil {
			return err
		}
		if applied {
			return nil
		}
	}
	return &FactorSetContendedError{UserID: newFactor.UserID, Attempts: factorSetCommitAttempts}
}

// Relabel updates the factor's human-readable label.
func (s *userAuthFactorService) Relabel(ctx context.Context, factorID string, label string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	factor, err := s.Get(ctx, factorID)
	if err != nil {
		return err
	}
	if factor.RevokedAt != nil {
		return &auth.FactorRevokedError{FactorID: factor.ID}
	}
	factor.Label = label
	data, err := json.Marshal(factor)
	if err != nil {
		return &auth.InvalidFactorError{Reason: "marshal: " + err.Error()}
	}
	return s.kv.Put(ctx, primaryKey(factor.ID), data)
}

// MarkUsed updates LastUsedAt. LastUsedAt itself is best-effort metadata,
// but the update is a read-modify-write of the FULL factor record, so it
// MUST hold the service mutex like every other writer: an unlocked update
// racing a concurrent Remove/Rotate can read the factor before the
// revocation lands and write the stale still-active record back after,
// silently resurrecting a revoked credential (fail-open). Serializing with
// s.mu makes that interleaving impossible.
func (s *userAuthFactorService) MarkUsed(ctx context.Context, factorID string, ts time.Time) error {
	if factorID == "" {
		return &auth.InvalidFactorError{Reason: "factorID is empty"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	factor, err := s.Get(ctx, factorID)
	if err != nil {
		return err
	}
	factor.LastUsedAt = &ts
	data, err := json.Marshal(factor)
	if err != nil {
		return &auth.InvalidFactorError{Reason: "marshal: " + err.Error()}
	}
	return s.kv.Put(ctx, primaryKey(factor.ID), data)
}

// countActive returns how many of the user's factors are still active.
// Caller must hold s.mu.
func (s *userAuthFactorService) countActive(ctx context.Context, userID uint64) (int, error) {
	factors, err := s.List(ctx, userID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range factors {
		if f.IsActive() {
			n++
		}
	}
	return n, nil
}

// lookupFactorID returns the factor ID stored under the (type, credential)
// lookup index, or empty string when not present. A missing key is NOT an
// error.
func (s *userAuthFactorService) lookupFactorID(ctx context.Context, t auth.AuthFactorType, cred []byte) (string, error) {
	raw, err := s.kv.Get(ctx, lookupKeyFor(t, cred))
	if err != nil || len(raw) == 0 {
		return "", nil
	}
	return string(raw), nil
}

// ---------------------------------------------------------------------------
// Key helpers
// ---------------------------------------------------------------------------

// primaryKey returns the canonical key for the AuthFactor record.
func primaryKey(factorID string) string {
	return userFactorPrimaryPrefix + factorID
}

// userIndexPrefix returns the per-user index prefix. Trailing slash makes
// the prefix a clean range query.
func userIndexPrefix(userID uint64) string {
	return userFactorByUserPrefix + strconv.FormatUint(userID, 10) + userFactorByUserSuffix
}

// userIndexKey returns the per-user index key for a single factor.
func userIndexKey(userID uint64, factorID string) string {
	return userIndexPrefix(userID) + factorID
}

// lookupKeyFor returns the storage key for the (type, credential lookup
// key) index. The lookup key is the SHA-256 hex of the credential blob for
// mtls / piv, or the credential blob itself (already a credentialID) for
// fido2. Both forms are hex-encoded inside the storage key to keep the
// path printable.
func lookupKeyFor(t auth.AuthFactorType, cred []byte) string {
	return userFactorLookupPrefix + string(t) + "/" + auth.CredentialKeyHex(cred)
}
