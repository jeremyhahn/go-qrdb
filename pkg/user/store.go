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

package user

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

const (
	// keyPrefixPrimary is the prefix for user primary records keyed by ID.
	keyPrefixPrimary = "__sys:users/id/"

	// keyPrefixTenant is the prefix for the tenant-to-user index.
	keyPrefixTenant = "__sys:users/tenant/"

	// keyPrefixUsername is the prefix for the username secondary index.
	keyPrefixUsername = "__sys:user_idx/username/"

	// keyPrefixEmail is the prefix for the email secondary index — the
	// canonical login-key index. Tenant-scoped, exactly like username.
	keyPrefixEmail = "__sys:user_idx/email/"

	// keyPrefixCertThumbprint is the prefix for the mTLS SPKI-thumbprint
	// secondary index (thumbprint -> user ID).
	keyPrefixCertThumbprint = "__sys:user_idx/cert_spki/"

	// defaultPageSize is used when ListUsersRequest.PageSize is zero.
	defaultPageSize = 100

	// compensationTimeout bounds index rollback after a failed update. The
	// rollback deliberately outlives the request's own context (see UpdateUser),
	// so it needs a deadline of its own: without one a wedged store would hang a
	// caller that has already given up.
	compensationTimeout = 5 * time.Second

	// reconcileTimeout bounds reconcileAmbiguousCreate's read-back after a
	// PutAllIfAllUnchanged call whose outcome is unknown. Same reasoning as
	// compensationTimeout: the read-back deliberately outlives the request's
	// own context (a done ctx is the common reason the outcome went
	// unknown in the first place), so it needs a deadline of its own. It is
	// also the default RETRY window: cmd/qrdbd's boot-time stores read via
	// StaleRead (local applied state, not linearizable ReadIndex), so a
	// proposal that committed but lost its response may not be locally
	// visible for a brief window while apply catches up — see
	// reconcileAmbiguousCreate's doc.
	reconcileTimeout = 5 * time.Second

	// defaultReconcilePollInterval is the default spacing between
	// reconcileAmbiguousCreate's retries. Short relative to reconcileTimeout:
	// a real local-apply catch-up is expected to resolve in single-digit
	// milliseconds, so this trades a few wasted reads for not adding
	// meaningful latency to the (rare) ambiguous-outcome path.
	defaultReconcilePollInterval = 10 * time.Millisecond
)

// Compile-time interface check.
var _ UserServicer = (*kvUserStore)(nil)

// kvUserStore implements UserServicer backed by a KVStore with secondary
// indexes for tenant listing and username uniqueness.
//
// User IDs are uint64 Snowflake values assigned at creation time from an
// injected dao.IDGenerator. The ID is encoded as a fixed-width 8-byte
// big-endian value everywhere it appears in a KEY so the lexical key order
// the KVStore scans in is also the numeric ID order; the same 8-byte
// big-endian encoding is stored as index VALUES (decoded with
// binary.BigEndian.Uint64).
//
// Key schema:
//
//	__sys:users/id/{be8(userID)}                          -> User JSON
//	__sys:users/tenant/{tenantID}/{be8(userID)}           -> be8(userID)
//	__sys:user_idx/username/{tenantID}/{username}         -> be8(userID)
//	__sys:user_idx/email/{tenantID}/{email}               -> be8(userID)
//	__sys:user_idx/cert_spki/{thumbprint}                 -> be8(userID)
//
// One index does NOT live here: the system-plane identity index that maps a
// user id to the tenant holding it. That entry is cross-tenant by nature and
// belongs in the system tenant's identity namespace, so it is written
// through an injected tenant.IdentityIndex (see WithIdentityIndex) rather
// than into this store — a tenant's own store must not be able to write
// outside its tenant. It is still maintained by the same functions that
// maintain the keys above, which is what keeps it in step with them.
type kvUserStore struct {
	store          kvstore.KVStore
	idgen          dao.IDGenerator
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

// NewStore creates a new KVStore-backed UserServicer. The provided kvstore
// and ID generator must not be nil. The ID generator is the coordination-free
// source of uint64 user identifiers (a dao.SnowflakeGenerator seeded with the
// node's Raft ReplicaID); there is no default because a node-unique seed is
// required to keep IDs globally unique across a cluster.
//
// Refuses construction — rather than silently degrading — when
// WithAtomicIdentity is set without WithIdentityIndex, or over a store that
// does not implement kvstore.GuardedBatchKVStore. See pkg/role's NewStore
// for the identical reasoning: both are caller configuration errors fixed
// at construction, not runtime conditions to defer.
func NewStore(store kvstore.KVStore, idgen dao.IDGenerator, opts ...Option) (UserServicer, error) {
	if store == nil {
		return nil, &StoreError{Op: "new", Detail: "kvstore must not be nil"}
	}
	if idgen == nil {
		return nil, &StoreError{Op: "new", Detail: "id generator must not be nil"}
	}
	s := &kvUserStore{store: store, idgen: idgen}
	for _, opt := range opts {
		opt(s)
	}
	if s.atomicIdentity {
		if s.identity == nil {
			return nil, &StoreError{Op: "new", Detail: "WithAtomicIdentity requires WithIdentityIndex"}
		}
		// A multi-guard capable store is required, not merely a
		// GuardedBatchKVStore: createUserAtomic guards the identity claim,
		// the username index, and (when present) the email index together,
		// which a single-guard store cannot honour for every request shape.
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

// WithQuotaChecker returns a new kvUserStore with the given quota.Checker
// applied. Passing nil disables quota enforcement.
func (s *kvUserStore) WithQuotaChecker(checker quota.Checker) *kvUserStore {
	s.quotaChecker = checker
	return s
}

// CreateUser creates a new user with a store-assigned Snowflake ID. It
// validates the request, checks username uniqueness within the tenant via the
// secondary index, and atomically writes the primary record plus both indexes.
func (s *kvUserStore) CreateUser(ctx context.Context, req *CreateUserRequest) (created *User, retErr error) {
	if req == nil {
		return nil, &ValidationError{Field: "request", Reason: "must not be nil"}
	}
	if req.Username == "" {
		return nil, &ValidationError{Field: "username", Reason: "must not be empty"}
	}

	// The account's initial state and its permitted methods are both resolved
	// BEFORE any id is generated or any key claimed: they are pure validation of
	// the request, and a request that cannot produce a valid record must leave
	// the store completely untouched.
	initial, lerr := initialLifecycle(req)
	if lerr != nil {
		return nil, lerr
	}
	authMethods, aerr := NormalizeAuthMethods(req.AuthMethods)
	if aerr != nil {
		return nil, aerr
	}

	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
	}

	if s.quotaChecker != nil {
		if err := s.quotaChecker.Check(ctx, tenantID, quota.ResourceUsers); err != nil {
			return nil, err
		}
	}

	// The id is generated FIRST because the handle claims below store it: a
	// claim must name its owner at the instant it is made, or a second caller
	// could adopt a placeholder claim as its own.
	userID := s.idgen.NextID(nil)
	idBytes := encodeID(userID)

	email := NormalizeEmail(req.Email)
	// A missing login key must be DECLARED. Without this an ordinary user could
	// be created with no entry in the index logins resolve through — succeeding,
	// and unreachable by the key the whole stack above keys on. Whitespace-only
	// input normalizes to empty and is refused identically.
	if email == "" && !req.WithoutEmailLoginKey {
		return nil, &ValidationError{
			Field:  "email",
			Reason: "must not be empty; set WithoutEmailLoginKey to create a user with no email login key",
		}
	}
	usernameKey := usernameIndexKey(tenantID, req.Username)
	emailKey := ""
	if email != "" {
		emailKey = emailIndexKey(tenantID, email)
	}

	if s.atomicIdentity {
		return s.createUserAtomic(ctx, req, tenantID, userID, idBytes, usernameKey, emailKey, email, initial, authMethods)
	}

	// The handle claims below WRITE index entries, so the id claim must settle
	// first: a refused id claim has to leave the store untouched, and claiming
	// handles ahead of it would leave an index entry naming a user that was
	// never created (caught by TestRoutingUserService_ARefusedClaimWritesNothing).
	// CLAIM the id before anything is written. A conflict here means the
	// Snowflake generator produced an id another tenant already holds — a
	// node-seed collision — and refusing before the first write keeps a
	// contested id from ever naming two records.
	claimToken, cerr := s.recordIdentity(ctx, userID, tenantID)
	if cerr != nil {
		return nil, &StoreError{Op: "create", Detail: "claim identity", Err: cerr}
	}

	// EVERY failure after the claim releases it, as a deferred compensation
	// on the named return so a write added later is covered by construction.
	// A user id is generated rather than chosen, so a leaked claim costs less
	// here than it does for a role — but a claim with no record still blocks
	// that id forever, and the record does not exist for DeleteUser to reach.
	// written records the keys this call actually put, in the order the
	// rollback must undo them: secondary indexes first, the primary last.
	// claimed holds the handle keys this call has reserved; written holds the
	// keys it has Put. Both are declared BEFORE the deferred rollback and
	// appended to afterwards, so a write or claim added later is covered by
	// construction rather than by remembering to undo it.
	var claimed []string
	var written []string
	defer func() {
		if retErr != nil {
			// Handle claims are released FIRST, matching the ordering rule for
			// `written`: secondary indexes go before the primary record, so an
			// interrupted rollback never leaves an index resolving to a record
			// that has already been deleted.
			failures := s.releaseClaims(ctx, claimed, idBytes)
			s.rollbackFailedCreate(ctx, written, userID, tenantID, claimToken)
			// A claim we could not give back outlives this call: the username or
			// email index still names a user id whose primary record was never
			// written, so the address is permanently unclaimable and resolves to
			// nothing. Reporting it as an ordinary create failure invites a retry
			// that comes back "already exists" forever.
			if len(failures) > 0 {
				retErr = &IndexCompensationError{
					Op:       "create",
					UserID:   userID,
					Cause:    retErr,
					Failures: failures,
				}
			}
		}
	}()

	// CLAIM both handles atomically, before any record exists. Claiming is what
	// makes uniqueness real under concurrency; the previous Get-then-Put let two
	// concurrent creates both observe the key absent and both proceed.
	//
	takenName, cerr := s.claimHandle(ctx, usernameKey, idBytes)
	if cerr != nil {
		return nil, &StoreError{Op: "create", Detail: "claim username index", Err: cerr}
	}
	if takenName {
		return nil, &UsernameExistsError{Username: req.Username, TenantID: tenantID}
	}
	claimed = append(claimed, usernameKey)

	if emailKey != "" {
		takenEmail, eerr := s.claimHandle(ctx, emailKey, idBytes)
		if eerr != nil {
			return nil, &StoreError{Op: "create", Detail: "claim email index", Err: eerr}
		}
		if takenEmail {
			return nil, &EmailExistsError{Email: email, TenantID: tenantID}
		}
		claimed = append(claimed, emailKey)
	}

	now := time.Now().UTC()
	user := &User{
		ID:       userID,
		Username: req.Username,
		Email:    email,
		Name:     req.Name,
		TenantID: tenantID,
		RoleIDs:  req.RoleIDs,
		// Lifecycle is the canonical and only account-state field, so this
		// is the one place in this function that decides whether the account
		// can authenticate.
		//
		// Pending arrived on main in the deferred tenant-admin enrollment seam
		// (#193) while this branch was rewriting the surrounding create path to
		// atomic claims. The conflict presented as "old Get-then-Put vs new
		// claims", and resolving it by structure alone would have dropped this
		// field silently — an invited-but-not-yet-enrolled account would come
		// back ENABLED. It now folds into Lifecycle via initialLifecycle.
		Lifecycle:   initial,
		System:      false,
		AuthMethods: authMethods,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if user.RoleIDs == nil {
		user.RoleIDs = []string{}
	}

	data, err := json.Marshal(user)
	if err != nil {
		return nil, &StoreError{Op: "create", Detail: "marshal", Err: err}
	}

	// Write primary record. Recorded LAST in `written` so the rollback removes
	// it last, keeping the record findable while its indexes are cleaned up.
	pk := primaryKey(user.ID)
	if err := s.store.Put(ctx, pk, data); err != nil {
		return nil, &StoreError{Op: "create", Detail: "put primary", Err: err}
	}
	written = append([]string{pk}, written...)

	// Write tenant index.
	tenantKey := tenantIndexKey(tenantID, user.ID)
	if err := s.store.Put(ctx, tenantKey, idBytes); err != nil {
		return nil, &StoreError{Op: "create", Detail: "put tenant index", Err: err}
	}
	written = append([]string{tenantKey}, written...)

	// The username and email index entries are NOT written here: the atomic
	// claims above already wrote them, naming this id. Re-Putting them would be
	// harmless but misleading — it would read as though the claim were only a
	// reservation, when the claim IS the index entry. They are released by
	// releaseClaims on failure.

	s.emitAudit(ctx, "create", user.ID, map[string]any{
		"username":  user.Username,
		"email":     user.Email,
		"tenantID":  tenantID,
		"role_ids":  user.RoleIDs,
		"lifecycle": string(user.Lifecycle),
	})

	return user, nil
}

// createUserAtomic commits the identity claim, the primary record, the
// tenant index, the username index, and — when the request has one — the
// email index as ONE guarded batch, so a leadership change anywhere in the
// sequence leaves either everything or nothing. It is reached for every
// create once the store is built WithAtomicIdentity; see that option's doc
// for why the batch must guard more than one key.
//
// The batch guards on the identity claim key (fencing a repeated generated
// id), the username index key (fencing a taken username), and the email
// index key when present (fencing a taken email) — all evaluated together by
// kvstore.MultiGuardedBatchKVStore, so no window exists between any two of
// those checks for a concurrent create to land in.
//
// On a guard failure this reads back the occupied key and returns the SAME
// typed error CreateUser has always returned for that conflict
// (UsernameExistsError, EmailExistsError) or the new UserIDExistsError for
// an id collision.
func (s *kvUserStore) createUserAtomic(ctx context.Context, req *CreateUserRequest, tenantID string, userID uint64, idBytes []byte, usernameKey, emailKey, email string, initial Lifecycle, authMethods []string) (*User, error) {
	guarded, ok := s.store.(kvstore.MultiGuardedBatchKVStore)
	if !ok {
		// Unreachable: NewStore refuses WithAtomicIdentity construction over a
		// store that does not implement this. Defensive rather than a panic if
		// that check is ever bypassed.
		return nil, &StoreError{Op: "create", Detail: "atomic identity requires a kvstore.MultiGuardedBatchKVStore"}
	}
	claimKV, _, err := s.identity.PrepareClaim(tenant.IdentityUser, identityID(userID), tenantID)
	if err != nil {
		return nil, &StoreError{Op: "create", Detail: "prepare claim", Err: err}
	}

	now := time.Now().UTC()
	u := &User{
		ID:          userID,
		Username:    req.Username,
		Email:       email,
		Name:        req.Name,
		TenantID:    tenantID,
		RoleIDs:     req.RoleIDs,
		Lifecycle:   initial,
		System:      false,
		AuthMethods: authMethods,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if u.RoleIDs == nil {
		u.RoleIDs = []string{}
	}

	data, err := json.Marshal(u)
	if err != nil {
		return nil, &StoreError{Op: "create", Detail: "marshal", Err: err}
	}
	pk := primaryKey(u.ID)
	tk := tenantIndexKey(tenantID, u.ID)
	guards := []kvstore.Guard{
		{Key: claimKV.Key},
		{Key: usernameKey},
	}
	writes := []kvstore.KV{
		claimKV,
		{Key: usernameKey, Value: idBytes},
		{Key: pk, Value: data},
		{Key: tk, Value: idBytes},
	}
	if emailKey != "" {
		guards = append(guards, kvstore.Guard{Key: emailKey})
		writes = append(writes, kvstore.KV{Key: emailKey, Value: idBytes})
	}

	applied, err := guarded.PutAllIfAllUnchanged(ctx, guards, writes)
	if err != nil {
		if errors.Is(err, kvstore.ErrNotApplied) {
			// A definite refusal — no leader to propose it, no transaction
			// subsystem — wrote nothing. Reconciling would only poll for a
			// write that cannot appear.
			return nil, &StoreError{Op: "create", Detail: "atomic create", Err: err}
		}
		return s.reconcileAmbiguousCreate(ctx, u, writes, err)
	}
	if !applied {
		return nil, s.diagnoseAtomicGuardFailure(ctx, req, tenantID, userID, claimKV.Key, usernameKey, emailKey, email)
	}

	s.emitAudit(ctx, "create", u.ID, map[string]any{
		"username":  u.Username,
		"email":     u.Email,
		"tenantID":  tenantID,
		"role_ids":  u.RoleIDs,
		"lifecycle": string(u.Lifecycle),
	})
	return u, nil
}

// diagnoseAtomicGuardFailure reads back each key createUserAtomic guarded to
// report the SAME typed conflict error CreateUser has always returned for
// that condition. PutAllIfAllUnchanged reports only that at least one guard
// did not hold, not which one, so this issues plain reads — outside the
// failed transaction, causing no new ambiguity — to find the occupied key.
//
// The identity claim is checked first: a caller-supplied or generator-repeated
// id is the condition go-qrdb#555's review added this guard for, and it is
// checked ahead of the pre-existing username/email conflicts so a fix to one
// class of collision does not change which error a caller already handling
// UsernameExistsError/EmailExistsError sees for that unrelated class.
//
// The read-back is POLLED within s.reconcileWindow, for the reason pkg/role's
// identical function gives: under cmd/qrdbd's boot-time StaleRead the losing
// node may not have applied the winner's keys yet.
func (s *kvUserStore) diagnoseAtomicGuardFailure(ctx context.Context, req *CreateUserRequest, tenantID string, userID uint64, claimKey, usernameKey, emailKey, email string) error {
	dctx, cancel := context.WithTimeout(ctx, s.reconcileWindow)
	defer cancel()
	var occupied error
	s.pollVisible(dctx, func() bool {
		switch {
		case s.keyPresent(dctx, claimKey):
			occupied = &UserIDExistsError{ID: userID, TenantID: tenantID}
		case s.keyPresent(dctx, usernameKey):
			occupied = &UsernameExistsError{Username: req.Username, TenantID: tenantID}
		case emailKey != "" && s.keyPresent(dctx, emailKey):
			occupied = &EmailExistsError{Email: email, TenantID: tenantID}
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
func (s *kvUserStore) keyPresent(ctx context.Context, key string) bool {
	v, err := s.store.Get(ctx, key)
	return err == nil && v != nil
}

// reconcileAmbiguousCreate handles a PutAllIfAllUnchanged error whose outcome
// is UNKNOWN per that method's own contract: the proposal may have
// committed and only the response been lost. An error marked
// kvstore.ErrNotApplied never reaches here; createUserAtomic returns it at
// once. See pkg/role's identical
// function for the full reasoning; this is its pkg/user sibling.
//
// A successful reconciliation emits the SAME create audit event the normal
// success path does: the batch committed durably either way, and skipping
// the event here would let a privileged bootstrap user exist with no audit
// trail whenever a proposal's response — not the proposal itself — was lost.
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
// the compensation contexts CreateUser's own rollback paths already use.
//
// The read-back RETRIES on s.reconcilePollInterval until s.reconcileWindow
// elapses, rather than reading once (go-qrdb#555 review, round 4): see
// pkg/role's identical reconcileAmbiguousCreate for the full reasoning —
// cmd/qrdbd's boot-time stores read via StaleRead, so a proposal that
// committed but lost its response may not be locally visible for a brief
// window while apply catches up, and a single immediate read would report a
// false failure (and skip the audit event) for a create that in fact just
// committed.
func (s *kvUserStore) reconcileAmbiguousCreate(ctx context.Context, u *User, writes []kvstore.KV, cause error) (*User, error) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.reconcileWindow)
	defer cancel()
	if !s.pollVisible(rctx, func() bool { return s.reconcileWritesMatch(rctx, writes) }) {
		return nil, &StoreError{Op: "create", Detail: "atomic create", Err: cause}
	}
	s.emitAudit(rctx, "create", u.ID, map[string]any{
		"username":       u.Username,
		"email":          u.Email,
		"tenantID":       u.TenantID,
		"role_ids":       u.RoleIDs,
		"lifecycle":      string(u.Lifecycle),
		"reconciliation": "ambiguous_response",
	})
	return u, nil
}

// pollVisible calls visible every s.reconcilePollInterval until it reports
// true or ctx is done, and reports whether it did. See pkg/role's identical
// helper: both read-backs after an atomic create read THIS node's applied
// state, which under cmd/qrdbd's boot-time StaleRead can lag the commit.
func (s *kvUserStore) pollVisible(ctx context.Context, visible func() bool) bool {
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
func (s *kvUserStore) reconcileWritesMatch(ctx context.Context, writes []kvstore.KV) bool {
	for _, w := range writes {
		got, err := s.store.Get(ctx, w.Key)
		if err != nil || !bytes.Equal(got, w.Value) {
			return false
		}
	}
	return true
}

// GetUser retrieves a user by its globally unique ID.
func (s *kvUserStore) GetUser(ctx context.Context, id uint64) (*User, error) {
	if id == 0 {
		return nil, &ValidationError{Field: "id", Reason: "must not be zero"}
	}

	data, err := s.store.Get(ctx, primaryKey(id))
	if err != nil {
		return nil, &StoreError{Op: "get", Detail: "primary lookup", Err: err}
	}
	if data == nil {
		return nil, &UserNotFoundError{ID: id}
	}

	var user User
	if err := json.Unmarshal(data, &user); err != nil {
		return nil, &StoreError{Op: "get", Detail: "unmarshal", Err: err}
	}
	return &user, nil
}

// GetUserByUsername resolves a username within a tenant via the secondary
// index, then fetches the primary record.
func (s *kvUserStore) GetUserByUsername(ctx context.Context, tenantID, username string) (*User, error) {
	if username == "" {
		return nil, &ValidationError{Field: "username", Reason: "must not be empty"}
	}
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
	}

	indexKey := usernameIndexKey(tenantID, username)
	idBytes, err := s.store.Get(ctx, indexKey)
	if err != nil {
		return nil, &StoreError{Op: "get_by_username", Detail: "index lookup", Err: err}
	}
	if idBytes == nil {
		return nil, &UserNotFoundError{Lookup: tenantID + "/" + username}
	}
	id, decErr := decodeID(idBytes)
	if decErr != nil {
		return nil, &StoreError{Op: "get_by_username", Detail: "decode index value", Err: decErr}
	}

	u, err := s.GetUser(ctx, id)
	if err != nil {
		return nil, err
	}

	// Same confirmation as the email path. The username is not the canonical
	// login key, but a stale entry resolving to a user who no longer holds the
	// name is wrong for the same reason, and leaving one path unguarded is how
	// the next reader concludes the guard is optional.
	if u.TenantID != tenantID || u.Username != username {
		return nil, &UserNotFoundError{Lookup: tenantID + "/" + username}
	}
	return u, nil
}

// GetUserByEmail resolves an email login key within a tenant via the secondary
// index, then fetches the primary record. This is the lookup a login ceremony
// performs: the factor proves possession, this resolves what the holder may do.
//
// The tenant is required rather than derived. An email is unique within a
// tenant, not globally, so a lookup without one is ambiguous by construction —
// and answering it by scanning would let a caller reach a tenant it never named.
func (s *kvUserStore) GetUserByEmail(ctx context.Context, tenantID, email string) (*User, error) {
	normalized := NormalizeEmail(email)
	if normalized == "" {
		return nil, &ValidationError{Field: "email", Reason: "must not be empty"}
	}
	if tenantID == "" {
		tenantID = tenant.SystemTenantID
	}

	idBytes, err := s.store.Get(ctx, emailIndexKey(tenantID, normalized))
	if err != nil {
		return nil, &StoreError{Op: "get_by_email", Detail: "index lookup", Err: err}
	}
	if idBytes == nil {
		return nil, &UserNotFoundError{Lookup: tenantID + "/" + normalized}
	}
	id, decErr := decodeID(idBytes)
	if decErr != nil {
		return nil, &StoreError{Op: "get_by_email", Detail: "decode index value", Err: decErr}
	}

	u, err := s.GetUser(ctx, id)
	if err != nil {
		return nil, err
	}

	// CONFIRM AGAINST THE RECORD. The index is a hint; the record is the
	// authority. A stale entry — left by a partial mutation, an unresolved
	// compensation, or a repair gap — would otherwise let a RETIRED address
	// authenticate as its former owner, which for the canonical login key is
	// the whole game. The certificate lookup has always validated its binding
	// after resolving the index; this is the same defence for the login key
	// (Codex, PR #191).
	if u.TenantID != tenantID || NormalizeEmail(u.Email) != normalized {
		return nil, &UserNotFoundError{Lookup: tenantID + "/" + normalized}
	}
	return u, nil
}

// UserByCertThumbprint resolves a user by an mTLS certificate's SPKI thumbprint
// via the secondary index. This is the mTLS-auth lookup: given a presented
// client cert, compute user.ComputeSPKIThumbprint and resolve the bound user.
// Returns UserNotFoundError when no user is bound to the thumbprint.
func (s *kvUserStore) UserByCertThumbprint(ctx context.Context, thumbprint string) (*User, error) {
	if thumbprint == "" {
		return nil, &ValidationError{Field: "thumbprint", Reason: "must not be empty"}
	}
	tenantID := tenant.FromContext(ctx)
	idBytes, err := s.store.Get(ctx, certThumbprintIndexKey(tenantID, thumbprint))
	if err != nil {
		return nil, &StoreError{Op: "get_by_cert_thumbprint", Detail: "index lookup", Err: err}
	}
	if idBytes == nil {
		// Dual-read fallback: resolve via the legacy un-scoped global key so
		// deployments predating tenant-scoping keep working. The resolved
		// user's binding is still validated below, so this cannot resolve a
		// thumbprint to a user in a foreign tenant.
		idBytes, err = s.store.Get(ctx, legacyCertThumbprintIndexKey(thumbprint))
		if err != nil {
			return nil, &StoreError{Op: "get_by_cert_thumbprint", Detail: "legacy index lookup", Err: err}
		}
	}
	if idBytes == nil {
		return nil, &UserNotFoundError{Lookup: "cert_spki/" + tenantID + "/" + thumbprint}
	}
	id, decErr := decodeID(idBytes)
	if decErr != nil {
		return nil, &StoreError{Op: "get_by_cert_thumbprint", Detail: "decode index value", Err: decErr}
	}
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return nil, err
	}
	// Enforce binding validity on the resolution path: the index entry may be
	// stale (the binding was removed) or the binding may have expired. Only a
	// currently-valid, non-expired binding resolves the identity — otherwise a
	// decommissioned or expired credential could be replayed.
	if !u.HasCertBinding(thumbprint) {
		return nil, &UserNotFoundError{Lookup: "cert_spki/" + thumbprint}
	}
	return u, nil
}

// certBindingMutationAttempts bounds the retry on a lost conditional commit in
// mutateCertBindings. Each attempt is a fresh read plus one compare-and-swap
// with no backoff, and a binding write that loses three consecutive races to
// other writers of the SAME record has a contention problem no retry count
// will fix. It matches lastAuthMergeAttempts for the same reasons.
const certBindingMutationAttempts = 3

// certBindingMutator computes the binding set to commit from the record as
// CURRENTLY COMMITTED. It returns the new set, whether anything changed (a
// false skips the write entirely, so a no-op cannot lose a race), and any
// refusal.
//
// It is handed the decoded record rather than just the slice because the
// enrolment mutator needs the record's tenant to check the identity index.
type certBindingMutator func(u *User) (bindings []CertBinding, changed bool, err error)

// mutateCertBindings applies mutate to a user's cert bindings and commits the
// result AGAINST THE RECORD THE MUTATOR SAW.
//
// Both binding verbs go through here, and both need it for the same reason.
// "Add T" and "remove T" are complete specifications: they say what to do to a
// set without depending on what the caller last saw it contain. What they must
// never do is commit the CALLER'S VIEW of the rest of the set, because that
// view is a snapshot and the rest of the set can move underneath it.
//
// Reading the record and then calling UpdateUser — which reads it again and
// guards on ITS read — is exactly that mistake, and it is not theoretical. An
// enrolment that read the set before a concurrent withdrawal committed would
// swap its stale set in cleanly afterwards, putting the withdrawn credential
// back on an account whose operator had been told the withdrawal succeeded
// (go-qrdb#518). Passing base straight through to updateUserAt makes that a
// LOST swap instead.
//
// A lost swap is then RETRIED rather than reported, on the same argument
// mergeLastAuth makes: there is nothing stale for the caller to reconcile,
// because re-reading and re-applying "add T" or "remove T" to the record that
// actually won is always correct. The bound is small and there is no sleep —
// re-reading is the only thing that can change the outcome.
func (s *kvUserStore) mutateCertBindings(ctx context.Context, op string, userID uint64, mutate certBindingMutator) (*User, error) {
	if _, ok := s.store.(kvstore.AtomicKVStore); !ok {
		return nil, &StoreError{Op: op, Detail: "conditional commit", Err: ErrStoreNotAtomic}
	}
	if userID == 0 {
		return nil, &ValidationError{Field: "id", Reason: "must not be zero"}
	}

	var lastConflict error
	for attempt := 0; attempt < certBindingMutationAttempts; attempt++ {
		base, err := s.store.Get(ctx, primaryKey(userID))
		if err != nil {
			return nil, &StoreError{Op: op, Detail: "primary lookup", Err: err}
		}
		if base == nil {
			return nil, &UserNotFoundError{ID: userID}
		}
		existing := &User{}
		if uerr := json.Unmarshal(base, existing); uerr != nil {
			return nil, &StoreError{Op: op, Detail: "unmarshal", Err: uerr}
		}

		// A record whose deletion has BEGUN accepts no binding write in either
		// direction. Enrolment is the obvious one — a certificate bound after
		// the deletion cascade swept the index is an orphan credential by
		// construction — but withdrawal is refused for the plainer reason that
		// a terminal record accepts no edits at all, and the cascade has
		// already removed the index entries a withdrawal would maintain.
		//
		// updateUserAt refuses it too; checking here as well keeps the refusal
		// a guaranteed no-op and gives it before the mutator runs.
		if existing.Lifecycle.Terminal() {
			return nil, &LifecycleTransitionError{
				ID:   existing.ID,
				From: existing.Lifecycle,
				To:   existing.Lifecycle,
			}
		}

		bindings, changed, merr := mutate(existing)
		if merr != nil {
			return nil, merr
		}
		if !changed {
			return existing, nil
		}

		updated, uerr := s.updateUserAt(ctx, &UpdateUserRequest{ID: userID, CertBindings: bindings}, base)
		if uerr == nil {
			return updated, nil
		}
		var lost *ConcurrentUpdateError
		if !errors.As(uerr, &lost) {
			return nil, uerr
		}
		lastConflict = uerr
	}

	// Exhausted the bound. Report the conflict rather than pretending: an
	// operator withdrawing a compromised key must never read a success that
	// did not happen.
	return nil, lastConflict
}

// AddCertBinding binds an mTLS certificate's SPKI thumbprint to the user and
// refreshes the secondary index so UserByCertThumbprint resolves it. A
// notAfterUnix > 0 sets the binding's expiry; otherwise the binding never
// expires. Returns the updated user.
func (s *kvUserStore) AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*User, error) {
	if thumbprint == "" {
		return nil, &ValidationError{Field: "thumbprint", Reason: "must not be empty"}
	}
	// Only canonical SPKI thumbprints (base64url of 32 bytes) may be stored, so
	// the identity index cannot be polluted with arbitrary or non-canonical
	// strings that a canonical-encoding lookup would never match.
	if !IsValidSPKIThumbprint(thumbprint) {
		return nil, &ValidationError{Field: "thumbprint", Reason: "must be a base64url-encoded SHA-256 SPKI thumbprint"}
	}

	return s.mutateCertBindings(ctx, "add_cert_binding", userID, func(u *User) ([]CertBinding, bool, error) {
		// A thumbprint resolves to exactly one identity within a tenant. Reject
		// binding a thumbprint that is already bound to a different user in the
		// same tenant — silently overwriting it would let a caller hijack
		// another user's mTLS identity. The index is tenant-scoped, so the
		// conflict check is performed under the user's own tenant.
		existingID, err := s.store.Get(ctx, certThumbprintIndexKey(u.TenantID, thumbprint))
		if err != nil {
			return nil, false, &StoreError{Op: "add_cert_binding", Detail: "index lookup", Err: err}
		}
		if existingID != nil {
			boundID, decErr := decodeID(existingID)
			if decErr != nil {
				return nil, false, &StoreError{Op: "add_cert_binding", Detail: "decode index value", Err: decErr}
			}
			if boundID != userID {
				return nil, false, &CertBindingConflictError{
					Thumbprint:   thumbprint,
					BoundUserID:  boundID,
					TargetUserID: userID,
				}
			}
		}
		b := CertBinding{Thumbprint: thumbprint, Kind: CertBindingSPKIThumbprint}
		if notAfterUnix > 0 {
			b.NotAfter = time.Unix(notAfterUnix, 0).UTC()
		}
		// u is a local decode of the committed record; mutating it writes
		// nothing. AddCertBinding de-duplicates by thumbprint, so re-binding an
		// existing thumbprint refreshes its expiry rather than doubling it.
		u.AddCertBinding(b)
		return u.CertBindings, true, nil
	})
}

// RemoveCertBinding withdraws the binding for thumbprint from the user: the
// record entry goes, the tenant-scoped SPKI index entry goes, and the legacy
// un-scoped entry goes with it, so neither lookup path resolves the key and a
// certificate issued for it no longer authenticates as this account.
//
// It is the counterpart of AddCertBinding and the only way to unenroll a key.
// Certificate revocation is a DIFFERENT operation on a different object:
// revocation refuses a certificate, while a binding is over the
// SubjectPublicKeyInfo and outlives every certificate issued for that key. The
// two are deliberately not coupled — a revocation performed for routine
// rotation must not silently unenroll the key it is rotating.
//
// REMOVING THE LAST BINDING WIDENS THE ACCOUNT. An empty binding set states no
// restriction, so the account returns to admitting any certificate whose CN and
// OU name it (see pkg/auth/middleware.checkCertBinding). That is a real posture
// change, which is why it is audited with last_binding_removed rather than
// being left to be discovered.
//
// It deliberately does NOT validate the thumbprint's shape. IsValidSPKIThumbprint
// is a write-side gate added after the field existed, so a record written before
// it can hold a non-canonical string; refusing to withdraw one would make
// exactly the entries that gate exists to keep out the only entries nobody can
// remove.
func (s *kvUserStore) RemoveCertBinding(ctx context.Context, userID uint64, thumbprint string) (*User, error) {
	if thumbprint == "" {
		return nil, &ValidationError{Field: "thumbprint", Reason: "must not be empty"}
	}

	updated, err := s.mutateCertBindings(ctx, "remove_cert_binding", userID, func(u *User) ([]CertBinding, bool, error) {
		next := make([]CertBinding, 0, len(u.CertBindings))
		found := false
		for _, b := range u.CertBindings {
			if b.Thumbprint == thumbprint {
				found = true
				continue
			}
			next = append(next, b)
		}
		if !found {
			// A withdrawal that removed nothing REPORTS it. Succeeding
			// vacuously would read as "the key is withdrawn" to an operator who
			// mistyped the thumbprint, while the real binding stays live.
			return nil, false, &CertBindingNotFoundError{UserID: u.ID, Thumbprint: thumbprint}
		}
		return next, true, nil
	})
	if err != nil {
		return nil, err
	}

	s.emitAudit(ctx, "remove_cert_binding", userID, map[string]any{
		"username":             updated.Username,
		"tenantID":             updated.TenantID,
		"thumbprint":           thumbprint,
		"bindings_remaining":   len(updated.CertBindings),
		"last_binding_removed": len(updated.CertBindings) == 0,
	})
	return updated, nil
}

// UpdateUser applies partial updates to an existing user. An empty
// Username means no change; a nil RoleIDs means no change. If the
// username changes, uniqueness is re-validated and the secondary index
// is updated atomically.
//
// It reads the record and then commits against THAT read (see updateUserAt).
// A caller that computed its request from a record it read itself — the
// cert-binding verbs do — must call updateUserAt with the bytes it read
// instead, or the write is guarded against a version it never saw.
func (s *kvUserStore) UpdateUser(ctx context.Context, req *UpdateUserRequest) (*User, error) {
	if req == nil {
		return nil, &ValidationError{Field: "request", Reason: "must not be nil"}
	}
	if req.ID == 0 {
		return nil, &ValidationError{Field: "id", Reason: "must not be zero"}
	}

	// A request whose ONLY mutation is LastAuth is a different kind of write
	// from every other update, and takes a different path.
	//
	// It is a high-water-mark MERGE, not a replacement: the value it commits is
	// max(stored, requested), which makes it commutative and therefore safe to
	// retry. Every other field is a replacement, where a lost race means the
	// caller's view was stale and only the caller can decide what to do about
	// it — which is why UpdateUser reports ConcurrentUpdateError rather than
	// retrying on its own.
	//
	// Handling it here matters because of WHO writes it. LastAuth is written on
	// the login path, once per successful authentication, while the ordinary
	// update path is where an administrator edits roles or an address. Sending
	// the login through the general path made a login's timestamp lost whenever
	// an admin happened to be editing the same account — and worse, silently
	// below the true high-water mark, because the loser computed its max against
	// a base the winner had already replaced (Codex, PR #196). It also drags a
	// login through the whole secondary-index machinery to move one timestamp.
	if lastAuthOnly(req) {
		return s.mergeLastAuth(ctx, req.ID, req.LastAuth.UTC())
	}

	// Read the record's RAW bytes, not just the decoded value: they are the
	// version this update commits against. Two concurrent updates of the SAME
	// user would otherwise both read it, both claim distinct new handles, both
	// delete the shared old one, and both write the primary — leaving the loser's
	// handle as a live alias resolving to a user whose canonical value is
	// different, and reserved against every future account (Codex, PR #191).
	base, err := s.store.Get(ctx, primaryKey(req.ID))
	if err != nil {
		return nil, &StoreError{Op: "update", Detail: "primary lookup", Err: err}
	}
	if base == nil {
		return nil, &UserNotFoundError{ID: req.ID}
	}
	return s.updateUserAt(ctx, req, base)
}

// updateUserAt applies req to the record held in base and COMMITS AGAINST IT.
//
// base is the exact primary-record bytes the caller computed req from, and
// splitting it out of UpdateUser is what lets a caller that read the record
// itself keep the read and the commit on one version. Re-reading here instead
// would reopen the window between the caller's read and this one: a request
// derived from a stale snapshot would then swap cleanly over whatever landed
// in between, silently reinstating the state the caller had seen. On
// CertBindings that is not an abstract lost update — it resurrects a
// credential a concurrent withdrawal had just removed (go-qrdb#518).
//
// req.ID must equal the record encoded in base; UpdateUser and the
// cert-binding verbs are the only callers and both read base by that id.
func (s *kvUserStore) updateUserAt(ctx context.Context, req *UpdateUserRequest, base []byte) (updated *User, retErr error) {
	// The identity sets are validated FIRST, before any index key is claimed or
	// released. They are pure functions of the request, so a request that
	// cannot produce a valid record must be refused while the store is still
	// unwritten.
	//
	// Deferring them into the apply block below would have left the refusal
	// dependent on the compensation path — and compensation is best-effort by
	// construction (it is itself a write that can fail, and reports
	// IndexCompensationError when it does). Trading a guaranteed no-op for a
	// probable undo is a bad trade when the check costs nothing.
	authMethods, aerr := NormalizeAuthMethods(req.AuthMethods)
	if aerr != nil {
		return nil, aerr
	}

	existing := &User{}
	if uerr := json.Unmarshal(base, existing); uerr != nil {
		return nil, &StoreError{Op: "update", Detail: "unmarshal", Err: uerr}
	}

	// The lifecycle transition is checked here, immediately after the decode and
	// still before the first index mutation, so a refused transition is also a
	// guaranteed no-op. It is validated against the state as COMMITTED — the
	// bytes this update conditionally swaps against — so a concurrent transition
	// that beat us here loses that swap rather than having this one layered on
	// top of a state it never saw.
	//
	// `existing` is a local decode: mutating it now writes nothing. Nothing is
	// persisted until the CompareAndSwap at the end of this function.
	lifecycleChanged := false
	if req.Lifecycle != "" {
		before := existing.Lifecycle
		if lerr := applyLifecycle(existing, req.Lifecycle); lerr != nil {
			return nil, lerr
		}
		lifecycleChanged = existing.Lifecycle != before
	}

	// A record whose deletion has BEGUN accepts no edits. Refusing only the
	// lifecycle transition was not enough: every other field stayed writable, so
	// a caller could still rename a deleting account, replace its roles, or bind
	// a certificate to it — and a credential bound after the deletion sweep is
	// exactly the orphan the marker exists to prevent.
	//
	// Re-asserting the state the record already holds is still permitted (it is
	// how a partially completed deletion is retried); anything that would CHANGE
	// the record is not.
	if existing.Lifecycle.Terminal() && !onlyReassertsLifecycle(req) {
		return nil, &LifecycleTransitionError{
			ID:   existing.ID,
			From: existing.Lifecycle,
			To:   existing.Lifecycle,
		}
	}

	idBytes := encodeID(existing.ID)

	// A handle change rewrites a SECONDARY INDEX BEFORE the primary record is
	// written. If the primary Put then fails, the index has already moved: the
	// retired handle no longer resolves, the new one resolves to a record whose
	// own field still holds the old value, and the new handle stays locked
	// against a future create. On the email index that is the canonical LOGIN
	// key, so the account becomes unreachable.
	//
	// Reordering does not fix it. Writing the primary first would leave a retry
	// reading a record that already carries the new handle, concluding nothing
	// changed, and never repairing the index — a silent permanent inconsistency
	// in place of a loud transient one.
	//
	// So the index mutations are COMPENSATED, the way CreateUser compensates its
	// writes: each records how to undo itself, and any failure after it — including
	// one added below later — runs the undo in reverse order. Compensation is
	// best-effort by nature (it is itself a write that can fail), which is why the
	// primary record is the last thing written: until it lands, the record still
	// describes the handles the indexes point at.
	// Compensation must NOT inherit the failed request's context. The most
	// common reason the primary write fails is that ctx was canceled or its
	// deadline expired — and undo calls made on that same ctx fail immediately,
	// so the rollback provably does not run in exactly the case that needs it.
	// context.WithoutCancel keeps the values (tenant, auth, tracing) that the
	// store's own reads depend on while dropping the cancellation, under its own
	// bounded deadline so cleanup cannot hang a caller that has already left.
	//
	// Cleanup failures are REPORTED, not swallowed. If an undo fails the store is
	// left inconsistent on the canonical login key, and a caller told only
	// "update failed" would have no way to know the account may be unreachable.
	//
	// There are two undo kinds, and on a LOST update BOTH are reconciled against
	// the record the winner committed. Neither is unconditionally safe:
	//
	//   claimedKeys  — index entries THIS call inserted. Giving one back was
	//                  documented here as "always correct on failure: nobody else
	//                  can be relying on it". That premise is FALSE for two
	//                  concurrent updates of the SAME user: both write the same
	//                  id bytes, so a winner that adds the same handle finds our
	//                  entry already present and already its own, adopts it, and
	//                  registers no claim of its own. Releasing it then deleted
	//                  an index entry the winner's committed record depends on —
	//                  reproducibly leaving a live mTLS credential resolving to
	//                  no user at all.
	//   releasedKeys — index entries THIS call removed. Putting one back is
	//                  correct only when the winner still relies on it; if the
	//                  winner legitimately retired that handle and committed its
	//                  own, restoring it recreates the loser's alias — the very
	//                  defect the conditional commit exists to prevent, arriving
	//                  through the rollback instead (caught by
	//                  TestUpdateUser_ConcurrentUpdatesOfOneUserLeaveOneHandle).
	//
	// So the rule is one rule, applied to both: KEEP what the winner needs, undo
	// what it does not. Both are held as keys rather than closures because that
	// question can only be asked of a key.
	var claimedKeys []string
	var releasedKeys []string
	defer func() {
		if retErr == nil {
			return
		}
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), compensationTimeout)
		defer cancel()

		// An UNKNOWN commit outcome stands compensation down entirely: we cannot
		// tell whether the record adopted the new handles, and either undo is
		// wrong in one of the two possible worlds.
		var unknown *CommitOutcomeUnknownError
		if errors.As(retErr, &unknown) {
			return
		}

		// Which released handles to put back depends on WHY this failed.
		//
		// A definite failure means nothing changed, so every release is undone.
		//
		// A LOST update is the subtle case, and a blanket skip was wrong. It
		// assumed the winner had retired the same handles we did — true only if
		// the winner changed them too. If A moves the email while B changes only
		// the display name, B's committed record still carries the OLD email;
		// skipping the restore then leaves an enabled account that
		// GetUserByEmail can no longer resolve. A login-availability defect
		// inside ordinary concurrent use (Codex, PR #191).
		//
		// So a lost update RECONCILES: it reads the committed record and puts
		// back exactly those handles the winner still relies on, leaving the
		// ones the winner genuinely retired alone.
		var failures []error

		// A definite failure changed nothing, so every undo applies: give back
		// what we claimed, put back what we released.
		release := claimedKeys
		restore := releasedKeys

		var lost *ConcurrentUpdateError
		if errors.As(retErr, &lost) && len(claimedKeys)+len(releasedKeys) > 0 {
			needed, cerr := s.handlesTheWinnerNeeds(cctx, req.ID)
			switch {
			case cerr != nil:
				// Undecidable. Restoring blindly is how a lost race to a DELETE
				// recreates indexes for a record that is gone; releasing blindly
				// is how it deletes one the winner is using. Touch NOTHING and
				// report — an untouched index is inspectable, a half-reconciled
				// one is not.
				release, restore = nil, nil
				failures = append(failures, cerr)
			default:
				release = excluding(claimedKeys, needed)
				restore = intersecting(releasedKeys, needed)
			}
		}

		for i := len(release) - 1; i >= 0; i-- {
			if _, err := s.releaseHandle(cctx, release[i], idBytes); err != nil {
				failures = append(failures, err)
			}
		}
		for i := len(restore) - 1; i >= 0; i-- {
			if err := s.restoreHandle(cctx, restore[i], idBytes); err != nil {
				failures = append(failures, err)
			}
		}
		if len(failures) > 0 {
			retErr = &IndexCompensationError{
				Op:       "update",
				UserID:   req.ID,
				Cause:    retErr,
				Failures: failures,
			}
		}
	}()

	usernameChanged := req.Username != "" && req.Username != existing.Username

	if usernameChanged {
		// CLAIM the new handle before releasing the old one. Claim-then-release
		// is the order that matters: a Get-then-Put check let two concurrent
		// updates both see the key absent and both take it, and releasing first
		// would leave the account reachable by neither handle if the claim then
		// failed.
		newKey := usernameIndexKey(existing.TenantID, req.Username)
		taken, cerr := s.claimHandle(ctx, newKey, idBytes)
		if cerr != nil {
			return nil, &StoreError{Op: "update", Detail: "claim username index", Err: cerr}
		}
		if taken {
			// A key already held by THIS user is not a duplicate. Two updates
			// racing the same user to the same new handle both find it taken by
			// their own id; reporting "someone else owns this" is simply false,
			// and permanent from the caller's point of view. Fall through and
			// let the conditional commit decide which one wins — registering no
			// release, because this call did not insert it (the cert path in
			// this same function already worked this way).
			ownerID, oerr := s.handleOwner(ctx, newKey)
			if oerr != nil {
				return nil, &StoreError{Op: "update", Detail: "read username index owner", Err: oerr}
			}
			if ownerID != existing.ID {
				return nil, &UsernameExistsError{Username: req.Username, TenantID: existing.TenantID}
			}
		} else {
			claimedKeys = append(claimedKeys, newKey)
		}

		// Release the old handle only once the new one is held.
		oldKey := usernameIndexKey(existing.TenantID, existing.Username)
		// GUARDED. This record's view of the old handle can be stale: another
		// update may have moved it away and a third account legitimately claimed
		// it. An unconditional Delete then evicts that owner, and this call goes
		// on to lose its CAS anyway — so the eviction is pure collateral damage.
		removed, err := s.releaseHandle(ctx, oldKey, idBytes)
		if err != nil {
			return nil, &StoreError{Op: "update", Detail: "delete old username index", Err: err}
		}
		if removed {
			releasedKeys = append(releasedKeys, oldKey)
		}

		existing.Username = req.Username
	}

	// Move the email index when the login key changes. Uniqueness is
	// re-validated first: an unchecked rewrite would let a second user take an
	// address already in use and silently repoint the index that logins resolve
	// through — i.e. take over the other account's ceremony.
	newEmail := NormalizeEmail(req.Email)
	emailChanged := newEmail != "" && newEmail != existing.Email
	if emailChanged {
		// Claim then release, as above. This is the canonical login key, so a
		// racy check here is the most damaging version of the bug: the loser of
		// a concurrent claim stays enabled with a login key resolving to another
		// account.
		newKey := emailIndexKey(existing.TenantID, newEmail)
		taken, cerr := s.claimHandle(ctx, newKey, idBytes)
		if cerr != nil {
			return nil, &StoreError{Op: "update", Detail: "claim email index", Err: cerr}
		}
		if taken {
			// Self-owned is not a duplicate — see the username branch above.
			ownerID, oerr := s.handleOwner(ctx, newKey)
			if oerr != nil {
				return nil, &StoreError{Op: "update", Detail: "read email index owner", Err: oerr}
			}
			if ownerID != existing.ID {
				return nil, &EmailExistsError{Email: newEmail, TenantID: existing.TenantID}
			}
		} else {
			claimedKeys = append(claimedKeys, newKey)
		}

		if existing.Email != "" {
			oldKey := emailIndexKey(existing.TenantID, existing.Email)
			// Guarded, for the reason on the username release above — and it
			// matters more here, because this is the login key.
			removed, err := s.releaseHandle(ctx, oldKey, idBytes)
			if err != nil {
				return nil, &StoreError{Op: "update", Detail: "delete old email index", Err: err}
			}
			if removed {
				releasedKeys = append(releasedKeys, oldKey)
			}
		}

		existing.Email = newEmail
	}

	if req.Name != "" {
		existing.Name = req.Name
	}

	if req.RoleIDs != nil {
		existing.RoleIDs = req.RoleIDs
	}
	if req.RoleBindings != nil {
		existing.RoleBindings = req.RoleBindings
	}
	if req.Attributes != nil {
		existing.Attributes = req.Attributes
	}
	if req.Factors != nil {
		existing.Factors = req.Factors
	}
	if req.AuthMethods != nil {
		existing.AuthMethods = authMethods
	}
	if req.LastAuth != nil {
		// FORWARD ONLY. A login is recorded after the ceremony completes, so two
		// logins that overlap in flight can be written out of order — and an
		// account whose last-seen time moves backwards misleads exactly the
		// people who read it: an operator auditing dormant accounts, an
		// idle-expiry policy deciding what to reap. Ignoring the older value is
		// not a lost write, it is the correct merge for a high-water mark.
		at := req.LastAuth.UTC()
		if existing.LastAuth == nil || at.After(*existing.LastAuth) {
			existing.LastAuth = &at
		}
	}

	if req.CertBindings != nil {
		// DIFF the key sets before touching the index.
		//
		// Releasing everything and re-claiming everything looks symmetrical and
		// is not: for a binding that is being KEPT, the old and new keys are the
		// same key, so it is released and then claimed. On a failed commit the
		// rollback runs the restore first — which finds the key present and
		// reports it reclaimed — and the release then deletes it. Net effect: a
		// failed no-op or purely additive cert update silently removes a still
		// valid mTLS credential from the index while the record still lists it
		// (Codex, PR #191).
		//
		// So retained keys are left alone. Only removed keys are released, and
		// only genuinely new keys are claimed, which makes the rollback
		// unambiguous because no key appears in both lists.
		retained := make(map[string]bool, len(req.CertBindings))
		for _, b := range req.CertBindings {
			retained[certThumbprintIndexKey(existing.TenantID, b.Thumbprint)] = true
		}
		held := make(map[string]bool, len(existing.CertBindings))
		for _, b := range existing.CertBindings {
			held[certThumbprintIndexKey(existing.TenantID, b.Thumbprint)] = true
		}

		for _, b := range existing.CertBindings {
			scoped := certThumbprintIndexKey(existing.TenantID, b.Thumbprint)
			if retained[scoped] {
				continue // kept: leave the index entry exactly as it is
			}
			removed, err := s.releaseHandle(ctx, scoped, idBytes)
			if err != nil {
				return nil, &StoreError{Op: "update", Detail: "delete old cert index", Err: err}
			}
			if removed {
				releasedKeys = append(releasedKeys, scoped)
			}
		}

		// The legacy un-scoped entry is only ever cleared, never claimed, so it
		// cannot collide with itself the way the scoped key did. Clearing it for
		// every binding is what migrates a deployment off the global key.
		for _, b := range existing.CertBindings {
			legacy := legacyCertThumbprintIndexKey(b.Thumbprint)
			removed, err := s.releaseHandle(ctx, legacy, idBytes)
			if err != nil {
				return nil, &StoreError{Op: "update", Detail: "delete legacy cert index", Err: err}
			}
			// Only when THIS call removed it. After migration the legacy key is
			// normally absent, and recording it here would have the rollback
			// create one that never existed.
			if removed {
				releasedKeys = append(releasedKeys, legacy)
			}
		}

		existing.CertBindings = req.CertBindings

		for _, b := range req.CertBindings {
			key := certThumbprintIndexKey(existing.TenantID, b.Thumbprint)
			// NO SKIP on the claim side, deliberately. `held` records that the
			// BINDING existed, not that its scoped index entry does — and the
			// legacy loop below clears the un-scoped key for every existing
			// binding. A user reachable only through the legacy key therefore
			// lost its only lookup entry on an update that KEPT the binding,
			// locking it out of mTLS while the record still listed the
			// certificate (Codex, PR #191).
			//
			// Claiming unconditionally is safe and is what performs the
			// migration: an entry already ours reports taken with our own id and
			// registers no release (we did not insert it), while an absent one
			// is created here. The set diff still applies on the RELEASE side,
			// which is where release-then-reclaim actually caused harm.
			// A thumbprint is an authentication credential: claim it, so two
			// users cannot both take one because each read it as absent.
			taken, cerr := s.claimHandle(ctx, key, idBytes)
			if cerr != nil {
				return nil, &StoreError{Op: "update", Detail: "claim cert index", Err: cerr}
			}
			if taken {
				boundID, oerr := s.handleOwner(ctx, key)
				if oerr != nil {
					return nil, &StoreError{Op: "update", Detail: "read cert index owner", Err: oerr}
				}
				if boundID != existing.ID {
					return nil, &CertBindingConflictError{
						Thumbprint:   b.Thumbprint,
						BoundUserID:  boundID,
						TargetUserID: existing.ID,
					}
				}
				// Already present and already OURS — this call did not insert
				// it, so it must not undo it.
				//
				// Registering a release here made a losing update delete a
				// WINNING one's credential: two concurrent updates adding the
				// same binding to the same user both end up holding the key,
				// the second is told "taken, and it is yours", and its
				// compensation CompareAndDeletes successfully because both use
				// the same id bytes. The record then carries a binding with no
				// index entry, so a freshly committed mTLS credential cannot
				// authenticate (Codex, PR #191).
				//
				// Compensation may only undo what this invocation actually did.
				continue
			}
			claimedKeys = append(claimedKeys, key)
		}
	}

	existing.UpdatedAt = time.Now().UTC()

	data, err := json.Marshal(existing)
	if err != nil {
		return nil, &StoreError{Op: "update", Detail: "marshal", Err: err}
	}

	// COMMIT conditionally on the version this call read. Losing the swap means
	// another update committed first; the deferred compensation then releases the
	// handles this call claimed, so the loser leaves nothing behind. An
	// unconditional Put here is what let both racers keep their claimed indexes.
	atomicStore, ok := s.store.(kvstore.AtomicKVStore)
	if !ok {
		return nil, &StoreError{Op: "update", Detail: "conditional commit", Err: ErrStoreNotAtomic}
	}
	swapped, serr := atomicStore.CompareAndSwap(ctx, primaryKey(existing.ID), base, data)
	switch {
	case serr != nil:
		// AMBIGUOUS, not failed. CompareAndSwap's contract says a non-nil error
		// means the outcome is UNKNOWN — the swap may have committed and only
		// the reply been lost. Compensating blindly is a coin flip on which half
		// of the record to corrupt: if the commit DID land, undoing the index
		// moves leaves the login key resolving to the old address while the
		// record carries the new one.
		//
		// Guessing is avoidable, so we do not guess: read the record back and
		// let it say which world we are in. The read is detached from ctx for
		// the usual reason — the likeliest cause of the failure is that ctx
		// ended, and a read on it would fail too.
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), compensationTimeout)
		actual, rerr := s.store.Get(rctx, primaryKey(existing.ID))
		rcancel()
		switch {
		case rerr != nil:
			// Genuinely undecidable. Compensation stands down; the residue — a
			// handle claimed by a record that may not have adopted it — is
			// reconcilable, whereas a reverted commit is data loss.
			return nil, &CommitOutcomeUnknownError{ID: existing.ID, Err: serr}
		case bytes.Equal(actual, data):
			// It landed. The indexes already match, so this is the success it
			// was — and it FALLS THROUGH to the audit path below rather than
			// returning here. Returning early meant a mutation that actually
			// committed produced no audit record, precisely during a backend
			// failure: a security-relevant gap opened by the very branch that
			// made this case succeed (Codex, PR #191).
		case bytes.Equal(actual, base):
			// It did not land, and nothing else has moved: a definite failure,
			// so the full compensation is correct and safe.
			return nil, &StoreError{Op: "update", Detail: "put primary", Err: serr}
		default:
			// Someone else committed. Release only what we claimed; the winner
			// owns the handles it retired.
			return nil, &ConcurrentUpdateError{ID: existing.ID}
		}
	case !swapped:
		return nil, &ConcurrentUpdateError{ID: existing.ID}
	}

	s.emitAudit(ctx, "update", existing.ID, map[string]any{
		"username":          existing.Username,
		"email":             existing.Email,
		"tenantID":          existing.TenantID,
		"email_changed":     emailChanged,
		"username_changed":  usernameChanged,
		"roles_changed":     req.RoleIDs != nil,
		"role_ids":          existing.RoleIDs,
		"lifecycle":         string(existing.Lifecycle),
		"lifecycle_changed": lifecycleChanged,
	})

	return existing, nil
}

// DeleteUser removes a user by ID. System users cannot be deleted.
func (s *kvUserStore) DeleteUser(ctx context.Context, id uint64) (retErr error) {
	if id == 0 {
		return &ValidationError{Field: "id", Reason: "must not be zero"}
	}

	// Read the RAW bytes: this delete commits against the version it read, so a
	// concurrent update cannot slip a new handle past it. Without that, an
	// UpdateUser that claims a new email between this read and the primary
	// delete leaves its entry pointing at a record this call then removes —
	// resolving the canonical login key to a missing user and reserving that
	// address until someone repairs it by hand (Codex, PR #191).
	base, err := s.store.Get(ctx, primaryKey(id))
	if err != nil {
		return &StoreError{Op: "delete", Detail: "primary lookup", Err: err}
	}
	if base == nil {
		return &UserNotFoundError{ID: id}
	}
	existing := &User{}
	if uerr := json.Unmarshal(base, existing); uerr != nil {
		return &StoreError{Op: "delete", Detail: "unmarshal", Err: uerr}
	}
	if existing.System {
		return &SystemUserImmutableError{ID: id}
	}

	// CLAIM the record by marking it deleting, conditionally on the version just
	// read, BEFORE the cascade removes anything.
	//
	// This is the invariant LifecycleDeleting exists for, applied to qrdb's own
	// delete rather than only to a consumer's. Until the mark lands, this call
	// has not won the record: another writer can still bind a credential or
	// change a handle, and the cascade below would then be sweeping a snapshot
	// that no longer describes the account. Marking first collapses that to one
	// question — did we win? — asked once, before any destructive step.
	//
	// It also makes the account UNAUTHENTICABLE from the first write instead of
	// from the last. Between the mark and the primary delete the record still
	// exists, and without the mark it stays fully able to log in throughout,
	// including through a credential another caller binds in that window.
	//
	// The state is terminal, so a delete that fails part way leaves an account
	// that cannot authenticate and cannot be re-enabled. That is the correct
	// direction to fail: the deletion HAS begun, and a retry of DeleteUser
	// finishes it (re-asserting the mark is a no-op). An account silently
	// restored to service by a half-finished delete would be the worse outcome.
	marked, merr := s.markDeleting(ctx, id, base, existing)
	if merr != nil {
		return merr
	}
	// Everything below commits against the MARKED version, which is now the
	// record on disk.
	base = marked

	idBytes := encodeID(id)

	// Index removals are recorded so any outcome that leaves the record ALIVE
	// can put them back.
	//
	// The compensation is DEFERRED, installed before the first removal. It used
	// to run only after the conditional delete, which meant an ordinary backend
	// error partway through — a failed username release after the tenant index
	// was already gone — returned immediately and left a live record missing
	// indexes: present, unlistable, and unreachable by whichever handles had
	// been removed by then (Codex, PR #191). Every early return is now covered
	// by construction rather than by remembering to call the restore.
	var releasedKeys []string
	var committed bool
	defer func() {
		if retErr == nil || committed || len(releasedKeys) == 0 {
			return
		}
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), compensationTimeout)
		defer cancel()

		// WHAT to restore depends on the outcome, and restoring everything is
		// wrong in two of the three cases.
		restore := releasedKeys
		var failures []error

		var unknown *CommitOutcomeUnknownError
		var lost *ConcurrentUpdateError
		switch {
		case errors.As(retErr, &unknown):
			// The delete may have committed. Putting handles back would then
			// leave them resolving to a record that is gone, and leaving them
			// off is recoverable, so touch nothing and let the caller reconcile.
			return
		case errors.As(retErr, &lost):
			// Lost to a concurrent update — which may itself have moved the
			// username or email. Restoring every released key would recreate
			// stale aliases for handles the winning record no longer owns.
			//
			// A delete claims nothing, so only the restore side is reconciled.
			if len(releasedKeys) == 0 {
				break
			}
			needed, cerr := s.handlesTheWinnerNeeds(cctx, id)
			if cerr != nil {
				restore = nil
				failures = append(failures, cerr)
			} else {
				restore = intersecting(releasedKeys, needed)
			}
		}

		for i := len(restore) - 1; i >= 0; i-- {
			if rerr := s.restoreHandle(cctx, restore[i], idBytes); rerr != nil {
				failures = append(failures, rerr)
			}
		}
		if len(failures) > 0 {
			retErr = &IndexCompensationError{Op: "delete", UserID: id, Cause: retErr, Failures: failures}
		}
	}()

	// ORDER IS LOAD-BEARING — see the equivalent note in role.DeleteRole. The
	// primary record is removed LAST of the store's writes and the identity
	// claim after it, so an interrupted delete is recoverable by a plain
	// retry: before the primary delete the record is still findable and the
	// idempotent index deletes re-run; after it, ONLY the claim can remain,
	// which is what lets tenant.ReconcileDeletedIdentity finish the job
	// instead of reporting success over leaked index entries.

	// Delete tenant index. Tracked like every other removal: it is not a unique
	// handle, but a lost delete that fails to restore it leaves a LIVE record
	// invisible to ListUsers and every tenant-prefix scan.
	tenantKey := tenantIndexKey(existing.TenantID, id)
	if err := s.store.Delete(ctx, tenantKey); err != nil {
		return &StoreError{Op: "delete", Detail: "tenant index", Err: err}
	}
	releasedKeys = append(releasedKeys, tenantKey)

	// Delete username index.
	usernameKey := usernameIndexKey(existing.TenantID, existing.Username)
	removedName, err := s.releaseHandle(ctx, usernameKey, idBytes)
	if err != nil {
		return &StoreError{Op: "delete", Detail: "username index", Err: err}
	}
	if removedName {
		releasedKeys = append(releasedKeys, usernameKey)
	}

	// Delete email index. Before the primary delete, like every other index, so
	// an interrupted delete leaves the record findable and a plain retry re-runs
	// the idempotent index removals.
	if existing.Email != "" {
		emailKey := emailIndexKey(existing.TenantID, existing.Email)
		removedEmail, err := s.releaseHandle(ctx, emailKey, idBytes)
		if err != nil {
			return &StoreError{Op: "delete", Detail: "email index", Err: err}
		}
		if removedEmail {
			releasedKeys = append(releasedKeys, emailKey)
		}
	}

	// Delete any mTLS SPKI-thumbprint index entries.
	for _, b := range existing.CertBindings {
		scoped := certThumbprintIndexKey(existing.TenantID, b.Thumbprint)
		removedScoped, err := s.releaseHandle(ctx, scoped, idBytes)
		if err != nil {
			return &StoreError{Op: "delete", Detail: "cert index", Err: err}
		}
		if removedScoped {
			releasedKeys = append(releasedKeys, scoped)
		}

		legacy := legacyCertThumbprintIndexKey(b.Thumbprint)
		removedLegacy, err := s.releaseHandle(ctx, legacy, idBytes)
		if err != nil {
			return &StoreError{Op: "delete", Detail: "legacy cert index", Err: err}
		}
		if removedLegacy {
			releasedKeys = append(releasedKeys, legacy)
		}
	}

	// Delete the primary record LAST, and CONDITIONALLY on the version this call
	// read. An unconditional delete let a concurrent update slip a newly claimed
	// handle past the snapshot above: that entry survived and resolved to a
	// record this call then removed.
	//
	// Losing means the record still exists and still owns handles this call has
	// released, so they are put back before reporting the conflict.
	atomicStore, ok := s.store.(kvstore.AtomicKVStore)
	if !ok {
		return &StoreError{Op: "delete", Detail: "conditional delete", Err: ErrStoreNotAtomic}
	}
	deleted, derr := atomicStore.CompareAndDelete(ctx, primaryKey(id), base)
	if derr != nil {
		// AMBIGUOUS, and by this point every index is already gone. Returning
		// "unknown" without looking would leave a LIVE record with no tenant,
		// username, email or cert entry — unreachable by listing or by any
		// lookup. Read back and let the record say which world we are in, the
		// same way UpdateUser does.
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), compensationTimeout)
		actual, rerr := s.store.Get(rctx, primaryKey(id))
		rcancel()
		switch {
		case rerr != nil:
			return &CommitOutcomeUnknownError{ID: id, Err: derr}
		case actual == nil:
			// It landed: the record is gone and the indexes should be too, so
			// the deferred restore must stand down.
			committed = true
			deleted = true
		case bytes.Equal(actual, base):
			// It did not land and nothing else moved: the defer puts the
			// indexes back.
			return &StoreError{Op: "delete", Detail: "primary", Err: derr}
		default:
			// The record survives in a form somebody else committed.
			return &ConcurrentUpdateError{ID: id}
		}
	}
	if !deleted {
		return &ConcurrentUpdateError{ID: id}
	}
	committed = true

	// Release the claim. A stale one would keep routing the id to a tenant
	// that no longer holds it, and the id is never reissued, so nothing later
	// would correct it. If this fails the retry path completes it
	// (routingUserService.DeleteUser).
	if err := s.forgetIdentity(ctx, id, existing.TenantID); err != nil {
		return &StoreError{Op: "delete", Detail: "identity index", Err: err}
	}

	s.emitAudit(ctx, "delete", id, map[string]any{
		"username": existing.Username,
		"tenantID": existing.TenantID,
	})

	return nil
}

// ListUsers returns a paginated list of users for the given tenant by
// scanning the tenant index prefix.
func (s *kvUserStore) ListUsers(ctx context.Context, req *ListUsersRequest) (*ListUsersResponse, error) {
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

	// Collect all user IDs for the tenant via the tenant index.
	prefix := keyPrefixTenant + tenantID + "/"
	var userIDs []uint64
	var scanErr error
	err := s.store.Scan(ctx, prefix, func(_ string, value []byte) error {
		id, decErr := decodeID(value)
		if decErr != nil {
			scanErr = decErr
			return decErr
		}
		userIDs = append(userIDs, id)
		return nil
	})
	if err != nil {
		if scanErr != nil {
			return nil, &StoreError{Op: "list", Detail: "decode tenant index value", Err: scanErr}
		}
		return nil, &StoreError{Op: "list", Detail: "scan tenant index", Err: err}
	}

	// Sort numerically for deterministic pagination.
	sort.Slice(userIDs, func(i, j int) bool { return userIDs[i] < userIDs[j] })

	total := len(userIDs)
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := start + pageSize
	if end > total {
		end = total
	}

	pageIDs := userIDs[start:end]
	users := make([]*User, 0, len(pageIDs))
	for _, id := range pageIDs {
		user, err := s.GetUser(ctx, id)
		if err != nil {
			return nil, err
		}
		users = append(users, user)
	}

	return &ListUsersResponse{
		Users:   users,
		Total:   total,
		HasMore: end < total,
	}, nil
}

// EnableUser activates a user account: the lifecycle transition to active.
func (s *kvUserStore) EnableUser(ctx context.Context, id uint64) error {
	if id == 0 {
		return &ValidationError{Field: "id", Reason: "must not be zero"}
	}

	// Read the raw bytes: this writes the WHOLE record back, so committing
	// unconditionally would silently revert a concurrent UpdateUser — including
	// an email change whose index entry has already moved, leaving the login key
	// resolving to a record that no longer carries it.
	base, err := s.store.Get(ctx, primaryKey(id))
	if err != nil {
		return &StoreError{Op: "enable", Detail: "primary lookup", Err: err}
	}
	if base == nil {
		return &UserNotFoundError{ID: id}
	}
	user := &User{}
	if uerr := json.Unmarshal(base, user); uerr != nil {
		return &StoreError{Op: "enable", Detail: "unmarshal", Err: uerr}
	}

	// EnableUser is the lifecycle transition to active, not a boolean set. That
	// matters for the one case a boolean cannot express: an account already
	// marked deleting must not be re-enabled by a request that raced the
	// deletion, and a plain boolean set would do exactly that.
	if err := applyLifecycle(user, LifecycleActive); err != nil {
		return err
	}
	user.UpdatedAt = time.Now().UTC()

	data, err := json.Marshal(user)
	if err != nil {
		return &StoreError{Op: "enable", Detail: "marshal", Err: err}
	}

	atomicStore, ok := s.store.(kvstore.AtomicKVStore)
	if !ok {
		return &StoreError{Op: "enable", Detail: "conditional commit", Err: ErrStoreNotAtomic}
	}
	swapped, serr := atomicStore.CompareAndSwap(ctx, primaryKey(id), base, data)
	switch {
	case serr != nil:
		// Ambiguous, not failed — see UpdateUser, and disambiguated the same
		// way. No index is involved here, so the only question is whether the
		// caller should treat the write as done.
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), compensationTimeout)
		actual, rerr := s.store.Get(rctx, primaryKey(id))
		rcancel()
		switch {
		case rerr != nil:
			return &CommitOutcomeUnknownError{ID: id, Err: serr}
		case bytes.Equal(actual, data):
			// It landed — fall through to the audit path below. Returning here
			// let an account state change succeed with no audit record during a
			// backend failure.
		case bytes.Equal(actual, base):
			return &StoreError{Op: "enable", Detail: "put", Err: serr}
		default:
			return &ConcurrentUpdateError{ID: id}
		}
	case !swapped:
		return &ConcurrentUpdateError{ID: id}
	}

	s.emitAudit(ctx, "enable", id, map[string]any{
		"username":  user.Username,
		"tenantID":  user.TenantID,
		"lifecycle": string(user.Lifecycle),
	})
	return nil
}

// DisableUser deactivates a user account: the lifecycle transition to disabled.
func (s *kvUserStore) DisableUser(ctx context.Context, id uint64) error {
	if id == 0 {
		return &ValidationError{Field: "id", Reason: "must not be zero"}
	}

	// Read the raw bytes: this writes the WHOLE record back, so committing
	// unconditionally would silently revert a concurrent UpdateUser — including
	// an email change whose index entry has already moved, leaving the login key
	// resolving to a record that no longer carries it.
	base, err := s.store.Get(ctx, primaryKey(id))
	if err != nil {
		return &StoreError{Op: "disable", Detail: "primary lookup", Err: err}
	}
	if base == nil {
		return &UserNotFoundError{ID: id}
	}
	user := &User{}
	if uerr := json.Unmarshal(base, user); uerr != nil {
		return &StoreError{Op: "disable", Detail: "unmarshal", Err: uerr}
	}

	// The transition to disabled, for the same reason EnableUser transitions:
	// the state, not the boolean, is what the record is. Disabling an account
	// already marked deleting is refused — the deletion is further along than
	// the disable, and letting a disable land on top of it would report a
	// success that changed nothing about the account's fate.
	if err := applyLifecycle(user, LifecycleDisabled); err != nil {
		return err
	}
	user.UpdatedAt = time.Now().UTC()

	data, err := json.Marshal(user)
	if err != nil {
		return &StoreError{Op: "disable", Detail: "marshal", Err: err}
	}

	atomicStore, ok := s.store.(kvstore.AtomicKVStore)
	if !ok {
		return &StoreError{Op: "disable", Detail: "conditional commit", Err: ErrStoreNotAtomic}
	}
	swapped, serr := atomicStore.CompareAndSwap(ctx, primaryKey(id), base, data)
	switch {
	case serr != nil:
		// Ambiguous, not failed — see UpdateUser, and disambiguated the same
		// way. No index is involved here, so the only question is whether the
		// caller should treat the write as done.
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), compensationTimeout)
		actual, rerr := s.store.Get(rctx, primaryKey(id))
		rcancel()
		switch {
		case rerr != nil:
			return &CommitOutcomeUnknownError{ID: id, Err: serr}
		case bytes.Equal(actual, data):
			// It landed — fall through to the audit path below. Returning here
			// let an account state change succeed with no audit record during a
			// backend failure.
		case bytes.Equal(actual, base):
			return &StoreError{Op: "disable", Detail: "put", Err: serr}
		default:
			return &ConcurrentUpdateError{ID: id}
		}
	case !swapped:
		return &ConcurrentUpdateError{ID: id}
	}

	s.emitAudit(ctx, "disable", id, map[string]any{
		"username":  user.Username,
		"tenantID":  user.TenantID,
		"lifecycle": string(user.Lifecycle),
	})
	return nil
}

// onlyReassertsLifecycle reports whether req asks for nothing except the
// lifecycle state the record already holds — the shape of a retried deletion
// marker, which must stay permitted on a terminal record so a partially
// completed deletion can be resumed.
//
// Like lastAuthOnly, it is an exhaustive field list: a field added to
// UpdateUserRequest without being named here would become writable on an
// account whose deletion has already begun.
func onlyReassertsLifecycle(req *UpdateUserRequest) bool {
	return req.Lifecycle != "" &&
		req.Username == "" &&
		req.Email == "" &&
		req.Name == "" &&
		req.LastAuth == nil &&
		req.RoleIDs == nil &&
		req.RoleBindings == nil &&
		req.Attributes == nil &&
		req.CertBindings == nil &&
		req.Factors == nil &&
		req.AuthMethods == nil
}

// markDeleting commits the transition to LifecycleDeleting against the exact
// bytes the caller read, and returns the marked encoding so the deletion
// cascade can commit against it in turn.
//
// It is the delete's CLAIM on the record: winning it means no other writer held
// the record at that instant, and because the state is terminal none can take
// it afterwards — UpdateUser and AddCertBinding both refuse a terminal record.
// A record already marked is accepted, which is what makes a retried delete
// idempotent.
func (s *kvUserStore) markDeleting(ctx context.Context, id uint64, base []byte, existing *User) ([]byte, error) {
	if existing.Lifecycle == LifecycleDeleting {
		// Already claimed by an earlier attempt at this delete. Nothing to
		// commit, and `base` still describes the record on disk.
		return base, nil
	}
	if err := applyLifecycle(existing, LifecycleDeleting); err != nil {
		return nil, err
	}
	existing.UpdatedAt = time.Now().UTC()

	data, merr := json.Marshal(existing)
	if merr != nil {
		return nil, &StoreError{Op: "delete", Detail: "marshal delete marker", Err: merr}
	}

	atomicStore, ok := s.store.(kvstore.AtomicKVStore)
	if !ok {
		return nil, &StoreError{Op: "delete", Detail: "mark deleting", Err: ErrStoreNotAtomic}
	}
	swapped, serr := atomicStore.CompareAndSwap(ctx, primaryKey(id), base, data)
	switch {
	case serr != nil:
		// AMBIGUOUS, and this is the CHEAPEST possible place to be ambiguous:
		// nothing has been deleted yet, so reading back to decide costs one Get
		// and either answer is actionable.
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), compensationTimeout)
		actual, rerr := s.store.Get(rctx, primaryKey(id))
		rcancel()
		switch {
		case rerr != nil:
			return nil, &CommitOutcomeUnknownError{ID: id, Err: serr}
		case bytes.Equal(actual, data):
			// It landed.
			return data, nil
		case bytes.Equal(actual, base):
			return nil, &StoreError{Op: "delete", Detail: "mark deleting", Err: serr}
		default:
			return nil, &ConcurrentUpdateError{ID: id}
		}
	case !swapped:
		return nil, &ConcurrentUpdateError{ID: id}
	}
	return data, nil
}

// lastAuthOnly reports whether req asks for nothing but a LastAuth bump.
//
// Every other mutable field must be at its zero value: a request that also
// renames the user, or moves its lifecycle, is an ordinary update and has to go
// through the index and transition machinery. The check is written as an
// exhaustive field list on purpose — a field added to UpdateUserRequest without
// being named here would silently be DROPPED by the fast path, so the list is
// the one place that has to be kept in step, and `mergeLastAuth` writes nothing
// but the timestamp.
func lastAuthOnly(req *UpdateUserRequest) bool {
	return req.LastAuth != nil &&
		req.Username == "" &&
		req.Email == "" &&
		req.Name == "" &&
		req.Lifecycle == "" &&
		req.RoleIDs == nil &&
		req.RoleBindings == nil &&
		req.Attributes == nil &&
		req.CertBindings == nil &&
		req.Factors == nil &&
		req.AuthMethods == nil
}

// lastAuthMergeAttempts bounds the retry on a lost conditional commit. The
// merge is commutative, so a retry is always correct; the bound exists so a
// pathologically contended record cannot hold a login request open forever. It
// is small because each attempt is a fresh read plus one compare-and-swap with
// no backoff, and a login that loses three consecutive races to other writers of
// the SAME user record has a contention problem no retry count will fix.
const lastAuthMergeAttempts = 3

// mergeLastAuth raises a user's LastAuth to at (a high-water-mark merge) and
// returns the resulting record.
//
// It writes NOTHING when the stored value is already at or after at. That is
// the common case for a duplicate or delayed login report, and skipping the
// write means such a report costs one read and cannot lose a race at all.
//
// On a lost conditional commit it retries rather than reporting
// ConcurrentUpdateError, because unlike every other update this one has nothing
// stale to reconcile: the value it wants to commit is a function of the request
// and whatever is stored now, so re-reading and recomputing is always correct.
// The retry is bounded and has no sleep — each attempt re-reads, which is the
// only thing that could change the outcome.
func (s *kvUserStore) mergeLastAuth(ctx context.Context, id uint64, at time.Time) (*User, error) {
	atomicStore, ok := s.store.(kvstore.AtomicKVStore)
	if !ok {
		return nil, &StoreError{Op: "record_auth", Detail: "conditional commit", Err: ErrStoreNotAtomic}
	}

	var lastConflict error
	for attempt := 0; attempt < lastAuthMergeAttempts; attempt++ {
		base, err := s.store.Get(ctx, primaryKey(id))
		if err != nil {
			return nil, &StoreError{Op: "record_auth", Detail: "primary lookup", Err: err}
		}
		if base == nil {
			return nil, &UserNotFoundError{ID: id}
		}
		existing := &User{}
		if uerr := json.Unmarshal(base, existing); uerr != nil {
			return nil, &StoreError{Op: "record_auth", Detail: "unmarshal", Err: uerr}
		}

		// The terminal guard applies HERE TOO. This path runs before
		// UpdateUser's own check — that is the point of it being a fast path —
		// so without this the one mutation that could still land on an account
		// whose deletion has begun would be the login timestamp. It would also
		// be a lie: a deleting account cannot authenticate, so a successful
		// authentication is not something that happened to it.
		if existing.Lifecycle.Terminal() {
			return nil, &LifecycleTransitionError{
				ID:   existing.ID,
				From: existing.Lifecycle,
				To:   existing.Lifecycle,
			}
		}

		// Already at or past the mark: nothing to commit, and therefore no race
		// to lose.
		if existing.LastAuth != nil && !at.After(*existing.LastAuth) {
			return existing, nil
		}

		stamp := at
		existing.LastAuth = &stamp
		existing.UpdatedAt = time.Now().UTC()

		data, merr := json.Marshal(existing)
		if merr != nil {
			return nil, &StoreError{Op: "record_auth", Detail: "marshal", Err: merr}
		}

		swapped, serr := atomicStore.CompareAndSwap(ctx, primaryKey(id), base, data)
		switch {
		case serr != nil:
			// AMBIGUOUS, exactly as in UpdateUser: a non-nil error means the
			// outcome is unknown. Read back to decide rather than guessing.
			// No index is involved here, so the only question is whether the
			// write landed.
			rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), compensationTimeout)
			actual, rerr := s.store.Get(rctx, primaryKey(id))
			rcancel()
			switch {
			case rerr != nil:
				return nil, &CommitOutcomeUnknownError{ID: id, Err: serr}
			case bytes.Equal(actual, data):
				// It landed; fall through to the audit below.
			case bytes.Equal(actual, base):
				return nil, &StoreError{Op: "record_auth", Detail: "put", Err: serr}
			default:
				// Someone else committed. Re-read and merge against theirs.
				lastConflict = &ConcurrentUpdateError{ID: id}
				continue
			}
		case !swapped:
			lastConflict = &ConcurrentUpdateError{ID: id}
			continue
		}

		s.emitAudit(ctx, "record_auth", id, map[string]any{
			"username":  existing.Username,
			"tenantID":  existing.TenantID,
			"lifecycle": string(existing.Lifecycle),
		})
		return existing, nil
	}

	// Exhausted the bound. The mark was not raised, and saying so is better than
	// pretending: the caller decides whether a login timestamp is worth another
	// attempt.
	return nil, lastConflict
}

// encodeID returns the fixed-width 8-byte big-endian encoding of a user ID.
// Big-endian preserves numeric order under the KVStore's lexical key ordering.
func encodeID(id uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], id)
	return b[:]
}

// idStringKey returns the canonical key-segment encoding of a user ID: the
// fixed-width 8-byte big-endian bytes as a string. It is used inside KEYS so
// scan order is numeric.
func idStringKey(id uint64) string {
	b := encodeID(id)
	return string(b)
}

// decodeID decodes an 8-byte big-endian user-id index value. It returns an
// IDEncodingError when the value is not exactly 8 bytes.
func decodeID(b []byte) (uint64, error) {
	if len(b) != 8 {
		return 0, &IDEncodingError{Length: len(b)}
	}
	return binary.BigEndian.Uint64(b), nil
}

// IDEncodingError is returned when a stored user-id index value does not have
// the expected fixed-width 8-byte big-endian form.
type IDEncodingError struct {
	Length int
}

// Error returns a human-readable description of the encoding failure.
func (e *IDEncodingError) Error() string {
	return "user: malformed id index value: expected 8 bytes, got " + strconv.Itoa(e.Length)
}

// primaryKey builds the primary record key for a user ID.
func primaryKey(id uint64) string {
	return keyPrefixPrimary + idStringKey(id)
}

// tenantIndexKey builds the tenant index key for a user within a tenant.
func tenantIndexKey(tenantID string, userID uint64) string {
	return keyPrefixTenant + tenantID + "/" + idStringKey(userID)
}

// usernameIndexKey builds the username secondary index key.
func usernameIndexKey(tenantID, username string) string {
	return keyPrefixUsername + tenantID + "/" + username
}

// claimHandle atomically reserves a secondary-index key for a user id.
//
// It reports taken=true when another record already holds the handle. A
// Get-then-Put would not: two callers can both observe the key absent in the
// window between the two operations and both write, so the last write wins and
// the loser is left as an enabled user whose login key resolves to somebody
// else's record — identity-index corruption, not a lost update
// (Codex review, go-qrdb PR #191; the same hazard kvstore.AtomicKVStore's own
// doc comment warns about).
//
// It FAILS CLOSED when the store cannot claim atomically. A fallback to
// Get-then-Put here would be a uniqueness guarantee that silently is not one,
// which is worse than refusing: the caller believes the handle is exclusive.
// Every production KVStore behind a user store (barrier, namespaced, service,
// database) implements AtomicKVStore.
func (s *kvUserStore) claimHandle(ctx context.Context, key string, idBytes []byte) (taken bool, err error) {
	atomicStore, ok := s.store.(kvstore.AtomicKVStore)
	if !ok {
		return false, ErrStoreNotAtomic
	}
	stored, perr := atomicStore.PutIfAbsent(ctx, key, idBytes)
	if perr != nil {
		return false, perr
	}
	return !stored, nil
}

// releaseClaims gives back handle keys this call reserved but did not commit.
//
// It uses CompareAndDelete, never Delete: a guarded create paired with an
// unguarded release is half a primitive (see kvstore.AtomicKVStore, go-qrdb#41).
// Between our claim failing and this release, another caller can legitimately
// take the key — an unguarded delete would then remove THEIR claim and leave a
// live record's handle unreserved. Comparing against our own id makes the
// release a no-op in exactly that case.
//
// It runs on a context detached from the caller's, for the reason given on
// UpdateUser: the likeliest failure is a canceled or expired context, and a
// release issued on it would not run.
//
// It RETURNS the releases that failed rather than discarding them. A claim this
// call could not give back is a login handle reserved against a user record that
// was never written: a later create for the same address sees the key taken and
// reports a duplicate, while every lookup through it resolves to nothing. That
// is unreachable-by-design state, it does not heal, and the caller is the only
// party positioned to escalate it — so CreateUser wraps these in an
// IndexCompensationError exactly as UpdateUser and DeleteUser already do.
//
// A release that removed nothing but errored NOT is not a failure: between our
// claim being abandoned and this release, another caller may legitimately hold
// the key, and comparing against our own id makes the delete a correct no-op in
// precisely that case. Only a store error means the handle may still be ours.
func (s *kvUserStore) releaseClaims(ctx context.Context, keys []string, idBytes []byte) []error {
	if len(keys) == 0 {
		return nil
	}
	atomicStore, ok := s.store.(kvstore.AtomicKVStore)
	if !ok {
		// Unreachable in practice: claiming a handle needs the same interface,
		// so a non-atomic store fails at claimHandle with ErrStoreNotAtomic and
		// never reaches here with keys to release. Reported rather than dropped
		// because "we could not release" is the one thing this function exists
		// to tell the caller.
		return []error{ErrStoreNotAtomic}
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), compensationTimeout)
	defer cancel()
	var failures []error
	for i := len(keys) - 1; i >= 0; i-- {
		if _, err := atomicStore.CompareAndDelete(cctx, keys[i], idBytes); err != nil {
			failures = append(failures, err)
		}
	}
	return failures
}

// handleOwner reports which user id currently holds a handle index key.
//
// It exists because the answer was being computed inline at three call sites
// with both errors discarded (`owner, _ := ...`), which turned a transient read
// failure or a malformed stored value into a DUPLICATE-HANDLE conflict — telling
// a caller to pick another name when the truth was that the store could not
// answer, and letting the cert path report BoundUserID: 0 as though user zero
// held the thumbprint (Codex, PR #191).
//
// A store failure is a store failure. Only a successfully read and decoded owner
// justifies a conflict error.
func (s *kvUserStore) handleOwner(ctx context.Context, key string) (uint64, error) {
	raw, err := s.store.Get(ctx, key)
	if err != nil {
		return 0, err
	}
	if raw == nil {
		// The holder released it between the failed claim and this read. Report
		// a zero owner with no error; the caller treats "not us" as a conflict,
		// which is the safe reading of a key that is contended right now.
		return 0, nil
	}
	return decodeID(raw)
}

// releaseHandle removes a handle index entry only while it still names this
// user. Delete would remove it unconditionally.
//
// The window is real and the same one the rollback guards: a record can move
// off a handle (UpdateUser) and another account can legitimately claim it before
// a caller holding a STALE read of the first record gets round to deleting what
// it believes is that record's handle. An unguarded delete then evicts the new
// owner, leaving their live account unreachable by its own unique key. Comparing
// against our own id makes the delete a no-op in exactly that case.
//
// A missing or re-owned key is NOT an error: the entry is gone or belongs to
// somebody else, which is the desired end state either way.
// It reports whether THIS call removed the entry. That fact is not incidental:
// compensation may only undo what its own invocation did, and discarding it
// meant a key that was already absent — the normal state of a legacy index after
// migration — got recorded as released and then RECREATED by a failed update's
// rollback, reversing the migration and leaving persistent lookup state the
// operation never touched (Codex, PR #191).
func (s *kvUserStore) releaseHandle(ctx context.Context, key string, idBytes []byte) (bool, error) {
	atomicStore, ok := s.store.(kvstore.AtomicKVStore)
	if !ok {
		return false, ErrStoreNotAtomic
	}
	return atomicStore.CompareAndDelete(ctx, key, idBytes)
}

// handlesTheWinnerStillNeeds filters keys this call released down to those the
// COMMITTED record still depends on.
//
// It exists because "we lost the race" does not imply "the winner retired the
// handles we retired". A losing update that changed the email may have lost to
// one that only changed a display name, and that winner's record still resolves
// through the old email — which this call has just deleted.
//
// The three outcomes are distinct and the middle one is easy to get wrong:
//
//	winner exists      -> restore the subset it still relies on
//	winner is GONE     -> restore NOTHING. The race was lost to a DELETE, and
//	                      putting handles back would leave login indexes
//	                      resolving to a record that no longer exists.
//	cannot read it     -> restore nothing and say so, so the caller reports an
//	                      unresolved compensation instead of guessing.
//
// An earlier version restored everything on any read failure, reasoning that a
// stale entry is more visible than a missing one. That is true when the record
// survives and false when it does not — and "not found" is exactly the reply a
// concurrent delete produces (Codex, PR #191).
func (s *kvUserStore) handlesTheWinnerNeeds(ctx context.Context, id uint64) (map[string]bool, error) {
	winner, err := s.GetUser(ctx, id)
	if err != nil {
		var notFound *UserNotFoundError
		if errors.As(err, &notFound) {
			// The winner was a DELETE. It needs nothing, so every claim is
			// released and no release is restored — an empty set says exactly
			// that, and is not the same as the nil an error returns.
			return map[string]bool{}, nil
		}
		return nil, err // undecidable: touch nothing, and report it
	}

	needed := map[string]bool{
		usernameIndexKey(winner.TenantID, winner.Username): true,
		// The tenant index is not a unique handle, but a surviving record needs
		// it or it vanishes from ListUsers and every tenant-prefix scan. Omitting
		// it here made a lost DELETE drop a live record from its tenant index —
		// the same class of miss as leaving it out of releasedKeys two rounds
		// earlier, now on the restore side.
		tenantIndexKey(winner.TenantID, winner.ID): true,
	}
	if winner.Email != "" {
		needed[emailIndexKey(winner.TenantID, winner.Email)] = true
	}
	for _, b := range winner.CertBindings {
		needed[certThumbprintIndexKey(winner.TenantID, b.Thumbprint)] = true
	}

	// The legacy un-scoped cert key is deliberately ABSENT from this set.
	//
	// It is a migration artefact: GetUserByCertThumbprint reads it only as a
	// fallback, when the scoped key resolves to nothing. Marking it needed for
	// every surviving binding made a loser recreate a global entry the winner
	// had migrated away from, reinstating the un-scoped lookup path for that
	// credential.
	//
	// Excluding it is safe only because the scoped key above is guaranteed to
	// cover every binding the winner still carries: whichever call inserted that
	// entry, this reconciliation keeps it — released ones are restored, claimed
	// ones are not given back. Before both undo kinds were reconciled that
	// guarantee did not hold, and dropping the legacy key here would have left a
	// live credential resolving to nothing.
	return needed, nil
}

// excluding returns the keys not present in the set, preserving order.
func excluding(keys []string, set map[string]bool) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if !set[k] {
			out = append(out, k)
		}
	}
	return out
}

// intersecting returns the keys present in the set, preserving order.
func intersecting(keys []string, set map[string]bool) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if set[k] {
			out = append(out, k)
		}
	}
	return out
}

// restoreHandle puts a released handle key back during compensation.
//
// It is CONDITIONAL, for the same reason releaseClaims is: between the moment
// UpdateUser deleted the old key and the moment this rollback runs, another
// create or update can legitimately claim it — the key was genuinely free. An
// unconditional Put would overwrite that winner's claim and leave THEIR live
// record unreachable by its own unique handle, turning our failed update into
// somebody else's outage.
//
// A key that has been re-taken is therefore a compensation FAILURE, not a
// success: the caller is told the rollback was incomplete (IndexCompensationError)
// instead of silently trading its problem for another account's.
func (s *kvUserStore) restoreHandle(ctx context.Context, key string, idBytes []byte) error {
	atomicStore, ok := s.store.(kvstore.AtomicKVStore)
	if !ok {
		return ErrStoreNotAtomic
	}
	stored, err := atomicStore.PutIfAbsent(ctx, key, idBytes)
	if err != nil {
		return err
	}
	if !stored {
		return &HandleReclaimedError{Key: key}
	}
	return nil
}

// NormalizeEmail canonicalises an email login key. Addresses are compared
// case-insensitively, so the normalized form is what both the stored record and
// its index key carry: without this, "Op@x.com" and "op@x.com" occupy two index
// slots and become two accounts for one person — and either can then be
// registered by whoever asks second.
//
// Only case and surrounding space are normalized. No local-part rewriting (dot
// stripping, plus-tag removal): those rules are provider-specific, and applying
// them here would silently merge addresses that a provider treats as distinct.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// NormalizeAuthMethods canonicalises a permitted-method set: trimmed,
// lowercased, de-duplicated, first-seen order preserved.
//
// Case IS folded because these are protocol names from a small closed
// vocabulary ("webauthn", "piv") rather than minted identifiers — and a policy
// list in which "WebAuthn" and "webauthn" are two different permissions is a
// list that will eventually deny a method it meant to allow.
func NormalizeAuthMethods(methods []string) ([]string, error) {
	return dedupeTokens(methods, "authMethods", true)
}

// dedupeTokens trims, optionally lowercases, and de-duplicates an opaque token
// list, refusing an entry that is empty once trimmed.
func dedupeTokens(in []string, field string, lower bool) ([]string, error) {
	if in == nil {
		return nil, nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for i, raw := range in {
		v := strings.TrimSpace(raw)
		if lower {
			v = strings.ToLower(v)
		}
		if v == "" {
			return nil, &ValidationError{
				Field:  field,
				Reason: "entry " + strconv.Itoa(i) + " must not be empty",
			}
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out, nil
}

// emailIndexKey builds the email secondary index key — the canonical login-key
// lookup. Tenant-scoped for the reason given on User.Email.
func emailIndexKey(tenantID, email string) string {
	return keyPrefixEmail + tenantID + "/" + NormalizeEmail(email)
}

// certThumbprintIndexKey builds the tenant-scoped mTLS SPKI-thumbprint
// secondary index key. Scoping by tenant prevents a thumbprint collision
// across tenants from resolving to the wrong identity: the same physical
// certificate (or a deliberately-crafted SPKI collision) bound in two tenants
// no longer shares one global index slot.
func certThumbprintIndexKey(tenantID, thumbprint string) string {
	return keyPrefixCertThumbprint + tenantID + "/" + thumbprint
}

// legacyCertThumbprintIndexKey builds the pre-tenant-scoping global index key.
// It is consulted only on the read path as a dual-read fallback so existing
// deployments keep resolving until the index is rewritten under the
// tenant-scoped key. New writes always use certThumbprintIndexKey.
func legacyCertThumbprintIndexKey(thumbprint string) string {
	return keyPrefixCertThumbprint + thumbprint
}
