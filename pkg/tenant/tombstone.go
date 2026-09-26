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
	"encoding/json"
	"errors"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// ---------------------------------------------------------------------------
// A deleted tenant id is retired, not released
//
// Tenant ids are caller-supplied names. Before this existed, deleting a tenant
// removed its metadata and its barrier registration and left its keyspace in
// place, so creating a tenant under the same id again re-attached the NEW
// principal to the OLD principal's data: its keys, its secondary indexes, its
// relational tables and its identity records. That is reachable with no
// storage access at all -- create, populate, delete, create again, read.
//
// The question a delete is supposed to answer is "is the second `acme` the
// same principal as the first?". The system cannot answer it: an id is a
// string a caller chose. So it must not guess. A deleted id is recorded here
// and is permanently refused afterwards, which converts the worst outcome --
// a new principal silently reading a previous principal's data -- into a loud
// refusal that names the deletion.
//
// The record is written to the same store the tenant metadata lives in, which
// in a cluster is the replicated system shard, so it survives restart and is
// visible on every node. It is bounded by the number of deletions and holds
// no secret: an id, the isolation type it used, and two timestamps.
// ---------------------------------------------------------------------------

// tombstonePrefix is the key prefix for tenant tombstones.
//
// It is deliberately a SIBLING of metadataPrefix rather than a child: a
// tombstone under "__sys:tenants/" would be returned by ListTenants' prefix
// scan and would have to be filtered out at every reader. "tenants-deleted/"
// cannot collide with "tenants/" because the byte after "tenants" differs, so
// neither prefix scan sees the other's keys.
//
// Like metadataPrefix it lives under "__sys:" so the records are excluded
// from user-facing KV list and scan by the store's internal-key filter.
const tombstonePrefix = "__sys:tenants-deleted/"

// Tombstone is the durable record of a deleted tenant id.
//
// It is the mechanism behind two guarantees: CreateTenant refuses a
// tombstoned id forever, and StoreResolver refuses to resolve one to a
// keyspace. Both need the same record, which is why "tombstone or purge" is
// not a choice -- the tombstone is what makes the id unusable, and the purge
// is what reclaims the bytes.
type Tombstone struct {
	// ID is the retired tenant id.
	ID string `json:"id"`

	// Name is the deleted tenant's display name, kept so an operator reading
	// a refusal can tell which tenant retired the id.
	Name string `json:"name,omitempty"`

	// IsolationType is the isolation the deleted tenant used. It tells a
	// later reclamation where that tenant's data lived without having to
	// re-derive it from a record that no longer exists.
	IsolationType IsolationType `json:"isolation_type,omitempty"`

	// DataRoot is the dedicated filesystem root the deleted tenant occupied,
	// empty for prefix and barrier isolation. It is recorded for the same
	// reason IsolationType is: an operator inspecting a leaked directory can
	// map it back to the deletion that abandoned it.
	DataRoot string `json:"data_root,omitempty"`

	// CreatedAt is when the deleted tenant was created.
	CreatedAt time.Time `json:"created_at,omitempty"`

	// DeletedAt is when the tenant was deleted. It is the timestamp the
	// refusal reports.
	DeletedAt time.Time `json:"deleted_at"`
}

// tombstoneKey returns the storage key holding tenantID's tombstone.
func tombstoneKey(tenantID string) string { return tombstonePrefix + tenantID }

// newTombstone builds the tombstone for a tenant being deleted at now.
func newTombstone(info *TenantInfo, now time.Time) *Tombstone {
	t := &Tombstone{DeletedAt: now}
	if info != nil {
		t.ID = info.ID
		t.Name = info.Name
		t.IsolationType = info.IsolationType
		t.DataRoot = info.DataRoot
		t.CreatedAt = info.CreatedAt
	}
	return t
}

// tombstoneReader is the read half of a tombstone store. Both the
// seal.StorageBackend used by TenantService and the kvstore.KVStore used by
// kvTenantService are adapted onto it, because the refusal must behave
// identically on a node with the seal subsystem on and one with it off -- an
// id retired on an encrypted deployment is retired on an unencrypted one for
// the same reason.
type tombstoneReader func(ctx context.Context, key string) ([]byte, error)

// checkTombstone refuses tenantID when a tombstone exists for it.
//
// A read failure other than "absent" FAILS the caller rather than being
// treated as "no tombstone". Creating a tenant is the operation whose whole
// safety property is that the id was never used before; answering it from an
// unreadable store would defeat the record entirely.
func checkTombstone(ctx context.Context, read tombstoneReader, tenantID string) error {
	data, err := read(ctx, tombstoneKey(tenantID))
	if err != nil {
		if isAbsent(err) {
			return nil
		}
		return &StoreError{Op: "tombstone-check", Err: err}
	}
	if data == nil {
		return nil
	}
	ts := &Tombstone{ID: tenantID}
	if uerr := json.Unmarshal(data, ts); uerr != nil {
		// A tombstone that cannot be decoded still means the id was deleted.
		// Refusing on the id alone is the conservative reading and is the
		// only one that keeps a corrupt record from releasing an id.
		ts = &Tombstone{ID: tenantID}
	}
	return &TenantDeletedError{TenantID: tenantID, DeletedAt: ts.DeletedAt}
}

// fencedCreatorOf returns store's atomic fenced-create capability, or
// ErrTenantStoreNotAtomic when it has none. CreateTenant asks before any side
// effect, so a store that cannot honour the guarantee refuses a create that
// has changed nothing.
func fencedCreatorOf(store any) (kvstore.FencedCreator, error) {
	fenced, ok := store.(kvstore.FencedCreator)
	if !ok {
		return nil, ErrTenantStoreNotAtomic
	}
	return fenced, nil
}

// commitCreate writes a new tenant's metadata record only while that record
// AND the tenant's tombstone are both absent, decided in one atomic step.
//
// This is what serializes a create against a delete of the same id. The
// earlier tombstone check is only a fast refusal: it is a read, and a delete
// that writes the tombstone and removes the metadata after it would let the
// create's later write commit a live record for an id retired forever --
// metadata beside a tombstone, a barrier that comes back on every restart,
// and a tenant nothing can resolve (go-qrdb#484). With both absences and the
// write decided together, every interleaving ends in exactly one outcome:
// the create lands before the tombstone (and a delete that follows it removes
// it like any other tenant) or it is refused.
//
// A refusal is reported as the fence that held: *TenantDeletedError when the
// tombstone exists, ErrTenantAlreadyExists when another create won the id.
// The store is unchanged in both cases. A store error is an UNKNOWN outcome
// and is reported as *StoreError{Op: "put"}.
func commitCreate(ctx context.Context, fenced kvstore.FencedCreator, read tombstoneReader, id string, record []byte) error {
	created, err := fenced.PutIfAllAbsent(ctx, metadataPrefix+id, record, tombstoneKey(id))
	if err != nil {
		return &StoreError{Op: "put", Err: err}
	}
	if created {
		return nil
	}
	if err := checkTombstone(ctx, read, id); err != nil {
		return err
	}
	return ErrTenantAlreadyExists
}

// isAbsent reports whether err means "this key does not exist".
//
// The two tenant services read through different stores whose absent-key
// contracts differ -- seal.StorageBackend returns seal.ErrSecretNotFound,
// kvstore.KVStore returns (nil, nil) -- so the (nil, nil) case is handled by
// the caller and this covers the error case for both.
func isAbsent(err error) bool {
	return errors.Is(err, seal.ErrSecretNotFound)
}

// tombstoneLookup is the read seam StoreResolver uses to refuse a retired
// tenant id.
//
// Its method is UNEXPORTED, so only an implementation inside this package can
// satisfy it and the compile-time assertions below are the whole population.
// That is deliberate: the alternative was a method on TenantServicer, which
// would make every foreign implementation of that interface -- including the
// test doubles in four other packages -- silently opt out of the refusal by
// omission, which is the failure mode a double narrower than production
// produces.
type tombstoneLookup interface {
	deletedAt(ctx context.Context, tenantID string) (time.Time, bool, error)
}

// Compile-time checks: BOTH tenant services answer the tombstone question,
// because an id retired on a node with the seal subsystem enabled is retired
// on one without it for the same reason.
var (
	_ tombstoneLookup = (*TenantService)(nil)
	_ tombstoneLookup = (*kvTenantService)(nil)
)

// deletedAt returns the time tenantID was deleted, and whether a tombstone
// for it exists. It reports the zero time when the record cannot be decoded,
// because the existence of the record is the fact that matters.
func (s *TenantService) deletedAt(ctx context.Context, tenantID string) (time.Time, bool, error) {
	if tenantID == "" {
		return time.Time{}, false, ErrEmptyTenantID
	}
	data, err := s.store.Get(ctx, tombstoneKey(tenantID))
	if err != nil {
		if isAbsent(err) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, &StoreError{Op: "tombstone-get", Err: err}
	}
	return decodeDeletedAt(data)
}

// deletedAt returns the time tenantID was deleted, and whether a tombstone
// for it exists. See TenantService.deletedAt.
func (s *kvTenantService) deletedAt(ctx context.Context, tenantID string) (time.Time, bool, error) {
	if tenantID == "" {
		return time.Time{}, false, ErrEmptyTenantID
	}
	data, err := s.store.Get(ctx, tombstoneKey(tenantID))
	if err != nil {
		return time.Time{}, false, &StoreError{Op: "tombstone-get", Err: err}
	}
	return decodeDeletedAt(data)
}

// decodeDeletedAt turns a raw tombstone record into (deletedAt, exists).
func decodeDeletedAt(data []byte) (time.Time, bool, error) {
	if data == nil {
		return time.Time{}, false, nil
	}
	var ts Tombstone
	if err := json.Unmarshal(data, &ts); err != nil {
		return time.Time{}, true, nil
	}
	return ts.DeletedAt, true, nil
}
