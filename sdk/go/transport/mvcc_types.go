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

package transport

// MVCCKeyValue is a versioned key-value pair with MVCC revision metadata.
type MVCCKeyValue struct {
	Key            []byte `json:"key"`
	Value          []byte `json:"value"`
	CreateRevision int64  `json:"create_revision"`
	ModRevision    int64  `json:"mod_revision"`
	Version        int64  `json:"version"`
	LeaseID        int64  `json:"lease_id"`
}

// SortOrder specifies the sort direction for range results.
type SortOrder uint8

const (
	// SortNone returns results in natural key order.
	SortNone SortOrder = iota

	// SortAscend returns results in ascending order.
	SortAscend

	// SortDescend returns results in descending order.
	SortDescend
)

// SortTarget specifies which field to sort by.
type SortTarget uint8

const (
	// SortByKey sorts by the key.
	SortByKey SortTarget = iota

	// SortByVersion sorts by the version counter.
	SortByVersion

	// SortByCreateRevision sorts by the creation revision.
	SortByCreateRevision

	// SortByModRevision sorts by the modification revision.
	SortByModRevision

	// SortByValue sorts by the value.
	SortByValue
)

// RangeOptions configures a range query.
type RangeOptions struct {
	Limit      int64      `json:"limit,omitempty"`
	Revision   int64      `json:"revision,omitempty"`
	CountOnly  bool       `json:"count_only,omitempty"`
	KeysOnly   bool       `json:"keys_only,omitempty"`
	SortOrder  SortOrder  `json:"sort_order,omitempty"`
	SortTarget SortTarget `json:"sort_target,omitempty"`
}

// RangeResponse is the response from a range query.
type RangeResponse struct {
	Kvs      []*MVCCKeyValue `json:"kvs"`
	Count    int64           `json:"count"`
	Revision int64           `json:"revision,omitempty"`
}

// PutWithLeaseResponse is returned by PutWithLease.
type PutWithLeaseResponse struct {
	Kv *MVCCKeyValue `json:"kv"`
}

// CurrentRevisionResponse is returned by CurrentRevision.
type CurrentRevisionResponse struct {
	Revision int64 `json:"revision"`
}

// ---------------------------------------------------------------------------
// Selective point-in-time restore
// ---------------------------------------------------------------------------

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
// Exactly one of Prefix and Keys must be set: an unscoped request (neither
// set) is refused, and a request setting both is refused as ambiguous. Both
// are []byte, not string, so an arbitrary byte-valued key or prefix
// round-trips byte-for-byte across every transport.
type RestoreRevisionRequest struct {
	// Revision is the target revision to restore the scoped keys to. It
	// must be positive, at or below the store's current committed
	// revision, and above the compaction floor.
	Revision int64 `json:"revision"`

	// Prefix scopes the restore to every key beginning with it. Mutually
	// exclusive with Keys.
	Prefix []byte `json:"prefix,omitempty"`

	// Keys scopes the restore to an explicit key list. Mutually exclusive
	// with Prefix. Duplicates are collapsed by the server.
	Keys [][]byte `json:"keys,omitempty"`

	// DryRun computes and returns the plan without writing anything.
	DryRun bool `json:"dry_run,omitempty"`

	// Force applies the plan unconditionally, overwriting any key that has
	// been written since the plan was computed. The default (false) is
	// compare-and-swap: a key that moved on since the plan was computed is
	// left alone and reported in RestoreRevisionResult.Conflicts.
	Force bool `json:"force,omitempty"`
}

// RestoreKeyChange is one entry in a restore plan: the change that would be
// (or was) applied to a single key. The restored value is deliberately not
// carried here -- only its length (ValueBytes) -- so a plan is not a bulk
// data export of the keyspace at a historical revision.
type RestoreKeyChange struct {
	// Key is the affected key.
	Key []byte `json:"key"`

	// Action is the change the plan applies to Key.
	Action RestoreAction `json:"action"`

	// FromRevision is the key's current ModRevision, or 0 when the key does
	// not currently exist (a recreate).
	FromRevision int64 `json:"from_revision,omitempty"`

	// ToRevision is the ModRevision of the version being restored, or 0
	// when the key did not exist at the target revision (a delete).
	ToRevision int64 `json:"to_revision,omitempty"`

	// ValueBytes is the length of the value the change writes, or 0 for a
	// delete.
	ValueBytes int64 `json:"value_bytes,omitempty"`
}

// RestoreRevisionResult reports the outcome of a selective restore.
type RestoreRevisionResult struct {
	// Revision is the target revision the plan was computed against.
	Revision int64 `json:"revision"`

	// Changes is the plan, ordered by key. On a dry run it is the plan
	// that would be applied; on an apply it is the plan that was
	// computed -- including any entry that turned out to conflict, which
	// is reported in Conflicts rather than removed from here.
	Changes []*RestoreKeyChange `json:"changes"`

	// Conflicts are the plan entries that were NOT applied because the key
	// changed between the diff and the write. Only populated when the
	// request did not set Force.
	Conflicts []*RestoreKeyChange `json:"conflicts,omitempty"`

	// Applied is the number of changes committed. It is 0 for a dry run.
	Applied int `json:"applied"`

	// Skipped is the number of in-scope keys whose current state already
	// matches the target revision, including keys absent in both.
	Skipped int `json:"skipped"`

	// Conflicted is len(Conflicts): the number of keys that changed since
	// the plan was computed and were therefore left untouched.
	Conflicted int `json:"conflicted"`

	// DryRun echoes the request's DryRun flag.
	DryRun bool `json:"dry_run"`
}
