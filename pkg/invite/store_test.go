// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
//  1. GNU Affero General Public License v3.0 (AGPL-3.0)
//
//  2. Commercial License
//     Contact licensing@automatethethings.com for commercial licensing options.

package invite

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// memKV is an in-memory kvstore.AtomicKVStore for these tests.
//
// PutIfAbsent holds one exclusive lock across the check and the write. That is
// the whole contract: releasing between them would let two goroutines both see
// the key missing and both write, and the concurrency test below would then
// pass for the wrong reason.
type memKV struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemKV() *memKV { return &memKV{data: make(map[string][]byte)} }

func (m *memKV) Put(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = append([]byte(nil), value...)
	return nil
}

func (m *memKV) PutIfAbsent(_ context.Context, key string, value []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.data[key]; exists {
		return false, nil
	}
	m.data[key] = append([]byte(nil), value...)
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (m *memKV) CompareAndDelete(_ context.Context, key string, expected []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.data[key]
	if !ok {
		return false, nil
	}
	if !bytes.Equal(current, expected) {
		return false, nil
	}
	delete(m.data, key)
	return true, nil
}

// CompareAndSwap is the edit half of the same guarded set: it replaces the
// value only when the stored one still equals expected, and does NOT create an
// absent key. Implemented for real because this store is asserted to
// kvstore.AtomicKVStore at RUN time — a stub would make the assertion succeed
// while the behaviour it promises is absent (go-qrdb#56).
func (m *memKV) CompareAndSwap(_ context.Context, key string, expected, next []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, ok := m.data[key]
	if !ok || !bytes.Equal(current, expected) {
		return false, nil
	}
	valueCopy := make([]byte, len(next))
	copy(valueCopy, next)
	m.data[key] = valueCopy
	return true, nil
}

func (m *memKV) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.data[key]
	if !ok {
		return nil, nil
	}
	return append([]byte(nil), v...), nil
}

func (m *memKV) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *memKV) Scan(_ context.Context, prefix string, fn func(key string, value []byte) error) error {
	m.mu.Lock()
	snapshot := make(map[string][]byte, len(m.data))
	for k, v := range m.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			snapshot[k] = append([]byte(nil), v...)
		}
	}
	m.mu.Unlock()

	for k, v := range snapshot {
		if err := fn(k, v); err != nil {
			return err
		}
	}
	return nil
}

func (m *memKV) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for k := range m.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *memKV) Exists(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.data[key]
	return ok, nil
}

func (m *memKV) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error { return nil }

func (m *memKV) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}

func (m *memKV) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

func (m *memKV) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

func (m *memKV) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

func newTestStore(t *testing.T) *DAOStore {
	t.Helper()
	store, err := NewDAOStore(newMemKV())
	if err != nil {
		t.Fatalf("NewDAOStore: %v", err)
	}
	return store
}

// TestCreateReturnsTokenAndStoresOnlyItsDigest is the confidentiality property.
//
// A stolen database must not yield redeemable invitations, so the plaintext
// token exists only in the value returned to the caller. Its digest is what
// reaches storage — the same reasoning that keeps password hashes rather than
// passwords.
func TestCreateReturnsTokenAndStoresOnlyItsDigest(t *testing.T) {
	kv := newMemKV()
	store, err := NewDAOStore(kv)
	if err != nil {
		t.Fatalf("NewDAOStore: %v", err)
	}

	token, entity, err := store.Create(context.Background(), "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if token == "" {
		t.Fatal("Create returned an empty token")
	}
	if entity.TokenHash != TokenDigest(token) {
		t.Fatal("stored digest does not match the issued token")
	}

	kv.mu.Lock()
	defer kv.mu.Unlock()
	for key, value := range kv.data {
		if bytesContain(value, []byte(token)) {
			t.Fatalf("plaintext token found in stored value under %q", key)
		}
	}
}

// bytesContain reports whether haystack contains needle.
func bytesContain(haystack, needle []byte) bool {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == string(needle) {
			return true
		}
	}
	return false
}

// TestRedeemConsumesExactlyOnce is the operation's defining property.
func TestRedeemConsumesExactlyOnce(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	token, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	entity, err := store.Redeem(ctx, token)
	if err != nil {
		t.Fatalf("first Redeem: %v", err)
	}
	if entity.UserID != "user-1" {
		t.Errorf("redeemed invitation is for %q, want user-1", entity.UserID)
	}

	if _, err := store.Redeem(ctx, token); !errors.Is(err, ErrAlreadyRedeemed) {
		t.Fatalf("second Redeem err = %v, want ErrAlreadyRedeemed", err)
	}
	if _, err := store.Lookup(ctx, token); !errors.Is(err, ErrAlreadyRedeemed) {
		t.Fatalf("Lookup after redemption err = %v, want ErrAlreadyRedeemed", err)
	}
}

// TestRedeemIsSingleUseUnderConcurrency is the test the whole atomic-write
// effort exists for.
//
// Sixteen goroutines race to redeem one token. Exactly one may win. Before
// PutIfAbsent existed this could only be done with a read-then-write, where two
// callers both observe "unredeemed" and both proceed — which for a first-factor
// enrollment means two different authenticators bound to one identity.
func TestRedeemIsSingleUseUnderConcurrency(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	token, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	const racers = 16
	var (
		start   = make(chan struct{})
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
		other   []error
	)

	wg.Add(racers)
	for i := 0; i < racers; i++ {
		go func() {
			defer wg.Done()
			<-start
			_, redeemErr := store.Redeem(ctx, token)

			mu.Lock()
			defer mu.Unlock()
			switch {
			case redeemErr == nil:
				winners++
			case errors.Is(redeemErr, ErrAlreadyRedeemed):
				// The expected outcome for everyone who lost.
			default:
				other = append(other, redeemErr)
			}
		}()
	}
	close(start)
	wg.Wait()

	if winners != 1 {
		t.Fatalf("%d goroutines redeemed the same invitation, want exactly 1", winners)
	}
	if len(other) > 0 {
		t.Fatalf("unexpected errors from losing racers: %v", other)
	}
}

// TestExpiredInvitationIsRefused bounds an invitation in time as well as in
// count: a link that leaks later must be worthless even if never redeemed.
func TestExpiredInvitationIsRefused(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	token, entity, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Age the invitation by rewriting it with an expiry in the past.
	entity.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	if err := store.invites.Save(ctx, entity); err != nil {
		t.Fatalf("Save aged invite: %v", err)
	}

	if _, err := store.Lookup(ctx, token); !errors.Is(err, ErrExpired) {
		t.Fatalf("Lookup err = %v, want ErrExpired", err)
	}
	if _, err := store.Redeem(ctx, token); !errors.Is(err, ErrExpired) {
		t.Fatalf("Redeem err = %v, want ErrExpired", err)
	}
}

// TestUnknownTokenIsNotFound covers the lookup miss, and pins that a wrong
// token never resolves to somebody else's invitation.
func TestUnknownTokenIsNotFound(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if _, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour); err != nil {
		t.Fatalf("Create: %v", err)
	}

	other, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if _, err := store.Lookup(ctx, other); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Lookup(other) err = %v, want ErrNotFound", err)
	}
	if _, err := store.Lookup(ctx, ""); !errors.Is(err, ErrEmptyToken) {
		t.Fatalf("Lookup(empty) err = %v, want ErrEmptyToken", err)
	}
}

// TestCreateBadInput covers the refusals.
func TestCreateBadInput(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if _, _, err := store.Create(ctx, "", "tenant-a", time.Hour); !errors.Is(err, ErrMissingUserID) {
		t.Errorf("empty userID err = %v, want ErrMissingUserID", err)
	}
	// A non-positive TTL would make the invitation a permanent bearer
	// credential, which is why it is refused rather than defaulted.
	for _, ttl := range []time.Duration{0, -time.Second} {
		if _, _, err := store.Create(ctx, "user-1", "tenant-a", ttl); !errors.Is(err, ErrNonPositiveTTL) {
			t.Errorf("ttl %v err = %v, want ErrNonPositiveTTL", ttl, err)
		}
	}
}

// TestNewDAOStoreRefusesNonAtomicStore proves the capability is required at
// construction.
//
// Discovering the limitation on the first redemption would be far too late: the
// invitation would already be redeemable twice.
func TestNewDAOStoreRefusesNonAtomicStore(t *testing.T) {
	if _, err := NewDAOStore(nil); !errors.Is(err, ErrNilStore) {
		t.Fatalf("NewDAOStore(nil) err = %v, want ErrNilStore", err)
	}

	// The refusal is the stack's one atomic-refusal marker, and it proves
	// nothing was written.
	_, err := NewDAOStore(plainKV{})
	if !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Fatalf("NewDAOStore(non-atomic) err = %v, want kvstore.ErrAtomicWriteUnsupported", err)
	}
	if !errors.Is(err, kvstore.ErrNotApplied) {
		t.Fatalf("NewDAOStore(non-atomic) err = %v, want it to match kvstore.ErrNotApplied", err)
	}
}

// plainKV implements kvstore.KVStore but NOT kvstore.AtomicKVStore.
type plainKV struct{}

func (plainKV) Put(context.Context, string, []byte) error   { return nil }
func (plainKV) Get(context.Context, string) ([]byte, error) { return nil, nil }
func (plainKV) Delete(context.Context, string) error        { return nil }
func (plainKV) List(context.Context, string) ([]string, error) {
	return nil, nil
}
func (plainKV) Exists(context.Context, string) (bool, error) { return false, nil }
func (plainKV) Scan(context.Context, string, func(string, []byte) error) error {
	return nil
}
func (plainKV) RegisterEntityIndexes(string, []kvstore.EntityIndex) error { return nil }
func (plainKV) QueryIndex(context.Context, string, []byte) ([][]byte, error) {
	return nil, nil
}
func (plainKV) ScanIndex(context.Context, string, []byte, []byte) (map[string][][]byte, error) {
	return nil, nil
}

func (plainKV) QueryCompoundIndex(context.Context, string, [][]byte) ([][]byte, error) {
	return nil, nil
}

func (plainKV) ScanCompoundIndex(context.Context, string, [][]byte, []byte, []byte) (map[string][][]byte, error) {
	return nil, nil
}

// TestGCRemovesSpentInvitations checks the collector clears redeemed and
// expired records and leaves live ones alone.
func TestGCRemovesSpentInvitations(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	liveToken, _, err := store.Create(ctx, "live-user", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create(live): %v", err)
	}
	spentToken, _, err := store.Create(ctx, "spent-user", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create(spent): %v", err)
	}
	if _, err := store.Redeem(ctx, spentToken); err != nil {
		t.Fatalf("Redeem(spent): %v", err)
	}

	removed, err := store.GC(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if removed != 1 {
		t.Fatalf("GC removed %d invitations, want 1", removed)
	}

	if _, err := store.Lookup(ctx, liveToken); err != nil {
		t.Fatalf("the live invitation must survive GC: %v", err)
	}
	if _, err := store.Lookup(ctx, spentToken); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Lookup(spent) after GC err = %v, want ErrNotFound", err)
	}
}

// TestTenantIsolationOfInvitations pins that an invitation carries its tenant,
// so a redeeming handler can refuse a token issued for another tenant rather
// than discovering the mismatch after enrolling a factor.
func TestTenantIsolationOfInvitations(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	tokenA, _, err := store.Create(ctx, "user-1", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create(a): %v", err)
	}
	tokenB, _, err := store.Create(ctx, "user-2", "tenant-b", time.Hour)
	if err != nil {
		t.Fatalf("Create(b): %v", err)
	}

	a, err := store.Lookup(ctx, tokenA)
	if err != nil {
		t.Fatalf("Lookup(a): %v", err)
	}
	b, err := store.Lookup(ctx, tokenB)
	if err != nil {
		t.Fatalf("Lookup(b): %v", err)
	}
	if a.TenantID != "tenant-a" || b.TenantID != "tenant-b" {
		t.Fatalf("tenants = %q/%q, want tenant-a/tenant-b", a.TenantID, b.TenantID)
	}
	if a.TokenHash == b.TokenHash {
		t.Fatal("two invitations share a digest")
	}
}

// stubResolver routes each tenant to its own KV store, mimicking what a
// barrier-isolated deployment does: tenant A's data physically lives somewhere
// tenant B's store cannot see.
type stubResolver struct {
	mu     sync.Mutex
	stores map[string]*memKV
	fail   map[string]error
}

func newStubResolver() *stubResolver {
	return &stubResolver{stores: make(map[string]*memKV), fail: make(map[string]error)}
}

// Resolve mirrors the production resolver, which namespaces per TENANT: the
// store serves identity, and identity is tenant-wide rather than per-database.
func (r *stubResolver) Resolve(ctx context.Context, tenantID string) (kvstore.KVStore, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err, sealed := r.fail[tenantID]; sealed {
		return nil, err
	}
	store, ok := r.stores[tenantID]
	if !ok {
		store = newMemKV()
		r.stores[tenantID] = store
	}
	return store, nil
}

// TestTenantStoreIsolatesInvitationsByStore is the isolation property, and it
// is stronger than labelling.
//
// A token issued for tenant A must not merely be *marked* as A's — it must be
// absent from tenant B's store entirely, so a redemption attempted against the
// wrong tenant is indistinguishable from a fabricated token. Anything weaker
// lets a caller probe across tenants to learn that a token exists somewhere.
func TestTenantStoreIsolatesInvitationsByStore(t *testing.T) {
	store, err := NewTenantStore(newStubResolver())
	if err != nil {
		t.Fatalf("NewTenantStore: %v", err)
	}
	ctx := context.Background()

	tokenA, _, err := store.Create(ctx, "user-a", "tenant-a", time.Hour)
	if err != nil {
		t.Fatalf("Create(tenant-a): %v", err)
	}

	if _, err := store.Lookup(ctx, "tenant-b", tokenA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant Lookup err = %v, want ErrNotFound", err)
	}
	if _, err := store.Redeem(ctx, "tenant-b", tokenA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant Redeem err = %v, want ErrNotFound", err)
	}

	// And the token is still usable in its own tenant: the refusal above was
	// isolation, not consumption.
	if _, err := store.Redeem(ctx, "tenant-a", tokenA); err != nil {
		t.Fatalf("Redeem in the owning tenant: %v", err)
	}
}

// TestTenantStoreSealedTenantFailsClosed pins that a sealed tenant surfaces the
// resolver's error rather than being treated as a tenant with no invitations —
// which would turn "temporarily unavailable" into "this token does not exist".
func TestTenantStoreSealedTenantFailsClosed(t *testing.T) {
	resolver := newStubResolver()
	sealedErr := errors.New("tenant is sealed")
	resolver.fail["tenant-sealed"] = sealedErr

	store, err := NewTenantStore(resolver)
	if err != nil {
		t.Fatalf("NewTenantStore: %v", err)
	}

	if _, _, err := store.Create(context.Background(), "u", "tenant-sealed", time.Hour); !errors.Is(err, sealedErr) {
		t.Fatalf("Create on a sealed tenant err = %v, want the resolver error", err)
	}
	if _, err := store.Lookup(context.Background(), "tenant-sealed", "tok"); !errors.Is(err, sealedErr) {
		t.Fatalf("Lookup on a sealed tenant err = %v, want the resolver error", err)
	}
}

// TestNewTenantStoreRequiresResolver refuses construction without a resolver:
// defaulting to one shared store would put every tenant's invitations together,
// which is the isolation this type exists to provide.
func TestNewTenantStoreRequiresResolver(t *testing.T) {
	if _, err := NewTenantStore(nil); !errors.Is(err, ErrNilStoreResolver) {
		t.Fatalf("NewTenantStore(nil) err = %v, want ErrNilStoreResolver", err)
	}
}

// TestTenantStoreIsolatesInvitationsByDatabase covers a review finding: the
// TenantStore cached its DAOStore by tenantID alone while the resolver
// resolves per {tenant}/{db}/.
//
// TestTenantStoreInvitationsAreTenantWide replaces a test that asserted a
// tenant's databases hold SEPARATE invitations.
//
// That followed from an earlier draft where StoreResolver namespaced at
// "{tenant}/{db}/". It namespaces at "{tenant}/" now, because the store also
// serves identity and identity is tenant-wide — namespacing it per database
// broke login outright, since firstboot writes the admin user with no database
// bound. An invitation is identity too: it is issued to a person for a tenant,
// not for one of that tenant's databases, so it must redeem the same whichever
// database the redeeming request names.
func TestTenantStoreInvitationsAreTenantWide(t *testing.T) {
	resolver := newStubResolver()
	store, err := NewTenantStore(resolver)
	if err != nil {
		t.Fatalf("NewTenantStore: %v", err)
	}

	ctxA := tenant.WithDatabase(context.Background(), "alpha")
	ctxB := tenant.WithDatabase(context.Background(), "beta")

	token, _, err := store.Create(ctxA, "user-1", "acme", time.Hour)
	if err != nil {
		t.Fatalf("Create in database alpha: %v", err)
	}

	if _, err := store.Lookup(ctxB, "acme", token); err != nil {
		t.Fatalf("Lookup from database beta = %v; an invitation is tenant-wide and "+
			"must resolve whichever database the request names", err)
	}
	if _, err := store.Redeem(ctxB, "acme", token); err != nil {
		t.Errorf("Redeem from database beta: %v", err)
	}

	// And it is single-use across the tenant, not once per database.
	if _, err := store.Redeem(ctxA, "acme", token); !errors.Is(err, ErrAlreadyRedeemed) {
		t.Errorf("second Redeem = %v; want ErrAlreadyRedeemed — one invitation, one use", err)
	}
}

// TestTenantStoreFailsClosedAfterTenantBecomesSealed covers a review finding:
// a fail-OPEN path created by consulting the cache before the resolver.
//
// A lookup for an UNKNOWN tenant used to cache that tenant's shared-prefix
// store under its id. If the tenant was then created with barrier isolation
// and its barrier was sealed, every later invitation operation kept using the
// cached unencrypted shared store — never consulting the barrier, never
// surfacing ErrTenantSealed, and operating on plaintext while the barrier sat
// sealed. That is the precise opposite of the fail-closed guarantee
// TenantStore documents.
//
// The resolver now runs on every call, before any cache lookup, and the cache
// is keyed by the RESOLVED store rather than the tenant id.
func TestTenantStoreFailsClosedAfterTenantBecomesSealed(t *testing.T) {
	resolver := newStubResolver()
	store, err := NewTenantStore(resolver)
	if err != nil {
		t.Fatalf("NewTenantStore: %v", err)
	}
	ctx := context.Background()

	// Prime the cache while the tenant is still unknown / unsealed.
	if _, err := store.Lookup(ctx, "acme", "no-such-token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("priming Lookup = %v; want ErrNotFound", err)
	}

	// The tenant is now barrier-isolated and its barrier is sealed.
	resolver.mu.Lock()
	resolver.fail["acme"] = tenant.ErrTenantSealed
	resolver.mu.Unlock()

	// EVERY operation must now fail closed, not serve the cached store.
	if _, _, err := store.Create(ctx, "user-1", "acme", time.Hour); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Errorf("Create = %v; want ErrTenantSealed — a cached store must not outlive sealing", err)
	}
	if _, err := store.Lookup(ctx, "acme", "tok"); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Errorf("Lookup = %v; want ErrTenantSealed", err)
	}
	if _, err := store.Redeem(ctx, "acme", "tok"); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Errorf("Redeem = %v; want ErrTenantSealed", err)
	}
	if _, err := store.GC(ctx, "acme", time.Now()); !errors.Is(err, tenant.ErrTenantSealed) {
		t.Errorf("GC = %v; want ErrTenantSealed", err)
	}
}

// nonComparableKV is a valid kvstore.KVStore whose dynamic type is NOT
// comparable — a named slice with value receivers. Nothing in the interface
// forbids this, and StoreResolver returns the interface, so an exported type
// that uses the resolved value as a map key must cope with it.
type nonComparableKV []byte

func (nonComparableKV) Put(context.Context, string, []byte) error { return nil }
func (nonComparableKV) Get(context.Context, string) ([]byte, error) {
	return nil, nil
}
func (nonComparableKV) Delete(context.Context, string) error { return nil }
func (nonComparableKV) Scan(context.Context, string, func(string, []byte) error) error {
	return nil
}
func (nonComparableKV) List(context.Context, string) ([]string, error) { return nil, nil }
func (nonComparableKV) Exists(context.Context, string) (bool, error)   { return false, nil }
func (nonComparableKV) RegisterEntityIndexes(string, []kvstore.EntityIndex) error {
	return kvstore.ErrIndexUnsupported
}
func (nonComparableKV) QueryIndex(context.Context, string, []byte) ([][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}
func (nonComparableKV) ScanIndex(context.Context, string, []byte, []byte) (map[string][][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}
func (nonComparableKV) QueryCompoundIndex(context.Context, string, [][]byte) ([][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}
func (nonComparableKV) ScanCompoundIndex(context.Context, string, [][]byte, []byte, []byte) (map[string][][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

// PutIfAbsent makes the fake an AtomicKVStore, which NewDAOStore requires —
// invitations are single-use, so the store asserts the capability at boot.
func (nonComparableKV) PutIfAbsent(context.Context, string, []byte) (bool, error) {
	return true, nil
}

// CompareAndDelete mirrors PutIfAbsent's guard on the release side:
// it removes the key only when the stored value still equals expected.
func (nonComparableKV) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return false, nil
}

func (nonComparableKV) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return false, nil
}

// nonComparableResolver hands back a non-comparable store.
type nonComparableResolver struct{}

func (nonComparableResolver) Resolve(context.Context, string) (kvstore.KVStore, error) {
	return nonComparableKV{}, nil
}

// TestTenantStoreToleratesANonComparableStore covers a review finding.
//
// Caching by resolved KVStore identity fixed a fail-open (see
// TestTenantStoreFailsClosedAfterTenantBecomesSealed) but introduced a Go
// hazard: kvstore.KVStore is an interface, nothing requires implementations to
// be comparable, and a non-comparable dynamic type used as a sync.Map key
// panics with "hash of unhashable type" on EVERY operation. The previous
// tenant-id key accepted any conforming resolver, so this was a regression in
// what the exported type accepts.
//
// The cache is an optimization; correctness must not depend on the caller's
// choice of representation. A non-comparable store now skips the cache.
func TestTenantStoreToleratesANonComparableStore(t *testing.T) {
	store, err := NewTenantStore(nonComparableResolver{})
	if err != nil {
		t.Fatalf("NewTenantStore: %v", err)
	}
	ctx := context.Background()

	// Every one of these would panic with "hash of unhashable type" if the
	// resolved store were used as a sync.Map key.
	if _, _, err := store.Create(ctx, "user-1", "acme", time.Hour); err != nil {
		t.Errorf("Create: %v", err)
	}
	if _, err := store.Lookup(ctx, "acme", "tok"); err == nil {
		t.Error("Lookup of an absent token should not succeed")
	}
	if _, err := store.Redeem(ctx, "acme", "tok"); err == nil {
		t.Error("Redeem of an absent token should not succeed")
	}
	if _, err := store.GC(ctx, "acme", time.Now()); err != nil {
		t.Errorf("GC: %v", err)
	}
}
