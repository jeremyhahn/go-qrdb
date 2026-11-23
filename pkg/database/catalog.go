package database

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sync"
	"time"

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

// catalogGenerationPrefix is the key prefix of the per-name generation
// counters (catalogGenerationKey).
const catalogGenerationPrefix = "__sys:dbgeneration/"

// catalogGenerationKey is the key of the generation counter of tenantID's
// database name: the highest generation any create of the name was given,
// as 8 big-endian bytes. It lives in the same store as the name's record
// and Delete never removes it, so there is one key per name ever created
// and a name created again after a drop counts on from its last create.
func catalogGenerationKey(tenantID, name string) string {
	return catalogGenerationPrefix + tenantid.Normalize(tenantID) + "/" + name
}

// nextGeneration advances the generation counter at key in store by one,
// by compare-and-swap (retrying a lost swap), and returns the new value.
// A counter that is absent, or not 8 bytes, counts from zero.
func nextGeneration(ctx context.Context, store kvstore.AtomicKVStore, key string) (uint64, error) {
	for {
		current, err := store.Get(ctx, key)
		if err != nil {
			return 0, err
		}
		var last uint64
		if len(current) == 8 {
			last = binary.BigEndian.Uint64(current)
		}
		next := make([]byte, 8)
		binary.BigEndian.PutUint64(next, last+1)
		var swapped bool
		if current == nil {
			swapped, err = store.PutIfAbsent(ctx, key, next)
		} else {
			swapped, err = store.CompareAndSwap(ctx, key, current, next)
		}
		if err != nil {
			return 0, err
		}
		if swapped {
			return last + 1, nil
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
}

// RootClaimKeyPrefix is the system-store key prefix of the root claims of
// databases whose records live in an isolated tenant's home store. A claim
// holds only a record's placement (tenant, name, incarnation, backend,
// protocol, mode, data_dir), never its connection, SQL or barrier
// settings, so the store roots a database owns stay readable while its
// tenant's home is sealed: a root-collision check never needs to unseal a
// tenant to keep another tenant's database off its roots. A claim's key is
// RootClaimKeyPrefix + <tenant> + "/" + <incarnation> + "/" + <name>.
const RootClaimKeyPrefix = "__sys:dbrootclaim/"

// RootClaimIndexedPrefix marks, per tenant, that the tenant's root claims
// cover every record in its home store: ListAll writes it after it has
// claimed every record it read there. A sealed tenant without the marker
// (records created before claims existed, never listed since) cannot be
// skipped and fails a root listing closed.
const RootClaimIndexedPrefix = "__sys:dbrootclaim-indexed/"

func rootClaimKey(tenantID, incarnation, name string) string {
	return RootClaimKeyPrefix + tenantid.Normalize(tenantID) + "/" + incarnation + "/" + name
}

func rootClaimIndexedKey(tenantID string) string {
	return RootClaimIndexedPrefix + tenantid.Normalize(tenantID)
}

// rootClaim is the placement-only projection of meta a root claim stores.
func rootClaim(meta DatabaseMeta) ([]byte, error) {
	return json.Marshal(DatabaseMeta{
		TenantID:    meta.TenantID,
		Name:        meta.Name,
		Incarnation: meta.Incarnation,
		Backend:     meta.Backend,
		Protocol:    meta.Protocol,
		Mode:        meta.Mode,
		DataDir:     meta.DataDir,
	})
}

// Catalog is the replicated record of every database: what Manager reads to
// open a database and writes when one is created, reconfigured or dropped.
// It replaces any record on a node's own disk, so every node sees the
// databases every other node created.
type Catalog interface {
	// Create records meta. It returns *AlreadyExistsError when a record of
	// the same tenant and name exists, so two nodes creating one name cannot
	// both succeed. The record it writes carries the create's generation
	// (DatabaseMeta.Generation), one higher than any earlier create of the
	// tenant's name, whatever meta.Generation holds; a drop does not reset
	// the count.
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

	// claimLocks holds one mutex per tenant. Create holds it across the
	// root claim put and the record put, and the stale-claim reaper holds
	// it across its record read and claim scan, so in this process the
	// reaper never sees a claim before its record. A second process
	// writing the same tenant home is not covered: the reaper there may
	// free the claim of a create in flight here, and a claim either
	// process leaves only over-reserves its root until the next reap.
	claimMu    sync.Mutex
	claimLocks map[string]*sync.Mutex
}

// claimReleaseTimeout bounds the best-effort root claim release of a create
// whose record write failed.
const claimReleaseTimeout = 10 * time.Second

// lockClaims locks tenantID's root claims and returns the unlock.
func (c *KVCatalog) lockClaims(tenantID string) func() {
	id := tenantid.Normalize(tenantID)
	c.claimMu.Lock()
	if c.claimLocks == nil {
		c.claimLocks = make(map[string]*sync.Mutex)
	}
	mu, ok := c.claimLocks[id]
	if !ok {
		mu = &sync.Mutex{}
		c.claimLocks[id] = mu
	}
	c.claimMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// releaseClaim deletes the root claim of a create whose record write did
// not land. It runs on a fresh bounded context, so a cancelled create still
// frees its root.
func (c *KVCatalog) releaseClaim(ctx context.Context, claimKey string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claimReleaseTimeout)
	defer cancel()
	if err := c.system.Delete(ctx, claimKey); err != nil {
		return &CatalogWriteError{Key: claimKey, Err: err}
	}
	return nil
}

// reapStaleClaims deletes each of tenantID's root claims that no record in
// records backs (same incarnation and name): the claim of a failed create,
// or of a delete that crashed between its record delete and its claim
// delete. records must be the tenant home's records read under the claim
// lock, which the caller holds. A sealed tenant is never reaped: its home
// is unreadable.
func (c *KVCatalog) reapStaleClaims(ctx context.Context, tenantID string, records []DatabaseMeta) error {
	type claimID struct{ incarnation, name string }
	live := make(map[claimID]struct{}, len(records))
	for _, meta := range records {
		live[claimID{meta.Incarnation, meta.Name}] = struct{}{}
	}
	type staleClaim struct {
		key   string
		value []byte
	}
	var stale []staleClaim
	prefix := RootClaimKeyPrefix + tenantid.Normalize(tenantID) + "/"
	err := c.system.Scan(ctx, prefix, func(key string, value []byte) error {
		claim, err := decodeCatalogRecord(key, value)
		if err != nil {
			return err
		}
		if _, ok := live[claimID{claim.Incarnation, claim.Name}]; !ok {
			stale = append(stale, staleClaim{key: key, value: bytes.Clone(value)})
		}
		return nil
	})
	if err != nil {
		var readErr *CatalogReadError
		if errors.As(err, &readErr) {
			return &CatalogWriteError{Key: readErr.Key, Err: readErr}
		}
		return &CatalogWriteError{Key: prefix, Err: err}
	}
	for _, claim := range stale {
		if _, err := c.system.CompareAndDelete(ctx, claim.key, claim.value); err != nil {
			return &CatalogWriteError{Key: claim.key, Err: err}
		}
	}
	return nil
}

// reapTenantHome reads tenantID's records from its home store under the
// claim lock and reaps the claims none of them backs.
func (c *KVCatalog) reapTenantHome(ctx context.Context, tenantID string, store kvstore.KVStore) error {
	unlock := c.lockClaims(tenantID)
	defer unlock()
	records, err := scanCatalog(ctx, store, CatalogKeyPrefix+tenantid.Normalize(tenantID)+"/")
	if err != nil {
		return err
	}
	return c.reapStaleClaims(ctx, tenantID, records)
}

// readAndReapTenant lists an isolated tenant's records from its readable
// home, reaps the root claims none of them backs, then indexes the claims
// of the records it holds. The record read and the reap share the claim
// lock, so a create in flight in this process keeps its claim.
func (c *KVCatalog) readAndReapTenant(ctx context.Context, tenantID string) ([]DatabaseMeta, error) {
	unlock := c.lockClaims(tenantID)
	defer unlock()
	metas, err := c.ListTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if err := c.reapStaleClaims(ctx, tenantID, metas); err != nil {
		return nil, err
	}
	read := func() ([]DatabaseMeta, error) { return metas, nil }
	if err := c.indexRootClaims(ctx, tenantID, read); err != nil {
		return nil, err
	}
	return metas, nil
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
	store, _, err := c.storeFor(ctx, tenantID)
	return store, err
}

// storeFor is store, and whether the store is the tenant's home (not the
// system store).
// createClaimReleaser is implemented by a catalog whose Create keeps a root
// claim when the record write's outcome is unknown. The manager calls it once
// settlement finds no record of that create, so the claim is released only
// when the record is definitively absent.
type createClaimReleaser interface {
	ReleaseCreateClaim(ctx context.Context, meta DatabaseMeta) error
}

// ReleaseCreateClaim deletes the root claim a Create with an unknown outcome
// kept, after the caller has settled that no record of meta's incarnation
// survives. A record of meta's incarnation still in the tenant's home keeps
// the claim. It is a no-op for a tenant whose records are not in its home.
func (c *KVCatalog) ReleaseCreateClaim(ctx context.Context, meta DatabaseMeta) error {
	store, inHome, err := c.storeFor(ctx, meta.TenantID)
	if err != nil {
		return err
	}
	if !inHome {
		return nil
	}
	unlock := c.lockClaims(meta.TenantID)
	defer unlock()
	data, err := store.Get(ctx, CatalogKey(meta.TenantID, meta.Name))
	if err != nil {
		return err
	}
	if data != nil {
		var stored DatabaseMeta
		if err := json.Unmarshal(data, &stored); err != nil {
			return err
		}
		if stored.Incarnation == meta.Incarnation {
			return nil
		}
	}
	return c.releaseClaim(ctx, rootClaimKey(meta.TenantID, meta.Incarnation, meta.Name))
}

func (c *KVCatalog) storeFor(ctx context.Context, tenantID string) (kvstore.AtomicKVStore, bool, error) {
	if c.homes == nil || tenantid.IsSystem(tenantID) {
		return c.system, false, nil
	}
	inHome, err := c.homes.InHome(ctx, tenantID)
	if err != nil {
		return nil, false, &CatalogStoreError{TenantID: tenantID, Err: err}
	}
	if !inHome {
		return c.system, false, nil
	}
	home, err := c.homes.Resolve(ctx, tenantID)
	if err != nil {
		return nil, false, &CatalogStoreError{TenantID: tenantID, Err: err}
	}
	atomicHome, ok := home.(kvstore.AtomicKVStore)
	if !ok {
		return nil, false, &CatalogStoreNotAtomicError{TenantID: tenantID}
	}
	return atomicHome, true, nil
}

// Create implements Catalog.
func (c *KVCatalog) Create(ctx context.Context, meta DatabaseMeta) error {
	key := CatalogKey(meta.TenantID, meta.Name)
	store, inHome, err := c.storeFor(ctx, meta.TenantID)
	if err != nil {
		return err
	}
	// Number this create first: the name's counter advances whether or not
	// the record write below succeeds, so generations are monotonic, not
	// dense, and no two creates of a name ever share one.
	generation, err := nextGeneration(ctx, store, catalogGenerationKey(meta.TenantID, meta.Name))
	if err != nil {
		return &CatalogWriteError{Key: key, Err: err}
	}
	meta.Generation = generation
	data, err := json.Marshal(meta)
	if err != nil {
		return &CatalogWriteError{Key: key, Err: err}
	}
	// A record in an isolated tenant's home is claimed in the system store
	// first, so no instant exists in which the record holds a root that a
	// sealed-tenant root listing cannot see. A failed record write releases
	// the claim; a claim a crash leaves is reaped by the next read of the
	// tenant's records (reapStaleClaims).
	claimKey := ""
	if inHome {
		// The home is readable now: claim the records it already holds
		// (once per tenant), so the tenant is indexed when it seals.
		existing := func() ([]DatabaseMeta, error) {
			return scanCatalog(ctx, store, CatalogKeyPrefix+tenantid.Normalize(meta.TenantID)+"/")
		}
		if err := c.indexRootClaims(ctx, meta.TenantID, existing); err != nil {
			return err
		}
		claim, err := rootClaim(meta)
		if err != nil {
			return &CatalogWriteError{Key: key, Err: err}
		}
		// Hold the claim lock across the claim put and the record put, so
		// the reaper never sees this claim without its record.
		unlock := c.lockClaims(meta.TenantID)
		defer unlock()
		claimKey = rootClaimKey(meta.TenantID, meta.Incarnation, meta.Name)
		if err := c.system.Put(ctx, claimKey, claim); err != nil {
			return &CatalogWriteError{Key: claimKey, Err: err}
		}
	}
	stored, err := store.PutIfAbsent(ctx, key, data)
	if err != nil {
		writeErr := &CatalogWriteError{Key: key, Err: err}
		// A write that may have committed keeps its claim: the record
		// it may have left must stay visible to a sealed-tenant root
		// listing. The manager releases the claim once settlement finds
		// the record definitively absent (ReleaseCreateClaim); a crash
		// before that leaves a claim the reaper frees.
		if claimKey != "" && !createOutcomeUnknown(writeErr) {
			if releaseErr := c.releaseClaim(ctx, claimKey); releaseErr != nil {
				return errors.Join(writeErr, releaseErr)
			}
		}
		return writeErr
	}
	if !stored {
		if claimKey != "" {
			if err := c.releaseClaim(ctx, claimKey); err != nil {
				return err
			}
		}
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
	store, inHome, err := c.storeFor(ctx, tenantID)
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
			if !inHome {
				return nil
			}
			// The record is gone: release its root claim. A crash before
			// this leaves a claim the next reap of this tenant frees.
			claimKey := rootClaimKey(tenantID, incarnation, name)
			if err := c.system.Delete(ctx, claimKey); err != nil {
				return &CatalogWriteError{Key: claimKey, Err: err}
			}
			return c.reapTenantHome(ctx, tenantID, store)
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

// CatalogTenantLister lists the id of every tenant the registry holds. A
// CatalogHomes that implements it lets ListAll reach the records held in
// every isolated tenant's home store, opened here or not.
type CatalogTenantLister interface {
	TenantIDs(ctx context.Context) ([]string, error)
}

// AllTenantsCatalog is a Catalog that can list the records of every tenant,
// those in isolated tenants' home stores included, not only those List
// returns from the system store.
type AllTenantsCatalog interface {
	ListAll(ctx context.Context) ([]DatabaseMeta, error)
}

// RootListingCatalog is a Catalog that lists the placement of every record,
// a sealed tenant's included (from its root claims), for root-collision
// checks.
type RootListingCatalog interface {
	ListRoots(ctx context.Context) ([]DatabaseMeta, error)
}

var (
	_ AllTenantsCatalog  = (*KVCatalog)(nil)
	_ RootListingCatalog = (*KVCatalog)(nil)
)

// ListAll returns every record the catalog holds: List's, and those of
// every isolated tenant in its home store. A catalog whose homes cannot
// list tenants has no isolated tenant to reach, and returns List's. A
// tenant whose home is sealed is skipped: the records read are returned
// with a *TenantsSkippedError naming it, so one sealed tenant does not hide
// every other tenant's databases. Any other failure fails the listing. A
// caller guarding store roots uses ListRoots, which never skips a root.
func (c *KVCatalog) ListAll(ctx context.Context) ([]DatabaseMeta, error) {
	metas, sealed, err := c.listAll(ctx)
	if err != nil {
		return nil, err
	}
	if len(sealed) > 0 {
		return metas, &TenantsSkippedError{Tenants: sealed}
	}
	return metas, nil
}

// ListRoots returns the placement of every record the catalog holds: ListAll's
// records, and for a tenant whose home is sealed, the root claims the system
// store holds for it (RootClaimKeyPrefix). A sealed tenant whose claims were
// never indexed (RootClaimIndexedPrefix) fails the listing closed, so a
// root-collision check guarded on it never misses a root.
func (c *KVCatalog) ListRoots(ctx context.Context) ([]DatabaseMeta, error) {
	metas, sealed, err := c.listAll(ctx)
	if err != nil {
		return nil, err
	}
	for _, skipped := range sealed {
		indexed, err := c.system.Exists(ctx, rootClaimIndexedKey(skipped.TenantID))
		if err != nil {
			return nil, &CatalogReadError{Key: rootClaimIndexedKey(skipped.TenantID), Err: err}
		}
		if !indexed {
			return nil, &CatalogStoreError{TenantID: skipped.TenantID, Err: skipped.Err}
		}
		claims, err := scanCatalog(ctx, c.system, RootClaimKeyPrefix+tenantid.Normalize(skipped.TenantID)+"/")
		if err != nil {
			return nil, err
		}
		metas = append(metas, claims...)
	}
	return metas, nil
}

// listAll is ListAll's records and the tenants skipped because their home is
// sealed (tenant.ErrTenantSealed). Every other failure is returned. Each
// readable home's records are claimed in the system store and the tenant is
// marked indexed, so ListRoots can serve its roots once it seals.
func (c *KVCatalog) listAll(ctx context.Context) ([]DatabaseMeta, []SkippedTenant, error) {
	metas, err := c.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	lister, ok := c.homes.(CatalogTenantLister)
	if !ok {
		return metas, nil, nil
	}
	ids, err := lister.TenantIDs(ctx)
	if err != nil {
		return nil, nil, &CatalogStoreError{Err: err}
	}
	var sealed []SkippedTenant
	for _, id := range ids {
		if tenantid.IsSystem(id) {
			continue
		}
		inHome, err := c.homes.InHome(ctx, id)
		if err != nil {
			if errors.Is(err, tenant.ErrTenantSealed) {
				sealed = append(sealed, SkippedTenant{TenantID: id, Err: err})
				continue
			}
			return nil, nil, &CatalogStoreError{TenantID: id, Err: err}
		}
		if !inHome {
			// A shared tenant's records are in the system store List read.
			continue
		}
		tenantMetas, err := c.readAndReapTenant(ctx, id)
		if err != nil {
			if errors.Is(err, tenant.ErrTenantSealed) {
				sealed = append(sealed, SkippedTenant{TenantID: id, Err: err})
				continue
			}
			return nil, nil, err
		}
		metas = append(metas, tenantMetas...)
	}
	return metas, sealed, nil
}

// indexRootClaims claims, in the system store, every record of tenantID its
// home holds (records reads them) and marks the tenant indexed, unless it is
// already. It only adds claims: a claim
// without a record is a create in flight or a delete a crash interrupted,
// and over-reserves a root rather than freeing one a record still holds.
func (c *KVCatalog) indexRootClaims(ctx context.Context, tenantID string, records func() ([]DatabaseMeta, error)) error {
	marker := rootClaimIndexedKey(tenantID)
	indexed, err := c.system.Exists(ctx, marker)
	if err != nil {
		return &CatalogReadError{Key: marker, Err: err}
	}
	if indexed {
		// Create claims every record made since the marker was written.
		return nil
	}
	metas, err := records()
	if err != nil {
		return err
	}
	for _, meta := range metas {
		claim, err := rootClaim(meta)
		if err != nil {
			return &CatalogWriteError{Key: CatalogKey(meta.TenantID, meta.Name), Err: err}
		}
		key := rootClaimKey(meta.TenantID, meta.Incarnation, meta.Name)
		if _, err := c.system.PutIfAbsent(ctx, key, claim); err != nil {
			return &CatalogWriteError{Key: key, Err: err}
		}
	}
	if err := c.system.Put(ctx, marker, []byte("1")); err != nil {
		return &CatalogWriteError{Key: marker, Err: err}
	}
	return nil
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

// SkippedTenant is a tenant a listing skipped because its home store is
// sealed on this node; Err matches tenant.ErrTenantSealed.
type SkippedTenant struct {
	TenantID string
	Err      error
}

// TenantsSkippedError is the partial result of ListAll: the records it
// returns alongside it are every readable tenant's, and Tenants are the
// tenants whose sealed homes it could not read. It matches
// tenant.ErrTenantSealed.
type TenantsSkippedError struct {
	Tenants []SkippedTenant
}

func (e *TenantsSkippedError) Error() string {
	msg := "database catalog: listing skipped sealed tenants:"
	for i, t := range e.Tenants {
		if i > 0 {
			msg += ","
		}
		msg += " " + t.TenantID
	}
	return msg
}

// Unwrap returns tenant.ErrTenantSealed.
func (e *TenantsSkippedError) Unwrap() error {
	return tenant.ErrTenantSealed
}
