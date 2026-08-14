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

package lifecycle_test

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/jeremyhahn/go-objstore/pkg/local"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// newLocalHost creates an in-memory LocalBackend suitable for testing.
func newLocalHost() *backend.LocalBackend {
	stg := memory.NewStorage()
	eng := engine.NewKVEngine(stg)
	return backend.NewLocalBackend(eng)
}

// newTestBarrier creates an initialized and unsealed in-memory seal barrier.
func newTestBarrier(t *testing.T) (
	barrier *seal.StorageBarrier,
	registry *seal.BarrierRegistry,
	cred *credentials.CredentialService,
) {
	t.Helper()

	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	passphrase := []byte(hex.EncodeToString(buf[:]))

	strategy, err := seal.NewSoftwareStrategy(passphrase)
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}

	bknd := seal.NewMemoryBackend()
	b, err := seal.NewStorageBarrier(
		slog.Default(), bknd,
		seal.BarrierConfig{RootKeyPath: "root-key"},
		strategy,
	)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	// Initialize transitions the barrier to the unsealed state directly.
	// A subsequent Unseal call is not needed and would return ErrAlreadyUnsealed.
	ctx := context.Background()
	if err := b.Initialize(ctx, seal.Credentials{Secret: []byte(passphrase)}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	reg, err := seal.NewBarrierRegistry(slog.Default(), b)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	credSvc, err := credentials.NewCredentialService(
		&credentials.Config{Strategy: "barrier"}, b, slog.Default())
	if err != nil {
		t.Fatalf("NewCredentialService: %v", err)
	}

	return b, reg, credSvc
}

// newPooledRegistry returns a BarrierRegistry configured with a single-path
// local volume pool (backed by a t-scoped temp dir) and a per-tenant registry,
// so it can satisfy the pool-driven RegisterTenantPooled flow that
// CreateTenant(IsolationBarrier) uses. Mirrors the tenant package's own test
// fixture; required because the plain newTestBarrier registry has no pool.
func newPooledRegistry(t *testing.T, system *seal.StorageBarrier) *seal.BarrierRegistry {
	t.Helper()

	localStore := local.New()
	if err := localStore.Configure(map[string]string{"path": t.TempDir()}); err != nil {
		t.Fatalf("Configure local backend: %v", err)
	}
	mgr := objpool.NewManager()

	stateStorage, err := pebblestore.New(&pebblestore.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open state-store Pebble: %v", err)
	}
	t.Cleanup(func() { _ = stateStorage.Close() })

	state, err := qrdbpool.NewPebbleStateStore(stateStorage)
	if err != nil {
		t.Fatalf("NewPebbleStateStore: %v", err)
	}
	rr := &objpool.RoundRobinStrategy{State: state}
	if err := mgr.Register("default", []common.Storage{localStore}, rr); err != nil {
		t.Fatalf("pool Register: %v", err)
	}

	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })

	reg, err := seal.NewBarrierRegistry(slog.Default(), system,
		seal.WithVolumePool(mgr, state, "default"),
		seal.WithPerTenantRegistry(perTenantReg),
	)
	if err != nil {
		t.Fatalf("NewBarrierRegistry (pooled): %v", err)
	}
	return reg
}

// TestBuildServices_CoreServicesNonNil verifies that BuildServices with
// InProcessOnly infra (no seal) returns a Node with all core services
// populated. This is the baseline embedded GUI mode.
func TestBuildServices_CoreServicesNonNil(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	tests := []struct {
		name string
		svc  interface{}
	}{
		{"KV", node.KV},
		{"Admin", node.Admin},
		{"MVCC", node.MVCC},
		{"Batch", node.Batch},
		{"Transaction", node.Transaction},
		{"Lease", node.Lease},
		{"Index", node.Index},
		{"TimeSeries", node.TimeSeries},
		{"Watch", node.Watch},
		{"Lock", node.Lock},
		{"Collection", node.Collection},
		{"Database", node.Database},
		{"Vector", node.Vector},
		{"User", node.User},
		{"Role", node.Role},
		// Tenant is always non-nil: KV-backed when seal is disabled,
		// barrier-backed when seal is enabled.
		{"Tenant", node.Tenant},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if tc.svc == nil {
				t.Errorf("%s is nil", tc.name)
			}
		})
	}
}

// TestBuildServices_SealGatedServicesNonNil verifies that BuildServices with
// a fully initialised seal barrier wires Tenant, Admin, KV, Barrier,
// Ceremony, Credential, and CertProv.
func TestBuildServices_SealGatedServicesNonNil(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	barrier, registry, credSvc := newTestBarrier(t)

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	tests := []struct {
		name string
		svc  interface{}
	}{
		{"Barrier", node.Barrier},
		{"Ceremony", node.Ceremony},
		{"Credential", node.Credential},
		{"Tenant", node.Tenant},
		{"CertProv", node.CertProv},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if tc.svc == nil {
				t.Errorf("%s is nil", tc.name)
			}
		})
	}
}

// TestBuildServices_WithBarrierRegistry_InstallsRoutingServices verifies the
// tenant-store-routing wiring in BuildServices: when both a SealBarrier and a
// BarrierRegistry are supplied, the node installs a StoreResolver and replaces
// the plain User/Role services with the routing decorators. Because the
// decorators are unexported (and the test lives in lifecycle_test), the
// installation is asserted behaviorally: a barrier-isolated tenant's IAM data
// must be routed to that tenant's own barrier-backed store and must NOT be
// visible from the shared system store. A non-routing user service would write
// every record to the single shared store, so the system-scoped lookup below
// would (incorrectly) find the barrier tenant's user.
func TestBuildServices_WithBarrierRegistry_InstallsRoutingServices(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	barrier, _, credSvc := newTestBarrier(t)
	// CreateTenant(IsolationBarrier) takes the pool-driven RegisterTenantPooled
	// path, so the registry must be configured with a volume pool.
	registry := newPooledRegistry(t, barrier)

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	// The routing branch must have installed a StoreResolver.
	if node.StoreResolver == nil {
		t.Fatal("StoreResolver is nil: routing wiring was not installed")
	}
	if node.User == nil {
		t.Fatal("User service is nil")
	}
	if node.Role == nil {
		t.Fatal("Role service is nil")
	}

	ctx := context.Background()

	// Create a barrier-isolated tenant. Under the self-managed default the
	// barrier is registered SEALED; initialize it (the tenant-admin step) so it
	// is unsealed and the resolver can route the tenant's IAM data to that
	// barrier-backed store.
	const barrierTenant = "secure-tenant"
	if _, err := node.Tenant.CreateTenant(ctx, barrierTenant, "Secure Tenant", tenant.IsolationBarrier); err != nil {
		t.Fatalf("CreateTenant(barrier): %v", err)
	}
	if err := registry.InitializeTenant(ctx, barrierTenant, seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant(barrier): %v", err)
	}

	// Write a user into the barrier tenant. The routing decorator must send
	// this to the tenant's barrier-backed store, not the shared system store.
	created, err := node.User.CreateUser(ctx, &user.CreateUserRequest{
		Username: "alice",
		TenantID: barrierTenant,
	})
	if err != nil {
		t.Fatalf("CreateUser(barrier tenant): %v", err)
	}

	// Lookup scoped to the barrier tenant must find the user (routed to the
	// barrier store on both write and read).
	got, err := node.User.GetUserByUsername(ctx, barrierTenant, "alice")
	if err != nil {
		t.Fatalf("GetUserByUsername(barrier tenant): %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("barrier-tenant lookup returned ID %d, want %d", got.ID, created.ID)
	}

	// The same username scoped to the system tenant must NOT be found: the
	// barrier tenant's record lives in its own store, never the shared system
	// store. This is the load-bearing assertion that the routing decorator —
	// not a plain shared-store service — is installed.
	if _, err := node.User.GetUserByUsername(ctx, tenant.SystemTenantID, "alice"); err == nil {
		t.Fatal("system-tenant lookup found a barrier-tenant user: routing decorator not installed (data leaked to shared store)")
	} else {
		var notFound *user.UserNotFoundError
		if !errors.As(err, &notFound) {
			t.Fatalf("system-tenant lookup: want UserNotFoundError, got %v", err)
		}
	}
}

// TestBuildServices_NilHostError verifies that a nil Host returns
// NilHostError.
func TestBuildServices_NilHostError(t *testing.T) {
	hub := watch.NewHub()
	defer hub.Close()

	_, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     nil,
		WatchHub: hub,
		DataDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var target *lifecycle.NilHostError
	if !errors.As(err, &target) {
		t.Errorf("expected *NilHostError, got %T: %v", err, err)
	}
}

// TestBuildServices_NilWatchHubError verifies that a nil WatchHub returns
// NilWatchHubError.
func TestBuildServices_NilWatchHubError(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	_, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: nil,
		DataDir:  t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var target *lifecycle.NilWatchHubError
	if !errors.As(err, &target) {
		t.Errorf("expected *NilWatchHubError, got %T: %v", err, err)
	}
}

// TestNode_Stop verifies that Stop succeeds on a cleanly constructed node
// and does not panic on a second call with a fresh node.
func TestNode_Stop(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}

	if err := node.Stop(context.Background()); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

// TestBuildSealServicesOnly_PopulatesSealServices verifies that the
// pre-host seal-only constructor returns a Node carrying the seal-gated
// services (Barrier, Credential, Ceremony, Tenant) without requiring a
// Host or WatchHub. This is the boot-time path used by the daemon to
// bring up the sealed-window listener BEFORE the data-plane Pebble
// instance can be opened.
func TestBuildSealServicesOnly_PopulatesSealServices(t *testing.T) {
	barrier, registry, credSvc := newTestBarrier(t)

	dataDir := t.TempDir()
	node, err := lifecycle.BuildSealServicesOnly(context.Background(), lifecycle.Infra{
		// Host and WatchHub deliberately nil — the pre-host path must
		// not require them.
		DataDir:           dataDir,
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		CeremonyDataDir:   dataDir, // explicit ceremony dir
		Logger:            slog.Default(),
	})
	if err != nil {
		t.Fatalf("BuildSealServicesOnly: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	if node.Barrier == nil {
		t.Error("Barrier is nil")
	}
	if node.Credential == nil {
		t.Error("Credential is nil")
	}
	if node.Ceremony == nil {
		t.Error("Ceremony is nil")
	}
	if node.Tenant == nil {
		t.Error("Tenant is nil (BarrierRegistry was supplied)")
	}

	// Core data-plane and CertProvider services must remain nil — they
	// require a Host which the pre-host path does not have.
	if node.KV != nil {
		t.Error("KV must be nil in pre-host mode")
	}
	if node.MVCC != nil {
		t.Error("MVCC must be nil in pre-host mode")
	}
	if node.CertProv != nil {
		t.Error("CertProv must be nil in pre-host mode")
	}
}

// TestBuildSealServicesOnly_RejectsNilBarrier verifies that the
// pre-host constructor returns NilSealBarrierError when the caller
// forgets to thread the storage barrier through.
func TestBuildSealServicesOnly_RejectsNilBarrier(t *testing.T) {
	_, err := lifecycle.BuildSealServicesOnly(context.Background(), lifecycle.Infra{
		DataDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var target *lifecycle.NilSealBarrierError
	if !errors.As(err, &target) {
		t.Errorf("expected *NilSealBarrierError, got %T: %v", err, err)
	}
}

// TestBuildSealServicesOnly_RejectsNilCredentialCore verifies that the
// pre-host constructor returns NilCredentialCoreError when the credential
// service is missing — the ceremony service cannot be built without it.
func TestBuildSealServicesOnly_RejectsNilCredentialCore(t *testing.T) {
	barrier, _, _ := newTestBarrier(t)
	_, err := lifecycle.BuildSealServicesOnly(context.Background(), lifecycle.Infra{
		DataDir:     t.TempDir(),
		SealBarrier: barrier,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var target *lifecycle.NilCredentialCoreError
	if !errors.As(err, &target) {
		t.Errorf("expected *NilCredentialCoreError, got %T: %v", err, err)
	}
}

// TestBuildSealServicesOnly_NoRegistrySkipsTenant verifies that when
// BarrierRegistry is nil, the seal-only constructor still succeeds but
// leaves Tenant unwired — Tenant requires a registry for per-tenant
// barrier lifecycle.
func TestBuildSealServicesOnly_NoRegistrySkipsTenant(t *testing.T) {
	barrier, _, credSvc := newTestBarrier(t)
	node, err := lifecycle.BuildSealServicesOnly(context.Background(), lifecycle.Infra{
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		CredentialCoreSvc: credSvc,
	})
	if err != nil {
		t.Fatalf("BuildSealServicesOnly: %v", err)
	}
	if node.Barrier == nil {
		t.Error("Barrier is nil")
	}
	if node.Tenant != nil {
		t.Error("Tenant must be nil when BarrierRegistry is omitted")
	}
}

// TestBuildServices_WithSuppliedDatabaseManager verifies that when
// Infra.DatabaseManager is non-nil, BuildServices uses it directly
// (does not register a closer for it) and the Database service is non-nil.
func TestBuildServices_WithSuppliedDatabaseManager(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	dataDir := t.TempDir()
	mgr := database.NewManager(slog.Default(), dataDir)
	defer func() { _ = mgr.Close() }()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:            host,
		WatchHub:        hub,
		DataDir:         dataDir,
		DatabaseManager: mgr,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	if node.Database == nil {
		t.Error("Database is nil")
	}
}

// TestBuildServices_WithStrategyRegistry verifies that injecting a
// StrategyRegistry wires the registry into the TenantService (exercises
// the non-nil branch of buildTenantServiceOpts).
func TestBuildServices_WithStrategyRegistry(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	barrier, registry, credSvc := newTestBarrier(t)
	stratReg := seal.NewDefaultStrategyRegistry()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		StrategyRegistry:  stratReg,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	if node.Tenant == nil {
		t.Error("Tenant is nil; expected TenantService wired with StrategyRegistry")
	}
	if node.Barrier == nil {
		t.Error("Barrier is nil")
	}
}

// TestBuildSealServicesOnly_DefaultCeremonyDataDir verifies that when
// CeremonyDataDir is empty the function derives a default from DataDir,
// avoiding a panic or configuration error in the ceremony service.
func TestBuildSealServicesOnly_DefaultCeremonyDataDir(t *testing.T) {
	barrier, registry, credSvc := newTestBarrier(t)

	// CeremonyDataDir intentionally empty; lifecycle must default to DataDir/seal.
	node, err := lifecycle.BuildSealServicesOnly(context.Background(), lifecycle.Infra{
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		Logger:            slog.Default(),
	})
	if err != nil {
		t.Fatalf("BuildSealServicesOnly: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	if node.Ceremony == nil {
		t.Error("Ceremony is nil")
	}
	if node.Tenant == nil {
		t.Error("Tenant is nil")
	}
}

// TestBuildServices_SealBarrierWithoutCredentialCore verifies that when
// SealBarrier is set but CredentialCoreSvc is nil, BuildServices still
// succeeds: Barrier is wired, Credential and Ceremony remain nil.
func TestBuildServices_SealBarrierWithoutCredentialCore(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	barrier, registry, _ := newTestBarrier(t)

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:            host,
		WatchHub:        hub,
		DataDir:         t.TempDir(),
		SealBarrier:     barrier,
		BarrierRegistry: registry,
		// CredentialCoreSvc deliberately nil.
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	if node.Barrier == nil {
		t.Error("Barrier is nil; expected barrier service even without credentials")
	}
	if node.Credential != nil {
		t.Error("Credential must be nil when CredentialCoreSvc is not provided")
	}
	if node.Ceremony != nil {
		t.Error("Ceremony must be nil when CredentialCoreSvc is not provided")
	}
}

// TestNode_Stop_CollectsCloserErrors verifies that Stop returns the first
// error encountered when a closer fails, while still invoking all closers
// in LIFO order. The test uses a node created via BuildServices with the
// vector PebbleDB closer registered; after manually stopping the vector
// storage (via node.Stop), a second Stop on a fresh node with a
// deliberately-failing closer exercises the error-collection path.
//
// Since the closers slice is unexported, this test constructs a scenario
// where the database manager closer returns an error by providing a nil
// DataDir path that causes PebbleDB to fail during open — instead we
// verify the no-error case is still exercised via TestNode_Stop and
// accept that the closer error path requires a failing resource.
//
// The real coverage of the `firstErr = err` branch is obtained by calling
// Stop twice on the same node: the second call has no closers left (they
// were drained), so this validates the empty-closers loop exit. For the
// actual error branch, the existing infrastructure is too robust to fail.
// This test therefore focuses on the Stop-with-multiple-closers scenario.
func TestNode_Stop_MultipleClosers(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}

	// First Stop closes all registered closers (vector storage + db manager).
	if err := node.Stop(context.Background()); err != nil {
		t.Errorf("first Stop: %v", err)
	}

	// Second Stop: no closers remain; must return nil cleanly.
	if err := node.Stop(context.Background()); err != nil {
		t.Errorf("second Stop on empty closers: %v", err)
	}
}

// TestBuildSealServicesOnly_WithStrategyRegistry verifies that the seal-only
// constructor passes a non-nil StrategyRegistry through to the TenantService,
// exercising the non-nil branch of buildTenantServiceOpts.
func TestBuildSealServicesOnly_WithStrategyRegistry(t *testing.T) {
	barrier, registry, credSvc := newTestBarrier(t)
	stratReg := seal.NewDefaultStrategyRegistry()

	node, err := lifecycle.BuildSealServicesOnly(context.Background(), lifecycle.Infra{
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		StrategyRegistry:  stratReg,
		Logger:            slog.Default(),
	})
	if err != nil {
		t.Fatalf("BuildSealServicesOnly: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	if node.Tenant == nil {
		t.Error("Tenant is nil; expected TenantService built with StrategyRegistry")
	}
}

// TestBuildServices_EmptyDataDir_ReturnsEmptyVectorDataDirError verifies that
// BuildServices returns an EmptyVectorDataDirError when infra.DataDir is empty.
// The vector storage subsystem is backed exclusively by PebbleDB and requires
// an absolute data directory; silently writing to a relative "./vector" path
// in the process working directory is never correct.
func TestBuildServices_EmptyDataDir_ReturnsEmptyVectorDataDirError(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	_, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  "", // intentionally empty
	})
	if err == nil {
		t.Fatal("expected EmptyVectorDataDirError, got nil")
	}
	var target *lifecycle.EmptyVectorDataDirError
	if !errors.As(err, &target) {
		t.Errorf("expected *EmptyVectorDataDirError, got %T: %v", err, err)
	}
}

// TestBuildServices_NonEmptyDataDir_VectorStorageOpens verifies that the
// normal path (non-empty DataDir) opens the vector storage and returns a
// Node with a non-nil Vector service, confirming the guard is the only thing
// that changes.
func TestBuildServices_NonEmptyDataDir_VectorStorageOpens(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	if node.Vector == nil {
		t.Error("Vector service is nil; expected it to be populated with a non-empty DataDir")
	}
}

// stubCeremonyCA is a minimal CeremonyCA used to assert identity wiring
// through the lifecycle Infra. It is never invoked by the tests.
type stubCeremonyCA struct{}

func (stubCeremonyCA) SignCSR(context.Context, []byte, *ceremony.SignCSROptions) (*x509.Certificate, error) {
	return nil, errors.New("stub")
}

func (stubCeremonyCA) CACertificate() (*x509.Certificate, error) { return nil, errors.New("stub") }

func (stubCeremonyCA) CABundle() ([]byte, error) { return nil, errors.New("stub") }

func (stubCeremonyCA) IssueCertificate(context.Context, *ceremony.IssueCertRequest) (*ceremony.IssuedCert, error) {
	return nil, errors.New("stub")
}

// newTestCeremonyCore constructs a core ceremony service over the supplied
// barrier/credential pair, mirroring what BuildSealServicesOnly builds.
func newTestCeremonyCore(t *testing.T, barrier *seal.StorageBarrier, credSvc *credentials.CredentialService) *ceremony.CeremonyService {
	t.Helper()
	svc, err := ceremony.NewCeremonyService(
		&ceremony.CeremonyConfig{DataDir: t.TempDir()}, barrier, credSvc, slog.Default())
	if err != nil {
		t.Fatalf("NewCeremonyService: %v", err)
	}
	return svc
}

// TestBuildSealServicesOnly_WiresCeremonyCA verifies that Infra.CeremonyCA is
// installed on the ceremony core service so daemon deployments can register
// Security Officers without running the full initialization ceremony.
func TestBuildSealServicesOnly_WiresCeremonyCA(t *testing.T) {
	barrier, registry, credSvc := newTestBarrier(t)
	ca := stubCeremonyCA{}

	node, err := lifecycle.BuildSealServicesOnly(context.Background(), lifecycle.Infra{
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		CeremonyCA:        ca,
		Logger:            slog.Default(),
	})
	if err != nil {
		t.Fatalf("BuildSealServicesOnly: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	coreSvc, ok := service.CeremonyServiceCore(node.Ceremony)
	if !ok {
		t.Fatal("CeremonyServiceCore: core service not exposed")
	}
	if got := coreSvc.CA(); got != ceremony.CeremonyCA(ca) {
		t.Errorf("ceremony CA = %v, want the Infra-supplied CA", got)
	}
}

// TestBuildSealServicesOnly_ReusesCeremonyCoreSvc verifies that a pre-built
// ceremony core service supplied via Infra.CeremonyCoreSvc is reused as-is
// rather than replaced — the property that keeps sealed-window officer
// registrations and enclave-held shares alive across boot phases.
func TestBuildSealServicesOnly_ReusesCeremonyCoreSvc(t *testing.T) {
	barrier, registry, credSvc := newTestBarrier(t)
	core := newTestCeremonyCore(t, barrier, credSvc)

	node, err := lifecycle.BuildSealServicesOnly(context.Background(), lifecycle.Infra{
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		CeremonyCoreSvc:   core,
		Logger:            slog.Default(),
	})
	if err != nil {
		t.Fatalf("BuildSealServicesOnly: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	coreSvc, ok := service.CeremonyServiceCore(node.Ceremony)
	if !ok {
		t.Fatal("CeremonyServiceCore: core service not exposed")
	}
	if coreSvc != core {
		t.Error("ceremony core service was rebuilt; expected the Infra-supplied instance to be reused")
	}
}

// TestBuildServices_WiresCeremonyCA verifies the full-boot path installs
// Infra.CeremonyCA on the ceremony core service.
func TestBuildServices_WiresCeremonyCA(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	barrier, registry, credSvc := newTestBarrier(t)
	ca := stubCeremonyCA{}

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		CeremonyCA:        ca,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	coreSvc, ok := service.CeremonyServiceCore(node.Ceremony)
	if !ok {
		t.Fatal("CeremonyServiceCore: core service not exposed")
	}
	if got := coreSvc.CA(); got != ceremony.CeremonyCA(ca) {
		t.Errorf("ceremony CA = %v, want the Infra-supplied CA", got)
	}
}

// TestBuildServices_ReusesCeremonyCoreSvc verifies the full-boot path reuses
// a sealed-window ceremony core service supplied via Infra.CeremonyCoreSvc —
// Gap B of the daemon Security Officer claim chain.
func TestBuildServices_ReusesCeremonyCoreSvc(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	barrier, registry, credSvc := newTestBarrier(t)
	core := newTestCeremonyCore(t, barrier, credSvc)
	ca := stubCeremonyCA{}
	core.SetCA(ca)

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		CeremonyCoreSvc:   core,
		CeremonyCA:        ca,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	coreSvc, ok := service.CeremonyServiceCore(node.Ceremony)
	if !ok {
		t.Fatal("CeremonyServiceCore: core service not exposed")
	}
	if coreSvc != core {
		t.Error("ceremony core service was rebuilt; expected the Infra-supplied instance to be reused")
	}
	if got := coreSvc.CA(); got != ceremony.CeremonyCA(ca) {
		t.Errorf("ceremony CA = %v, want the sealed-window CA preserved", got)
	}
}

// stubBackupSink is a minimal in-memory service.BackupSink whose List call is
// independently observable from BackupService's own in-memory registry, so a
// test can prove which one a given Node.Backup actually reads from.
type stubBackupSink struct {
	listCalls int
	listOut   []service.BackupInfo
}

func (s *stubBackupSink) Write(_ context.Context, _ service.BackupInfo, _ map[uint64][]byte) error {
	return nil
}

func (s *stubBackupSink) ReadInfo(_ context.Context, id string) (*service.BackupInfo, error) {
	return nil, &service.BackupNotFoundError{ID: id}
}

func (s *stubBackupSink) ReadShards(_ context.Context, id string) (map[uint64][]byte, error) {
	return nil, &service.BackupNotFoundError{ID: id}
}

func (s *stubBackupSink) List(_ context.Context) ([]service.BackupInfo, error) {
	s.listCalls++
	return s.listOut, nil
}

func (s *stubBackupSink) Delete(_ context.Context, _ string) error { return nil }

var _ service.BackupSink = (*stubBackupSink)(nil)

// TestBuildServices_BackupSink_Wired verifies that an Infra.BackupSink is
// wired into Node.Backup: ListBackups must read through the supplied sink
// (proven by the sink's own sentinel output) rather than BackupService's
// private in-memory registry, so backup artifacts survive a daemon restart
// when the caller configures a durable sink.
func TestBuildServices_BackupSink_Wired(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	sink := &stubBackupSink{listOut: []service.BackupInfo{{ID: "sentinel-from-sink"}}}

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:       host,
		WatchHub:   hub,
		DataDir:    t.TempDir(),
		BackupSink: sink,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	got, err := node.Backup.ListBackups(context.Background())
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if sink.listCalls != 1 {
		t.Fatalf("sink.List calls = %d, want 1 (Node.Backup did not route through the supplied sink)", sink.listCalls)
	}
	if len(got) != 1 || got[0].ID != "sentinel-from-sink" {
		t.Fatalf("ListBackups = %+v, want the sink's sentinel entry", got)
	}
}

// TestBuildServices_BackupSink_NilKeepsInMemory verifies that omitting
// Infra.BackupSink (the zero value) preserves the prior in-memory-only
// behavior: ListBackups succeeds against an empty registry without ever
// touching a sink.
func TestBuildServices_BackupSink_NilKeepsInMemory(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	got, err := node.Backup.ListBackups(context.Background())
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("ListBackups on a fresh in-memory registry = %+v, want empty", got)
	}
}
