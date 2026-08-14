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

import "strconv"

// ---------------------------------------------------------------------------
// Selective point-in-time restore errors
//
// Every one of these is a refusal, not a partial result: RestoreToRevision
// either applies its whole computed plan or writes nothing. They are typed so
// each transport can map them to a distinct status (the scope and internal-key
// refusals are 400/InvalidArgument except where noted on the type).
// ---------------------------------------------------------------------------

// RestoreNilRequestError is returned when RestoreToRevision is called with a
// nil request.
type RestoreNilRequestError struct{}

// Error returns a human-readable description.
func (e *RestoreNilRequestError) Error() string {
	return "service: restore request is nil"
}

// RestoreRevisionInvalidError is returned when the requested target revision
// is not a positive revision number. Revision 0 is rejected rather than
// treated as "latest": a restore to "now" is a no-op the caller did not mean
// to ask for, and silently accepting it would make a mistyped request succeed.
type RestoreRevisionInvalidError struct {
	// Revision is the rejected value.
	Revision int64
}

// Error returns a human-readable description.
func (e *RestoreRevisionInvalidError) Error() string {
	return "service: restore revision " + strconv.FormatInt(e.Revision, 10) +
		" is invalid; a target revision must be greater than zero"
}

// RestoreRevisionFutureError is returned when the requested target revision
// is ahead of the store's current committed revision. There is no state to
// restore from, so the request is refused rather than clamped.
type RestoreRevisionFutureError struct {
	// Requested is the target revision the caller asked for.
	Requested int64
	// Current is the store's latest committed MVCC revision.
	Current int64
}

// Error returns a human-readable description.
func (e *RestoreRevisionFutureError) Error() string {
	return "service: restore revision " + strconv.FormatInt(e.Requested, 10) +
		" is ahead of the current revision " + strconv.FormatInt(e.Current, 10)
}

// RestoreScopeEmptyError is returned when a restore request names neither a
// prefix nor an explicit key list.
//
// This refusal is the reason the type exists. A restore with no scope would
// mean "roll the entire keyspace back", which is never what an operator meant
// to request by leaving both fields blank, and which a mistyped or truncated
// request would otherwise trigger. Scope is required by omission-denies, not
// defaulted.
type RestoreScopeEmptyError struct{}

// Error returns a human-readable description.
func (e *RestoreScopeEmptyError) Error() string {
	return "service: restore scope is empty; exactly one of prefix or keys is required " +
		"(an unscoped restore would roll back the whole keyspace and is never permitted)"
}

// RestoreScopeAmbiguousError is returned when a restore request names both a
// prefix and an explicit key list. The two are alternative scopes, and
// silently preferring one would restore a different set of keys than the
// caller described.
type RestoreScopeAmbiguousError struct {
	// Prefix is the prefix scope that was supplied.
	Prefix string
	// KeyCount is the number of explicit keys that were supplied.
	KeyCount int
}

// Error returns a human-readable description.
func (e *RestoreScopeAmbiguousError) Error() string {
	return "service: restore scope names both a prefix (" + e.Prefix + ") and " +
		strconv.Itoa(e.KeyCount) + " explicit keys; exactly one is required"
}

// RestoreEmptyKeyError is returned when an explicit restore key list contains
// an empty key.
type RestoreEmptyKeyError struct {
	// Index is the position of the empty key in the request's key list.
	Index int
}

// Error returns a human-readable description.
func (e *RestoreEmptyKeyError) Error() string {
	return "service: restore key at index " + strconv.Itoa(e.Index) + " is empty"
}

// RestoreInternalKeyError is returned when a restore scope reaches the
// internal key namespace (roles, users, time-series points and indexes,
// retention policies).
//
// The refusal is unconditional and has no override. Restoring "__sys:roles/…"
// or "__sys:users/…" to an earlier revision would undo a permission
// revocation or resurrect a deleted administrator — a privilege escalation
// performed entirely through a data-plane verb. Unlike the read paths, which
// let a caller that explicitly asks for the "__sys:" prefix see its own
// system records, restore has no such escape hatch: an explicitly internal
// scope is the case this error exists to reject.
//
// Transports map it to 403 Forbidden / codes.PermissionDenied, not 400: it
// reports an operation the caller is not permitted to perform, not a
// malformed one.
type RestoreInternalKeyError struct {
	// Key is the offending key, when an explicit key list was scoped.
	Key string
	// Prefix is the offending prefix, when a prefix was scoped.
	Prefix string
}

// Error returns a human-readable description.
func (e *RestoreInternalKeyError) Error() string {
	target := "key " + e.Key
	if e.Key == "" {
		target = "prefix " + e.Prefix
	}
	return "service: restore refused for " + target +
		": the internal key namespace is not restorable"
}

// RestoreScopeTooLargeError is returned when a restore scope matches more
// keys than a single restore may carry. The diff is materialized in memory,
// so an unbounded prefix would let one request exhaust the node's heap. The
// cap is explicit and the request is refused whole — a restore is never
// silently truncated to a prefix of the plan the caller asked for, because a
// partial logical undo leaves the keyspace in a state that existed at no
// revision.
type RestoreScopeTooLargeError struct {
	// Limit is the maximum number of keys a single restore may span
	// (MaxRestoreKeys).
	Limit int
	// Scope describes the refused scope for diagnostics.
	Scope string
}

// Error returns a human-readable description.
func (e *RestoreScopeTooLargeError) Error() string {
	return "service: restore scope " + e.Scope + " exceeds the maximum of " +
		strconv.Itoa(e.Limit) + " keys per restore; narrow the scope and repeat"
}

// RestoreUnboundedPrefixError is returned when a prefix scope has no finite
// exclusive upper bound (a prefix consisting entirely of 0xFF bytes). Such a
// prefix cannot be expressed as a bounded half-open range, and scanning it
// with an open upper bound would reach past the scope the caller named, so
// the request is refused instead.
type RestoreUnboundedPrefixError struct {
	// Prefix is the refused prefix.
	Prefix string
}

// Error returns a human-readable description.
func (e *RestoreUnboundedPrefixError) Error() string {
	return "service: restore prefix has no finite upper bound and cannot be scoped as a bounded range"
}

// RestoreChangeIndexUnavailableError is returned when a prefix-scoped restore
// cannot consult the durable revision index because the shard's MVCC backend
// does not maintain one.
//
// A prefix restore needs the index to find keys DELETED since the target
// revision; a range scan cannot see them. Without it the restore could still
// produce a plan, but that plan would silently omit every deleted key —
// exactly the case an operator recovering from a bulk delete is trying to fix.
// The refusal is therefore deliberate: an incomplete restore that looks
// successful is worse than one that declines. An explicit Keys scope still
// works on such a backend, because it reads each key's history directly.
type RestoreChangeIndexUnavailableError struct {
	// Err is the backend's refusal.
	Err error
}

// Error returns a human-readable description.
func (e *RestoreChangeIndexUnavailableError) Error() string {
	return "service: prefix-scoped restore requires the MVCC revision index, which this backend " +
		"does not maintain; name the keys explicitly instead: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *RestoreChangeIndexUnavailableError) Unwrap() error {
	return e.Err
}

// RestoreApplyError is returned when a write in the restore plan fails. The
// restore is not transactional across keys — each change is an ordinary
// Raft-replicated write — so Applied reports how many changes were committed
// before the failure. The caller can re-issue the same request: the plan is
// recomputed against the then-current state, so already-applied changes are
// skipped as identical.
type RestoreApplyError struct {
	// Key is the key whose write failed.
	Key string
	// Action is the plan action that failed.
	Action RestoreAction
	// Applied is the number of changes committed before the failure.
	Applied int
	// Err is the underlying write error.
	Err error
}

// Error returns a human-readable description.
func (e *RestoreApplyError) Error() string {
	return "service: restore failed applying " + string(e.Action) + " to key " + e.Key +
		" after " + strconv.Itoa(e.Applied) + " applied change(s): " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *RestoreApplyError) Unwrap() error {
	return e.Err
}
