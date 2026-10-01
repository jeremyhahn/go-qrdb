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

package role

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

const (
	// keyPrefixPrimary is the prefix for role primary records keyed by ID.
	keyPrefixPrimary = "__sys:roles/id/"

	// keyPrefixTenant is the prefix for the tenant-to-role index.
	keyPrefixTenant = "__sys:roles/tenant/"

	// keyPrefixName is the prefix for the role name secondary index.
	keyPrefixName = "__sys:role_idx/name/"

	// defaultPageSize is used when ListRolesRequest.PageSize is zero.
	defaultPageSize = 100
)

// Compile-time interface check.
var _ RoleServicer = (*kvRoleStore)(nil)

// kvRoleStore implements RoleServicer backed by a KVStore with secondary
// indexes for tenant listing and name uniqueness.
//
// Key schema:
//
//	__sys:roles/id/{roleID}                      -> Role JSON (primary)
//	__sys:roles/tenant/{tenantID}/{roleID}       -> roleID (as bytes)
//	__sys:role_idx/name/{tenantID}/{roleName}    -> roleID (as bytes)
//
// One index does NOT live here: the system-plane identity index that maps a
// role id to the tenant holding it. That entry is cross-tenant by nature and
// belongs in the system tenant's identity namespace, so it is written
// through an injected tenant.IdentityIndex (see WithIdentityIndex) rather
// than into this store — a tenant's own store must not be able to write
// outside its tenant.
type kvRoleStore struct {
	store          kvstore.KVStore
	quotaChecker   quota.Checker        // optional; nil skips quota enforcement
	audit          audit.AuditLogger    // optional; nil disables audit emission
	identity       tenant.IdentityIndex // optional; nil skips identity-index maintenance
	atomicIdentity bool                 // set by WithAtomicIdentity; see that option's doc

	// reconcilePollInterval/reconcileWindow bound reconcileAmbiguousCreate's
	// retry loop. Zero means "use the production default"; see that
	// function's doc and WithReconcilePolling for why a test overrides them.
	reconcilePollInterval time.Duration
	reconcileWindow       time.Duration
}

// NewStore creates a new KVStore-backed RoleServicer. The provided
// kvstore must not be nil. Optional Options (e.g. WithAuditLogger) tune
// the store's behavior.
//
// Refuses construction — rather than silently degrading — when
// WithAtomicIdentity is set without WithIdentityIndex (there would be no
// claim to make atomic) or over a store that does not implement
// kvstore.MultiGuardedBatchKVStore (there would be nothing to make it atomic
// WITH). Both are caller configuration errors, not runtime conditions: a
// store's capabilities and a package's options are fixed at construction, so
// there is nothing to gain by deferring the failure to the first CreateRole
// call — and something to lose, since that failure would then depend on
// which caller happens to create the first role.
//
// MultiGuardedBatchKVStore, not the single-guard GuardedBatchKVStore: the
// atomic batch guards the identity claim key AND the name-index key
// together (see createRoleAtomic), because guarding the claim alone leaves
// two concurrent creates for DIFFERENT ids but the SAME tenant/name free to
// both pass their own (distinct) claim guards and both commit, silently
// racing the name index (go-qrdb#555 review, round 3).
func NewStore(store kvstore.KVStore, opts ...Option) (RoleServicer, error) {
	if store == nil {
		return nil, &StoreError{Op: "new", Detail: "kvstore must not be nil"}
	}
	s := &kvRoleStore{store: store}
	for _, opt := range opts {
		opt(s)
	}
	if s.atomicIdentity {
		if s.identity == nil {
			return nil, &StoreError{Op: "new", Detail: "WithAtomicIdentity requires WithIdentityIndex"}
		}
		if _, ok := store.(kvstore.MultiGuardedBatchKVStore); !ok {
			return nil, &StoreError{Op: "new", Detail: "WithAtomicIdentity requires a kvstore.MultiGuardedBatchKVStore-capable store"}
		}
	}
	if s.reconcilePollInterval <= 0 {
		s.reconcilePollInterval = defaultReconcilePollInterval
	}
	if s.reconcileWindow <= 0 {
		s.reconcileWindow = reconcileTimeout
	}
	return s, nil
}

// WithQuotaChecker returns a new kvRoleStore with the given quota.Checker
// applied. Passing nil disables quota enforcement.
func (s *kvRoleStore) WithQuotaChecker(checker quota.Checker) *kvRoleStore {
	s.quotaChecker = checker
	return s
}

// CreateRole creates a new role with a generated UUID. It validates the
// request, checks name uniqueness within the tenant via the secondary
// index, validates all permissions, and writes the primary record plus both
// indexes.
//
// When the store was built WithAtomicIdentity, the identity claim and all
// three writes commit as ONE guarded batch (see createRoleAtomic): a
// leadership change anywhere in the sequence leaves either everything or
// nothing, closing the interrupted-create window go-qrdb#555 found. Every
// other construction keeps the original three-proposal sequence below,
// unchanged, because folding the claim into one batch requires the claim's
// backing store to be the exact store this call writes to — true for a
// boot-confined identity index, not provable for a StoreResolver-backed one
// (see WithAtomicIdentity).
func (s *kvRoleStore) CreateRole(ctx context.Context, req *CreateRoleRequest) (created *Role, retErr error) {
	if req == nil {
		return nil, &ValidationError{Field: "request", Reason: "must not be nil"}
	}
	if req.Name == "" {
		return nil, &ValidationError{Field: "name", Reason: "must not be empty"}
	}

	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
	}

	if s.quotaChecker != nil {
		if err := s.quotaChecker.Check(ctx, tenantID, quota.ResourceRoles); err != nil {
			return nil, err
		}
	}

	// Validate all permissions.
	if err := validatePermissions(req.Permissions); err != nil {
		return nil, err
	}

	// A reserved name may be taken only by the canonical role it denotes.
	// Checked before any write, and before the name-uniqueness lookup, so a
	// malformed built-in is refused on its own terms rather than by whichever
	// conflict it happens to hit first.
	if IsSystemRoleName(req.Name) {
		if err := requireCanonicalSystemRole(tenantID, req); err != nil {
			return nil, err
		}
	}

	// Check name uniqueness via the secondary index.
	nameKey := nameIndexKey(tenantID, req.Name)
	existing, err := s.store.Get(ctx, nameKey)
	if err != nil {
		return nil, &StoreError{Op: "create", Detail: "name index lookup", Err: err}
	}
	if existing != nil {
		return nil, &RoleNameExistsError{Name: req.Name, TenantID: tenantID}
	}

	now := time.Now().UTC()
	id := req.ID
	if id == "" {
		id = uuid.New().String()
	}
	r := &Role{
		ID:          id,
		Name:        req.Name,
		TenantID:    tenantID,
		Permissions: req.Permissions,
		// DERIVED, never supplied. The bootstrap seeding path reaches this
		// function through CreateRoleRequest, which has no System field, so
		// every built-in role used to be persisted with System: false and the
		// immutability guards in DeleteRole and UpdateRole never fired for a
		// single one of them (go-qrdb#17). Deriving it here is what makes the
		// flag true of the record on disk rather than only of the in-memory
		// value BuiltInRoles returns.
		System:    IsSystemRoleName(req.Name),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if r.Permissions == nil {
		r.Permissions = []permission.Permission{}
	}

	// CreateRoleRequest.ID is CALLER-SUPPLIED — the REST and gRPC surfaces
	// pass it straight through — so a duplicate id must be refused rather
	// than silently overwriting the record it collides with. This mirrors the
	// name-uniqueness check above; it is the id's equivalent.
	if req.ID != "" {
		clash, cerr := s.store.Get(ctx, primaryKey(r.ID))
		if cerr != nil {
			return nil, &StoreError{Op: "create", Detail: "id lookup", Err: cerr}
		}
		if clash != nil {
			return nil, &RoleIDExistsError{ID: r.ID, TenantID: tenantID}
		}
	}

	if s.atomicIdentity {
		return s.createRoleAtomic(ctx, r, tenantID, nameKey)
	}

	// CLAIM the id across ALL tenants before anything is written. The
	// per-tenant check above cannot see another tenant's store, and the
	// identity index is global: without a claim, a tenant could create a role
	// carrying ANOTHER tenant's role id, take over that id's index entry, and
	// silently redirect every by-id operation an administrator performs on it.
	// A conflict is reported as RoleIDExistsError naming the CALLER's tenant,
	// so the refusal does not disclose where the id already lives.
	claimToken, cerr := s.recordIdentity(ctx, r.ID, tenantID)
	if cerr != nil {
		var conflict *tenant.IdentityClaimConflictError
		if errors.As(cerr, &conflict) {
			return nil, &RoleIDExistsError{ID: r.ID, TenantID: tenantID}
		}
		return nil, &StoreError{Op: "create", Detail: "claim identity", Err: cerr}
	}

	// EVERY failure after the claim releases it. Role ids are caller-supplied
	// names, not generated ones, so a transient write error that left the
	// claim behind would permanently burn an id the caller chose and cannot
	// recover: the record does not exist, so DeleteRole cannot reach it to
	// release the claim either.
	//
	// This is a deferred compensation on the named return rather than a
	// release at each write, so a write added later is covered by
	// construction. Hand-placed compensations are the sibling-maintainer
	// shape that produced this whole class of defect.
	// written records the keys this call actually put, in the order the
	// rollback must undo them: secondary indexes first, the primary last.
	var written []string
	defer func() {
		if retErr != nil {
			s.rollbackFailedCreate(ctx, written, r.ID, tenantID, claimToken)
		}
	}()

	data, err := marshalRole(r)
	if err != nil {
		return nil, &StoreError{Op: "create", Detail: "marshal", Err: err}
	}

	// Write primary record. It is recorded LAST in `written` so the rollback
	// removes it last, keeping the record findable while its indexes are
	// cleaned up.
	pk := primaryKey(r.ID)
	if err := s.store.Put(ctx, pk, data); err != nil {
		return nil, &StoreError{Op: "create", Detail: "put primary", Err: err}
	}
	written = append([]string{pk}, written...)

	// Write tenant index.
	tk := tenantIndexKey(tenantID, r.ID)
	if err := s.store.Put(ctx, tk, []byte(r.ID)); err != nil {
		return nil, &StoreError{Op: "create", Detail: "put tenant index", Err: err}
	}
	written = append([]string{tk}, written...)

	// Write name index.
	if err := s.store.Put(ctx, nameKey, []byte(r.ID)); err != nil {
		return nil, &StoreError{Op: "create", Detail: "put name index", Err: err}
	}
	written = append([]string{nameKey}, written...)

	s.emitAudit(ctx, "create", "role:"+r.ID, map[string]any{
		"name":        r.Name,
		"tenantID":    tenantID,
		"permissions": len(r.Permissions),
	})

	return r, nil
}

// createRoleAtomic commits the identity claim, the name-index claim, and all
// three writes (primary record, tenant index, name index) as ONE guarded
// batch, so a leadership change anywhere in the sequence leaves either
// everything or nothing — never the partial states go-qrdb#555 found (a
// claim with no record, or a record missing one of its indexes). It is
// reached only when the store was built WithAtomicIdentity; see that
// option's doc for the precondition it asserts on the caller's behalf and
// why this cannot be the only path.
//
// The batch guards BOTH the identity claim key and the name-index key,
// evaluated together via kvstore.MultiGuardedBatchKVStore. A single guard on
// the claim alone is exact for the deterministic built-in roles
// WithAtomicIdentity's callers seed (their id IS derived from their name, so
// the claim and the name denote the same collision) but not for an ordinary
// caller-supplied id: two concurrent creates with DIFFERENT ids and the SAME
// tenant/name would each satisfy their own distinct claim guard and both
// commit, with the second silently overwriting the name index while both
// primary records, tenant indexes, and claims remained — an id-guard-only
// design cannot see a same-name collision at all. Guarding the name key too
// closes that window for every caller, not only the built-in-role case.
//
// On a guard failure this reads back the occupied key and returns the SAME
// typed error CreateRole has always returned for that conflict
// (RoleIDExistsError for an id collision, RoleNameExistsError for a name
// collision). It does not itself decide whether a
// concurrent, identical built-in role's create should be treated as
// success: that determination requires comparing the existing record's full
// definition against a KNOWN-GOOD one, which only the caller minting that
// definition (pkg/firstboot, for the six built-in roles) can do honestly.
// Doing it here would let ANY caller holding roles:create adopt another
// tenant's or another actor's in-flight custom role by supplying its exact
// name and a guessed id — the provenance problem an earlier version of this
// function had.
func (s *kvRoleStore) createRoleAtomic(ctx context.Context, r *Role, tenantID, nameKey string) (*Role, error) {
	guarded, ok := s.store.(kvstore.MultiGuardedBatchKVStore)
	if !ok {
		// Unreachable: NewStore refuses WithAtomicIdentity construction over a
		// store that does not implement this. Defensive rather than a panic if
		// that check is ever bypassed.
		return nil, &StoreError{Op: "create", Detail: "atomic identity requires a kvstore.MultiGuardedBatchKVStore"}
	}
	claimKV, _, err := s.identity.PrepareClaim(tenant.IdentityRole, r.ID, tenantID)
	if err != nil {
		return nil, &StoreError{Op: "create", Detail: "prepare claim", Err: err}
	}
	data, err := marshalRole(r)
	if err != nil {
		return nil, &StoreError{Op: "create", Detail: "marshal", Err: err}
	}
	pk := primaryKey(r.ID)
	tk := tenantIndexKey(tenantID, r.ID)
	guards := []kvstore.Guard{
		{Key: claimKV.Key},
		{Key: nameKey},
	}
	writes := []kvstore.KV{
		claimKV,
		{Key: pk, Value: data},
		{Key: tk, Value: []byte(r.ID)},
		{Key: nameKey, Value: []byte(r.ID)},
	}

	applied, err := guarded.PutAllIfAllUnchanged(ctx, guards, writes)
	if err != nil {
		if errors.Is(err, kvstore.ErrNotApplied) {
			// A definite refusal — no leader to propose it, no transaction
			// subsystem — wrote nothing. Reconciling would only poll for a
			// write that cannot appear.
			return nil, &StoreError{Op: "create", Detail: "atomic create", Err: err}
		}
		return s.reconcileAmbiguousCreate(ctx, r, writes, err)
	}
	if !applied {
		return nil, s.diagnoseAtomicGuardFailure(ctx, r, tenantID, claimKV.Key, nameKey)
	}

	s.emitAudit(ctx, "create", "role:"+r.ID, map[string]any{
		"name":        r.Name,
		"tenantID":    tenantID,
		"permissions": len(r.Permissions),
	})
	return r, nil
}

// diagnoseAtomicGuardFailure reads back each key createRoleAtomic guarded to
// report the SAME typed conflict error CreateRole has always returned for
// that condition. PutAllIfAllUnchanged reports only that at least one guard
// did not hold, not which one, so this issues plain reads — outside the
// failed transaction, causing no new ambiguity — to find the occupied key.
//
// The identity claim is checked first, matching pkg/user's equivalent: an id
// collision is the condition go-qrdb#555 added guarding for, checked ahead
// of the pre-existing name collision so this fix does not change which error
// a caller already handling RoleNameExistsError sees for that unrelated,
// pre-existing conflict.
//
// The read-back is POLLED (pollVisible), bounded by s.reconcileWindow. The
// state machine refused the guard because the winner's keys were committed,
// but under cmd/qrdbd's boot-time StaleRead this node may not have applied
// them yet: a single read would find every key absent and hand the losing
// first-boot node a generic failure instead of the typed already-exists
// error firstboot resolves idempotently.
func (s *kvRoleStore) diagnoseAtomicGuardFailure(ctx context.Context, r *Role, tenantID, claimKey, nameKey string) error {
	dctx, cancel := context.WithTimeout(ctx, s.reconcileWindow)
	defer cancel()
	var occupied error
	s.pollVisible(dctx, func() bool {
		switch {
		case s.keyPresent(dctx, claimKey):
			occupied = &RoleIDExistsError{ID: r.ID, TenantID: tenantID}
		case s.keyPresent(dctx, nameKey):
			occupied = &RoleNameExistsError{Name: r.Name, TenantID: tenantID}
		}
		return occupied != nil
	})
	if occupied != nil {
		return occupied
	}
	// Defensive: the guard reported failure but every guarded key stayed
	// absent for the whole window — most plausibly a concurrent release
	// racing these reads. Report the generic conflict rather than fabricating
	// a more specific one no read could confirm.
	return &StoreError{Op: "create", Detail: "atomic create guard failed"}
}

// keyPresent reports whether key currently holds a value on this node.
func (s *kvRoleStore) keyPresent(ctx context.Context, key string) bool {
	v, err := s.store.Get(ctx, key)
	return err == nil && v != nil
}

// reconcileAmbiguousCreate handles a PutAllIfAllUnchanged error whose outcome
// is UNKNOWN per that method's own contract: the proposal may have committed
// and only the response been lost. An error marked kvstore.ErrNotApplied is
// not unknown and never reaches here; createRoleAtomic returns it at once. It reads every key the batch would have
// written and treats the create as successful ONLY when every single one
// holds exactly the bytes this call would have written — never on the
// primary key alone, so a store that applied some of the batch's writes but
// not all of them (unreachable for a truly atomic store, but not something
// this function should assume without checking) is reported as a failure
// rather than a false success. Any other outcome returns a StoreError whose
// Err is the original cause, so errors.Is/As classification upstream (a
// NotLeader, say) still reaches it through Unwrap.
//
// A successful reconciliation emits the SAME create audit event the normal
// success path does. The batch committed durably either way — only the
// response describing it was lost — and skipping the event here would let a
// privileged role exist with no audit trail whenever that happens.
//
// The read-back and the audit emission run on a context DERIVED from ctx via
// context.WithoutCancel plus its own bound (s.reconcileWindow), not on ctx
// itself. The ambiguous outcome this function exists to resolve very often
// IS ctx already being done — a deadline exceeded or a cancellation is
// exactly the shape of "the response never arrived" — and reusing that same
// ctx for the reads would then fail EVERY read immediately, always
// reporting the create as failed and never emitting the audit event this
// function was added to guarantee. context.WithoutCancel preserves ctx's
// VALUES (tenant, auth, tracing) while dropping its cancellation, matching
// the compensation contexts CreateRole's own rollback path already uses.
//
// The read-back RETRIES on s.reconcilePollInterval until s.reconcileWindow
// elapses, rather than reading once (go-qrdb#555 review, round 4): cmd/qrdbd
// deliberately reads the boot-time role/user stores via StaleRead — the
// local state machine's own applied state, not a linearizable ReadIndex —
// to avoid a startup deadlock before the ReadIndex pipeline is warmed up.
// A proposal whose response was lost can therefore be committed to the Raft
// log but not yet visible to a StaleRead on THIS node for a brief window
// while local apply catches up; one immediate read during exactly that
// window would see stale/absent values and report a failure — and skip the
// audit event — for a create that in fact just committed. Retrying gives
// local apply the window it needs: a write that will never appear still
// times out and reports failure once s.reconcileWindow elapses, correctly.
func (s *kvRoleStore) reconcileAmbiguousCreate(ctx context.Context, r *Role, writes []kvstore.KV, cause error) (*Role, error) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.reconcileWindow)
	defer cancel()
	if !s.pollVisible(rctx, func() bool { return s.reconcileWritesMatch(rctx, writes) }) {
		return nil, &StoreError{Op: "create", Detail: "atomic create", Err: cause}
	}
	s.emitAudit(rctx, "create", "role:"+r.ID, map[string]any{
		"name":           r.Name,
		"tenantID":       r.TenantID,
		"permissions":    len(r.Permissions),
		"reconciliation": "ambiguous_response",
	})
	return r, nil
}

// pollVisible calls visible every s.reconcilePollInterval until it reports
// true or ctx is done, and reports whether it did. Both read-backs after an
// atomic create go through it — reconciling a lost response, and diagnosing a
// refused guard — because both read THIS node's applied state, which under
// cmd/qrdbd's boot-time StaleRead can lag the commit the read is about.
func (s *kvRoleStore) pollVisible(ctx context.Context, visible func() bool) bool {
	ticker := time.NewTicker(s.reconcilePollInterval)
	defer ticker.Stop()
	for {
		if visible() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
}

// reconcileWritesMatch reports whether every write's key currently holds
// exactly the bytes that write would have stored.
func (s *kvRoleStore) reconcileWritesMatch(ctx context.Context, writes []kvstore.KV) bool {
	for _, w := range writes {
		got, err := s.store.Get(ctx, w.Key)
		if err != nil || !bytes.Equal(got, w.Value) {
			return false
		}
	}
	return true
}

// GetRole retrieves a role by its globally unique ID.
func (s *kvRoleStore) GetRole(ctx context.Context, id string) (*Role, error) {
	if id == "" {
		return nil, &ValidationError{Field: "id", Reason: "must not be empty"}
	}

	data, err := s.store.Get(ctx, primaryKey(id))
	if err != nil {
		return nil, &StoreError{Op: "get", Detail: "primary lookup", Err: err}
	}
	if data == nil {
		return nil, &RoleNotFoundError{ID: id}
	}

	var r Role
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, &StoreError{Op: "get", Detail: "unmarshal", Err: err}
	}
	// DERIVED on every read, from the registry as it stands right now — see
	// Role.RetiredPermissions. GetRoleByName, ListRoles and ResolvePermissions
	// all funnel through here, so the drift signal reaches every read surface
	// from one place.
	r.RetiredPermissions = RetiredPermissions(r.Permissions)
	// Tenant ownership: a non-system caller may only read roles in its own
	// tenant. The system tenant (admin and internal RBAC permission
	// resolution, which runs under the identity's own tenant) reads any role.
	// This closes a cross-tenant role-metadata read in the shared
	// (prefix-isolation) store; barrier-isolated tenants are already
	// physically isolated by the routing layer. GetRoleByName and
	// ResolvePermissions both funnel through here, so they inherit the check.
	if ctxTenant := tenant.FromContext(ctx); !tenant.IsSystemTenant(ctxTenant) && r.TenantID != ctxTenant {
		return nil, &RoleNotFoundError{ID: id}
	}
	return &r, nil
}

// GetRoleByName resolves a role name within a tenant via the secondary
// index, then fetches the primary record.
func (s *kvRoleStore) GetRoleByName(ctx context.Context, tenantID, name string) (*Role, error) {
	if name == "" {
		return nil, &ValidationError{Field: "name", Reason: "must not be empty"}
	}
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
	}

	idBytes, err := s.store.Get(ctx, nameIndexKey(tenantID, name))
	if err != nil {
		return nil, &StoreError{Op: "get_by_name", Detail: "index lookup", Err: err}
	}
	if idBytes == nil {
		return nil, &RoleNotFoundError{ID: tenantID + "/" + name}
	}

	return s.GetRole(ctx, string(idBytes))
}

// UpdateRole applies partial updates to an existing role. An empty
// Name means no change; nil Permissions means no change. If the name
// changes, uniqueness is re-validated and the secondary index is
// updated atomically.
//
// A system role is immutable in both its name and its grants: it cannot be
// renamed, no role can be renamed into its name, and its permission set cannot
// be replaced. That is the contract docs/firstboot/user.md states to operators
// — "they are undeletable and their permissions are fixed. Create additional
// roles instead" — and docs/firstboot/architecture.md repeats as a non-goal.
// Only ordinary roles are updatable.
func (s *kvRoleStore) UpdateRole(ctx context.Context, req *UpdateRoleRequest) (*Role, error) {
	if req == nil {
		return nil, &ValidationError{Field: "request", Reason: "must not be nil"}
	}
	if req.ID == "" {
		return nil, &ValidationError{Field: "id", Reason: "must not be empty"}
	}

	existing, err := s.GetRole(ctx, req.ID)
	if err != nil {
		return nil, err
	}

	nameChanged := req.Name != "" && req.Name != existing.Name

	// The two halves of ONE invariant: the name a role carries and the System
	// flag stored on it must never disagree, because IsSystemRoleName is what
	// CreateRole derives the flag from. Renaming is the only operation that
	// can pull them apart, so it is guarded from both directions.
	//
	// Renaming a system role AWAY from its canonical name would strand a
	// record with System: true under a name nothing recognises, and would
	// silently break the RBAC authorizer's name-resolved controls — the SO
	// deny-list (FIPS 140-3 AS10.03) and the strict-separation-of-duties Owner
	// strip both reach the role through its name. UpdateRole's doc comment has
	// claimed this guard exists since the package was written; until
	// go-qrdb#17 it did not.
	if nameChanged && existing.System {
		return nil, &SystemRoleImmutableError{ID: existing.ID, Op: "renamed"}
	}

	// Renaming an ordinary role INTO a canonical name is the same divergence
	// from the other side: the record would carry a reserved name while its
	// stored System flag stayed false, so DeleteRole would happily remove the
	// role the authorizer had begun resolving by that name. Name uniqueness
	// blocks this whenever the built-in already exists, which is exactly why
	// it must be refused explicitly for the tenants where it does not.
	if nameChanged && IsSystemRoleName(req.Name) {
		return nil, &ValidationError{
			Field:  "name",
			Reason: "system role names are reserved and cannot be taken by renaming: " + req.Name,
		}
	}

	// A system role's GRANTS are fixed too, not just its identity.
	//
	// docs/firstboot/user.md states it to operators as a contract — "You want
	// to change the built-in roles. You cannot — they are undeletable and
	// their permissions are fixed. Create additional roles instead" — and
	// docs/firstboot/architecture.md repeats it as a deliberate non-goal. The
	// code never enforced either half.
	//
	// It matters more than the name guard: rewriting the custodian role's
	// single barrier:unseal-share grant, or widening Auditor past read-only,
	// changes the compliance posture (FIPS 140-3 AS10.03/AS10.06/AS10.10)
	// while the record still presents as a canonical system role to anything
	// that inspects it.
	//
	// This comment used to add "nothing in the tree ever performs such an
	// update — there is no built-in permission reconcile path on boot — so
	// this refusal costs no existing caller." That stopped being true: a
	// permission rename strands every tenant seeded before it, and repairing
	// that requires writing a built-in's grants (go-qrdb#354). There is now
	// exactly ONE caller that may, ReconcileBuiltInRoles, and it does not come
	// through here — it writes the primary record directly, because its target
	// is BuiltInRoles rather than anything a caller supplies. Every other
	// caller is still refused, which is the whole point: the guard was never
	// about the write, it was about who chooses the value.
	if existing.System && req.Permissions != nil {
		return nil, &SystemRoleImmutableError{ID: existing.ID, Op: "re-permissioned"}
	}

	if nameChanged {
		// Check that the new name is not already taken.
		newKey := nameIndexKey(existing.TenantID, req.Name)
		found, err := s.store.Get(ctx, newKey)
		if err != nil {
			return nil, &StoreError{Op: "update", Detail: "name index check", Err: err}
		}
		if found != nil {
			return nil, &RoleNameExistsError{Name: req.Name, TenantID: existing.TenantID}
		}

		// Remove old name index.
		oldKey := nameIndexKey(existing.TenantID, existing.Name)
		if err := s.store.Delete(ctx, oldKey); err != nil {
			return nil, &StoreError{Op: "update", Detail: "delete old name index", Err: err}
		}

		existing.Name = req.Name

		// Write new name index.
		if err := s.store.Put(ctx, newKey, []byte(existing.ID)); err != nil {
			return nil, &StoreError{Op: "update", Detail: "put new name index", Err: err}
		}
	}

	if req.Permissions != nil {
		if err := validatePermissions(req.Permissions); err != nil {
			return nil, err
		}
		existing.Permissions = req.Permissions
	}

	existing.UpdatedAt = time.Now().UTC()

	data, err := marshalRole(existing)
	if err != nil {
		return nil, &StoreError{Op: "update", Detail: "marshal", Err: err}
	}

	if err := s.store.Put(ctx, primaryKey(existing.ID), data); err != nil {
		return nil, &StoreError{Op: "update", Detail: "put primary", Err: err}
	}

	s.emitAudit(ctx, "update", "role:"+existing.ID, map[string]any{
		"name":                existing.Name,
		"tenantID":            existing.TenantID,
		"name_changed":        nameChanged,
		"permissions_changed": req.Permissions != nil,
		"permissions":         len(existing.Permissions),
	})

	return existing, nil
}

// DeleteRole removes a role by ID. System roles cannot be deleted.
func (s *kvRoleStore) DeleteRole(ctx context.Context, id string) error {
	if id == "" {
		return &ValidationError{Field: "id", Reason: "must not be empty"}
	}

	existing, err := s.GetRole(ctx, id)
	if err != nil {
		return err
	}
	if existing.System {
		return &SystemRoleImmutableError{ID: id, Op: "deleted"}
	}

	// ORDER IS LOAD-BEARING. The primary record is removed LAST of the
	// store's writes and the identity claim after it, because that is what
	// makes an interrupted delete recoverable by a plain retry:
	//
	//   - a failure before the primary delete leaves the record present, so
	//     GetRole still finds it and the retry re-runs the index deletes,
	//     which are idempotent;
	//   - a failure after it leaves ONLY the claim, and "record absent" then
	//     means every index entry is already gone — which is what lets
	//     tenant.ReconcileDeletedIdentity finish the job instead of reporting
	//     success over leaked index entries.
	//
	// Releasing the claim FIRST would be simpler and is wrong: between the
	// release and the record delete the id is claimable, and another tenant
	// taking it would leave this tenant's retry refused by the holder guard
	// with its record still present — listed, undeletable, unroutable by id.

	// Delete tenant index.
	if err := s.store.Delete(ctx, tenantIndexKey(existing.TenantID, id)); err != nil {
		return &StoreError{Op: "delete", Detail: "tenant index", Err: err}
	}

	// Delete name index.
	if err := s.store.Delete(ctx, nameIndexKey(existing.TenantID, existing.Name)); err != nil {
		return &StoreError{Op: "delete", Detail: "name index", Err: err}
	}

	// Delete the primary record LAST: it is what a retry keys on.
	if err := s.store.Delete(ctx, primaryKey(id)); err != nil {
		return &StoreError{Op: "delete", Detail: "primary", Err: err}
	}

	// Release the claim, so a stale one cannot keep routing the id to a
	// tenant that no longer holds it. If this fails the retry path completes
	// it (routingRoleService.DeleteRole).
	if err := s.forgetIdentity(ctx, id, existing.TenantID); err != nil {
		return &StoreError{Op: "delete", Detail: "identity index", Err: err}
	}

	s.emitAudit(ctx, "delete", "role:"+id, map[string]any{
		"name":     existing.Name,
		"tenantID": existing.TenantID,
	})

	return nil
}

// ListRoles returns a paginated list of roles for the given tenant by
// scanning the tenant index prefix.
func (s *kvRoleStore) ListRoles(ctx context.Context, req *ListRolesRequest) (*ListRolesResponse, error) {
	if req == nil {
		return nil, &ValidationError{Field: "request", Reason: "must not be nil"}
	}

	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
	}

	page := req.Page
	if page <= 0 {
		page = 1
	}
	pageSize := req.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}

	// Collect all role IDs for the tenant via the tenant index.
	prefix := keyPrefixTenant + tenantID + "/"
	var roleIDs []string
	err := s.store.Scan(ctx, prefix, func(_ string, value []byte) error {
		roleIDs = append(roleIDs, string(value))
		return nil
	})
	if err != nil {
		return nil, &StoreError{Op: "list", Detail: "scan tenant index", Err: err}
	}

	// Sort for deterministic pagination.
	sort.Strings(roleIDs)

	total := len(roleIDs)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}

	pageIDs := roleIDs[start:end]
	roles := make([]*Role, 0, len(pageIDs))
	for _, id := range pageIDs {
		r, err := s.GetRole(ctx, id)
		if err != nil {
			return nil, err
		}
		roles = append(roles, r)
	}

	return &ListRolesResponse{
		Roles:   roles,
		Total:   total,
		HasMore: end < total,
	}, nil
}

// ResolvePermissions returns the union of all permissions for the given
// role IDs. The result is sorted and deduplicated.
func (s *kvRoleStore) ResolvePermissions(ctx context.Context, roleIDs []string) ([]permission.Permission, error) {
	if len(roleIDs) == 0 {
		return []permission.Permission{}, nil
	}

	seen := make(map[permission.Permission]struct{})
	for _, id := range roleIDs {
		r, err := s.GetRole(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, p := range r.Permissions {
			seen[p] = struct{}{}
		}
	}

	result := make([]permission.Permission, 0, len(seen))
	for p := range seen {
		result = append(result, p)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i] < result[j]
	})

	return result, nil
}

// BuiltInRoles returns the predefined system roles for the given tenant.
// Each role uses a deterministic UUID derived from its name and tenant ID.
//
// The SO, Admin, Operator, User, and Auditor permission sets are NOT
// hand-maintained lists: each is a live projection of the permission
// registry's default role-grant metadata (permission.RolePermissions),
// registered once per permission in pkg/permission/builtin_role_grants.go
// rather than duplicated here. Role placement is metadata carried by the
// permission; this function only materializes it. Because
// permission.RolePermissions reads the registry at call time rather than a
// value cached at package-init time, a permission registered by a
// higher-layer package (go-quicbaas, go-xkms) before role seeding is
// reflected here even though it was contributed after this package's own
// init() ran.
//
// Roles are ordered as follows:
//  1. SO (Security Officer) — governance only, no crypto (FIPS 140-3 AS10.03,
//     enforced structurally by permission.RegisterRoleGrant)
//  2. Admin — key lifecycle and tenant management. Grants cross-tenant
//     authority only when the role is held in the system tenant
//     (Admin@system); the system-tenant rule in pkg/auth/rbac filters
//     cross-scope permissions to system-tenant identities.
//  3. Operator — routine crypto and maintenance
//  4. User — end-user cryptographic operations
//  5. Auditor — read-only audit and state visibility
//  6. Custodian — Shamir share holder; single permission only (a fixed,
//     minimal set, not registry-derived — see custodianPermissions)
func BuiltInRoles(tenantID string) []*Role {
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
	}

	now := time.Now().UTC()

	return []*Role{
		{
			ID:          SystemRoleID(tenantID, RoleNameSO),
			Name:        RoleNameSO,
			TenantID:    tenantID,
			Permissions: permission.RolePermissions(permission.RoleSO),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          SystemRoleID(tenantID, RoleNameAdmin),
			Name:        RoleNameAdmin,
			TenantID:    tenantID,
			Permissions: permission.RolePermissions(permission.RoleAdmin),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          SystemRoleID(tenantID, RoleNameOperator),
			Name:        RoleNameOperator,
			TenantID:    tenantID,
			Permissions: permission.RolePermissions(permission.RoleOperator),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          SystemRoleID(tenantID, RoleNameUser),
			Name:        RoleNameUser,
			TenantID:    tenantID,
			Permissions: permission.RolePermissions(permission.RoleUser),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          SystemRoleID(tenantID, RoleNameAuditor),
			Name:        RoleNameAuditor,
			TenantID:    tenantID,
			Permissions: permission.RolePermissions(permission.RoleAuditor),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          SystemRoleID(tenantID, RoleNameCustodian),
			Name:        RoleNameCustodian,
			TenantID:    tenantID,
			Permissions: custodianPermissions(),
			System:      true,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}
}

// systemRoleNames is the set of role names that denote a system role.
//
// DERIVED from BuiltInRoles and OwnerRole rather than written out a second
// time: a role added to either constructor becomes immutable without an edit
// here, so the two statements of "which roles are built in" cannot drift
// apart. A hand-maintained duplicate list is the shape that produced this
// defect in the first place.
//
// Built lazily rather than in a package initializer. BuiltInRoles resolves its
// permissions through the global permission registry, and an initializer would
// bind this set to whatever that registry happened to hold at package-init
// time — an ordering dependency this predicate does not need, since it reads
// only the names.
var systemRoleNames = sync.OnceValue(func() map[string]struct{} {
	builtIn := BuiltInRoles(tenant.SystemTenantID)
	names := make(map[string]struct{}, len(builtIn)+1)
	for _, r := range builtIn {
		names[r.Name] = struct{}{}
	}
	names[OwnerRole(tenant.SystemTenantID).Name] = struct{}{}
	return names
})

// IsSystemRoleName reports whether name denotes a system role — one of the six
// canonical FIPS 140-3 / PCI-DSS roles or Owner.
//
// This is the sole authority for the Role.System flag: CreateRole DERIVES the
// flag from the name it is given and CreateRoleRequest carries no System field
// at all. That is deliberate and is the security property, not an omission. An
// API-settable System flag would let any caller holding roles:create mint a
// role nothing can ever delete — a privilege-escalation surface offered in
// exchange for nothing, since the only roles that need the flag are the ones
// this package constructs itself.
//
// Immutability is keyed on the NAME, in every tenant, and deliberately NOT on
// the identifier. Two reasons:
//
//   - A cluster seeded before the move off UUIDv5 holds its system roles under
//     legacy identifiers (see SystemRoleID). Keying on the identifier would
//     leave exactly those roles mutable — the oldest deployments, the ones
//     with the most to lose.
//   - Role names are unique within a tenant, so "the role named admin in
//     tenant T" is a stable designation while an identifier is not.
//
// The consequence is that the canonical names are RESERVED in every tenant: a
// caller who creates a role named "admin" gets an immutable one, holding
// whatever permissions they asked for. That is the intended reading — the
// names carry meaning to the RBAC authorizer, which resolves the SO deny-list
// and the strict-separation-of-duties Owner strip through them.
func IsSystemRoleName(name string) bool {
	_, ok := systemRoleNames()[name]
	return ok
}

// canonicalSystemRole returns the role a system name MUST denote in the given
// tenant — the constructor-defined record, with its deterministic id and its
// fixed permission set — or nil when the name is not a system role name.
func canonicalSystemRole(tenantID, name string) *Role {
	for _, r := range BuiltInRoles(tenantID) {
		if r.Name == name {
			return r
		}
	}
	if owner := OwnerRole(tenantID); owner.Name == name {
		return owner
	}
	return nil
}

// requireCanonicalSystemRole refuses any create under a reserved name that is
// not the canonical role that name denotes.
//
// Deriving System from the name alone is not enough on its own. A caller
// holding roles:create could otherwise persist a role named "so" with a random
// id and one unrelated permission, and the derivation would make that record
// UNDELETABLE — squatting the unique name that bootstrap needs and that the
// RBAC authorizer resolves through, with no way to remove it. The immutability
// added for go-qrdb#17 would have handed an attacker a better weapon than the
// mutable roles it fixed, which is the blast-radius trap: a fix that opens a
// worse failure mode than the defect.
//
// Both halves of the identity are checked because docs/firstboot/architecture.md
// makes both normative: each built-in "gets a fixed UUIDv8 (SHA-256) derived
// from its name, so role identity is stable across clusters", and "the six
// built-in roles are fixed". Requiring the id alone would still admit a
// canonical-id role carrying arbitrary grants.
//
// Nothing is lost by being strict: both seeding paths — pkg/bootstrap's
// seedTenantRoles and pkg/firstboot — already pass exactly the constructor's
// id and permissions, so the only create this refuses is one that would have
// produced a malformed built-in. A caller who reconstructs the canonical role
// exactly is admitted, because that create is indistinguishable from seeding.
func requireCanonicalSystemRole(tenantID string, req *CreateRoleRequest) error {
	canonical := canonicalSystemRole(tenantID, req.Name)
	if canonical == nil {
		// Unreachable while systemRoleNames is derived from the same two
		// constructors; refuse rather than fall through to a permissive path
		// if that ever stops being true.
		return &ReservedRoleNameError{
			Name:     req.Name,
			TenantID: tenantID,
			Detail:   "reserved name denotes no canonical role",
		}
	}
	if req.ID != canonical.ID {
		return &ReservedRoleNameError{
			Name:     req.Name,
			TenantID: tenantID,
			Detail:   "a system role must be created with its canonical deterministic id",
		}
	}
	if !permissionSetsEqual(req.Permissions, canonical.Permissions) {
		return &ReservedRoleNameError{
			Name:     req.Name,
			TenantID: tenantID,
			Detail:   "a system role must be created with exactly its canonical permission set",
		}
	}
	return nil
}

// permissionSetsEqual compares two permission lists as SETS: order carries no
// meaning in a grant, and a repeated permission grants nothing extra.
func permissionSetsEqual(a, b []permission.Permission) bool {
	as := make(map[permission.Permission]struct{}, len(a))
	for _, p := range a {
		as[p] = struct{}{}
	}
	bs := make(map[permission.Permission]struct{}, len(b))
	for _, p := range b {
		bs[p] = struct{}{}
	}
	if len(as) != len(bs) {
		return false
	}
	for p := range as {
		if _, ok := bs[p]; !ok {
			return false
		}
	}
	return true
}

// OwnerRole returns the standard-posture Owner role for the given tenant.
// Owner holds the full operational permission set (permission.All()) and is
// the convenience superuser used when separation of duties is NOT enforced.
//
// It is deliberately NOT part of BuiltInRoles: it is seeded and assigned to
// the bootstrap admin only under the standard (non-strict) posture by
// pkg/firstboot, and the RBAC authorizer voids any Owner grant when strict
// separation of duties is active (FIPS 140-3 mode or
// auth.separation_of_duties=true). An empty tenantID defaults to
// SystemTenantID to match BuiltInRoles behavior.
func OwnerRole(tenantID string) *Role {
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
	}
	now := time.Now().UTC()
	return &Role{
		ID:          SystemRoleID(tenantID, RoleNameOwner),
		Name:        RoleNameOwner,
		TenantID:    tenantID,
		Permissions: ownerPermissions(),
		System:      true,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// ownerPermissions returns the permission set for the Owner role: every
// registered permission. This breadth is acceptable ONLY because the Owner
// role is confined to the standard (non-compliance) RBAC posture — see
// OwnerRole and RoleNameOwner.
func ownerPermissions() []permission.Permission {
	return permission.All()
}

// SystemRoleID returns the canonical deterministic UUID for any named
// system role in the given tenant. It is used by the RBAC authorizer for
// fast identity checks (e.g., the SO deny-list match by deterministic role
// ID) and by the bootstrap flow to grant the canonical Admin@system role
// with a stable cluster-wide identifier. An empty tenantID defaults to
// SystemTenantID to match BuiltInRoles behavior.
//
// A cluster seeded before the move off UUIDv5 still holds its system roles
// under their legacy identifiers; seeding is name-unique, so those records
// are never replaced by ones keyed on the value returned here. Code that
// must address the role AS PERSISTED — granting it to a user, or deciding
// whether an existing grant denotes it — resolves through the role store
// with ResolveSystemRoleID or SystemRoleIDs instead of calling this
// function directly.
func SystemRoleID(tenantID, roleName string) string {
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
	}
	return deterministicUUID(tenantID, roleName)
}

// systemRoleNamespace domain-separates role identifiers from every other
// SHA-256 derivation in the system. UUIDv8 leaves the derivation entirely
// to the application (RFC 9562 §5.8), so unlike a v3/v5 namespace this is
// hashed as a literal prefix rather than as sixteen namespace bytes.
const systemRoleNamespace = "go-qrdb:role-namespace/v8/"

// deterministicUUID derives the stable identifier of a named role in a
// tenant as an RFC 9562 §5.8 UUIDv8 over SHA-256. UUIDv8 is the version
// reserved for custom, application-defined derivations, which is what a
// name-based UUID over an approved hash is: RFC 4122 fixes v3 to MD5 and
// v5 to SHA-1, neither of which exists under FIPS 140-3, so no versioned
// name-based UUID can carry a SHA-256 derivation.
//
// The result keeps the 36-character UUID text shape, so no caller, index
// key, or wire format reformats. It is NOT the value the SHA-1 derivation
// produced for the same inputs — see SystemRoleID for how already-seeded
// clusters keep resolving.
//
// tenantID and name are length-prefixed rather than merely joined so that
// the encoding is injective: without framing, tenant "a/b" with role "c"
// and tenant "a" with role "b/c" would hash identically and two distinct
// roles would collide on one identifier.
func deterministicUUID(tenantID, name string) string {
	buf := make([]byte, 0, len(systemRoleNamespace)+len(tenantID)+len(name)+16)
	buf = append(buf, systemRoleNamespace...)
	buf = strconv.AppendInt(buf, int64(len(tenantID)), 10)
	buf = append(buf, ':')
	buf = append(buf, tenantID...)
	buf = strconv.AppendInt(buf, int64(len(name)), 10)
	buf = append(buf, ':')
	buf = append(buf, name...)

	sum := sha256.Sum256(buf)

	var u uuid.UUID
	copy(u[:], sum[:len(u)])
	u[6] = (u[6] & 0x0f) | 0x80 // version 8
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 9562 variant (10xx)
	return u.String()
}

// custodianPermissions returns the permission set for the Custodian built-in role.
// Custodians hold a single Shamir share and can ONLY submit that share during
// barrier unseal or tenant barrier operations. All other operations are denied
// (FIPS 140-3 AS10.06).
func custodianPermissions() []permission.Permission {
	return []permission.Permission{
		// Node barrier unseal share (ScopeCross: only the system tenant's
		// Custodian may submit to the node barrier's accumulator).
		permission.BarrierUnsealShare,
		// Tenant SO operations (ScopeTenantOnly: a tenant custodian may claim
		// their share and submit seal votes for their OWN tenant's barrier).
		permission.TenantBarrierClaimShare,
		permission.TenantBarrierSealVote,
	}
}

// validatePermissions checks that every permission in the slice is either
// registered in the global permission registry or a whole-segment wildcard
// (e.g. "keys:*"). Wildcards are admitted because the RBAC authorizer expands
// them against concrete resource:action checks at authorization time.
func validatePermissions(perms []permission.Permission) error {
	for _, p := range perms {
		if permission.IsWildcard(p) {
			continue
		}
		if !permission.IsRegistered(p) {
			return &InvalidPermissionError{Permission: string(p)}
		}
	}
	return nil
}

// marshalRole serializes a role for storage.
//
// It clears RetiredPermissions first, and it is the ONLY path that writes a
// role record, so that field cannot reach the store. That matters because
// GetRole populates it on read and UpdateRole writes back the very value
// GetRole returned: a plain json.Marshal at either call site would persist a
// snapshot of one build's registry into the record, and the next build would
// then read stale drift back out as if it were stored grant data.
func marshalRole(r *Role) ([]byte, error) {
	stored := *r
	stored.RetiredPermissions = nil
	return json.Marshal(&stored)
}

// primaryKey builds the primary record key for a role ID.
func primaryKey(id string) string {
	return keyPrefixPrimary + id
}

// tenantIndexKey builds the tenant index key for a role within a tenant.
func tenantIndexKey(tenantID, roleID string) string {
	return keyPrefixTenant + tenantID + "/" + roleID
}

// nameIndexKey builds the role name secondary index key.
func nameIndexKey(tenantID, name string) string {
	return keyPrefixName + tenantID + "/" + name
}
