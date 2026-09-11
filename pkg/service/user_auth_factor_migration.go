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
	"log/slog"
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Legacy untenanted auth-factor records (go-qrdb#393)
// ---------------------------------------------------------------------------
//
// Before factor resolution was tenant-routed, every factor record was written
// at the ROOT of the system shard:
//
//	__sys:auth/factors/{factorID}
//	__sys:auth/users/{userID}/factors/{factorID}
//	__sys:auth/lookup/{type}/{credKeyHex}
//
// The routed service reads "{tenant}/__sys:auth/...", so those records are
// unreachable once routing is on. Leaving them there is not an option: an
// unreachable credential is a login that stops working, and deferring the
// move would mean keeping the cross-tenant resolution this change exists to
// close for as long as the old keys were still read.
//
// The move needs one fact the record does not carry: its tenant. An
// AuthFactor has a UserID and no TenantID, and the key says nothing either,
// so unlike the secondary-index namespace in go-qrdb#410 — which was read
// back off a name the write path had already prefixed — there is nothing here
// to derive the tenant FROM. It is established from the owning USER instead,
// through tenant.IdentityIndex, which is this repository's authority on which
// tenant holds a globally unique identity id.
//
// Where the index has no claim on the user, the migration REFUSES that record
// and leaves it exactly where it is. It does not fall back to the caller's
// tenant, to the system tenant, or to the only tenant that happens to exist:
// a factor placed in a guessed tenant is a credential that authenticates
// somebody in an organization it was never issued for, and unlike an
// unreachable record that failure is silent.
//
// A REFUSAL IS PER RECORD, AND THE RUN COMPLETES. That is the whole reason
// refusals are not errors: one record the migration cannot place must not
// decide the fate of every other record, or of every other TENANT. The case
// that makes this concrete is a destination that is merely unreachable right
// now — a self-managed barrier tenant is registered SEALED at creation and
// stays sealed until its admin unseals it, and on a node that does not host a
// tenant's barrier there is none registered until key delivery arrives. Both
// are ordinary, designed states. Treating either as a STORE failure aborts the
// run, and because the run is what gates the routed service, one such record
// would take the login surface down for every tenant on the node — permanently,
// since the record is never deleted and every retry re-reaches it.
//
// So refusals come in two kinds and the reason says which:
//
//   - TERMINAL — the owner is unindexed, absent, or the record cannot be read.
//     Nothing about a later run changes the answer, so the migration is done
//     and re-running would only re-refuse.
//   - TRANSIENT — the owning tenant is known and its store is not reachable
//     from here yet. A later run may well place it, so the migration is NOT
//     done, and the caller re-runs over whatever is left in the legacy
//     keyspace.
//
// The kind is carried on the refusal rather than re-derived at the call site;
// see refusalIsTransient.

// ErrFactorOwnerUnindexed is the refusal reason for a legacy factor whose
// owning user has no claim in the identity index. The migration cannot
// establish the record's tenant and will not guess one.
var ErrFactorOwnerUnindexed = errors.New("service: auth-factor migration: the record's owning user is not in the identity index")

// ErrFactorOwnerAbsent is the refusal reason for a legacy factor record that
// names no user at all (UserID zero). Such a record cannot be placed and
// cannot be used: nothing can own the credential it holds.
var ErrFactorOwnerAbsent = errors.New("service: auth-factor migration: the record names no owning user")

// ErrFactorRecordUnreadable is the refusal reason for a legacy key whose
// value is absent or is not an AuthFactor. It is a refusal rather than a skip
// because a record that cannot be read cannot be shown to be harmless.
var ErrFactorRecordUnreadable = errors.New("service: auth-factor migration: the record could not be read")

// ErrFactorOwnerTenantUnreachable is the refusal reason for a record whose
// owning tenant IS known but whose store cannot be reached from this node right
// now — a sealed barrier, or a barrier this node has not been delivered yet.
//
// It is the one TRANSIENT reason. The owning tenant was established; nothing
// has to be guessed; the destination is simply unavailable at this moment. That
// is not the store failing — the store exists and is fine — so it must not
// abort a run that has every other record to place, and it must not mark the
// migration done.
var ErrFactorOwnerTenantUnreachable = errors.New("service: auth-factor migration: the record's owning tenant is not reachable from this node")

// refusalIsTransient is the single authority on whether a later run may
// resolve a refusal. Map-based rather than a switch at each site, and every
// reason is listed: an omitted reason would take a default, and defaulting
// wrong in either direction is a defect — terminal-as-transient re-scans the
// legacy keyspace on every call for ever, transient-as-terminal strands a
// record whose tenant unseals a minute later.
var refusalIsTransient = map[error]bool{
	ErrFactorOwnerUnindexed:         false,
	ErrFactorOwnerAbsent:            false,
	ErrFactorRecordUnreadable:       false,
	ErrFactorOwnerTenantUnreachable: true,
}

// newRefusal builds a refusal, classifying it from its reason so no call site
// decides the kind for itself.
func newRefusal(key, factorID string, userID uint64, reason error) *FactorMigrationRefusal {
	return &FactorMigrationRefusal{
		Key:       key,
		FactorID:  factorID,
		UserID:    userID,
		Err:       reason,
		Transient: refusalIsTransient[reason],
	}
}

// FactorMigrationRefusal names one legacy record the migration would not
// place, and why.
type FactorMigrationRefusal struct {
	// Key is the legacy storage key, so an operator can find the record.
	Key string

	// FactorID is the factor's id when the record could be read; empty
	// otherwise.
	FactorID string

	// UserID is the owning user the record named, zero when unknown.
	UserID uint64

	// Err is the refusal reason: one of ErrFactorOwnerUnindexed,
	// ErrFactorOwnerAbsent, ErrFactorRecordUnreadable or
	// ErrFactorOwnerTenantUnreachable.
	Err error

	// Transient reports whether a later run may place this record after all.
	// It is derived from Err by refusalIsTransient, never set by hand.
	Transient bool
}

// FactorMigrationRefusedError reports that the migration completed for every
// record it could place and REFUSED the rest. It is returned alongside a
// report describing what did move: the refusals are the incomplete part, not
// the whole run.
//
// The refused records stay in the legacy keyspace, where the routed service
// does not read them. They therefore resolve for nobody — which is the
// fail-closed outcome — and this error is how that becomes visible instead of
// being discovered as a login failure months later.
type FactorMigrationRefusedError struct {
	// Refusals names every record left behind.
	Refusals []FactorMigrationRefusal
}

// Transient reports whether ANY refused record may be placed by a later run.
//
// It is the caller's signal that the migration is not finished: a run with a
// transient refusal must be re-run rather than marked done. One transient
// refusal is enough, because one stranded credential is a user who cannot log
// in.
func (e *FactorMigrationRefusedError) Transient() bool {
	for _, r := range e.Refusals {
		if r.Transient {
			return true
		}
	}
	return false
}

// Error returns a human-readable description naming each refused record.
func (e *FactorMigrationRefusedError) Error() string {
	var b strings.Builder
	b.WriteString("service: auth-factor migration refused ")
	b.WriteString(strconv.Itoa(len(e.Refusals)))
	b.WriteString(" record(s) whose tenant could not be established:")
	for _, r := range e.Refusals {
		b.WriteString(" [")
		b.WriteString(r.Key)
		b.WriteString(": ")
		if r.Err != nil {
			b.WriteString(r.Err.Error())
		}
		b.WriteString("]")
	}
	return b.String()
}

// FactorMigrationReport counts what one migration run did.
type FactorMigrationReport struct {
	// Scanned is the number of legacy primary factor records found.
	Scanned int

	// Migrated is the number placed in their owning tenant's store.
	Migrated int

	// Pruned is the number of legacy secondary-index entries removed
	// because the factor they named no longer exists in the legacy
	// keyspace — either this run moved it, or an interrupted earlier run
	// did.
	Pruned int

	// Refusals names every record the migration would not place. Non-empty
	// exactly when the run returned *FactorMigrationRefusedError.
	Refusals []FactorMigrationRefusal
}

// factorOwnerLookup is the slice of tenant.IdentityIndex the migration needs:
// which tenant claims a user id. tenant.IdentityIndex satisfies it directly.
type factorOwnerLookup interface {
	Lookup(ctx context.Context, kind tenant.IdentityKind, id string) (tenant.Claim, bool, error)
}

// MigrateFactorsToTenants moves legacy untenanted auth-factor records into
// the store of the tenant that owns each record's user.
//
// shared is the UNCONFINED root store the legacy records were written to —
// what tenant.NewStoreResolver itself wraps, never a confined view of it.
// resolver supplies each destination store. owners establishes the owning
// tenant and is normally tenant.NewResolvedIdentityIndex(resolver).
//
// The run is per record and resumable. A record's four possible keys are
// written to the destination first and removed from the legacy keyspace
// after, so an interrupted run leaves a record readable in both places rather
// than in neither, and re-running completes it. Values are copied verbatim:
// the migration never re-marshals a factor, so a field a newer build does not
// know about survives it.
//
// It returns *FactorMigrationRefusedError when any record could not be placed.
// Everything placeable is still placed. Call Transient on that error to learn
// whether the run should be repeated: true means at least one record's owning
// tenant was merely unreachable from here and a later run may place it.
func MigrateFactorsToTenants(ctx context.Context, shared kvstore.KVStore, resolver tenant.StoreResolver, owners factorOwnerLookup, logger *slog.Logger) (*FactorMigrationReport, error) {
	if shared == nil {
		return nil, &auth.InvalidFactorError{Reason: "migration: shared store is required"}
	}
	if resolver == nil {
		return nil, &auth.InvalidFactorError{Reason: "migration: store resolver is required"}
	}
	if owners == nil {
		return nil, &auth.InvalidFactorError{Reason: "migration: identity index is required"}
	}
	if logger == nil {
		logger = slog.Default()
	}

	keys, err := shared.List(ctx, userFactorPrimaryPrefix)
	if err != nil {
		return nil, err
	}

	report := &FactorMigrationReport{Scanned: len(keys)}
	for _, key := range keys {
		refusal, mErr := migrateOneFactor(ctx, shared, resolver, owners, key)
		if mErr != nil {
			return report, mErr
		}
		if refusal != nil {
			report.Refusals = append(report.Refusals, *refusal)
			logger.Error("auth-factor migration refused a record; it stays in the legacy keyspace and resolves for nobody",
				slog.String("key", refusal.Key),
				slog.String("factor_id", refusal.FactorID),
				slog.Uint64("user_id", refusal.UserID),
				slog.String("reason", refusal.Err.Error()))
			continue
		}
		report.Migrated++
	}

	pruned, err := pruneDanglingFactorIndexes(ctx, shared)
	if err != nil {
		return report, err
	}
	report.Pruned = pruned

	if len(report.Refusals) > 0 {
		return report, &FactorMigrationRefusedError{Refusals: report.Refusals}
	}
	if report.Migrated > 0 || report.Pruned > 0 {
		logger.Info("auth-factor records migrated into their owning tenants",
			slog.Int("migrated", report.Migrated), slog.Int("pruned", report.Pruned))
	}
	return report, nil
}

// migrateOneFactor places a single legacy record. It returns a non-nil refusal
// when the record cannot be placed — because its tenant cannot be established,
// or because that tenant's store is not reachable from here — and an error only
// when THIS node's own store failed on a read or a write it was asked to
// perform. A store failure aborts the run, because continuing past one would
// report a migration that did not happen; a refusal does not, because one
// record must never decide the fate of the others.
func migrateOneFactor(ctx context.Context, shared kvstore.KVStore, resolver tenant.StoreResolver, owners factorOwnerLookup, key string) (*FactorMigrationRefusal, error) {
	raw, err := shared.Get(ctx, key)
	if err != nil || len(raw) == 0 {
		return newRefusal(key, "", 0, ErrFactorRecordUnreadable), nil
	}
	var factor auth.AuthFactor
	if err := json.Unmarshal(raw, &factor); err != nil {
		return newRefusal(key, "", 0, ErrFactorRecordUnreadable), nil
	}
	if factor.UserID == 0 {
		return newRefusal(key, factor.ID, 0, ErrFactorOwnerAbsent), nil
	}

	claim, ok, err := owners.Lookup(ctx, tenant.IdentityUser, tenant.IdentityID(factor.UserID))
	if err != nil {
		return nil, err
	}
	if !ok {
		return newRefusal(key, factor.ID, factor.UserID, ErrFactorOwnerUnindexed), nil
	}

	// A destination that cannot be reached is a REFUSAL of this record, not a
	// failure of the run.
	//
	// The owning tenant was established — the index just said so — and the
	// store behind it exists and is fine; it is unreachable from here at this
	// moment. A self-managed barrier tenant is registered SEALED at creation,
	// and a node that does not host a tenant's barrier has none registered
	// until key delivery lands, so this is a designed state rather than an
	// operator error. Returning it as a store failure aborted the run and, via
	// the service's migrate-before-serve gate, took the login surface down for
	// EVERY tenant on the node and kept it down, because the record is never
	// deleted and every retry reaches it again.
	dst, err := resolver.Resolve(ctx, claim.Tenant)
	if err != nil {
		return newRefusal(key, factor.ID, factor.UserID, ErrFactorOwnerTenantUnreachable), nil
	}

	// Write the destination's four records first. The primary carries the
	// ORIGINAL bytes; the three index entries are rebuilt from the record
	// rather than copied, because they are derivable from it and rebuilding
	// them repairs an index the legacy keyspace may have lost.
	if err := dst.Put(ctx, primaryKey(factor.ID), raw); err != nil {
		return nil, err
	}
	if err := dst.Put(ctx, userIndexKey(factor.UserID, factor.ID), []byte(factor.ID)); err != nil {
		return nil, err
	}
	if len(factor.Credential) > 0 {
		if err := dst.Put(ctx, lookupKeyFor(factor.Type, factor.Credential), []byte(factor.ID)); err != nil {
			return nil, err
		}
	}
	if len(factor.SPKICredential) > 0 {
		if err := dst.Put(ctx, lookupKeyFor(factor.Type, factor.SPKICredential), []byte(factor.ID)); err != nil {
			return nil, err
		}
	}

	// Remove the legacy records. The primary goes LAST: while it exists the
	// record is still discoverable by a re-run, so an interruption anywhere
	// above leaves work that can be finished rather than a record only the
	// destination knows about.
	legacy := []string{userIndexKey(factor.UserID, factor.ID)}
	if len(factor.Credential) > 0 {
		legacy = append(legacy, lookupKeyFor(factor.Type, factor.Credential))
	}
	if len(factor.SPKICredential) > 0 {
		legacy = append(legacy, lookupKeyFor(factor.Type, factor.SPKICredential))
	}
	legacy = append(legacy, key)
	for _, k := range legacy {
		if err := shared.Delete(ctx, k); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// pruneDanglingFactorIndexes removes legacy secondary-index entries that name
// a factor with no legacy primary record.
//
// Such an entry is either the tail of an interrupted earlier run or an index
// whose record was deleted by hand. Either way it names nothing, and leaving
// it would keep a credential-shaped key in a keyspace nothing reads. An entry
// naming a record that IS still present — a refused one — is left alone.
func pruneDanglingFactorIndexes(ctx context.Context, shared kvstore.KVStore) (int, error) {
	pruned := 0
	for _, prefix := range []string{userFactorLookupPrefix, userFactorByUserPrefix} {
		keys, err := shared.List(ctx, prefix)
		if err != nil {
			return pruned, err
		}
		for _, k := range keys {
			id, err := shared.Get(ctx, k)
			if err != nil {
				return pruned, err
			}
			if len(id) > 0 {
				present, err := shared.Exists(ctx, primaryKey(string(id)))
				if err != nil {
					return pruned, err
				}
				if present {
					continue
				}
			}
			if err := shared.Delete(ctx, k); err != nil {
				return pruned, err
			}
			pruned++
		}
	}
	return pruned, nil
}
