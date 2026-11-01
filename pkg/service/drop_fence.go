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
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// The database drop fence
//
// DROP DATABASE must not reclaim a keyspace while a write into it can still
// land, and "can still land" includes a write that was resolved, checked and
// proposed before the drop began and is merely waiting in the Raft log. A
// check made before proposing cannot see that write; only the state machine
// applying it can. So the fence is data, and the check is part of every
// write:
//
//   - Each database has a fence marker, DropFenceKey(tenant, db), in the
//     SAME Raft group as its data -- the system shard for a shared store,
//     the tenant's own Host shard under volume isolation, the database's own
//     shard for a dedicated-raft database.
//   - The marker holds the database's live INCARNATION, a random value
//     database.Manager.Create chooses and records in the database's catalog
//     entry (KeyspaceReclaimService.UnfenceDatabaseKeyspace). An empty
//     incarnation -- a database created before incarnations existed, or a
//     dedicated-raft database, whose own shard is its incarnation -- is
//     represented by the marker's absence.
//   - Every write a view of the database proposes carries
//     DropFenceGuard(tenant, db, incarnation): "the marker holds exactly my
//     incarnation", in the same Raft command (WriteGuard, OpGuarded).
//   - DROP DATABASE sets the marker to a tombstone no incarnation equals
//     (KeyspaceReclaimService.FenceDatabaseKeyspace) before it reclaims
//     anything. The tombstone carries the drop's claim, the token it holds
//     in the catalog record: a drop that takes another over re-stamps it,
//     and the reclamation's deletes and a failed drop's rollback are
//     guarded on the drop's own tombstone, so a drop that was taken over
//     can neither delete under, nor clear, the fence of the one that took
//     it over.
//
// Log order then decides every race. A write applied before the fence
// landed before it and is removed by the reclamation that follows; a write
// applied after it is refused by the state machine, on every replica, and
// its caller receives *database.DroppingError. There is no check-then-write
// window, because there is no separate check. And because the guard names
// the incarnation, a handle resolved before a drop stays refused after the
// name is created again: the new incarnation's value is not the one it
// carries.
//
// The tombstone stays after a drop completes. An incarnation-scoped handle
// would be refused by an absent marker too, but a handle of a database with
// an EMPTY incarnation compares against absence, so removing the tombstone
// would readmit exactly those handles. One small key per dropped name is the
// price, and creating the name again overwrites it.
// ---------------------------------------------------------------------------

// DropFencePrefix is the reserved key prefix every drop fence lives under.
// It is inside the "__sys:" namespace, so no namespaced view can write it:
// every key a namespaced view proposes begins with "<tenant>/<db>/".
const DropFencePrefix = InternalKeyPrefixSystem + "dropfence/"

// dropFenceTombstone is the value a dropped database's marker holds. It
// starts with a NUL byte, so it can never equal an incarnation (NewIncarnation
// produces hex), and it is non-empty, so it never reads as "absent".
var dropFenceTombstone = []byte("\x00dropped")

// dropFenceTombstoneFor returns the tombstone the drop holding claim stamps
// on a marker: dropFenceTombstone followed by claim. The claim is the drop's
// fencing token in the store, as it is in the catalog: a drop that takes
// another over re-stamps the marker with its own claim, in the same Raft
// group as the data, so every later write of the drop it took over -- the
// rollback that would clear the fence, a delete of its reclamation -- names
// a tombstone the marker no longer holds and is refused by the state machine
// (go-qrdb#893). An empty claim is the bare tombstone.
func dropFenceTombstoneFor(claim string) []byte {
	return append(append(make([]byte, 0, len(dropFenceTombstone)+len(claim)), dropFenceTombstone...), claim...)
}

// isDropFenceTombstone reports whether a marker value is a tombstone, of any
// drop's claim. No incarnation starts with the tombstone's NUL byte.
func isDropFenceTombstone(value []byte) bool {
	return bytes.HasPrefix(value, dropFenceTombstone)
}

// DropFenceKey returns the key whose presence fences (tenantID, db) against
// writes. An empty tenantID is the system tenant and an empty db the default
// database, exactly as tenant.ComposeNamespace resolves them.
func DropFenceKey(tenantID, db string) string {
	return DropFencePrefix + tenant.ComposeNamespace(tenantID, db)
}

// DropFenceGuard returns the guard every write to incarnation of (tenantID,
// db) carries: the database's fence marker holds exactly incarnation. An
// empty incarnation is satisfied by an absent marker (see
// protocol.CompareHolds), which is how a database with no incarnation of
// its own is live.
func DropFenceGuard(tenantID, db, incarnation string) (WriteGuard, error) {
	return NewWriteGuard(Compare{
		Key:    []byte(DropFenceKey(tenantID, db)),
		Target: CompareValue,
		Result: CompareEqual,
		Value:  []byte(incarnation),
	})
}

// droppingErrorFor returns the *database.DroppingError a refused write
// reports when key is a drop fence, and false for any other key.
func droppingErrorFor(key string) (*database.DroppingError, bool) {
	rest, ok := strings.CutPrefix(key, DropFencePrefix)
	if !ok {
		return nil, false
	}
	tenantID, rest, ok := strings.Cut(rest, "/")
	if !ok || tenantID == "" {
		return nil, false
	}
	db, rest, ok := strings.Cut(rest, "/")
	if !ok || db == "" || rest != "" {
		return nil, false
	}
	return &database.DroppingError{Name: db, TenantID: tenantID}, true
}

// guardRefusalError is the error a write reports when its guard did not hold
// at apply time. A refused drop fence is the database being dropped; any
// other key is a caller-supplied guard. Both prove nothing was stored.
func guardRefusalError(key []byte) error {
	if dropping, ok := droppingErrorFor(string(key)); ok {
		return markNotStored(dropping)
	}
	return &WriteGuardRefusedError{Key: string(key)}
}
