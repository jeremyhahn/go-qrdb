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

package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// collectionWorkPrefix is the key prefix of the collection work journal; see
// service.CollectionWorkPrefix, where it is declared so the tenant keyspace
// reclamation can remove a deleted tenant's entries.
const collectionWorkPrefix = service.CollectionWorkPrefix

// collectionWorkEntry is one journal entry. Tenant, Database and Name are
// read from the value, never parsed back out of the key, because a
// collection name may contain '/'; the key must equal the one they derive
// (collectionWorkKey) or the entry is not acted on. Nonce makes every Record
// a distinct value, so the sweeper's compare-and-delete of an entry it saw
// settled cannot remove one a later transition re-recorded.
type collectionWorkEntry struct {
	Tenant     string    `json:"tenant"`
	Database   string    `json:"database"`
	Name       string    `json:"name"`
	Nonce      string    `json:"nonce"`
	RecordedAt time.Time `json:"recorded_at"`
}

// collectionWorkKey returns the journal key of one collection.
func collectionWorkKey(tenantID, database, name string) string {
	return collectionWorkPrefix + tenantID + "/" + database + "/" + name
}

// collectionWorkJournal writes one tenant's journal entries into the raw
// (un-namespaced) KV of the store its collections live in.
type collectionWorkJournal struct {
	kv     service.KVServicer
	tenant string
	now    func() time.Time
}

// Compile-time interface check.
var _ service.CollectionWorkJournal = (*collectionWorkJournal)(nil)

// Record writes a fresh entry for database/name.
func (j *collectionWorkJournal) Record(ctx context.Context, database, name string) error {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	now := time.Now
	if j.now != nil {
		now = j.now
	}
	data, err := json.Marshal(&collectionWorkEntry{
		Tenant: j.tenant, Database: database, Name: name,
		Nonce: hex.EncodeToString(nonce[:]), RecordedAt: now(),
	})
	if err != nil {
		return err
	}
	return j.kv.Put(ctx, collectionWorkKey(j.tenant, database, name), data)
}

// collectionWorkItem is one journal entry as scanned: the entry, and the key
// and exact bytes a compare-and-delete settles it with.
type collectionWorkItem struct {
	key   string
	raw   []byte
	entry collectionWorkEntry
}

// scanCollectionWork reads every well-formed journal entry in kv. An entry
// that does not decode, or whose key is not the one its own fields derive, is
// skipped: nothing is torn down on the word of a record that does not agree
// with itself.
func scanCollectionWork(ctx context.Context, kv service.KVServicer) ([]collectionWorkItem, error) {
	raw, err := kv.Scan(ctx, collectionWorkPrefix)
	if err != nil {
		return nil, err
	}
	items := make([]collectionWorkItem, 0, len(raw))
	for key, value := range raw {
		var entry collectionWorkEntry
		if err := json.Unmarshal(value, &entry); err != nil {
			continue
		}
		if entry.Tenant == "" || entry.Database == "" || entry.Name == "" || key != collectionWorkKey(entry.Tenant, entry.Database, entry.Name) {
			continue
		}
		items = append(items, collectionWorkItem{key: key, raw: value, entry: entry})
	}
	return items, nil
}

// CollectionWorkNotServedError is returned when the collection sweeper asks
// for a journaled database this node does not serve from the store the
// journal entry was found in: a raft tenant (whose databases have no journal)
// or a volume tenant whose Host is not started here.
type CollectionWorkNotServedError struct {
	TenantID string
	Database string
}

// Error returns a human-readable description.
func (e *CollectionWorkNotServedError) Error() string {
	return "server: collection work for " + e.TenantID + "/" + e.Database + " is not served by this node"
}

// journaledCollectionService resolves the collection service of a database a
// collection work journal entry names, WITHOUT the node-local catalog check
// resolve applies to a request.
//
// The catalog cannot be consulted: a prefix tenant's named database lives in
// the shared replicated store, but only the node that created it holds its
// catalog entry, and the node leading the store after a leader change is
// usually not that node. The journal entry is itself the evidence: it was
// written into this store by a collection service built for exactly this
// (tenant, database). Tenant resolution -- existence, isolation, a sealed
// barrier -- still applies, and so does the storage quota wrapper, so a sweep
// releases what it frees.
func (r *restServiceRegistry) journaledCollectionService(tenantID, db string) (service.CollectionServicer, error) {
	isolation, info, err := r.resolveTenant(tenantID)
	if err != nil {
		return nil, err
	}
	var base *DatabaseServices
	switch isolation {
	case tenant.IsolationVolume:
		if r.tenantHosts == nil || !r.tenantHosts.Started(tenantID) {
			return nil, &CollectionWorkNotServedError{TenantID: tenantID, Database: db}
		}
		if base, err = r.tenantHosts.Services(context.Background(), tenantID); err != nil {
			return nil, err
		}
	case tenant.IsolationRaft:
		return nil, &CollectionWorkNotServedError{TenantID: tenantID, Database: db}
	default:
		// The bundle resolve serves this database from: one registered
		// under its name, else the default the shared store is built over.
		if base = r.dbReg.GetOrDefault(db); base == nil {
			base = r.dbReg.Default()
		}
	}
	if base == nil {
		return nil, &CollectionWorkNotServedError{TenantID: tenantID, Database: db}
	}
	svcs := r.applyQuota(r.applyComposite(base, tenantID, db), tenantID, info)
	if svcs.Collection == nil {
		return nil, notImplemented("collection", "")
	}
	return svcs.Collection, nil
}

// journaledCollectionResolver is the registry capability the collection
// sweeper resolves journaled work through. restServiceRegistry implements it.
type journaledCollectionResolver interface {
	journaledCollectionService(tenantID, db string) (service.CollectionServicer, error)
}

var _ journaledCollectionResolver = (*restServiceRegistry)(nil)
