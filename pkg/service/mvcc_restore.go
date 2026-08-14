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

package service

import (
	"bytes"
	"context"
	"errors"
	"sort"
)

// ---------------------------------------------------------------------------
// Selective point-in-time restore ("PITR-lite")
//
// RestoreToRevision returns a BOUNDED set of user keys to the value they held
// at an earlier MVCC revision. It is a LOGICAL UNDO, not a Raft rewind:
// committed history is never discarded, and every change the plan contains is
// applied as an ordinary new write through the normal service write path, so
// it is Raft-replicated, index-updated, watch-notified, and authorized and
// audited exactly like any other write.
//
// The operation is deliberately hard to misuse:
//
//   - Scope is required. A request that names neither a prefix nor a key list
//     is refused (*RestoreScopeEmptyError), because the "obvious" default —
//     the whole keyspace — is the one scope no operator should reach by
//     omission.
//   - The internal key namespace is unreachable. See restoreScopeReachesInternal.
//   - The blast radius is capped at MaxRestoreKeys and the request is refused
//     whole rather than silently truncated.
//   - Reads below the compaction floor fail closed, inheriting the MVCC
//     store's own *RevisionCompactedError.
// ---------------------------------------------------------------------------

// MaxRestoreKeys is the maximum number of keys a single RestoreToRevision may
// span. The diff between the as-of state and the current state is
// materialized in memory before any write is proposed, so an unbounded prefix
// would let one request exhaust the node's heap. A scope that matches more
// keys is refused with a *RestoreScopeTooLargeError rather than truncated: a
// partially applied logical undo would leave the keyspace in a state that
// existed at no revision.
const MaxRestoreKeys = 10000

// RestoreAction names the single change a restore plan makes to one key.
type RestoreAction string

const (
	// RestoreActionRevert overwrites a key that exists both now and at the
	// target revision with the value it held at the target revision.
	RestoreActionRevert RestoreAction = "revert"

	// RestoreActionRecreate re-puts a key that existed at the target
	// revision and does not exist now.
	RestoreActionRecreate RestoreAction = "recreate"

	// RestoreActionDelete removes a key that exists now and did not exist at
	// the target revision.
	RestoreActionDelete RestoreAction = "delete"
)

// RestoreRevisionRequest describes a selective point-in-time restore.
//
// Exactly one of Prefix and Keys must be set. Which one is not a stylistic
// choice: it is the scope of the mutation, and supplying neither or both is
// refused rather than resolved to a default.
type RestoreRevisionRequest struct {
	// Revision is the target revision R to restore the scoped keys to. It
	// must be positive, at or below the store's current committed revision,
	// and above the compaction floor.
	Revision int64

	// Prefix scopes the restore to every key beginning with it. Mutually
	// exclusive with Keys. An empty Prefix does NOT mean "all keys" — a
	// request with neither scope set is refused.
	Prefix string

	// Keys scopes the restore to an explicit key list. Mutually exclusive
	// with Prefix. Duplicates are collapsed.
	Keys []string

	// DryRun computes and returns the plan without writing anything. The
	// plan a dry run returns is the plan a subsequent apply executes,
	// evaluated against the state at the time of each call.
	DryRun bool

	// Force applies the plan unconditionally, overwriting any key that has
	// been written since the plan was computed.
	//
	// The default (false) is compare-and-swap: every change is committed
	// only while the key still looks exactly as the diff observed it, and a
	// key that has moved on is left alone and reported in
	// RestoreRevisionResult.Conflicts. That default exists because a restore
	// is computed from a snapshot and applied afterwards, so between the two
	// a live writer can legitimately update a key in scope — and silently
	// discarding that writer's committed value is data loss the operator
	// never asked for. Force is for the case where the operator has decided
	// the historical value must win regardless.
	Force bool
}

// RestoreKeyChange is one entry in a restore plan: the change that would be
// (or was) applied to a single key.
//
// The value being restored is deliberately NOT carried here. A plan is
// returned to the caller, logged, and rendered by transports; echoing the
// restored payloads back would turn a plan into a bulk data export of the
// keyspace at an arbitrary historical revision. ValueBytes carries the size
// alone, which is what a caller needs to judge the blast radius.
type RestoreKeyChange struct {
	// Key is the affected key.
	Key string

	// Action is the change to apply.
	Action RestoreAction

	// FromRevision is the key's current ModRevision, or 0 when the key does
	// not currently exist (a recreate).
	FromRevision int64

	// ToRevision is the ModRevision of the version being restored, or 0 when
	// the key did not exist at the target revision (a delete).
	ToRevision int64

	// ValueBytes is the length of the value the change writes, or 0 for a
	// delete.
	ValueBytes int64
}

// RestoreRevisionResult reports the outcome of a selective restore.
type RestoreRevisionResult struct {
	// Revision is the target revision the plan was computed against.
	Revision int64

	// Changes is the plan, ordered by key. On a dry run it is the plan that
	// would be applied; on an apply it is the plan that was computed —
	// including any entry that turned out to conflict, which is reported in
	// Conflicts rather than removed from here.
	Changes []RestoreKeyChange

	// Conflicts are the plan entries that were NOT applied because the key
	// changed between the diff and the write. They are only populated when
	// the request did not set Force. Each entry carries the FromRevision the
	// plan expected, so an operator can see exactly which keys a live writer
	// moved on and re-run the restore for them if the newer value is
	// unwanted.
	Conflicts []RestoreKeyChange

	// Applied is the number of changes committed. It is 0 for a dry run.
	Applied int

	// Skipped is the number of in-scope keys whose current state already
	// matches the target revision, including keys absent in both.
	Skipped int

	// Conflicted is len(Conflicts): the number of keys that changed since the
	// plan was computed and were therefore left untouched.
	Conflicted int

	// DryRun echoes the request's DryRun flag.
	DryRun bool
}

// restoreStep pairs a plan entry with the value the apply phase needs. The
// value is internal to the service: it never leaves in the result.
type restoreStep struct {
	change RestoreKeyChange
	value  []byte
}

// restoreWriter is the write surface a restore plan is applied through. It is
// deliberately the ordinary Put/Delete service path — the same one every
// other client write takes — so restore writes go through Raft consensus and
// receive the same index maintenance and watch notification. Writing to the
// MVCC store directly, or proposing a bespoke restore command, would bypass
// all three.
type restoreWriter interface {
	// Put stores a key-value pair through Raft consensus.
	Put(ctx context.Context, key string, value []byte) error
	// Delete removes a key through Raft consensus.
	Delete(ctx context.Context, key string) error
}

// restoreAppliers dispatches a plan action to the unconditional write that
// performs it, used only when the request sets Force. Every RestoreAction the
// planner can emit has an entry; an action with no entry is a programming
// error and is reported rather than skipped.
var restoreAppliers = map[RestoreAction]func(ctx context.Context, w restoreWriter, step restoreStep) error{
	RestoreActionRevert:   applyRestorePut,
	RestoreActionRecreate: applyRestorePut,
	RestoreActionDelete:   applyRestoreDelete,
}

// applyRestorePut writes the historical value back under the key.
func applyRestorePut(ctx context.Context, w restoreWriter, step restoreStep) error {
	return w.Put(ctx, step.change.Key, step.value)
}

// applyRestoreDelete removes a key that did not exist at the target revision.
func applyRestoreDelete(ctx context.Context, w restoreWriter, step restoreStep) error {
	return w.Delete(ctx, step.change.Key)
}

// restoreCASOps dispatches a plan action to the transaction operation that
// performs it under a compare-and-swap guard. It is the default (non-Force)
// write path and mirrors restoreAppliers entry for entry.
var restoreCASOps = map[RestoreAction]func(step restoreStep) Op{
	RestoreActionRevert:   restoreOpPut,
	RestoreActionRecreate: restoreOpPut,
	RestoreActionDelete:   restoreOpDelete,
}

// restoreOpPut builds the put operation that restores a historical value.
func restoreOpPut(step restoreStep) Op {
	return Op{Type: OpPut, Key: []byte(step.change.Key), Value: step.value}
}

// restoreOpDelete builds the delete operation that removes a key which did
// not exist at the target revision.
func restoreOpDelete(step restoreStep) Op {
	return Op{Type: OpDelete, Key: []byte(step.change.Key)}
}

// restoreGuard returns the precondition that holds a change to the state the
// diff observed.
//
// One comparison covers all three actions because RestoreKeyChange.FromRevision
// is the key's observed current ModRevision and is 0 exactly when the key did
// not exist. mvcc.Evaluate treats a missing key as having ModRevision 0, so
// "ModRevision == FromRevision" reads as "still at the version I diffed"
// for a revert or a delete, and as "still absent" for a recreate — without a
// bespoke existence check.
func restoreGuard(step restoreStep) Compare {
	return Compare{
		Key:         []byte(step.change.Key),
		Target:      CompareModRevision,
		Result:      CompareEqual,
		ModRevision: step.change.FromRevision,
	}
}

// ---------------------------------------------------------------------------
// Request validation
// ---------------------------------------------------------------------------

// ValidateRestoreRequest checks everything about a restore request that can be
// decided without reading the store: the target revision is positive, the
// scope is exactly one of prefix or keys, the scope does not reach the
// internal key namespace, and the explicit key list is within MaxRestoreKeys.
//
// It is exported because every MVCCKVServicer wrapper in the write path must
// apply it to the request as the CALLER wrote it, before any namespace prefix
// is attached. Once a tenant/database prefix is prepended, "__sys:roles/x"
// has become "acme/reports/__sys:roles/x", which is an ordinary user key and
// which the inner service's own guard would therefore admit. Validating the
// unprefixed request is what keeps the internal-key refusal meaningful inside
// a namespaced view, and validating again on the fully qualified key is what
// keeps it meaningful for the system tenant, whose prefix is empty.
func ValidateRestoreRequest(req *RestoreRevisionRequest) error {
	if req == nil {
		return &RestoreNilRequestError{}
	}
	if req.Revision <= 0 {
		return &RestoreRevisionInvalidError{Revision: req.Revision}
	}

	hasPrefix := req.Prefix != ""
	hasKeys := len(req.Keys) > 0

	switch {
	case hasPrefix && hasKeys:
		return &RestoreScopeAmbiguousError{Prefix: req.Prefix, KeyCount: len(req.Keys)}
	case !hasPrefix && !hasKeys:
		return &RestoreScopeEmptyError{}
	}

	if hasPrefix {
		return validateRestorePrefix(req.Prefix)
	}
	return validateRestoreKeys(req.Keys)
}

// validateRestorePrefix refuses a prefix scope that lies inside the internal
// key namespace.
//
// The refusal is on IsInternalKey(prefix), i.e. on a prefix that BEGINS with a
// reserved prefix — "__sys:", "__sys:roles/", "ts:metrics" and so on. That is
// precisely the hole the read paths leave open: pkg/engine's listKeys and
// lookupPrefix set skipFilter when the requested prefix starts with "__sys:",
// so an explicitly internal prefix bypasses their internal-key filter and the
// caller sees system records. Restore has no equivalent escape hatch, because
// a bypass there is not a disclosure but a write: reverting "__sys:roles/…" to
// an earlier revision reinstates a revoked permission, and reverting
// "__sys:users/…" resurrects a deleted administrator.
//
// A prefix that merely SPANS internal keys without being one — "i", which
// covers "idx:metric:…", or "r", which covers "retention:…" — is a legitimate
// user prefix and is admitted here; the internal keys its range returns are
// dropped from the plan by filterRestorableKeyValues.
func validateRestorePrefix(prefix string) error {
	if IsInternalKey(prefix) {
		return &RestoreInternalKeyError{Prefix: prefix}
	}
	return nil
}

// validateRestoreKeys refuses an explicit key list that is empty-valued,
// reaches the internal key namespace, or exceeds the per-restore cap.
func validateRestoreKeys(keys []string) error {
	if len(keys) > MaxRestoreKeys {
		return &RestoreScopeTooLargeError{Limit: MaxRestoreKeys, Scope: "explicit key list"}
	}
	for i, k := range keys {
		if k == "" {
			return &RestoreEmptyKeyError{Index: i}
		}
		if IsInternalKey(k) {
			return &RestoreInternalKeyError{Key: k}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// RestoreToRevision
// ---------------------------------------------------------------------------

// RestoreToRevision returns the scoped keys to the value they held at
// req.Revision by proposing new writes. Committed history is never discarded:
// the restore moves the keyspace forward to an earlier logical state.
//
// The plan is computed first and applied second, so a request that violates
// any guard writes nothing at all. Guards, in order: the request shape
// (ValidateRestoreRequest — positive revision, exactly one scope, no internal
// keys), the target revision against the store's current revision, the
// compaction floor (inherited from the as-of read and the change scan, both of
// which fail closed with *RevisionCompactedError), and the MaxRestoreKeys cap
// applied to the union of the candidate sets.
//
// Both scopes are complete with respect to deletes. A key that existed at the
// target revision and has since been DELETED is recreated under either scope:
// an explicit Keys scope reads its historical data entry directly with
// GetAtRevision, and a Prefix scope finds it through the durable revision
// index (MVCCScanChangesQuery), which records deletes as first-class changes
// and so sees keys a live-key range scan structurally cannot. Recovering from
// a bulk delete across a prefix therefore produces a real recreate plan.
//
// Writes are compare-and-swap by default. Each change is committed only while
// the key still matches the state the diff observed; a key a live writer moved
// on in the meantime is left untouched and reported in Result.Conflicts rather
// than clobbered, and the rest of the plan still applies. Setting
// Request.Force applies every change unconditionally.
//
// The write path is the ordinary service write path — a transaction proposal
// under CAS, a Put/Delete proposal under Force — so each change is an
// independent Raft proposal and the restore is not atomic across keys. A
// failure part way through returns a *RestoreApplyError carrying the number of
// changes already committed, and re-issuing the identical request is safe: the
// plan is recomputed against the then-current state and the already applied
// changes are counted as skipped.
func (s *MVCCKVService) RestoreToRevision(ctx context.Context, req *RestoreRevisionRequest) (*RestoreRevisionResult, error) {
	if err := ValidateRestoreRequest(req); err != nil {
		return nil, err
	}

	current, err := s.CurrentRevision(ctx)
	if err != nil {
		return nil, err
	}
	if req.Revision > current {
		return nil, &RestoreRevisionFutureError{Requested: req.Revision, Current: current}
	}

	steps, skipped, err := s.planRestore(ctx, req, current)
	if err != nil {
		return nil, err
	}

	result := &RestoreRevisionResult{
		Revision: req.Revision,
		Changes:  make([]RestoreKeyChange, 0, len(steps)),
		Skipped:  skipped,
		DryRun:   req.DryRun,
	}
	for _, step := range steps {
		result.Changes = append(result.Changes, step.change)
	}

	if req.DryRun {
		return result, nil
	}

	for _, step := range steps {
		applied, err := s.applyRestoreStep(ctx, req, step)
		if err != nil {
			return nil, &RestoreApplyError{
				Key:     step.change.Key,
				Action:  step.change.Action,
				Applied: result.Applied,
				Err:     err,
			}
		}
		if !applied {
			result.Conflicts = append(result.Conflicts, step.change)
			result.Conflicted++
			continue
		}
		result.Applied++
	}

	return result, nil
}

// applyRestoreStep commits one plan entry and reports whether it was applied.
// A false return with a nil error is a CAS conflict: the key changed between
// the diff and the write, so the change was deliberately not made.
func (s *MVCCKVService) applyRestoreStep(ctx context.Context, req *RestoreRevisionRequest, step restoreStep) (bool, error) {
	if req.Force {
		apply, ok := restoreAppliers[step.change.Action]
		if !ok {
			return false, &unknownRestoreActionError{Action: step.change.Action}
		}
		if err := apply(ctx, s, step); err != nil {
			return false, err
		}
		return true, nil
	}

	buildOp, ok := restoreCASOps[step.change.Action]
	if !ok {
		return false, &unknownRestoreActionError{Action: step.change.Action}
	}

	// The existing transaction path evaluates the precondition and performs
	// the write inside one Raft-replicated state-machine apply, so no window
	// exists between the check and the write. Reading the key again here and
	// then writing would reintroduce exactly the race the guard exists to
	// close.
	txn := &TransactionService{host: s.host, shardID: s.shardID}
	resp, err := txn.Execute(ctx,
		[]Compare{restoreGuard(step)},
		[]Op{buildOp(step)},
		nil,
	)
	if err != nil {
		return false, err
	}
	return resp.Succeeded, nil
}

// unknownRestoreActionError reports a plan entry whose action has no
// registered applier. It is unreachable through the public API — the planner
// only emits actions present in restoreAppliers — and exists so a future
// action added to one map and not the other fails loudly instead of being
// silently dropped from the plan.
type unknownRestoreActionError struct {
	Action RestoreAction
}

// Error returns a human-readable description.
func (e *unknownRestoreActionError) Error() string {
	return "service: no applier registered for restore action " + string(e.Action)
}

// planRestore dispatches to the planner for the request's scope. The scope is
// already known to be exactly one of the two.
func (s *MVCCKVService) planRestore(ctx context.Context, req *RestoreRevisionRequest, current int64) ([]restoreStep, int, error) {
	if len(req.Keys) > 0 {
		return s.planRestoreKeys(ctx, req)
	}
	return s.planRestorePrefix(ctx, req, current)
}

// planRestorePrefix computes the plan for a prefix scope.
//
// The candidate key set is the union of two sources, and it takes both to be
// complete:
//
//   - the keys LIVE NOW under the prefix, from an ordinary range scan; and
//   - the keys CHANGED in (target, current] under the prefix, from the durable
//     revision index.
//
// The second is what makes a prefix restore able to undo a delete. A range
// scan — at any revision — walks the live metadata namespace, so a key that
// has been deleted is absent from it even when the scan targets a revision at
// which the key existed. The revision index records the delete itself, so the
// key is still reachable. Their union is exhaustive: a key that existed at the
// target revision either is still live (first source) or was changed after it
// (second).
//
// The historical range doubles as a value cache. It resolves every currently
// live key at the target revision in one read, so a point read is needed only
// for candidates that are no longer live — precisely the deleted-since keys
// the index contributed.
func (s *MVCCKVService) planRestorePrefix(ctx context.Context, req *RestoreRevisionRequest, current int64) ([]restoreStep, int, error) {
	start := []byte(req.Prefix)
	end := prefixEnd(start)
	if end == nil {
		// An all-0xFF prefix has no finite successor, so it cannot be turned
		// into a bounded half-open range. Refuse rather than scan with an
		// open upper bound, which would reach past the intended scope.
		return nil, 0, &RestoreUnboundedPrefixError{Prefix: req.Prefix}
	}

	tooLarge := &RestoreScopeTooLargeError{Limit: MaxRestoreKeys, Scope: "prefix " + req.Prefix}

	// Each scan requests one more than the cap so that "at the cap" and "over
	// the cap" are distinguishable without a second count-only round trip.
	asOfKVs, _, _, err := s.Range(ctx, start, end,
		WithRevision(req.Revision), WithLimit(MaxRestoreKeys+1))
	if err != nil {
		// A target below the compaction floor surfaces here as
		// *RevisionCompactedError: the as-of read fails closed and the
		// restore never reaches the write phase.
		return nil, 0, err
	}
	if len(asOfKVs) > MaxRestoreKeys {
		return nil, 0, tooLarge
	}

	currentKVs, _, _, err := s.Range(ctx, start, end, WithLimit(MaxRestoreKeys+1))
	if err != nil {
		return nil, 0, err
	}
	if len(currentKVs) > MaxRestoreKeys {
		return nil, 0, tooLarge
	}

	asOf := filterRestorableKeyValues(asOfKVs)
	live := filterRestorableKeyValues(currentKVs)

	// Keys mutated after the target revision, which is where deletes surface.
	// The scan also fails closed on the compaction floor.
	changed, truncated, err := s.scanChangedKeys(ctx, req.Revision+1, current, req.Prefix)
	if err != nil {
		return nil, 0, err
	}
	if truncated {
		return nil, 0, tooLarge
	}

	candidates := restoreCandidates(live, changed)
	if len(candidates) > MaxRestoreKeys {
		return nil, 0, tooLarge
	}

	var steps []restoreStep
	skipped := 0

	for _, key := range candidates {
		is := live[key]

		var was *KeyValue
		if is != nil {
			// The as-of range already resolved every live key at the target
			// revision. Absence from it therefore means the key did not exist
			// then — the scan considered the key and excluded it — so no
			// extra read is needed.
			was = asOf[key]
		} else {
			// Not live now, so it reached the candidate set through the
			// revision index: a key deleted since the target revision. Its
			// historical value has to be read directly.
			was, err = s.readRestoreState(ctx, key, req.Revision)
			if err != nil {
				return nil, 0, err
			}
		}

		step, skip := diffRestoreKey(key, was, is)
		if skip {
			skipped++
			continue
		}
		steps = append(steps, step)
	}

	sortRestoreSteps(steps)
	return steps, skipped, nil
}

// planRestoreKeys computes the plan for an explicit key scope with a
// per-key as-of read and a per-key current read. Duplicates in the request
// are collapsed so a key named twice is planned once.
func (s *MVCCKVService) planRestoreKeys(ctx context.Context, req *RestoreRevisionRequest) ([]restoreStep, int, error) {
	var steps []restoreStep
	skipped := 0
	seen := make(map[string]struct{}, len(req.Keys))

	for _, key := range req.Keys {
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		was, err := s.readRestoreState(ctx, key, req.Revision)
		if err != nil {
			return nil, 0, err
		}
		is, err := s.readRestoreState(ctx, key, 0)
		if err != nil {
			return nil, 0, err
		}

		step, skip := diffRestoreKey(key, was, is)
		if skip {
			skipped++
			continue
		}
		steps = append(steps, step)
	}

	sortRestoreSteps(steps)
	return steps, skipped, nil
}

// readRestoreState reads one key at revision, or at the latest revision when
// revision is 0. A key that is absent — deleted, or never written at or below
// the requested revision — yields (nil, nil): absence is a state the diff
// needs, not an error. Every other error, including the compaction-floor
// refusal, is propagated so the restore fails closed.
func (s *MVCCKVService) readRestoreState(ctx context.Context, key string, revision int64) (*KeyValue, error) {
	var kv *KeyValue
	var err error
	if revision > 0 {
		kv, err = s.GetAtRevision(ctx, key, revision)
	} else {
		kv, err = s.GetRevision(ctx, key)
	}
	if err == nil {
		return kv, nil
	}

	// Compaction is checked before absence: below the floor the store cannot
	// know whether the key existed, so treating the refusal as "absent" would
	// turn an unreadable history into a plan that deletes live keys.
	var compacted *RevisionCompactedError
	if errors.As(err, &compacted) {
		return nil, err
	}

	var notFound *KeyNotFoundError
	if errors.As(err, &notFound) {
		return nil, nil
	}
	var noRevision *RevisionNotFoundError
	if errors.As(err, &noRevision) {
		return nil, nil
	}
	return nil, err
}

// diffRestoreKey compares a key's state at the target revision (was) with its
// current state (is) and returns the change that reconciles them. The second
// return reports that the two states already agree and no write is needed;
// that includes a key absent in both.
func diffRestoreKey(key string, was, is *KeyValue) (restoreStep, bool) {
	switch {
	case was != nil && is != nil:
		if bytes.Equal(was.Value, is.Value) {
			return restoreStep{}, true
		}
		return restoreStep{
			change: RestoreKeyChange{
				Key:          key,
				Action:       RestoreActionRevert,
				FromRevision: is.ModRevision,
				ToRevision:   was.ModRevision,
				ValueBytes:   int64(len(was.Value)),
			},
			value: was.Value,
		}, false

	case was != nil:
		return restoreStep{
			change: RestoreKeyChange{
				Key:        key,
				Action:     RestoreActionRecreate,
				ToRevision: was.ModRevision,
				ValueBytes: int64(len(was.Value)),
			},
			value: was.Value,
		}, false

	case is != nil:
		return restoreStep{
			change: RestoreKeyChange{
				Key:          key,
				Action:       RestoreActionDelete,
				FromRevision: is.ModRevision,
			},
		}, false

	default:
		return restoreStep{}, true
	}
}

// filterRestorableKeyValues indexes range results by key, dropping every key
// in the internal namespace.
//
// This is the second half of the internal-key guard and it is not redundant
// with validateRestorePrefix. A legitimate user prefix can SPAN internal keys
// without being one: "i" covers "idx:metric:…" and "idx:tag:…", "r" covers
// "retention:…", "t" covers "ts:…", and "_" covers "__sys:…" and "__tbl:…".
// The MVCC range query applies no internal-key filter of its own — pkg/engine
// filters only in listKeys/lookupPrefix, not on the MVCC range path — so
// without this the single-character prefix "_" would be an unguarded route to
// rewriting the RBAC store.
//
// Since the internal namespace gained the relational prefixes, this also
// excludes table rows ("__tbl:"), secondary index entries ("__idx:"), and
// sequences ("__seq:") from a KV-level restore. That exclusion is deliberate
// and it is a correctness fix, not collateral damage: restoreWriter applies
// its plan through the ordinary Put/Delete path, which maintains the KV
// secondary index (pkg/index) and nothing else. A relational row's "__idx:"
// entries are only ever written inside the row+index compare-and-swap in
// pkg/sql/provider/relational_writer.go, so restoring a row here would have
// produced a row with no index entries: invisible to every indexed lookup and
// with its unique constraints silently unenforced, which is a worse state than
// not restoring it. Point-in-time recovery of relational data belongs to the
// snapshot-based backup/restore path, which moves the whole keyspace
// consistently.
func filterRestorableKeyValues(kvs []*KeyValue) map[string]*KeyValue {
	out := make(map[string]*KeyValue, len(kvs))
	for _, kv := range kvs {
		if kv == nil {
			continue
		}
		key := string(kv.Key)
		if IsInternalKey(key) {
			continue
		}
		out[key] = kv
	}
	return out
}

// scanChangedKeys returns the distinct keys mutated in the inclusive revision
// window [startRev, endRev] that begin with prefix, and whether the scan hit
// its limit.
//
// Keys in the internal namespace are dropped here. The change index is a
// second route into the candidate set, and it is a route that reaches keys no
// range scan returns, so the internal-key guard has to cover it too — a
// deleted "__sys:roles/…" record is exactly the thing an attacker would want a
// prefix restore to resurrect. filterRestorableKeyValues covers the range
// scans; this covers the index.
func (s *MVCCKVService) scanChangedKeys(ctx context.Context, startRev, endRev int64, prefix string) ([]string, bool, error) {
	result, err := s.host.SyncRead(ctx, s.shardID, MVCCScanChangesQuery{
		StartRevision: startRev,
		EndRevision:   endRev,
		Prefix:        []byte(prefix),
		Limit:         MaxRestoreKeys + 1,
	})
	if err != nil {
		// A backend with no revision index cannot answer this, and an empty
		// answer would be indistinguishable from "nothing was deleted". Report
		// it as its own refusal so the operator learns the restore is not
		// merely empty but unanswerable on this backend.
		var unsupported changeIndexUnsupportedErr
		if errors.As(err, &unsupported) {
			return nil, false, &RestoreChangeIndexUnavailableError{Err: err}
		}
		return nil, false, s.translateReadError(err)
	}

	scan, ok := result.(*MVCCScanChangesResult)
	if !ok {
		return nil, false, &ReadError{Err: &unexpectedResultTypeError{}}
	}

	keys := make([]string, 0, len(scan.Keys))
	for _, k := range scan.Keys {
		if IsInternalKey(k) {
			continue
		}
		keys = append(keys, k)
	}
	return keys, scan.Truncated, nil
}

// restoreCandidates returns the sorted union of the currently live keys and
// the keys the revision index reported as changed. Sorting makes the plan and
// its apply order deterministic regardless of map iteration order.
func restoreCandidates(live map[string]*KeyValue, changed []string) []string {
	seen := make(map[string]struct{}, len(live)+len(changed))
	out := make([]string, 0, len(live)+len(changed))

	add := func(key string) {
		if _, dup := seen[key]; dup {
			return
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}

	for key := range live {
		add(key)
	}
	for _, key := range changed {
		// The index is filtered at the source, but a key reaching the plan is
		// the last place this can be caught, so it is checked again here.
		if IsInternalKey(key) {
			continue
		}
		add(key)
	}

	sort.Strings(out)
	return out
}

// sortRestoreSteps orders a plan by key so that the plan a dry run reports and
// the order an apply writes in are both deterministic. Map iteration order
// would otherwise make two dry runs of the same request differ.
func sortRestoreSteps(steps []restoreStep) {
	sort.Slice(steps, func(i, j int) bool {
		return steps[i].change.Key < steps[j].change.Key
	})
}
