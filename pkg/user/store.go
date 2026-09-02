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
	store        kvstore.KVStore
	idgen        dao.IDGenerator
	quotaChecker quota.Checker        // optional; nil skips quota enforcement
	audit        audit.AuditLogger    // optional; nil disables audit emission
	identity     tenant.IdentityIndex // optional; nil skips identity-index maintenance
}

// NewStore creates a new KVStore-backed UserServicer. The provided kvstore
// and ID generator must not be nil. The ID generator is the coordination-free
// source of uint64 user identifiers (a dao.SnowflakeGenerator seeded with the
// node's Raft ReplicaID); there is no default because a node-unique seed is
// required to keep IDs globally unique across a cluster.
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
		// Pending arrived on main in the deferred tenant-admin enrollment seam
		// (#193) while this branch was rewriting the surrounding create path to
		// atomic claims. The conflict presented as "old Get-then-Put vs new
		// claims", and resolving it by structure alone would have dropped this
		// field silently — an invited-but-not-yet-enrolled account would come
		// back ENABLED.
		Enabled:   !req.Pending,
		System:    false,
		CreatedAt: now,
		UpdatedAt: now,
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
		"username": user.Username,
		"email":    user.Email,
		"tenantID": tenantID,
		"role_ids": user.RoleIDs,
	})

	return user, nil
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

	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	// A thumbprint resolves to exactly one identity within a tenant. Reject
	// binding a thumbprint that is already bound to a different user in the
	// same tenant — silently overwriting it would let a caller hijack another
	// user's mTLS identity. The index is tenant-scoped, so the conflict check
	// is performed under the user's own tenant.
	existingID, err := s.store.Get(ctx, certThumbprintIndexKey(u.TenantID, thumbprint))
	if err != nil {
		return nil, &StoreError{Op: "add_cert_binding", Detail: "index lookup", Err: err}
	}
	if existingID != nil {
		boundID, decErr := decodeID(existingID)
		if decErr != nil {
			return nil, &StoreError{Op: "add_cert_binding", Detail: "decode index value", Err: decErr}
		}
		if boundID != userID {
			return nil, &CertBindingConflictError{
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
	u.AddCertBinding(b)
	return s.UpdateUser(ctx, &UpdateUserRequest{ID: userID, CertBindings: u.CertBindings})
}

// UpdateUser applies partial updates to an existing user. An empty
// Username means no change; a nil RoleIDs means no change. If the
// username changes, uniqueness is re-validated and the secondary index
// is updated atomically.
func (s *kvUserStore) UpdateUser(ctx context.Context, req *UpdateUserRequest) (updated *User, retErr error) {
	if req == nil {
		return nil, &ValidationError{Field: "request", Reason: "must not be nil"}
	}
	if req.ID == 0 {
		return nil, &ValidationError{Field: "id", Reason: "must not be zero"}
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
	existing := &User{}
	if uerr := json.Unmarshal(base, existing); uerr != nil {
		return nil, &StoreError{Op: "update", Detail: "unmarshal", Err: uerr}
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
		"username":         existing.Username,
		"email":            existing.Email,
		"tenantID":         existing.TenantID,
		"email_changed":    emailChanged,
		"username_changed": usernameChanged,
		"roles_changed":    req.RoleIDs != nil,
		"role_ids":         existing.RoleIDs,
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

// EnableUser activates a user account by setting Enabled to true.
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

	user.Enabled = true
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
		"username": user.Username,
		"tenantID": user.TenantID,
	})
	return nil
}

// DisableUser deactivates a user account by setting Enabled to false.
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

	user.Enabled = false
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
		"username": user.Username,
		"tenantID": user.TenantID,
	})
	return nil
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
