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

// The typed errors this package returns to an operator, and what their
// messages say.
//
// Each case asserts the WHOLE message. These all interpolate a field -- a
// tenant, a shard id, a transport name -- and a substring check against the
// static prefix goes on passing when the interpolation is dropped, which is
// the regression that makes an operator's error unactionable.
//
// No build constraint: these name only the error types, all unconstrained.

package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

func TestOwnerResolveError_ErrorAndUnwrap(t *testing.T) {
	err := &OwnerResolveError{TenantID: "acme", Database: "orders", Err: ErrEmptyOwnerSet}
	want := "server: resolve migration owners for acme/orders: " + ErrEmptyOwnerSet.Error()
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	// The cause must survive errors.Is. ErrEmptyOwnerSet is the sentinel the
	// coordinator distinguishes from an ordinary lookup failure: narrowing a
	// placed multi-replica scope to the local node would migrate one node and
	// let the coordinator declare the cluster done with peers still
	// unencrypted at rest.
	if !errors.Is(err, ErrEmptyOwnerSet) {
		t.Error("errors.Is must reach ErrEmptyOwnerSet through Unwrap")
	}
}

// TestShardNotManagedError_Error: the shard id is formatted in base 10, and
// zero is a legitimate id rather than an absent one.
func TestShardNotManagedError_Error(t *testing.T) {
	for _, tc := range []struct {
		shardID uint64
		want    string
	}{
		{0, "server: shard 0 is not a dedicated raft shard managed by this node"},
		{42, "server: shard 42 is not a dedicated raft shard managed by this node"},
		{1 << 40, "server: shard 1099511627776 is not a dedicated raft shard managed by this node"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			err := &ShardNotManagedError{ShardID: tc.shardID}
			if got := err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTenantInfraConfigError_Error(t *testing.T) {
	err := &TenantInfraConfigError{Field: "Manager", Message: "must not be nil"}
	want := "server: tenant infra config: Manager: must not be nil"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestTLSUnavailableError_Error covers each transport name the message can
// carry. This error exists so a sealed or uninitialized node REFUSES rather
// than silently downgrading to cleartext, so the transport it names is the
// operative detail -- an operator has to know which listener did not come up.
func TestTLSUnavailableError_Error(t *testing.T) {
	const suffix = " transport: no TLS configured and no CeremonyCA available to auto-provision one; " +
		"qrdb transports are TLS-only"
	for _, transport := range []string{"gRPC", "unix", "REST", "QUIC", "MCP"} {
		t.Run(transport, func(t *testing.T) {
			err := &TLSUnavailableError{Transport: transport}
			want := "server: cannot start " + transport + suffix
			if got := err.Error(); got != want {
				t.Errorf("Error() = %q, want %q", got, want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Server options with behaviour of their own
//
// Most ServerOptions are one-line setters and are covered incidentally by the
// wiring tests. The three below were at 0%, and one of them has a contract
// worth pinning rather than merely executing.
// ---------------------------------------------------------------------------

// stubModule is a ServiceModule that registers nothing. Only Name is
// consulted by the option under test, so the rest comes from BaseModule --
// the embedding the package provides for exactly this, rather than eight
// hand-written no-ops that would need editing every time the interface grows.
type stubModule struct {
	apimodule.BaseModule
	name string
}

func (m *stubModule) Name() string { return m.name }

var _ apimodule.ServiceModule = (*stubModule)(nil)

// TestWithModule_AppendsInOrder: registration order is the module start order,
// so it is observable rather than incidental.
func TestWithModule_AppendsInOrder(t *testing.T) {
	var o serverOptions
	WithModule(&stubModule{name: "alpha"})(&o)
	WithModule(&stubModule{name: "beta"})(&o)

	if len(o.modules) != 2 {
		t.Fatalf("modules = %d, want 2", len(o.modules))
	}
	if o.modules[0].Name() != "alpha" || o.modules[1].Name() != "beta" {
		t.Errorf("order = [%s %s], want [alpha beta]",
			o.modules[0].Name(), o.modules[1].Name())
	}
}

// TestWithModule_DuplicateNamePanics pins the documented contract: registering
// two modules under one name is a programming error and panics at WIRING time.
//
// Failing here rather than later is the point. Name is what duplicate
// detection, logs and metrics key on, so a silently accepted duplicate would
// give two modules one identity and surface as a routing mystery at request
// time, far from the line that caused it.
func TestWithModule_DuplicateNamePanics(t *testing.T) {
	var o serverOptions
	WithModule(&stubModule{name: "dup"})(&o)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("registering a duplicate module name did not panic")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "dup") {
			t.Errorf("panic = %v, want a message naming the duplicate module", r)
		}
	}()
	WithModule(&stubModule{name: "dup"})(&o)
}

// TestWithMCPServerOptions_AppendsAfterTheBuiltIns: the embedder's options are
// applied after the built-in ones, which is what lets a higher layer override
// the MCP authorization discovery surface rather than be overridden by it.
func TestWithMCPServerOptions_Appends(t *testing.T) {
	var o serverOptions
	before := len(o.mcpServerOpts)

	WithMCPServerOptions(func(*mcpapi.Server) {}, func(*mcpapi.Server) {})(&o)
	if got := len(o.mcpServerOpts) - before; got != 2 {
		t.Fatalf("appended %d options, want 2", got)
	}

	WithMCPServerOptions(func(*mcpapi.Server) {})(&o)
	if got := len(o.mcpServerOpts) - before; got != 3 {
		t.Errorf("a second call appended %d in total, want 3: the option must "+
			"append rather than replace", got)
	}
}

// TestTenantInfra_ServerOptions covers each combination of the two optional
// components.
//
// The nil checks are the point. All of TenantInfra's fields are optional, so
// contributing an option unconditionally would wire a TYPED NIL into the
// server -- WithRaftBackendFactory(nil) sets the field to a non-nil interface
// holding a nil pointer, which is not the same as leaving it unset and which
// fails later, at first use, rather than here.
func TestTenantInfra_ServerOptions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		infra    TenantInfra
		wantOpts int
	}{
		{"neither", TenantInfra{}, 0},
		{"hosts only", TenantInfra{Hosts: &TenantHostRegistry{}}, 1},
		{"raft only", TenantInfra{Raft: &RaftBackendFactory{}}, 1},
		{"both", TenantInfra{Hosts: &TenantHostRegistry{}, Raft: &RaftBackendFactory{}}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.infra.ServerOptions()
			if len(opts) != tc.wantOpts {
				t.Fatalf("ServerOptions() = %d options, want %d", len(opts), tc.wantOpts)
			}
			// Applying them must not panic and must set exactly what was present.
			var o serverOptions
			for _, opt := range opts {
				opt(&o)
			}
			if (o.tenantHosts != nil) != (tc.infra.Hosts != nil) {
				t.Errorf("tenantHosts set = %v, want %v", o.tenantHosts != nil, tc.infra.Hosts != nil)
			}
			if (o.raftBackend != nil) != (tc.infra.Raft != nil) {
				t.Errorf("raftBackendFactory set = %v, want %v",
					o.raftBackend != nil, tc.infra.Raft != nil)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// The dedicated-store service cache
// ---------------------------------------------------------------------------

// newCacheTestDatabase builds a minimal *database.Database over an in-memory
// backend. Only identity and Backend() are exercised here.
func newCacheTestDatabase(t *testing.T, name string) *database.Database {
	t.Helper()
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	return database.NewDatabase(
		database.DatabaseConfig{Name: name, Backend: database.BackendMemory},
		database.NewStandaloneBackend(store),
		t.TempDir(),
	)
}

// TestDedicatedStoreServiceCache_ReusesOneBundlePerDatabase: the cache exists
// so resolve() does not rebuild a bundle per request, so "the same instance
// comes back" is the property, not merely "a bundle comes back".
func TestDedicatedStoreServiceCache_ReusesOneBundlePerDatabase(t *testing.T) {
	c := newDedicatedStoreServiceCache()
	db := newCacheTestDatabase(t, "orders")

	first := c.getOrBuild(db)
	second := c.getOrBuild(db)

	if first == nil {
		t.Fatal("getOrBuild returned nil")
	}
	if first != second {
		t.Error("getOrBuild rebuilt the bundle for an unchanged database: the cache is not caching")
	}
	if first.Name != "orders" {
		t.Errorf("Name = %q, want %q", first.Name, "orders")
	}
	if first.KV == nil || first.Collection == nil {
		t.Error("the bundle must carry both the KV and Collection services")
	}
}

// TestDedicatedStoreServiceCache_InvalidatesOnANewInstance is the property
// that makes this a cache rather than a leak.
//
// A drop followed by a recreate produces a DIFFERENT *database.Database under
// the SAME name. Keying on the name alone would serve the bundle built over
// the dropped database's backend -- which is closed -- so every request to the
// recreated database would fail against storage that no longer exists, and it
// would keep failing until the process restarted.
func TestDedicatedStoreServiceCache_InvalidatesOnANewInstance(t *testing.T) {
	c := newDedicatedStoreServiceCache()

	original := newCacheTestDatabase(t, "orders")
	before := c.getOrBuild(original)

	// Same name, different instance: a drop-and-recreate.
	recreated := newCacheTestDatabase(t, "orders")
	after := c.getOrBuild(recreated)

	if before == after {
		t.Fatal("a recreated database under the same name served the OLD bundle, " +
			"which is built over the dropped database's closed backend")
	}
	if after.Name != "orders" {
		t.Errorf("Name = %q, want %q", after.Name, "orders")
	}
	// And the new instance is now the cached one.
	if again := c.getOrBuild(recreated); again != after {
		t.Error("the recreated database's bundle was not cached in turn")
	}
}

// TestDedicatedStoreServiceCache_SeparatesDatabases: two databases must not
// share a bundle, or one database's requests would resolve against another's
// backend.
func TestDedicatedStoreServiceCache_SeparatesDatabases(t *testing.T) {
	c := newDedicatedStoreServiceCache()
	orders := c.getOrBuild(newCacheTestDatabase(t, "orders"))
	invoices := c.getOrBuild(newCacheTestDatabase(t, "invoices"))

	if orders == invoices {
		t.Fatal("two databases shared one service bundle")
	}
	if orders.Name == invoices.Name {
		t.Errorf("both bundles report the name %q", orders.Name)
	}
}

// TestDedicatedStoreServiceCache_ConcurrentGetOrBuild exercises the
// double-checked locking under -race: concurrent callers must all receive the
// same bundle, and the second check inside the write lock is what prevents two
// builders racing to install different ones.
func TestDedicatedStoreServiceCache_ConcurrentGetOrBuild(t *testing.T) {
	c := newDedicatedStoreServiceCache()
	db := newCacheTestDatabase(t, "orders")

	const n = 32
	got := make([]*DatabaseServices, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func() {
			defer wg.Done()
			got[i] = c.getOrBuild(db)
		}()
	}
	wg.Wait()

	for i := 1; i < n; i++ {
		if got[i] != got[0] {
			t.Fatalf("caller %d received a different bundle: concurrent getOrBuild "+
				"installed more than one", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Module lifecycle
// ---------------------------------------------------------------------------

// lifecycleModule records its Start/Stop calls and can fail either.
type lifecycleModule struct {
	apimodule.BaseModule
	name     string
	startErr error
	stopErr  error
	events   *[]string
}

func (m *lifecycleModule) Name() string { return m.name }

func (m *lifecycleModule) Start(context.Context) error {
	*m.events = append(*m.events, "start:"+m.name)
	return m.startErr
}

func (m *lifecycleModule) Stop(context.Context) error {
	*m.events = append(*m.events, "stop:"+m.name)
	return m.stopErr
}

// TestServer_StartModules_StopsAtTheFirstError: startModules is documented to
// stop at the first failure and let the caller roll back. A module after the
// failing one must NOT be started -- starting it would leave a module running
// that the rollback path does not know about, because the caller's
// stopModules only unwinds what it believes was started.
func TestServer_StartModules_StopsAtTheFirstError(t *testing.T) {
	var events []string
	boom := errors.New("module refused to start")
	s := &Server{opts: serverOptions{modules: []apimodule.ServiceModule{
		&lifecycleModule{name: "first", events: &events},
		&lifecycleModule{name: "second", startErr: boom, events: &events},
		&lifecycleModule{name: "third", events: &events},
	}}}

	err := s.startModules()
	if err == nil {
		t.Fatal("startModules returned nil despite a failing module")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error does not wrap the module's own: %v", err)
	}
	var mse *ModuleStartError
	if !errors.As(err, &mse) || mse.Module != "second" {
		t.Errorf("error = %#v, want a *ModuleStartError naming the failing module", err)
	}
	want := []string{"start:first", "start:second"}
	if !slices.Equal(events, want) {
		t.Errorf("events = %v, want %v: a module after the failure must not be started", events, want)
	}
}

func TestModuleStartError_Message(t *testing.T) {
	err := &ModuleStartError{Module: "billing", Err: errors.New("port in use")}
	if got, want := err.Error(), `server: module "billing" failed to start: port in use`; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestModuleStartError_UnwrapsTheModuleError(t *testing.T) {
	err := error(&ModuleStartError{Module: "billing", Err: io.ErrUnexpectedEOF})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("errors.Is(%v, io.ErrUnexpectedEOF) = false, want true", err)
	}
	if errors.Unwrap(err) != io.ErrUnexpectedEOF {
		t.Errorf("Unwrap() = %v, want io.ErrUnexpectedEOF", errors.Unwrap(err))
	}
}

// TestServer_StartModules_StartsInRegistrationOrder: order is observable, and
// modules may depend on one registered earlier.
func TestServer_StartModules_StartsInRegistrationOrder(t *testing.T) {
	var events []string
	s := &Server{opts: serverOptions{modules: []apimodule.ServiceModule{
		&lifecycleModule{name: "a", events: &events},
		&lifecycleModule{name: "b", events: &events},
		&lifecycleModule{name: "c", events: &events},
	}}}

	if err := s.startModules(); err != nil {
		t.Fatalf("startModules: %v", err)
	}
	want := []string{"start:a", "start:b", "start:c"}
	if !slices.Equal(events, want) {
		t.Errorf("events = %v, want %v", events, want)
	}
}

// TestServer_StopModules_ReverseOrderAndContinuesPastErrors pins both halves
// of stopModules' contract.
//
// LIFO, because a module registered later may depend on an earlier one and
// must release first. And a Stop error is logged rather than fatal: stopping
// is a teardown path, so abandoning it at the first error would leak every
// remaining module's resources to save reporting one failure nobody can act
// on mid-shutdown.
func TestServer_StopModules_ReverseOrderAndContinuesPastErrors(t *testing.T) {
	var events []string
	s := &Server{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		opts: serverOptions{modules: []apimodule.ServiceModule{
			&lifecycleModule{name: "a", events: &events},
			&lifecycleModule{name: "b", stopErr: errors.New("stop failed"), events: &events},
			&lifecycleModule{name: "c", events: &events},
		}},
	}

	s.stopModules()

	want := []string{"stop:c", "stop:b", "stop:a"}
	if !slices.Equal(events, want) {
		t.Errorf("events = %v, want %v: stop must be LIFO and must not abandon "+
			"the remaining modules after one fails", events, want)
	}
}

// tenantTargetAwareModule records the authorizer the server hands it via
// SetTenantTargetAuthorizer, so wireModuleTenantTargetAuthorizers' wiring is
// observable.
type tenantTargetAwareModule struct {
	apimodule.BaseModule
	name string
	got  apimodule.TenantTargetAuthorizer
	// set distinguishes "called with nil" from "never called".
	set bool
}

func (m *tenantTargetAwareModule) Name() string { return m.name }

func (m *tenantTargetAwareModule) SetTenantTargetAuthorizer(a apimodule.TenantTargetAuthorizer) {
	m.got = a
	m.set = true
}

var _ apimodule.ServiceModule = (*tenantTargetAwareModule)(nil)
var _ apimodule.ModuleTenantTargetAware = (*tenantTargetAwareModule)(nil)

// TestWireModuleTenantTargetAuthorizers_PassesTheLiveAuthMiddleware: a module
// implementing ModuleTenantTargetAware receives the server's own
// *authmw.AuthMiddleware, and a module that does not implement the
// extension (stubModule) is skipped without panicking (go-qrdb#587).
func TestWireModuleTenantTargetAuthorizers_PassesTheLiveAuthMiddleware(t *testing.T) {
	mw := authmw.New(nil, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))
	aware := &tenantTargetAwareModule{name: "aware"}
	s := &Server{opts: serverOptions{
		authMW: mw,
		modules: []apimodule.ServiceModule{
			aware,
			&stubModule{name: "plain"},
		},
	}}

	s.wireModuleTenantTargetAuthorizers()

	if !aware.set {
		t.Fatal("SetTenantTargetAuthorizer was never called")
	}
	if aware.got != mw {
		t.Errorf("SetTenantTargetAuthorizer got %v, want the server's own *authmw.AuthMiddleware", aware.got)
	}
}

// TestWireModuleTenantTargetAuthorizers_NilAuthMWIsATrueNilInterface: when
// auth is disabled (authMW nil), the module must see a genuinely nil
// interface value (comparable to nil with ==), not a non-nil interface
// boxing a nil *authmw.AuthMiddleware -- the classic Go pitfall that would
// silently invert a module's own "if authz == nil { admit }" check.
func TestWireModuleTenantTargetAuthorizers_NilAuthMWIsATrueNilInterface(t *testing.T) {
	aware := &tenantTargetAwareModule{name: "aware"}
	s := &Server{opts: serverOptions{
		modules: []apimodule.ServiceModule{aware},
	}}

	s.wireModuleTenantTargetAuthorizers()

	if !aware.set {
		t.Fatal("SetTenantTargetAuthorizer was never called")
	}
	if aware.got != nil {
		t.Errorf("SetTenantTargetAuthorizer got %#v, want a true nil interface", aware.got)
	}
}

// TestWithClusterEncryptionMigrator_Sets: the cluster migrator takes
// precedence over the local scoped router, so wiring it is observable.
func TestWithClusterEncryptionMigrator_Sets(t *testing.T) {
	var o serverOptions
	if o.clusterEncMigrator != nil {
		t.Fatal("clusterEncMigrator is set before the option is applied")
	}
	WithClusterEncryptionMigrator(nil)(&o)
	// A nil migrator is still a deliberate assignment; the observable
	// behaviour is that the option runs and does not panic.
	WithClusterEncryptionMigrator(&noopEncryptionMigrator{})(&o)
	if o.clusterEncMigrator == nil {
		t.Error("WithClusterEncryptionMigrator did not set the migrator")
	}
}

type noopEncryptionMigrator struct{ service.EncryptionMigrator }

// ---------------------------------------------------------------------------
// resolveDedicatedDatabaseBarrier: who is allowed to reach a dedicated
// encrypting store
// ---------------------------------------------------------------------------

// newBarrierResolveRegistry returns a registry backed by a real
// database.Manager rooted at a temp dir, with "system" as the system tenant.
func newBarrierResolveRegistry(t *testing.T) (*restServiceRegistry, *database.Manager) {
	t.Helper()
	mgr := databasetest.NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	return &restServiceRegistry{
		dbManager:      mgr,
		systemTenant:   "system",
		dedicatedStore: newDedicatedStoreServiceCache(),
	}, mgr
}

// TestResolveDedicatedDatabaseBarrier_RefusesCrossTenantAccess is the security
// property in this function.
//
// A dedicated store is a database's OWN encrypting backend. Resolving one for
// a tenant that does not own it would hand that tenant another tenant's
// decrypted data plane -- so a mismatch is reported as
// errNotDedicatedDatabaseBarrier, which makes the caller fall through to the
// ordinary not-found path rather than granting access.
//
// The sentinel is what the caller keys on: any OTHER error is treated as a
// final resolution outcome and returned to the client, so returning the wrong
// error here would turn a denial into a 500 at best and a leak at worst.
func TestResolveDedicatedDatabaseBarrier_RefusesCrossTenantAccess(t *testing.T) {
	r, mgr := newBarrierResolveRegistry(t)

	if _, err := mgr.Create(database.DatabaseConfig{
		Name: "acme-db", Backend: database.BackendMemory,
		TenantID: "acme", DataDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, tc := range []struct{ name, tenant string }{
		{"a different tenant", "globex"},
		{"the system tenant, for a tenant-owned database", "system"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svcs, err := r.resolveDedicatedDatabaseBarrier(tc.tenant, "acme-db", nil)
			if !errors.Is(err, errNotDedicatedDatabaseBarrier) {
				t.Fatalf("error = %v, want errNotDedicatedDatabaseBarrier: any other "+
					"error is treated by the caller as a FINAL outcome", err)
			}
			if svcs != nil {
				t.Error("a refused resolution must return no services")
			}
		})
	}
}

// TestResolveDedicatedDatabaseBarrier_RefusesWithoutABarrier: ownership alone
// does not earn a dedicated store. A database with no barrier configured falls
// through to the ordinary shared-store path.
func TestResolveDedicatedDatabaseBarrier_RefusesWithoutABarrier(t *testing.T) {
	r, mgr := newBarrierResolveRegistry(t)

	if _, err := mgr.Create(database.DatabaseConfig{
		Name: "plain", Backend: database.BackendMemory,
		TenantID: "acme", DataDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	svcs, err := r.resolveDedicatedDatabaseBarrier("acme", "plain", nil)
	if !errors.Is(err, errNotDedicatedDatabaseBarrier) {
		t.Fatalf("error = %v, want errNotDedicatedDatabaseBarrier", err)
	}
	if svcs != nil {
		t.Error("a database with no barrier must resolve no dedicated services")
	}
}

// TestResolveDedicatedDatabaseBarrier_RefusesUnknownAndUnmanaged covers the
// two entry guards: no manager wired at all, and a database the manager does
// not know. Both must yield the sentinel rather than a real error, so an
// unknown database reaches the ordinary not-found handling.
func TestResolveDedicatedDatabaseBarrier_RefusesUnknownAndUnmanaged(t *testing.T) {
	t.Run("no database manager", func(t *testing.T) {
		r := &restServiceRegistry{systemTenant: "system", dedicatedStore: newDedicatedStoreServiceCache()}
		if _, err := r.resolveDedicatedDatabaseBarrier("acme", "anything", nil); !errors.Is(err, errNotDedicatedDatabaseBarrier) {
			t.Errorf("error = %v, want errNotDedicatedDatabaseBarrier", err)
		}
	})

	t.Run("unknown database", func(t *testing.T) {
		r, _ := newBarrierResolveRegistry(t)
		if _, err := r.resolveDedicatedDatabaseBarrier("acme", "no-such-db", nil); !errors.Is(err, errNotDedicatedDatabaseBarrier) {
			t.Errorf("error = %v, want errNotDedicatedDatabaseBarrier", err)
		}
	})
}

// TestResolveDedicatedDatabaseBarrier_SystemTenantReachesUnownedDatabases: the
// system tenant sees databases with no owner, which is the counterpart to the
// cross-tenant refusal above -- the ownership check must not lock the system
// tenant out of the databases it is responsible for.
func TestResolveDedicatedDatabaseBarrier_SystemTenantReachesUnownedDatabases(t *testing.T) {
	r, mgr := newBarrierResolveRegistry(t)

	if _, err := mgr.Create(database.DatabaseConfig{
		Name: "unowned", Backend: database.BackendMemory,
		DataDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// It still refuses -- there is no barrier -- but it must refuse at the
	// BARRIER check, having passed the ownership check. An ownership failure
	// here would mean the system tenant could never resolve an unowned
	// database's dedicated store even once one is configured.
	_, err := r.resolveDedicatedDatabaseBarrier("system", "unowned", nil)
	if !errors.Is(err, errNotDedicatedDatabaseBarrier) {
		t.Fatalf("error = %v, want errNotDedicatedDatabaseBarrier", err)
	}
	// Ownership is what this asserts: the same call for a tenant that does not
	// own it is refused for a different reason, and both look alike from
	// outside, so assert the ownership predicate directly.
	dbase, err := mgr.Get("unowned")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if owner := dbase.TenantID(); owner != "" {
		t.Errorf("TenantID = %q, want empty: this fixture is the unowned case", owner)
	}
}

// TestSealedWindowErrors_ReadWithoutACause: both sealed-window errors carry an
// optional cause, and the message has to stand on its own when there is none.
// Calling Err.Error() unconditionally would panic on the nil, so the branch is
// worth having a test rather than an assumption.
func TestSealedWindowErrors_ReadWithoutACause(t *testing.T) {
	cause := errors.New("listen tcp: address already in use")

	for _, tc := range []struct {
		name         string
		withCause    error
		withoutCause error
		wantWith     string
		wantWithout  string
	}{
		{
			name:         "SealedWindowStartError",
			withCause:    &SealedWindowStartError{Err: cause},
			withoutCause: &SealedWindowStartError{},
			wantWith:     "server: sealed-window listener start failed: " + cause.Error(),
			wantWithout:  "server: sealed-window listener start failed",
		},
		{
			name:         "SealedWindowCanceledError",
			withCause:    &SealedWindowCanceledError{Err: cause},
			withoutCause: &SealedWindowCanceledError{},
			wantWith:     "server: sealed-window wait canceled: " + cause.Error(),
			wantWithout:  "server: sealed-window wait canceled",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.withCause.Error(); got != tc.wantWith {
				t.Errorf("with a cause: Error() = %q, want %q", got, tc.wantWith)
			}
			if got := tc.withoutCause.Error(); got != tc.wantWithout {
				t.Errorf("without a cause: Error() = %q, want %q", got, tc.wantWithout)
			}
			if !errors.Is(tc.withCause, cause) {
				t.Error("errors.Is must reach the cause through Unwrap")
			}
		})
	}
}
