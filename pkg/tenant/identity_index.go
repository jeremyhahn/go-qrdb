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

package tenant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// IdentityID renders a numeric identity id as the index's key segment.
//
// Numeric ids are decimal here rather than the fixed-width big-endian
// encoding the user store's own keys use: the index is never scanned in id
// order, and the CLI and every transport name a user by its decimal id.
//
// It is exported for the same reason IdentityNamespace is. More than one
// package composes an index key for a user id — pkg/user's store and router
// write and read entries, and the auth-factor migration in pkg/service reads
// them to establish which tenant owns a legacy record — and a reader that
// rendered the id differently from the writer would find nothing and report
// it as "no claim", which is indistinguishable from an id that genuinely has
// none. One formula, called by both sides.
func IdentityID(id uint64) string {
	return strconv.FormatUint(id, 10)
}

// IdentityKind names the family of identity record an index entry
// describes. The kind is the first key segment, so the families occupy
// disjoint keyspaces and a user id can never resolve a role's entry.
type IdentityKind string

const (
	// IdentityUser is the kind for pkg/user records.
	IdentityUser IdentityKind = "user"

	// IdentityRole is the kind for pkg/role records.
	IdentityRole IdentityKind = "role"
)

// identityIndexKeyPrefix is the reserved keyspace the index occupies WITHIN the
// system tenant's identity namespace. The physical key is therefore
// "system/__sys:identity_idx/tenant/{kind}/{id}" — IdentityNamespace supplies
// the leading segment, never a hand-rolled string here.
//
// Deliberately NOT "__sys:placement/": that prefix already belongs to
// pkg/placement, which records where a dedicated database's Raft shard lives.
// Two unrelated indexes under one prefix would be a collision waiting for the
// day one of them is scanned by prefix.
const identityIndexKeyPrefix = kvstore.ReservedPrefixSystem + "identity_idx/tenant/"

// ErrNilIdentityIndexStore is returned when an identity index is constructed
// over a nil store.
var ErrNilIdentityIndexStore = errors.New("tenant: nil identity index store")

// ErrNilIdentityIndexResolver is returned when an identity index is
// constructed over a nil StoreResolver.
var ErrNilIdentityIndexResolver = errors.New("tenant: nil identity index store resolver")

// ErrEmptyIdentityID is returned when an index operation names an empty id.
// An empty id would key every record in the family to one entry.
var ErrEmptyIdentityID = errors.New("tenant: empty identity id")

// ErrEmptyClaimToken is returned when a release names no claim. Releasing
// without a token would be an unguarded delete.
var ErrEmptyClaimToken = errors.New("tenant: empty claim token")

// ErrIdentityStoreNotAtomic is returned when a claim is attempted against a
// store with no compare-and-set.
//
// A claim that reads and then writes is two operations with a window between
// them, and two proposers racing through that window both observe "absent"
// and both win — which is the capture this index exists to refuse. There is
// no honest degraded mode: a non-atomic claim is a claim in name only, so
// this refuses instead. Every state machine implements the conditional write
// the claim needs (PebbleStateMachine, MVCCEngine and KVEngine, the last as
// of #41), so a store reaching this is a wiring mistake, not a deployment.
var ErrIdentityStoreNotAtomic = errors.New("tenant: identity index store cannot claim atomically")

// IdentityIndex maps a globally unique identity id to the tenant whose
// store physically holds that record.
//
// It exists because identity ids are global while identity STORAGE is not.
// A Snowflake user id and a UUID role id are unique across the whole
// deployment, and every transport exposes operations that name a record by
// id alone — `users delete <id>`, `roles assign --role <id>`. Before tenant
// keyspaces were confined (#41) those operations worked by accident: every
// prefix tenant shared one keyspace, so "the caller's tenant" and "the
// tenant that holds the record" were the same store. Confining the keyspaces
// made that accident stop working, which is what this index replaces — a
// deliberate lookup instead of a coincidence.
//
// SCOPE. An entry carries the id and the tenant, and nothing else. It lives
// in the system tenant's identity namespace, which means it necessarily
// sits OUTSIDE a barrier tenant's encryption: a caller who can read the
// system store learns that an id exists and which tenant holds it. It must
// therefore never carry a username, a role name, permissions, or any other
// attribute of the record — those stay inside the tenant's store, behind
// the tenant's barrier. Resolution is only ever a routing decision; the
// authorization decision stays with EffectiveTenant and the record's own
// store, and a sealed barrier still refuses (see OwningTenant).
type IdentityIndex interface {
	// Record CLAIMS an id for a tenant and returns the claim's TOKEN. It
	// succeeds ONLY when this call creates the claim: every pre-existing
	// claim is refused with *IdentityClaimConflictError and changes nothing,
	// including one this same tenant already holds.
	//
	// Refusing the holder's own re-claim is a requirement, not an oversight.
	// A create that proceeds on a claim it did not win can have that claim
	// released out from under it by a concurrent same-tenant delete, leaving
	// a record with no entry that no by-id operation can reach. A caller
	// retrying a failed create releases its claim first rather than
	// re-recording over it.
	//
	// The returned token identifies THIS claim, not merely its tenant. Two
	// claims made by the same tenant for the same id at different times have
	// different tokens, which is what lets Forget tell one generation from
	// the next. Callers pass it back verbatim; it is opaque.
	//
	// An EMPTY token with a nil error means no claim was taken, because the
	// tenant keeps no entry in this index (an isolated tenant, whose records
	// live in its home store). There is nothing to release.
	Record(ctx context.Context, kind IdentityKind, id, tenantID string) (token string, err error)

	// Forget releases the claim identified by token, and only that claim.
	//
	// A token that no longer matches what is stored means the claim it named
	// is gone — released already, or released and RE-CREATED by a newer one.
	// That returns *IdentityClaimStaleError and changes nothing: a releaser
	// holding a stale token must never delete the claim that replaced it.
	// Releasing an id with no claim at all is not an error.
	//
	// Matching on the tenant alone would not be enough. Two concurrent
	// releases for one tenant both read the claim, the first succeeds, a
	// concurrent create re-uses the id and writes a NEW claim whose tenant is
	// the same, and the second, stale release matches it and deletes it —
	// leaving a live record unroutable by id.
	Forget(ctx context.Context, kind IdentityKind, id, token string) error

	// Lookup returns the claim on an id, and false when it has none.
	Lookup(ctx context.Context, kind IdentityKind, id string) (Claim, bool, error)

	// PrepareClaim returns the write Record would itself perform for a FRESH
	// claim on id in tenantID — the key and a newly minted token — WITHOUT
	// performing it and without touching any store. It exists so a caller
	// that also writes its own record can fold both into ONE atomic
	// operation instead of Record's own check-then-write:
	//
	//	claimKV, token, err := index.PrepareClaim(kind, id, tenantID)
	//	// ... build the caller's own writes ...
	//	applied, err := guarded.PutAllIfUnchanged(ctx, claimKV.Key, nil,
	//		append([]kvstore.KV{claimKV}, ownWrites...))
	//
	// closing the exact window Record's own doc warns about: "a create which
	// dies after claiming leaves the id claimed with no record behind it."
	// That window cannot open here, because the claim key and the caller's
	// own keys commit in the SAME proposal — either all of them land, or
	// none do (go-qrdb#555).
	//
	// PrepareClaim performs NO uniqueness check itself: it is pure key and
	// token synthesis, so two concurrent callers can each prepare a claim
	// for the same id, and only the GUARD in each one's own
	// PutAllIfUnchanged call decides which — if either — actually commits.
	// The caller is entirely responsible for guarding on this write's key
	// being absent.
	//
	// It is meaningful ONLY when the caller's batch targets the EXACT store
	// this index would itself resolve to. That holds for an index built with
	// NewIdentityIndex, whose store is fixed at construction — which is
	// exactly the shape a boot-time composer confined to the system
	// identity namespace has. It does NOT hold for NewResolvedIdentityIndex,
	// which resolves the system tenant's store fresh on every call and may
	// legitimately differ from a per-tenant caller's own store (a
	// barrier-isolated tenant's, for one) — folding a claim prepared there
	// into a DIFFERENT store's batch would silently write it to the wrong
	// place. Nothing in this method can detect that misuse; it is the
	// caller's obligation, exactly as WithIdentityIndex's own doc already
	// requires ("one function writes the record and its entry, so the two
	// cannot disagree").
	PrepareClaim(kind IdentityKind, id, tenantID string) (kvstore.KV, string, error)
}

// Claim is a recorded identity claim: which tenant holds the id, and the
// token identifying this particular claim of it.
//
// Tenant is what routing needs; Token is what releasing needs. They are
// separate fields because a release guarded on the tenant alone cannot tell
// one generation of a claim from the next.
type Claim struct {
	// Tenant holds the id.
	Tenant string
	// Token identifies this claim. Opaque to callers.
	Token string
}

// IdentityClaimConflictError is returned when an id is already claimed by a
// DIFFERENT tenant.
//
// The claim is what makes the index a routing authority rather than a
// last-writer-wins map. Role ids are CALLER-SUPPLIED — CreateRoleRequest.ID
// travels from the REST and gRPC surfaces straight into the store — so
// without a claim a tenant could create a role carrying another tenant's role
// id, overwrite that id's entry, and silently redirect every by-id operation
// an administrator performs on it.
//
// It deliberately does NOT name the tenant holding the claim. The caller
// supplied only an id; telling it where that id already lives is the same
// placement disclosure OwningTenant refuses to make.
type IdentityClaimConflictError struct {
	// Kind is the record family.
	Kind IdentityKind
	// ID is the contested id.
	ID string
}

func (e *IdentityClaimConflictError) Error() string {
	return "tenant: " + string(e.Kind) + " id " + e.ID + " is already claimed"
}

// ErrIdentityClaimContended is returned when a claim could not be settled
// because the holder kept changing underneath it. It is a transient
// condition, distinct from a conflict with a stable holder.
var ErrIdentityClaimContended = errors.New("tenant: identity claim contended")

// IdentityClaimStaleError is returned when a release names a claim that is no
// longer the one stored: released already, or released and RE-CREATED since.
//
// It is the ABA guard. Two concurrent releases for one tenant both read the
// claim; the first succeeds; a concurrent create re-uses the id and writes a
// new claim whose TENANT is identical; and the second, stale release would
// match on tenant and delete the replacement, leaving a live record no by-id
// operation can reach. Tokens make the two generations distinguishable, and
// this is what the loser is told.
type IdentityClaimStaleError struct {
	// Kind is the record family.
	Kind IdentityKind
	// ID is the id whose claim was replaced.
	ID string
}

func (e *IdentityClaimStaleError) Error() string {
	return "tenant: the " + string(e.Kind) + " claim on " + e.ID +
		" is no longer the one this release names"
}

// claimTokenSeparator joins the tenant to the nonce inside a claim value.
//
// NUL is safe because validateTenantIDCharset rejects control characters in a
// tenant id, so it cannot occur in the first field and the split is
// unambiguous.
const claimTokenSeparator = "\x00"

// newClaimToken mints the value stored for a claim: the tenant, so Lookup can
// answer routing questions from the stored bytes alone, plus a random nonce
// that makes THIS claim distinguishable from any later claim of the same id
// by the same tenant.
//
// A random nonce rather than a store revision: the index must behave
// identically on all three state machines, and the only primitive it can rely
// on across them is byte equality through CompareAndDelete. KVEngine and
// PebbleStateMachine expose no revision through kvstore.KVStore at all — only
// MVCC has one — so a revision-based token would either work on one engine
// and not the others or require widening the store contract. A nonce needs
// nothing from the store beyond the comparison it already performs.
func newClaimToken(tenantID string) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return tenantID + claimTokenSeparator + hex.EncodeToString(nonce), nil
}

// tenantFromClaimToken extracts the tenant a stored claim value names.
func tenantFromClaimToken(token string) string {
	if i := strings.Index(token, claimTokenSeparator); i >= 0 {
		return token[:i]
	}
	return token
}

// IdentityIndexError reports a failed identity-index operation.
type IdentityIndexError struct {
	// Op is the attempted operation: "record", "forget" or "lookup".
	Op string
	// Kind is the record family.
	Kind IdentityKind
	// ID is the record id.
	ID string
	// Err is the underlying cause.
	Err error
}

func (e *IdentityIndexError) Error() string {
	return "tenant: identity index " + e.Op + " failed for " + string(e.Kind) + " " + e.ID + ": " + e.Err.Error()
}

// Unwrap exposes the underlying cause to errors.Is and errors.As.
func (e *IdentityIndexError) Unwrap() error { return e.Err }

// Compile-time interface check.
var _ IdentityIndex = (*kvIdentityIndex)(nil)

// kvIdentityIndex is the KVStore-backed IdentityIndex. It holds a store
// SOURCE rather than a store because the two callers reach the same
// keyspace by different routes: the daemon's bootstrap path has a store
// confined to the system identity namespace before any StoreResolver
// exists, while the routed services have only the resolver. Both end up at
// the same physical keys, which is the point.
type kvIdentityIndex struct {
	store func(context.Context) (kvstore.KVStore, error)
	// home, when set, names the tenants whose records live in their home
	// store. They take no claim: see Record.
	home HomeLocator
}

// NewIdentityIndex returns an IdentityIndex over a store that is ALREADY
// confined to the system tenant's identity namespace — the shape
// cmd/qrdbd's bootstrap store has, built with
// kvstore.NewNamespacedKVStore(raw, IdentityNamespace(tenantid.System)).
//
// Passing an unconfined store puts the index at the root of the shared
// keyspace instead of under "system/", where the resolver-backed readers
// will not find it. That divergence between a write path and a read path is
// the exact failure IdentityNamespace exists to prevent, so callers compose
// the prefix with that function and never by hand.
func NewIdentityIndex(store kvstore.KVStore) (IdentityIndex, error) {
	if store == nil {
		return nil, ErrNilIdentityIndexStore
	}
	return &kvIdentityIndex{
		store: func(context.Context) (kvstore.KVStore, error) { return store, nil },
	}, nil
}

// NewResolvedIdentityIndex returns an IdentityIndex that reaches the system
// tenant's identity namespace through the StoreResolver on every operation.
//
// Resolving per operation rather than caching a store keeps the index on the
// same footing as every other resolver consumer: it observes the system
// store the resolver hands out now, not the one it handed out at
// construction.
//
// A resolver that is also a HomeLocator — the one NewStoreResolver returns —
// decides which tenants take no claim at all (see Record).
func NewResolvedIdentityIndex(resolver StoreResolver) (IdentityIndex, error) {
	if resolver == nil {
		return nil, ErrNilIdentityIndexResolver
	}
	home, _ := resolver.(HomeLocator)
	return &kvIdentityIndex{
		store: func(ctx context.Context) (kvstore.KVStore, error) {
			return resolver.Resolve(ctx, tenantid.System)
		},
		home: home,
	}, nil
}

// identityIndexKey composes the index key for a record. The kind segment keeps
// the families disjoint.
func identityIndexKey(kind IdentityKind, id string) string {
	return identityIndexKeyPrefix + string(kind) + "/" + id
}

// Record implements IdentityIndex.
//
// It CLAIMS the id: it succeeds only when this call is the one that created
// the entry. A pre-existing entry is refused whatever tenant holds it —
// including this one.
//
// Refusing the holder's own re-claim is deliberate. Treating it as success
// would mean a create could proceed on a claim it did not win, and a delete
// running concurrently would then release the claim out from under the record
// that create had just written: a record with no entry, unroutable by id
// forever, produced by an ordinary same-tenant delete-then-recreate race. A
// claim either was won here or the operation does not proceed.
//
// The cost is that a create which dies after claiming leaves the id claimed
// with no record behind it. That id is simply never reused — Snowflakes and
// UUIDs are not scarce — and refusing is the safe direction: it fails a
// create rather than producing an inconsistent pair.
//
// A tenant whose records live in its home store claims nothing: Record writes
// no entry and returns an empty token, which every caller already reads as
// "nothing to release". An isolated tenant's ids are resolved in its own
// tenant and nowhere else (architecture.md "Identity routing without a global
// index"), so an entry would only put the id and the tenant in the system
// shard, which is what isolation keeps out of it.
func (i *kvIdentityIndex) Record(ctx context.Context, kind IdentityKind, id, tenantID string) (string, error) {
	if id == "" {
		return "", &IdentityIndexError{Op: "record", Kind: kind, ID: id, Err: ErrEmptyIdentityID}
	}
	// Canonicalize so the empty id and tenantid.System — the same tenant —
	// cannot be stored as two different values and route two different ways.
	if tenantid.IsSystem(tenantID) {
		tenantID = tenantid.System
	}
	if i.home != nil {
		inHome, herr := i.home.InHome(ctx, tenantID)
		if herr != nil {
			return "", &IdentityIndexError{Op: "record", Kind: kind, ID: id, Err: herr}
		}
		if inHome {
			return "", nil
		}
	}
	store, err := i.store(ctx)
	if err != nil {
		return "", &IdentityIndexError{Op: "record", Kind: kind, ID: id, Err: err}
	}
	atomicStore, ok := store.(kvstore.AtomicKVStore)
	if !ok {
		return "", &IdentityIndexError{Op: "record", Kind: kind, ID: id, Err: ErrIdentityStoreNotAtomic}
	}

	token, terr := newClaimToken(tenantID)
	if terr != nil {
		return "", &IdentityIndexError{Op: "record", Kind: kind, ID: id, Err: terr}
	}
	key := identityIndexKey(kind, id)

	// Two attempts, not a loop: a refused claim whose holder has since been
	// released (a concurrent delete) is retried once, rather than reported as
	// a conflict with nobody. A second refusal with no holder is contention,
	// and reporting that is more honest than spinning.
	for attempt := 0; attempt < 2; attempt++ {
		stored, perr := atomicStore.PutIfAbsent(ctx, key, []byte(token))
		if perr != nil {
			return "", &IdentityIndexError{Op: "record", Kind: kind, ID: id, Err: perr}
		}
		if stored {
			return token, nil
		}
		_, held, herr := i.holder(ctx, store, key)
		if herr != nil {
			return "", &IdentityIndexError{Op: "record", Kind: kind, ID: id, Err: herr}
		}
		if !held {
			continue
		}
		return "", &IdentityClaimConflictError{Kind: kind, ID: id}
	}
	return "", &IdentityIndexError{Op: "record", Kind: kind, ID: id, Err: ErrIdentityClaimContended}
}

// Forget implements IdentityIndex. It releases only a claim the named tenant
// holds, so one tenant's delete path can never release another's and free the
// id for capture.
//
// The release is GUARDED: it deletes only if the stored value is still this
// tenant's, evaluated in one operation by the store. A read, a compare and
// then a plain delete is not the same thing, and the difference is a real
// defect the hosted review of #41 caught — two releasers both read holder A,
// the first deletes, tenant B legitimately claims the now-absent id with
// PutIfAbsent, and the second delete removes B's claim, leaving B holding a
// record with no routing entry. No amount of re-reading closes that, because
// the window is between the read and the delete.
//
// A guard that refuses is not an error: the claim was already released, or
// already belongs to someone else, and either way this call has nothing to
// do. It is reported the same way an absent claim is.
func (i *kvIdentityIndex) Forget(ctx context.Context, kind IdentityKind, id, token string) error {
	if id == "" {
		return &IdentityIndexError{Op: "forget", Kind: kind, ID: id, Err: ErrEmptyIdentityID}
	}
	if token == "" {
		return &IdentityIndexError{Op: "forget", Kind: kind, ID: id, Err: ErrEmptyClaimToken}
	}
	store, err := i.store(ctx)
	if err != nil {
		return &IdentityIndexError{Op: "forget", Kind: kind, ID: id, Err: err}
	}
	atomicStore, ok := store.(kvstore.AtomicKVStore)
	if !ok {
		return &IdentityIndexError{Op: "forget", Kind: kind, ID: id, Err: ErrIdentityStoreNotAtomic}
	}

	key := identityIndexKey(kind, id)
	deleted, derr := atomicStore.CompareAndDelete(ctx, key, []byte(token))
	if derr != nil {
		return &IdentityIndexError{Op: "forget", Kind: kind, ID: id, Err: derr}
	}
	if deleted {
		return nil
	}

	// The guard refused. Absent means the claim is already gone and this
	// release has nothing to do; PRESENT means it was replaced by a newer
	// claim that is not ours to delete.
	if _, held, herr := i.holder(ctx, store, key); herr != nil {
		return &IdentityIndexError{Op: "forget", Kind: kind, ID: id, Err: herr}
	} else if !held {
		return nil
	}
	return &IdentityClaimStaleError{Kind: kind, ID: id}
}

// holder reads the tenant currently holding a claim. held is false when the
// key is absent.
func (i *kvIdentityIndex) holder(ctx context.Context, store kvstore.KVStore, key string) (string, bool, error) {
	value, err := store.Get(ctx, key)
	if err != nil {
		return "", false, err
	}
	if len(value) == 0 {
		return "", false, nil
	}
	return string(value), true, nil
}

// Lookup implements IdentityIndex.
func (i *kvIdentityIndex) Lookup(ctx context.Context, kind IdentityKind, id string) (Claim, bool, error) {
	if id == "" {
		return Claim{}, false, &IdentityIndexError{Op: "lookup", Kind: kind, ID: id, Err: ErrEmptyIdentityID}
	}
	store, err := i.store(ctx)
	if err != nil {
		return Claim{}, false, &IdentityIndexError{Op: "lookup", Kind: kind, ID: id, Err: err}
	}
	value, err := store.Get(ctx, identityIndexKey(kind, id))
	if err != nil {
		return Claim{}, false, &IdentityIndexError{Op: "lookup", Kind: kind, ID: id, Err: err}
	}
	if len(value) == 0 {
		return Claim{}, false, nil
	}
	token := string(value)
	return Claim{Tenant: tenantFromClaimToken(token), Token: token}, true, nil
}

// PrepareClaim implements IdentityIndex. See the interface doc: this touches
// no store at all, so canonicalizing tenantID is the only work — the same
// canonicalization Record applies, so a claim prepared here and one Record
// would have written are byte-for-byte identical.
func (i *kvIdentityIndex) PrepareClaim(kind IdentityKind, id, tenantID string) (kvstore.KV, string, error) {
	if id == "" {
		return kvstore.KV{}, "", &IdentityIndexError{Op: "prepare_claim", Kind: kind, ID: id, Err: ErrEmptyIdentityID}
	}
	if tenantid.IsSystem(tenantID) {
		tenantID = tenantid.System
	}
	token, err := newClaimToken(tenantID)
	if err != nil {
		return kvstore.KV{}, "", &IdentityIndexError{Op: "prepare_claim", Kind: kind, ID: id, Err: err}
	}
	return kvstore.KV{Key: identityIndexKey(kind, id), Value: []byte(token)}, token, nil
}

// ReconcileDeletedIdentity releases a claim an interrupted delete left behind:
// the record is gone but its entry survives, because the delete failed between
// removing the record and releasing the claim.
//
// It exists so a delete is RECOVERABLE BY RETRY rather than atomic. The claim
// and the record live in different stores — the claim in the system tenant's
// identity namespace, the record in the tenant's own, which for a barrier
// tenant is reached through an entirely different wrapper — and kvstore.KVStore
// offers no transaction handle spanning them. Compensation plus an idempotent
// retry is the honest shape at this layer; a multi-key transaction here would
// mean plumbing one through the store interface.
//
// The delete removes the record LAST, so "record absent" means every other
// index entry is already gone and the claim is the only thing that can remain.
// Releasing it here therefore COMPLETES the delete rather than papering over a
// half-finished one.
//
// The claim is deliberately NOT released first. Between releasing it and
// removing the record the id would be claimable, and another tenant taking it
// — which is the capture defence working as designed for a caller-supplied id
// — would leave the original tenant's retry refused by the holder guard while
// its record still existed: listed, undeletable, unroutable by id. That is the
// state this function exists to eliminate, so the window must not exist.
//
// It reports whether a claim was released. False with a nil error means the id
// was genuinely absent — a delete of something that never existed — and the
// caller keeps its not-found answer.
func ReconcileDeletedIdentity(ctx context.Context, index IdentityIndex, kind IdentityKind, id, owner string) (bool, error) {
	if index == nil || id == "" {
		return false, nil
	}
	claim, held, err := index.Lookup(ctx, kind, id)
	if err != nil {
		return false, err
	}
	if !held {
		return false, nil
	}
	if tenantid.IsSystem(owner) {
		owner = tenantid.System
	}
	if claim.Tenant != owner {
		// Someone else holds it. Not ours to release, and Forget would refuse
		// anyway — say so by declining rather than by surfacing a conflict the
		// caller cannot act on.
		return false, nil
	}
	// Guarded on the token READ ABOVE: if a newer claim replaced it between
	// the read and here, the release refuses rather than deleting the
	// replacement, and this reports that it released nothing.
	if ferr := index.Forget(ctx, kind, id, claim.Token); ferr != nil {
		var stale *IdentityClaimStaleError
		if errors.As(ferr, &stale) {
			return false, nil
		}
		return false, ferr
	}
	return true, nil
}

// OwningTenant returns the tenant a by-id operation must be routed to.
//
// It is the single read-path rule shared by every router, because "which
// store holds this id" must not be answered two slightly different ways in
// two packages:
//
//   - No index, or no entry for the id: the caller's own tenant. Absence
//     means "held nowhere else", so the operation stays exactly as
//     confined as it was before this index existed. It never widens reach.
//
//   - An entry the caller is entitled to reach — its own tenant, or any
//     tenant when the caller is the system tenant: that tenant. This is the
//     same reconciliation CreateUser and ListUsers already apply through
//     EffectiveTenant, applied to a tenant the index supplies rather than
//     one the client typed.
//
//   - An entry the caller is NOT entitled to reach: the caller's own
//     tenant, where the id is genuinely absent and the store answers with
//     its ordinary not-found. Returning CrossTenantDeniedError here would
//     be a cross-tenant existence oracle: the caller supplied only an id and
//     a denial would confirm both that the record exists and where it lives.
//     A caller who names another tenant EXPLICITLY still gets the denial
//     from EffectiveTenant, because there the caller already knows what it
//     asked for.
//
// A lookup failure is returned, never swallowed. In particular a sealed
// barrier surfaces ErrTenantSealed from the routing that follows, so the
// operation fails closed rather than reading somewhere else.
func OwningTenant(ctx context.Context, index IdentityIndex, kind IdentityKind, id, op string) (string, error) {
	caller := FromContext(ctx)
	if index == nil || id == "" {
		// An empty id is not this function's error to report. Routing to the
		// caller's own tenant lets the store answer with the ValidationError
		// its callers already expect, instead of an index error that
		// describes the lookup rather than the request.
		return caller, nil
	}
	claim, ok, err := index.Lookup(ctx, kind, id)
	if err != nil {
		return "", err
	}
	if !ok {
		return caller, nil
	}
	effective, err := EffectiveTenant(ctx, claim.Tenant, op)
	if err != nil {
		var denied *CrossTenantDeniedError
		if errors.As(err, &denied) {
			return caller, nil
		}
		return "", err
	}
	return effective, nil
}
