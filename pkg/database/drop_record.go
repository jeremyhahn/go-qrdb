// Copyright (c) 2025 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package database

import (
	"context"
	"errors"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// DropRecordKeyPrefix is the key prefix of a drop record: the name and id of
// a database whose drop removed its catalog record but has not yet torn
// down its replicated placement. A record's key is DropRecordKeyPrefix +
// <tenant> + "/" + <name>; its value is the database id.
//
// The placement table is keyed by the opaque id and learns a database's
// name only from the tenant's catalog, so once the catalog record is gone a
// resumed drop could find its placement entry only through a name some
// process happened to cache. The drop record carries the name-to-id mapping
// across that window, durably and in the tenant's own store, beside the
// catalog it stands in for.
const DropRecordKeyPrefix = "__sys:dbdrop/"

// DropRecordKey returns the key of the drop record of tenantID's database
// name.
func DropRecordKey(tenantID, name string) string {
	return DropRecordKeyPrefix + tenantid.Normalize(tenantID) + "/" + name
}

// DropRecorder keeps drop records. KVCatalog implements it; the Manager
// reaches it through its catalog.
type DropRecorder interface {
	// PutDropRecord records that tenantID's database name, of databaseID,
	// is being dropped. It writes only while the catalog record of name
	// carries databaseID (or the record already does, for a resumed drop);
	// any other incarnation is a *DropRecordSupersededError.
	PutDropRecord(ctx context.Context, tenantID, name, databaseID string) error
	// DropRecord returns the id the drop record of tenantID's database name
	// carries, or "" when there is none.
	DropRecord(ctx context.Context, tenantID, name string) (string, error)
	// DeleteDropRecord removes the drop record of tenantID's database name
	// while it carries databaseID; a record of another id is left.
	DeleteDropRecord(ctx context.Context, tenantID, name, databaseID string) error
}

var _ DropRecorder = (*KVCatalog)(nil)

// PutDropRecord implements DropRecorder.
func (c *KVCatalog) PutDropRecord(ctx context.Context, tenantID, name, databaseID string) error {
	key := DropRecordKey(tenantID, name)
	store, err := c.store(ctx, tenantID)
	if err != nil {
		return err
	}
	catalogKey := CatalogKey(tenantID, name)
	for range maxDropRecordAttempts {
		// The drop record is read BEFORE the catalog: a record of a later
		// incarnation exists only once the catalog named that incarnation,
		// and ids are never reused, so a catalog read after it can never
		// name this (earlier) id again.
		cur, err := store.Get(ctx, key)
		if err != nil {
			return &CatalogReadError{Key: key, Err: err}
		}
		if string(cur) == databaseID {
			return nil // a resumed drop of this incarnation
		}
		data, err := store.Get(ctx, catalogKey)
		if err != nil {
			return &CatalogReadError{Key: catalogKey, Err: err}
		}
		if data == nil {
			return &DropRecordSupersededError{TenantID: tenantID, Name: name, DatabaseID: databaseID}
		}
		meta, err := decodeCatalogRecord(catalogKey, data)
		if err != nil {
			return err
		}
		if meta.ID != databaseID {
			return &DropRecordSupersededError{TenantID: tenantID, Name: name, DatabaseID: databaseID}
		}
		var written bool
		if cur == nil {
			written, err = store.PutIfAbsent(ctx, key, []byte(databaseID))
		} else {
			// A stale record of an earlier incarnation, whose teardown
			// finished without clearing it.
			written, err = store.CompareAndSwap(ctx, key, cur, []byte(databaseID))
		}
		if err != nil {
			return &CatalogWriteError{Key: key, Err: err}
		}
		if written {
			return nil
		}
	}
	return &CatalogWriteError{Key: key, Err: errDropRecordContended}
}

// maxDropRecordAttempts bounds PutDropRecord's compare-and-set retries under
// concurrent drops of the same name.
const maxDropRecordAttempts = 8

var errDropRecordContended = errors.New("database: drop record contended")

// DropRecordSupersededError refuses a drop record for an incarnation of
// TenantID's database Name that the catalog no longer names: a delayed drop
// of a predecessor, whose drop another call completed, must not overwrite
// the recovery record of a successor created under the name since.
type DropRecordSupersededError struct {
	TenantID   string
	Name       string
	DatabaseID string
}

func (e *DropRecordSupersededError) Error() string {
	return "database: drop of " + strconv.Quote(e.Name) + " (id " + e.DatabaseID +
		") in tenant " + strconv.Quote(e.TenantID) + " is superseded: the catalog no longer names that incarnation"
}

// DropRecord implements DropRecorder.
func (c *KVCatalog) DropRecord(ctx context.Context, tenantID, name string) (string, error) {
	key := DropRecordKey(tenantID, name)
	store, err := c.store(ctx, tenantID)
	if err != nil {
		return "", err
	}
	data, err := store.Get(ctx, key)
	if err != nil {
		return "", &CatalogReadError{Key: key, Err: err}
	}
	return string(data), nil
}

// DeleteDropRecord implements DropRecorder. It is a compare-and-delete, so
// a drop finishing late never removes the record of a later drop of a
// database created again under the name.
func (c *KVCatalog) DeleteDropRecord(ctx context.Context, tenantID, name, databaseID string) error {
	key := DropRecordKey(tenantID, name)
	store, err := c.store(ctx, tenantID)
	if err != nil {
		return err
	}
	if _, err := store.CompareAndDelete(ctx, key, []byte(databaseID)); err != nil {
		return &CatalogWriteError{Key: key, Err: err}
	}
	return nil
}

// dropRecorder returns the catalog's DropRecorder, or nil when the catalog
// keeps none.
func (m *Manager) dropRecorder() DropRecorder {
	ref := m.catalogRef.Load()
	if ref == nil {
		return nil
	}
	r, _ := ref.Catalog.(DropRecorder)
	return r
}

// RecordDrop writes the drop record of tenantID's database name, of
// databaseID, before a drop removes the catalog record (see
// DropRecordKeyPrefix). A catalog that keeps no drop records records none.
func (m *Manager) RecordDrop(ctx context.Context, tenantID, name, databaseID string) error {
	if r := m.dropRecorder(); r != nil {
		return r.PutDropRecord(ctx, tenantID, name, databaseID)
	}
	return nil
}

// DroppedID returns the id the drop record of tenantID's database name
// carries, or "" when there is none.
func (m *Manager) DroppedID(ctx context.Context, tenantID, name string) (string, error) {
	if r := m.dropRecorder(); r != nil {
		return r.DropRecord(ctx, tenantID, name)
	}
	return "", nil
}

// ClearDropRecord removes the drop record of tenantID's database name once
// the drop of databaseID has torn down everything it names.
func (m *Manager) ClearDropRecord(ctx context.Context, tenantID, name, databaseID string) error {
	if r := m.dropRecorder(); r != nil {
		return r.DeleteDropRecord(ctx, tenantID, name, databaseID)
	}
	return nil
}
