package database

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// CatalogKeyPrefix is the key prefix of every database catalog record. A
// record's key is CatalogKeyPrefix + <tenant> + "/" + <name> (CatalogKey).
const CatalogKeyPrefix = "__sys:dbcatalog/"

// CatalogKey returns the key of the catalog record of tenantID's database
// name. The system tenant's two spellings ("" and tenantid.System) map to one
// key, so a record written under one is found under the other.
func CatalogKey(tenantID, name string) string {
	return CatalogKeyPrefix + tenantid.Normalize(tenantID) + "/" + name
}

// NewDatabaseID returns a fresh database id: 128 random bits, hex encoded.
// It is opaque (nothing about the database's name or tenant can be read
// from it) and, being random at that width, never reused: a database
// dropped and created again under the same name gets a different id.
func NewDatabaseID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", &DatabaseIDError{Err: err}
	}
	return hex.EncodeToString(b[:]), nil
}

// Catalog is the replicated record of every database: what Manager reads to
// open a database and writes when one is created, reconfigured or dropped.
// It replaces any record on a node's own disk, so every node sees the
// databases every other node created.
type Catalog interface {
	// Create records meta. It returns *AlreadyExistsError when a record of
	// the same tenant and name exists, so two nodes creating one name cannot
	// both succeed.
	Create(ctx context.Context, meta DatabaseMeta) error

	// Update applies mutate to the record of tenantID's database name and
	// writes the result, only while that record carries incarnation. mutate
	// is applied to the record as the catalog holds it, never to the
	// caller's copy of it, so a field mutate does not set keeps what the
	// record holds: another node's configuration change, or the drop fence
	// another node set. It returns *NotFoundError when there is no record,
	// or when the record is of another incarnation: the database the caller
	// holds was dropped, or dropped and created again, and writing its
	// configuration would bring it back or overwrite its successor's. An
	// error mutate returns is returned as it is, with nothing written.
	// mutate may run more than once, and must not change the record's
	// tenant, name or incarnation.
	Update(ctx context.Context, tenantID, name, incarnation string, mutate func(*DatabaseMeta) error) error

	// Get returns the record of tenantID's database name, or
	// *NotFoundError when there is none.
	Get(ctx context.Context, tenantID, name string) (DatabaseMeta, error)

	// Delete removes the record of tenantID's database name, only while it
	// carries incarnation and dropClaim (DatabaseMeta.DropClaim; empty for a
	// record no drop has claimed). A record of another incarnation -- the
	// database was dropped and created again -- is left, with
	// *NotFoundError; a record another drop has claimed since is left, with
	// *DropInProgressError, for that drop to finish. Removing an absent
	// record is not an error, except under a dropClaim: a claimed record
	// that is gone was deleted by the drop that took the claim over, and
	// this delete gets *NotFoundError, so only the drop that removed the
	// record reports success.
	Delete(ctx context.Context, tenantID, name, incarnation, dropClaim string) error

	// List returns every record held in the system store: the system
	// tenant's and every shared tenant's. An isolated tenant's records are
	// in its home store and are listed by ListTenant.
	List(ctx context.Context) ([]DatabaseMeta, error)

	// ListTenant returns every record of tenantID's databases, from
	// whichever store holds them.
	ListTenant(ctx context.Context, tenantID string) ([]DatabaseMeta, error)
}

// catalogRef boxes a Catalog for an atomic.Pointer.
type catalogRef struct{ Catalog }

// SetCatalog attaches the catalog the Manager reads and writes every
// database record through. It is late-bound, like SetShardIDClaimer: the
// Manager exists before the replicated store the catalog lives in. A nil c
// detaches it, after which every operation that needs it fails with
// *CatalogRequiredError.
func (m *Manager) SetCatalog(c Catalog) {
	if c == nil {
		m.catalogRef.Store(nil)
		return
	}
	m.catalogRef.Store(&catalogRef{c})
}

// catalog returns the attached catalog, or *CatalogRequiredError naming op
// when there is none. There is no fallback: a record the Manager kept itself
// would be node-local.
func (m *Manager) catalog(op string) (Catalog, error) {
	ref := m.catalogRef.Load()
	if ref == nil {
		return nil, &CatalogRequiredError{Operation: op}
	}
	return ref.Catalog, nil
}

// CatalogHomes locates and resolves an isolated tenant's home store. The
// tenant package's store resolver implements it.
type CatalogHomes interface {
	tenant.StoreResolver
	tenant.HomeLocator
}

// KVCatalog is the Catalog over the replicated key-value store. The system
// tenant's and every shared tenant's records live in the system store; an
// isolated tenant's live in its home store, never in the system store.
type KVCatalog struct {
	system kvstore.AtomicKVStore
	homes  CatalogHomes
}

var _ Catalog = (*KVCatalog)(nil)

// NewKVCatalog returns the catalog over system, the un-namespaced system
// store, routing an isolated tenant's records through homes. A nil homes
// is a composition with no tenant registry, in which no tenant is isolated
// and every record is in the system store.
func NewKVCatalog(system kvstore.AtomicKVStore, homes CatalogHomes) (*KVCatalog, error) {
	if system == nil {
		return nil, &CatalogStoreRequiredError{}
	}
	return &KVCatalog{system: system, homes: homes}, nil
}

// store returns the store holding tenantID's records.
func (c *KVCatalog) store(ctx context.Context, tenantID string) (kvstore.AtomicKVStore, error) {
	if c.homes == nil || tenantid.IsSystem(tenantID) {
		return c.system, nil
	}
	inHome, err := c.homes.InHome(ctx, tenantID)
	if err != nil {
		return nil, &CatalogStoreError{TenantID: tenantID, Err: err}
	}
	if !inHome {
		return c.system, nil
	}
	home, err := c.homes.Resolve(ctx, tenantID)
	if err != nil {
		return nil, &CatalogStoreError{TenantID: tenantID, Err: err}
	}
	atomicHome, ok := home.(kvstore.AtomicKVStore)
	if !ok {
		return nil, &CatalogStoreNotAtomicError{TenantID: tenantID}
	}
	return atomicHome, nil
}

// Create implements Catalog.
func (c *KVCatalog) Create(ctx context.Context, meta DatabaseMeta) error {
	key := CatalogKey(meta.TenantID, meta.Name)
	if meta.ID == "" {
		return &DatabaseIDRequiredError{Name: meta.Name}
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return &CatalogWriteError{Key: key, Err: err}
	}
	store, err := c.store(ctx, meta.TenantID)
	if err != nil {
		return err
	}
	stored, err := store.PutIfAbsent(ctx, key, data)
	if err != nil {
		return &CatalogWriteError{Key: key, Err: err}
	}
	if !stored {
		return &AlreadyExistsError{Name: meta.Name}
	}
	return nil
}

// Update implements Catalog. It is read, mutate, compare-and-swap against
// the bytes read: a write landing between the read and the swap -- another
// node's update, a drop fence, a drop and re-create -- makes the swap fail,
// and the loop reads the record again and applies mutate to what it now
// holds. Nothing the caller held before the read is ever written back.
func (c *KVCatalog) Update(ctx context.Context, tenantID, name, incarnation string, mutate func(*DatabaseMeta) error) error {
	key := CatalogKey(tenantID, name)
	store, err := c.store(ctx, tenantID)
	if err != nil {
		return err
	}
	for {
		current, err := store.Get(ctx, key)
		if err != nil {
			return &CatalogReadError{Key: key, Err: err}
		}
		if current == nil {
			return &NotFoundError{Name: name}
		}
		recorded, err := decodeCatalogRecord(key, current)
		if err != nil {
			return err
		}
		if recorded.Incarnation != incarnation {
			return &NotFoundError{Name: name}
		}
		if err := mutate(&recorded); err != nil {
			return err
		}
		data, err := json.Marshal(recorded)
		if err != nil {
			return &CatalogWriteError{Key: key, Err: err}
		}
		swapped, err := store.CompareAndSwap(ctx, key, current, data)
		if err != nil {
			return &CatalogWriteError{Key: key, Err: err}
		}
		if swapped {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return &CatalogWriteError{Key: key, Err: err}
		}
	}
}

// Get implements Catalog.
func (c *KVCatalog) Get(ctx context.Context, tenantID, name string) (DatabaseMeta, error) {
	key := CatalogKey(tenantID, name)
	store, err := c.store(ctx, tenantID)
	if err != nil {
		return DatabaseMeta{}, err
	}
	data, err := store.Get(ctx, key)
	if err != nil {
		return DatabaseMeta{}, &CatalogReadError{Key: key, Err: err}
	}
	if data == nil {
		return DatabaseMeta{}, &NotFoundError{Name: name}
	}
	return decodeCatalogRecord(key, data)
}

// Delete implements Catalog. It is read, compare, compare-and-delete against
// the bytes read, never an unconditional delete: a drop paused between its
// read and its delete cannot remove a record written after the read -- a
// successor's, or a claim another drop took over.
func (c *KVCatalog) Delete(ctx context.Context, tenantID, name, incarnation, dropClaim string) error {
	key := CatalogKey(tenantID, name)
	store, err := c.store(ctx, tenantID)
	if err != nil {
		return err
	}
	for {
		current, err := store.Get(ctx, key)
		if err != nil {
			return &CatalogReadError{Key: key, Err: err}
		}
		if current == nil {
			if dropClaim != "" {
				return &NotFoundError{Name: name}
			}
			return nil
		}
		recorded, err := decodeCatalogRecord(key, current)
		if err != nil {
			return err
		}
		if recorded.Incarnation != incarnation {
			return &NotFoundError{Name: name}
		}
		if recorded.DropClaim != dropClaim {
			return &DropInProgressError{Name: name}
		}
		deleted, err := store.CompareAndDelete(ctx, key, current)
		if err != nil {
			return &CatalogWriteError{Key: key, Err: err}
		}
		if deleted {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return &CatalogWriteError{Key: key, Err: err}
		}
	}
}

// List implements Catalog.
func (c *KVCatalog) List(ctx context.Context) ([]DatabaseMeta, error) {
	return scanCatalog(ctx, c.system, CatalogKeyPrefix)
}

// ListTenant implements Catalog.
func (c *KVCatalog) ListTenant(ctx context.Context, tenantID string) ([]DatabaseMeta, error) {
	store, err := c.store(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return scanCatalog(ctx, store, CatalogKeyPrefix+tenantid.Normalize(tenantID)+"/")
}

// scanCatalog decodes every record under prefix in store. A record that
// does not decode fails the whole scan: skipping it would hide a database.
func scanCatalog(ctx context.Context, store kvstore.KVStore, prefix string) ([]DatabaseMeta, error) {
	var metas []DatabaseMeta
	err := store.Scan(ctx, prefix, func(key string, value []byte) error {
		meta, err := decodeCatalogRecord(key, value)
		if err != nil {
			return err
		}
		metas = append(metas, meta)
		return nil
	})
	if err != nil {
		var readErr *CatalogReadError
		if errors.As(err, &readErr) {
			return nil, readErr
		}
		return nil, &CatalogReadError{Key: prefix, Err: err}
	}
	return metas, nil
}

// decodeCatalogRecord decodes the record stored under key. A field this
// build does not know is refused rather than dropped, so a record written
// by a newer node is not rewritten without it.
func decodeCatalogRecord(key string, data []byte) (DatabaseMeta, error) {
	var meta DatabaseMeta
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&meta); err != nil {
		return DatabaseMeta{}, &CatalogReadError{Key: key, Err: err}
	}
	return meta, nil
}
