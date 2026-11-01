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
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/keywrap"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
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

	strategy, err := seal.NewSoftwareStrategy(context.Background(), passphrase)
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
	if err := b.Initialize(ctx, seal.MustCredentials(context.Background(), bytes.Clone(passphrase))); err != nil {
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
// so it can satisfy the pool-driven RegisterBarrierTenant flow that
// CreateTenant(IsolationBarrier) uses. Mirrors the tenant package's own test
// fixture; required because the plain newTestBarrier registry has no pool.
func newPooledRegistry(t *testing.T, system *seal.StorageBarrier) *seal.BarrierRegistry {
	t.Helper()
	reg, _, _ := sealtest.PooledRegistry(t, system)
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
		t.Run(tc.name, func(t *testing.T) {
			if tc.svc == nil {
				t.Errorf("%s is nil", tc.name)
			}
		})
	}
}

// TestBuildServices_MigratesLegacyTenantMetadataIntoTheReplicatedStore pins
// the upgrade path: older builds kept TenantInfo in the node-local seal store,
// where a follower could never see it. BuildServices copies any such record
// into the replicated store once, so the tenant survives the upgrade and is
// served by the new tenant service, and reports how many it moved.
func TestBuildServices_MigratesLegacyTenantMetadataIntoTheReplicatedStore(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()
	hub := watch.NewHub()
	defer hub.Close()

	barrier, registry, credSvc := newTestBarrier(t)
	ctx := context.Background()

	legacy, err := json.Marshal(tenant.TenantInfo{ID: "legacy", Name: "Legacy Co", IsolationType: tenant.IsolationPrefix})
	if err != nil {
		t.Fatalf("marshal legacy TenantInfo: %v", err)
	}
	// The key older builds wrote TenantInfo under in the node-local seal store.
	if err := barrier.Put(ctx, "__sys:tenants/legacy", legacy); err != nil {
		t.Fatalf("seed legacy record: %v", err)
	}

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	node, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		Logger:            log,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(ctx) }()

	info, err := node.Tenant.GetTenant(ctx, "legacy")
	if err != nil {
		t.Fatalf("legacy tenant not served after BuildServices: %v", err)
	}
	if info.Name != "Legacy Co" {
		t.Fatalf("migrated tenant name = %q, want %q", info.Name, "Legacy Co")
	}

	rec := findLogRecord(t, buf.String(), "migrated tenant metadata from local seal store to replicated store")
	if rec == nil {
		t.Fatalf("no migration record logged; log:\n%s", buf.String())
	}
	if rec["count"] != float64(1) {
		t.Fatalf("logged count = %v, want 1", rec["count"])
	}
}

// plaintextWarningMsg is the exact message BuildServices logs when
// infra.SealEnabled is false. Shared by every test in this section so a
// wording change only needs updating in one place.
const plaintextWarningMsg = "qrdb/seal: seal disabled; at-rest data is stored in plaintext"

// findLogRecord scans a slog JSON-handler log (one JSON object per line) for
// the first record whose "msg" equals want, returning its decoded fields or
// nil if no such record exists. Used to assert on level and attributes, not
// just substring presence.
func findLogRecord(t *testing.T, log, want string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		if rec["msg"] == want {
			return rec
		}
	}
	return nil
}

// TestBuildServices_SealDisabled_LogsPlaintextWarningOnce verifies that
// BuildServices emits exactly one WARN, at level WARN and carrying a "hint"
// attribute, when infra.SealEnabled is false -- the actual seal.enabled:
// false decision, not merely an absent barrier -- so an operator sees the
// plaintext-at-rest fact regardless of which composer started the process.
func TestBuildServices_SealDisabled_LogsPlaintextWarningOnce(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
		Logger:   log,
		// SealEnabled left at its zero value (false): the composer never
		// requested a barrier, which is the only case this warning covers.
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	out := buf.String()
	if got := strings.Count(out, plaintextWarningMsg); got != 1 {
		t.Fatalf("plaintext warning count = %d, want 1 (log: %s)", got, out)
	}
	rec := findLogRecord(t, out, plaintextWarningMsg)
	if rec == nil {
		t.Fatalf("could not decode the plaintext warning record: %s", out)
	}
	if rec["level"] != "WARN" {
		t.Errorf("level = %v, want WARN", rec["level"])
	}
	if hint, _ := rec["hint"].(string); hint == "" {
		t.Errorf("hint attribute missing or empty: %v", rec)
	}
}

// TestBuildServices_SealEnabled_NoPlaintextWarning verifies that BuildServices
// with infra.SealEnabled true and a fully initialised seal barrier does NOT
// log the seal-disabled plaintext warning: the barrier (even under
// StrategyNone) is a distinct, unchanged warning path owned by go-quicraft,
// not this one.
func TestBuildServices_SealEnabled_NoPlaintextWarning(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	barrier, registry, credSvc := newTestBarrier(t)

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		SealEnabled:       true,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		Logger:            log,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	out := buf.String()
	const unwanted = "qrdb/seal: seal disabled"
	if strings.Contains(out, unwanted) {
		t.Errorf("seal-disabled warning logged with a barrier present: %s", out)
	}
}

// TestBuildServices_SealEnabledButBarrierMissing_NoPlaintextWarning is the
// go-qrdb#550 review regression test: a composer that REQUESTED a barrier
// (SealEnabled true) but whose barrier construction failed for an unrelated
// reason (a bad passphrase, a corrupt seal store, an invalid strategy config)
// passes SealBarrier nil too -- exactly the same shape BuildServices sees for
// an actual seal.enabled: false deployment. Without a signal independent of
// SealBarrier's nilness, that construction failure would be misreported as
// the operator's own decision to run without encryption. It must not be.
func TestBuildServices_SealEnabledButBarrierMissing_NoPlaintextWarning(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:        host,
		WatchHub:    hub,
		DataDir:     t.TempDir(),
		SealEnabled: true,
		// SealBarrier intentionally left nil: simulates a failed construction
		// the composer already reported through its own error path.
		Logger: log,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	out := buf.String()
	if strings.Contains(out, "qrdb/seal: seal disabled") {
		t.Errorf("a barrier-construction failure with SealEnabled true must not log the disabled warning: %s", out)
	}
}

// TestBuildServices_SealBarrierPresentWithoutSealEnabled_NoPlaintextWarning
// covers the fourth cell of the truth table the other three tests in this
// section leave implicit: SealEnabled false (its zero value) with a real
// SealBarrier present -- an older call site built before SealEnabled existed,
// or one that never learned to set it, supplying a genuine barrier. The
// warning's condition is `!SealEnabled && SealBarrier == nil`, deliberately
// requiring BOTH signals to agree there is no barrier (see the condition's
// doc comment in lifecycle.go); this test is what would catch an edit that
// simplified the condition down to the SealBarrier nil check alone, which
// would leave this exact case correctly silent by accident rather than by
// the guarantee the second clause provides.
func TestBuildServices_SealBarrierPresentWithoutSealEnabled_NoPlaintextWarning(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	barrier, registry, credSvc := newTestBarrier(t)

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		Logger:            log,
		// SealEnabled intentionally left at its zero value (false): an
		// older or non-updated call site that still supplies a real
		// barrier must not be accused of running one it doesn't have.
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	out := buf.String()
	if strings.Contains(out, "qrdb/seal: seal disabled") {
		t.Errorf("a real barrier without SealEnabled set must not log the disabled warning: %s", out)
	}
}

// buildRoutedIAMNode builds a node whose IAM is tenant-routed: a SealBarrier
// plus a pool-backed BarrierRegistry, which is the combination BuildServices
// requires before it installs the StoreResolver and the routing decorators.
//
// Its state machine is pkg/engine's KVEngine, which makes every test built on
// it a check of that engine specifically — including its conditional-write
// handler, which #41 added.
func buildRoutedIAMNode(t *testing.T) (*lifecycle.Node, *seal.BarrierRegistry) {
	t.Helper()

	host := newLocalHost()
	t.Cleanup(func() { _ = host.Close() })
	return buildRoutedIAMNodeOn(t, host)
}

// buildRoutedIAMNodeOn is buildRoutedIAMNode over a caller-supplied host.
func buildRoutedIAMNodeOn(t *testing.T, host service.AdminHost) (*lifecycle.Node, *seal.BarrierRegistry) {
	t.Helper()

	hub := watch.NewHub()
	t.Cleanup(hub.Close)

	barrier, _, credSvc := newTestBarrier(t)
	// CreateTenant(IsolationBarrier) takes the pool-driven RegisterBarrierTenant
	// path, so the registry must be configured with a volume pool.
	registry := newPooledRegistry(t, barrier)

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		KeyWrapRegistry:   fakeSoftwareKeyWrapRegistry(),
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	t.Cleanup(func() { _ = node.Stop(context.Background()) })
	// A standalone node: isolated tenants' home stores are their barriers.
	if !lifecycle.AttachLocalTenantHomes(node, registry, nil) {
		t.Fatal("AttachLocalTenantHomes = false")
	}

	return node, registry
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
	node, registry := buildRoutedIAMNode(t)

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
	secureCtx := tenant.WithTenantID(ctx, barrierTenant)
	created, err := node.User.CreateUser(secureCtx, &user.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "alice",
		TenantID: barrierTenant,
	})
	if err != nil {
		t.Fatalf("CreateUser(barrier tenant): %v", err)
	}

	// Lookup scoped to the barrier tenant must find the user (routed to the
	// barrier store on both write and read).
	got, err := node.User.GetUserByUsername(secureCtx, barrierTenant, "alice")
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
	sysCtx := tenant.WithTenantID(ctx, tenantid.System)
	if _, err := node.User.GetUserByUsername(sysCtx, tenantid.System, "alice"); err == nil {
		t.Fatal("system-tenant lookup found a barrier-tenant user: routing decorator not installed (data leaked to shared store)")
	} else {
		var notFound *user.UserNotFoundError
		if !errors.As(err, &notFound) {
			t.Fatalf("system-tenant lookup: want UserNotFoundError, got %v", err)
		}
	}
}

// unactivatedHost is a host whose groups can hold other binaries, as the raft
// adapter's is, and on which no feature has been activated.
type unactivatedHost struct {
	*backend.LocalBackend
}

var _ service.AdminHost = unactivatedHost{}

func (unactivatedHost) FeatureEnabled(uint64, feature.Feature) bool { return false }

var _ service.FeatureGatedHost = unactivatedHost{}

// Every member serving an isolated tenant initialises its own key from the
// owner's statement, so on a host whose groups can hold other binaries an
// isolated tenant is refused until per-node-barrier-keys is activated, and
// nothing is written.
func TestBuildServices_IsolatedTenantIsGatedOnPerNodeBarrierKeys(t *testing.T) {
	host := unactivatedHost{LocalBackend: newLocalHost()}
	t.Cleanup(func() { _ = host.Close() })
	node, _ := buildRoutedIAMNodeOn(t, host)

	ctx := context.Background()
	const isolated = "isolated-tenant"
	var notActive *feature.NotActiveError
	if _, err := node.Tenant.CreateTenant(ctx, isolated, "Isolated Tenant", tenant.IsolationBarrier); !errors.As(err, &notActive) || notActive.Feature != featuregate.PerNodeBarrierKeys {
		t.Fatalf("CreateTenant(barrier) before activation = %v, want NotActiveError(%s)", err, featuregate.PerNodeBarrierKeys)
	}
	if _, err := node.Tenant.GetTenant(ctx, isolated); err == nil {
		t.Fatal("GetTenant after a refused create succeeded; the refusal wrote the tenant")
	}
}

// A standalone node holds only this binary and gates nothing: an isolated
// tenant is admitted and its barrier initialised from its first start.
func TestBuildServices_StandaloneIsolatedTenantIsNotGated(t *testing.T) {
	node, registry := buildRoutedIAMNode(t)

	ctx := context.Background()
	const isolated = "isolated-tenant"
	if _, err := node.Tenant.CreateTenant(ctx, isolated, "Isolated Tenant", tenant.IsolationBarrier); err != nil {
		t.Fatalf("CreateTenant(barrier) on a standalone node: %v", err)
	}
	if err := registry.InitializeTenant(ctx, isolated, seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant(barrier): %v", err)
	}
}

// The identity-index CLAIM, exercised end to end over the KVEngine-backed
// state machine this node runs — the third implementation of
// kv.OpPutIfAbsent, alongside PebbleStateMachine and MVCCEngine, and the one
// that had no handler until #41 (a conditional write failed the whole
// proposal with engine: unsupported operation: 0x04).
//
// Role IDs are caller-supplied: CreateRoleRequest.ID travels from the REST
// and gRPC surfaces straight into the store. Without a claim, one tenant
// could create a role carrying ANOTHER tenant's role id, take over that id's
// entry in the system-plane index, and silently redirect every by-id
// operation an administrator performs on it. Only shared tenants claim: an
// isolated tenant's ids live in its own home store and are resolved there.
func TestBuildServices_IdentityClaimRefusesACrossTenantCapture(t *testing.T) {
	node, _ := buildRoutedIAMNode(t)
	ctx := context.Background()

	const victimTenant = "victim-tenant"
	const captorTenant = "captor-tenant"
	for _, id := range []string{victimTenant, captorTenant} {
		if _, err := node.Tenant.CreateTenant(ctx, id, id, tenant.IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
	}

	victimCtx := tenant.WithTenantID(ctx, victimTenant)
	victim, err := node.Role.CreateRole(victimCtx, &role.CreateRoleRequest{
		Name:        "victim-admin",
		TenantID:    victimTenant,
		Permissions: []permission.Permission{permission.KVGet},
	})
	if err != nil {
		t.Fatalf("CreateRole(victim): %v", err)
	}

	// The capture attempt: the captor names the victim's id explicitly, as
	// its own caller.
	captorCtx := tenant.WithTenantID(ctx, captorTenant)
	before, err := node.Role.ListRoles(captorCtx, &role.ListRolesRequest{TenantID: captorTenant})
	if err != nil {
		t.Fatalf("ListRoles(captor): %v", err)
	}
	_, err = node.Role.CreateRole(captorCtx, &role.CreateRoleRequest{
		ID:          victim.ID,
		Name:        "impostor",
		TenantID:    captorTenant,
		Permissions: []permission.Permission{permission.KVPut},
	})
	var exists *role.RoleIDExistsError
	if !errors.As(err, &exists) {
		t.Fatalf("cross-tenant capture of a role id = %v, want *role.RoleIDExistsError", err)
	}

	// Nothing was written for the captor: the claim is settled BEFORE the
	// record, so a refused capture leaves no orphan behind.
	captorRoles, err := node.Role.ListRoles(captorCtx, &role.ListRolesRequest{TenantID: captorTenant})
	if err != nil {
		t.Fatalf("ListRoles(captor): %v", err)
	}
	if len(captorRoles.Roles) != len(before.Roles) {
		t.Fatalf("captor tenant holds %d roles after a refused capture, %d before",
			len(captorRoles.Roles), len(before.Roles))
	}

	// And the id still routes to the victim's role, unchanged.
	got, err := node.Role.GetRole(victimCtx, victim.ID)
	if err != nil {
		t.Fatalf("GetRole after the refused capture: %v", err)
	}
	if got.Name != "victim-admin" || got.TenantID != victimTenant {
		t.Fatalf("GetRole = %+v; the id was redirected to the impostor", got)
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

// softwareIDKeyWrapProvider is a keywrap.Provider claiming the built-in
// "software" provider ID. Wrap/Unwrap are never reached by these tests.
type softwareIDKeyWrapProvider struct{}

func (softwareIDKeyWrapProvider) ProviderID() string { return keywrap.SoftwareProviderID }
func (softwareIDKeyWrapProvider) Wrap(context.Context, string, []byte) ([]byte, error) {
	return nil, errors.New("softwareIDKeyWrapProvider: Wrap is not used by this test")
}
func (softwareIDKeyWrapProvider) Unwrap(context.Context, string, []byte) ([]byte, error) {
	return nil, errors.New("softwareIDKeyWrapProvider: Unwrap is not used by this test")
}

// TestBuildServices_KeyWrapProviderCollidingWithTheDefaultIsRefused: an
// injected key-wrap provider that claims the ID of the built-in software
// provider would silently shadow (or be shadowed by) it in every backup
// envelope, so BuildServices refuses the node with *BackupServiceError rather
// than booting with an ambiguous provider ID.
func TestBuildServices_KeyWrapProviderCollidingWithTheDefaultIsRefused(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()
	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:            host,
		WatchHub:        hub,
		DataDir:         t.TempDir(),
		KeyWrapProvider: softwareIDKeyWrapProvider{},
	})
	if node != nil {
		t.Fatal("BuildServices returned a node despite the provider-ID collision")
	}
	var target *lifecycle.BackupServiceError
	if !errors.As(err, &target) {
		t.Fatalf("BuildServices error = %T (%v), want *BackupServiceError", err, err)
	}
	if target.Unwrap() == nil {
		t.Fatal("BackupServiceError carries no cause")
	}
	if !strings.Contains(target.Error(), "backup service initialization failed") {
		t.Fatalf("Error() = %q, want it to name the backup service", target.Error())
	}
}

// TestBuildServices_KeyWrapProviderJoinsAnInjectedRegistry is the control: the
// same provider ID is NOT a collision when the composer supplies its own
// registry, because the built-in software provider is registered only into
// the default one. The node builds and the provider is resolvable by its ID.
func TestBuildServices_KeyWrapProviderJoinsAnInjectedRegistry(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()
	hub := watch.NewHub()
	defer hub.Close()

	reg := keywrap.NewRegistry()
	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:            host,
		WatchHub:        hub,
		DataDir:         t.TempDir(),
		KeyWrapRegistry: reg,
		KeyWrapProvider: softwareIDKeyWrapProvider{},
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	t.Cleanup(func() { _ = node.Stop(context.Background()) })

	p, err := reg.Lookup(keywrap.SoftwareProviderID)
	if err != nil {
		t.Fatalf("injected provider not registered in the composer's registry: %v", err)
	}
	if _, ok := p.(softwareIDKeyWrapProvider); !ok {
		t.Fatalf("registry resolved %T, want the injected provider", p)
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
	mgr := databasetest.NewManager(slog.Default(), dataDir)
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

// TestBuildServices_UnreadableDatabaseCatalogFails pins that a node whose
// database catalog cannot be read refuses to start rather than serving
// without the databases the catalog records.
func TestBuildServices_UnreadableDatabaseCatalogFails(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()
	hub := watch.NewHub()
	defer hub.Close()

	ctx := context.Background()
	key := database.CatalogKey("", "orders")
	if err := service.NewKVService(host, shard.ShardKV).Put(ctx, key, []byte("{not json")); err != nil {
		t.Fatalf("seed catalog record: %v", err)
	}

	dataDir := t.TempDir()
	mgr := databasetest.NewManager(slog.Default(), dataDir)
	defer func() { _ = mgr.Close() }()

	node, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host:            host,
		WatchHub:        hub,
		DataDir:         dataDir,
		DatabaseManager: mgr,
	})
	if node != nil {
		_ = node.Stop(ctx)
	}
	var catErr *lifecycle.DatabaseCatalogError
	var readErr *database.CatalogReadError
	if node != nil || !errors.As(err, &catErr) || !errors.As(err, &readErr) || readErr.Key != key {
		t.Fatalf("node = %v, err = %v; want *DatabaseCatalogError wrapping *database.CatalogReadError for %q", node, err, key)
	}
}

// TestBuildServices_WithStrategyRegistry verifies that BuildServices builds
// the TenantService with a StrategyRegistry injected; that the registry feeds
// the default BarrierKeyProvider is pinned by
// TestBuildTenantServiceOpts_ProviderWinsOverRegistry.
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

// TestBuildServices_MemoryBackend_VectorStorageIsInMemory verifies that
// BuildServices routes vector storage to the in-memory implementation for
// a node whose primary engine is explicitly database.BackendMemory
// (go-qrdb#578): it opens successfully with a populated Vector service
// backed by an in-memory store, and never touches the filesystem for it,
// even with an empty DataDir.
func TestBuildServices_MemoryBackend_VectorStorageIsInMemory(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	before, err := os.ReadDir(wd)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", wd, err)
	}

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  "", // intentionally empty: declared as a memory-engine node below
		Backend:  database.BackendMemory,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	if node.Vector == nil {
		t.Fatal("Vector service is nil; expected it to be populated even with an empty DataDir")
	}

	after, err := os.ReadDir(wd)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", wd, err)
	}
	if len(after) != len(before) {
		t.Errorf("working directory grew from %d entries to %d; a memory-backend node must create nothing on disk",
			len(before), len(after))
	}
}

// TestBuildServices_PersistentBackend_EmptyDataDirRefuses proves the other
// half of go-qrdb#578's non-blocking fix: a PERSISTENT backend (the zero
// value, Pebble's implicit default, and every explicit non-memory value)
// with an empty DataDir is refused with *lifecycle.EmptyVectorDataDirError
// rather than silently handed an in-memory vector store that would lose
// data on every restart. DataDir's emptiness alone is never, on its own, a
// declaration of intent to run ephemeral.
func TestBuildServices_PersistentBackend_EmptyDataDirRefuses(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	for _, backend := range []database.BackendType{"", database.BackendPebble, database.BackendFile} {
		_, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
			Host:     host,
			WatchHub: hub,
			DataDir:  "",
			Backend:  backend,
		})
		var refusal *lifecycle.EmptyVectorDataDirError
		if !errors.As(err, &refusal) {
			t.Fatalf("backend %q: err = %v (%T), want *lifecycle.EmptyVectorDataDirError", backend, err, err)
		}
	}
}

// TestBuildServices_MemoryBackend_WithDataDir_VectorStorageUsesPebble
// proves the directory still wins when a memory-backend node ALSO has one:
// sdk/go/driver/node deliberately roots even a memory-backend node's
// vector store in a real directory when it has one (its own
// resolveDataDir doc comment), so BuildServices must not force
// MemoryVectorStorage just because Backend is database.BackendMemory --
// only an EMPTY DataDir makes Backend the deciding factor.
func TestBuildServices_MemoryBackend_WithDataDir_VectorStorageUsesPebble(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	dataDir := t.TempDir()
	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  dataDir,
		Backend:  database.BackendMemory,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	if node.Vector == nil {
		t.Fatal("Vector service is nil")
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "vector")); statErr != nil {
		t.Errorf("expected the supplied data dir to hold the vector store: %v", statErr)
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

func (stubCeremonyCA) CACertificate(context.Context) (*x509.Certificate, error) {
	return nil, errors.New("stub")
}

func (stubCeremonyCA) CABundle() ([]byte, error) { return nil, errors.New("stub") }

func (stubCeremonyCA) IssueCertificate(context.Context, *ceremony.IssueCertRequest) (*ceremony.IssuedCert, error) {
	return nil, errors.New("stub")
}

// newTestCeremonyCore constructs a core ceremony service over the supplied
// barrier/credential pair, mirroring what BuildSealServicesOnly builds.
func newTestCeremonyCore(t *testing.T, barrier *seal.StorageBarrier, credSvc *credentials.CredentialService) *ceremony.CeremonyService {
	t.Helper()
	svc, err := ceremony.NewCeremonyService(
		&ceremony.CeremonyConfig{DataDir: t.TempDir()}, barrier, slog.Default())
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

// TestBuildServices_OverridesCeremonyCAWithClusterCAProv is the B4 proof:
// once BuildServices constructs a cluster CA (Key A) provider --
// BarrierRegistry non-nil is what triggers that, in this fixture the
// built-in cakey.SoftwareProvider -- ceremony signing moves onto it,
// overriding whatever bootstrap-window CeremonyCA Infra.CeremonyCA
// supplied (cmd/qrdbd's StaticProvider-backed one, stubbed here). Before
// this fix, ceremony-sign-csr kept signing with the in-memory bootstrap
// key forever, never moving to the replicated store's Key A once it
// became the cluster's actual CA of record.
func TestBuildServices_OverridesCeremonyCAWithClusterCAProv(t *testing.T) {
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

	if node.ClusterCAProv == nil {
		t.Fatal("fixture: BuildServices did not construct a cluster CA provider")
	}

	coreSvc, ok := service.CeremonyServiceCore(node.Ceremony)
	if !ok {
		t.Fatal("CeremonyServiceCore: core service not exposed")
	}
	got := coreSvc.CA()
	if got == ceremony.CeremonyCA(ca) {
		t.Fatal("ceremony CA is still the bootstrap-window stub; BuildServices did not override it with the cluster CA provider")
	}
	providerCA, ok := got.(*ceremony.ProviderCA)
	if !ok {
		t.Fatalf("ceremony CA = %T, want *ceremony.ProviderCA wrapping node.ClusterCAProv", got)
	}

	// Signing through the override actually reaches node.ClusterCAProv: the
	// certificate it issues chains to that provider's own CA certificate,
	// not to the stub's (which errors on every call and so could never have
	// produced one).
	want, err := node.ClusterCAProv.Certificate(context.Background())
	if err != nil {
		t.Fatalf("ClusterCAProv.Certificate: %v", err)
	}
	got2, err := providerCA.CACertificate(t.Context())
	if err != nil {
		t.Fatalf("ProviderCA.CACertificate: %v", err)
	}
	if got2.SerialNumber.Cmp(want.SerialNumber) != 0 {
		t.Fatalf("ProviderCA resolves a different CA than node.ClusterCAProv: serial %s != %s", got2.SerialNumber, want.SerialNumber)
	}
}

// TestBuildServices_ReusesCeremonyCoreSvc verifies the full-boot path reuses
// a sealed-window ceremony core service supplied via Infra.CeremonyCoreSvc —
// Gap B of the daemon Security Officer claim chain. The CA on that reused
// core service is still overridden to the cluster CA provider (B4, see
// TestBuildServices_OverridesCeremonyCAWithClusterCAProv): reuse is about the
// core SERVICE instance (and so its in-memory enclave state, pending officer
// registrations, etc.), not about pinning whatever CA it happened to be
// signing with during the sealed window.
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
	if got := coreSvc.CA(); got == ceremony.CeremonyCA(ca) {
		t.Fatal("ceremony CA on the reused core service is still the bootstrap-window stub; want it overridden to the cluster CA provider")
	} else if _, ok := got.(*ceremony.ProviderCA); !ok {
		t.Fatalf("ceremony CA = %T, want *ceremony.ProviderCA wrapping node.ClusterCAProv", got)
	}
}

// stubBackupSink is a minimal in-memory service.BackupSink whose List call is
// independently observable from BackupService's own in-memory registry, so a
// test can prove which one a given Node.Backup actually reads from.
type stubBackupSink struct {
	listCalls int
	listOut   []service.BackupInfo
}

func (s *stubBackupSink) Write(_ context.Context, _ service.BackupInfo, _ map[uint64][]byte, _ *service.BackupEnvelope) error {
	return nil
}

func (s *stubBackupSink) ReadInfo(_ context.Context, id string) (*service.BackupInfo, error) {
	return nil, &service.BackupNotFoundError{ID: id}
}

func (s *stubBackupSink) ReadShards(_ context.Context, id string) (map[uint64][]byte, error) {
	return nil, &service.BackupNotFoundError{ID: id}
}

func (s *stubBackupSink) ReadEnvelope(_ context.Context, id string) (*service.BackupEnvelope, error) {
	return nil, nil
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

// THE SEAL-DISABLED NODE. BuildServices explicitly supports SealBarrier ==
// nil, and in that branch it used to leave n.User and n.Role on the raw
// unconfined store — so two prefix tenants on an embedded node with
// encryption off shared ONE IAM keyspace. That is the exposure #41 closes
// everywhere else, and keyspace isolation is not a property of encryption:
// a node that has not turned on the seal subsystem still has tenants, and
// they are still entitled to be separate.
func TestBuildServices_SealDisabled_StillIsolatesPrefixTenants(t *testing.T) {
	host := newLocalHost()
	t.Cleanup(func() { _ = host.Close() })

	hub := watch.NewHub()
	t.Cleanup(hub.Close)

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
		// No SealBarrier and no BarrierRegistry: the seal-disabled branch.
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	t.Cleanup(func() { _ = node.Stop(context.Background()) })

	if node.StoreResolver == nil {
		t.Fatal("no StoreResolver on a seal-disabled node: IAM is unconfined, " +
			"so every prefix tenant shares one keyspace")
	}

	ctx := tenant.WithTenantID(context.Background(), "acme")
	acme, err := node.User.CreateUser(ctx, &user.CreateUserRequest{WithoutEmailLoginKey: true,
		Username: "alice", TenantID: "acme",
	})
	if err != nil {
		t.Fatalf("CreateUser(acme): %v", err)
	}

	// globex must not see acme's user by name... as globex's own caller
	// (the system-tenant lift was removed, owner ruling 2026-10-01, so a
	// cross-tenant attempt now reads *CrossTenantDeniedError before the
	// keyspace question is even reached; the keyspace-sharing question this
	// test is about is tested from globex's own identity instead).
	globexCtx := tenant.WithTenantID(context.Background(), "globex")
	if _, gerr := node.User.GetUserByUsername(globexCtx, "globex", "alice"); gerr == nil {
		t.Fatal("globex resolved acme's user by username — the two prefix " +
			"tenants share one IAM keyspace")
	} else {
		var notFound *user.UserNotFoundError
		if !errors.As(gerr, &notFound) {
			t.Fatalf("cross-tenant lookup: want UserNotFoundError, got %v", gerr)
		}
	}

	// ...nor by listing.
	listed, err := node.User.ListUsers(globexCtx, &user.ListUsersRequest{TenantID: "globex"})
	if err != nil {
		t.Fatalf("ListUsers(globex): %v", err)
	}
	for _, u := range listed.Users {
		if u.ID == acme.ID {
			t.Fatalf("globex's user list contains acme's user %d", acme.ID)
		}
	}

	// And acme still reaches its own record, by name and by id.
	if got, gerr := node.User.GetUserByUsername(ctx, "acme", "alice"); gerr != nil || got.ID != acme.ID {
		t.Fatalf("acme cannot read its own user: (%+v, %v)", got, gerr)
	}
	if got, gerr := node.User.GetUser(ctx, acme.ID); gerr != nil || got.TenantID != "acme" {
		t.Fatalf("by-id routing on a seal-disabled node: (%+v, %v)", got, gerr)
	}
}

// TestBuildServices_SealDisabled_ServesBarrierProtectionNone is go-qrdb#1285:
// the seal-disabled branch built no barrier service, so neither qrdbd nor an
// embedded node registered one and every transport answered BarrierStatus
// with "barrier service is not configured".
func TestBuildServices_SealDisabled_ServesBarrierProtectionNone(t *testing.T) {
	host := newLocalHost()
	t.Cleanup(func() { _ = host.Close() })

	hub := watch.NewHub()
	t.Cleanup(hub.Close)

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:     host,
		WatchHub: hub,
		DataDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	t.Cleanup(func() { _ = node.Stop(context.Background()) })

	if node.Barrier == nil {
		t.Fatal("no barrier service on a seal-disabled node")
	}
	st, err := node.Barrier.BarrierStatus(context.Background())
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if st.Sealed || !st.Initialized || st.Strategy != string(seal.StrategyNone) {
		t.Fatalf("status = %+v, want strategy none, initialized, unsealed", st)
	}
	var sde *service.SealDisabledError
	if err := node.Barrier.BarrierSeal(context.Background()); !errors.As(err, &sde) {
		t.Fatalf("BarrierSeal = %v, want *service.SealDisabledError", err)
	}
}

// TestBuildServices_SealRequestedButAbsent_ServesNoBarrier: a composer that
// asked for a seal and failed to build its barrier passes SealEnabled with no
// SealBarrier. That is a failure the composer reports, not protection none,
// so no barrier service claims the node is unsealed.
func TestBuildServices_SealRequestedButAbsent_ServesNoBarrier(t *testing.T) {
	host := newLocalHost()
	t.Cleanup(func() { _ = host.Close() })

	hub := watch.NewHub()
	t.Cleanup(hub.Close)

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:        host,
		WatchHub:    hub,
		DataDir:     t.TempDir(),
		SealEnabled: true,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	t.Cleanup(func() { _ = node.Stop(context.Background()) })

	if node.Barrier != nil {
		t.Fatal("a requested barrier that failed to construct was served as protection none")
	}
}

// TestBuildServices_WiresTheKeyspaceReclaimerIntoTheManager pins the wiring
// go-qrdb#412 depends on.
//
// This is the ONE place the database manager and the data-plane engines meet
// in every composition that can drop a database -- the daemon, the SDK's
// in-process node, an embedding -- which is why the reclaimer is wired here
// rather than at a server. A manager without one reclaims a dropped
// database's directory and leaves every key it owned in the shared store.
func TestBuildServices_WiresTheKeyspaceReclaimerIntoTheManager(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	mgr := databasetest.NewManager(nil, t.TempDir())
	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:            host,
		WatchHub:        hub,
		DataDir:         t.TempDir(),
		DatabaseManager: mgr,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	if node.KeyspaceReclaim == nil {
		t.Fatal("Node.KeyspaceReclaim is nil; a composer has nothing to attach a StoreResolver to")
	}
	wired := mgr.KeyspaceReclaimer()
	if wired == nil {
		t.Fatal("the database manager has no keyspace reclaimer; every drop would leak the database's keys")
	}
	if wired != database.KeyspaceReclaimer(node.KeyspaceReclaim) {
		t.Error("the manager must hold the same reclaimer the Node exposes, or a StoreResolver attached to one never reaches the other")
	}
}

// TestBuildServices_WiresTheHomeRetirerIntoTheTenantService pins the seam
// DELETE TENANT retires a strict tenant's home group through: without it the
// home group's replicas outlive the barrier they write through.
func TestBuildServices_WiresTheHomeRetirerIntoTheTenantService(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:            host,
		WatchHub:        hub,
		DataDir:         t.TempDir(),
		DatabaseManager: databasetest.NewManager(nil, t.TempDir()),
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	seams, ok := node.Tenant.(interface{ HomeRetirer() tenant.HomeRetirer })
	if !ok {
		t.Fatalf("tenant service %T carries no teardown seams", node.Tenant)
	}
	wired := seams.HomeRetirer()
	if wired == nil {
		t.Fatal("the tenant service has no home retirer; deleting a strict tenant would leave its home group running")
	}
	if wired != node.Database.(tenant.HomeRetirer) {
		t.Error("the home retirer must be the database service the node serves, whose placement table holds the home group")
	}
}

// TestBuildServices_ReclaimerReachesEveryShapeADatabaseOwns is the
// behavioural half of the wiring, and the one test in this change that runs
// against a REAL engine and a REAL *service.KVService rather than a double.
//
// It exists because a double let a defect through. Every other reclamation
// test drives service.ReclaimKV with an in-memory double, and until it was
// made faithful that double applied no internal-key guard while the
// production *KVService does. The reclaimer enumerated "ts:<tenant>/<db>/"
// through the user-facing List, which returns an EMPTY list for it -- the
// keys are internal and the prefix is not inside a namespace the guard
// waives -- so it deleted nothing under the three time-series prefixes and
// reported the drop a success. Measured, not reasoned: this test failed on
// exactly those three keys and passed on the other five (go-qrdb#412).
//
// Two properties of its construction matter:
//
//   - The residue is probed with Get on the EXACT key, never with List. A
//     List-based assertion would be vacuous for precisely the keys that were
//     leaking, because the guard hides them whether or not they are there.
//   - Every shape database_keys.go claims is in the fixture, so the test
//     fails on the first one a future change stops reclaiming rather than on
//     whichever one someone remembered.
func TestBuildServices_ReclaimerReachesEveryShapeADatabaseOwns(t *testing.T) {
	ctx := context.Background()
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	mgr := databasetest.NewManager(nil, t.TempDir())
	node, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host:            host,
		WatchHub:        hub,
		DataDir:         t.TempDir(),
		DatabaseManager: mgr,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(ctx) }()

	if _, err := mgr.Create(database.DatabaseConfig{
		Name: "reports", Backend: database.BackendMemory,
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	ns := tenant.ComposeNamespace(tenantid.System, "reports")
	other := tenant.ComposeNamespace(tenantid.System, "ledger")

	reclaimed := map[string]string{
		"plain kv":               ns + "greeting",
		"relational catalog":     ns + "__sys:tables/reports/orders",
		"relational row":         ns + "__tbl:reports:7/pk1",
		"relational index entry": ns + "__idx:reports:7:by_customer/acme",
		"table-id sequence":      ns + "__seq:reports:__tableid",
		"auto-increment":         ns + "__seq:reports:__autoinc:7",
		"privilege":              ns + "__sys:privs/reports/alice",
		"time-series point":      timeseries.KeyPrefixPoints + ns + "cpu:0001:abcd",
		"time-series metric idx": timeseries.KeyPrefixMetricIndex + ns + "cpu",
		"time-series retention":  timeseries.KeyPrefixRetention + ns + "cpu",
	}
	kept := map[string]string{
		"another database":    other + "greeting",
		"another db's series": timeseries.KeyPrefixPoints + other + "cpu:0001:abcd",
		"no database owns it": "__sys:roles/admin",
		"the tag index":       timeseries.KeyPrefixTagIndex + "host:web1",
	}

	for _, set := range []map[string]string{reclaimed, kept} {
		for _, k := range set {
			if err := node.KV.Put(ctx, k, []byte("v")); err != nil {
				t.Fatalf("put %q: %v", k, err)
			}
		}
	}

	if err := mgr.Drop(ctx, "reports"); err != nil {
		t.Fatalf("drop: %v", err)
	}

	for what, key := range reclaimed {
		if _, err := node.KV.Get(ctx, key); err == nil {
			t.Errorf("%s survived the drop: %q", what, key)
		}
	}
	for what, key := range kept {
		if _, err := node.KV.Get(ctx, key); err != nil {
			t.Errorf("%s must not be reclaimed by another database's drop: %q (%v)", what, key, err)
		}
	}
}

// TestBuildServices_ReclaimerDoesNotDependOnTheDiscoveryFilter states the
// root cause as a property rather than a story.
//
// The production List is filtered and returns nothing for a time-series
// prefix; ListAll is not and returns the keys. A reclamation that reads
// through the first cannot see what it is meant to delete. If these two ever
// agree again for this prefix, either the guard changed or the seam collapsed
// back into the filtered call, and both are the regression.
func TestBuildServices_ReclaimerDoesNotDependOnTheDiscoveryFilter(t *testing.T) {
	ctx := context.Background()
	host := newLocalHost()
	defer func() { _ = host.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(ctx, lifecycle.Infra{
		Host: host, WatchHub: hub, DataDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(ctx) }()

	ns := tenant.ComposeNamespace(tenantid.System, "reports")
	prefix := timeseries.KeyPrefixPoints + ns
	if err := node.KV.Put(ctx, prefix+"cpu:0001:abcd", []byte("v")); err != nil {
		t.Fatalf("put: %v", err)
	}

	filtered, err := node.KV.List(ctx, prefix)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(filtered) != 0 {
		t.Errorf("List(%q) = %v, want empty — the discovery guard is what makes a List-based reclamation a no-op", prefix, filtered)
	}

	all, err := node.KV.ListAll(ctx, prefix)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("ListAll(%q) = %v, want the one key — the reclaimer reads through this, and it must see it", prefix, all)
	}
}

// TestBuildServices_ClusterCAProv_GeneratesOnFirstUse covers the default
// path every ordinary caller takes: no Infra.ClusterCA{Cert,Key}PEM and no
// Infra.ClusterCAKeyProvider. ClusterCAProv is still non-nil, generated on
// first use exactly like CertProv (Key B) already is.
func TestBuildServices_ClusterCAProv_GeneratesOnFirstUse(t *testing.T) {
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

	if node.ClusterCAProv == nil {
		t.Fatal("ClusterCAProv is nil, want a generated-on-first-use SoftwareProvider")
	}
	cert, err := node.ClusterCAProv.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	if !cert.IsCA {
		t.Fatal("generated cluster CA certificate is not a CA certificate")
	}
}

// TestBuildServices_ClusterCAProv_AdoptsInMemoryMaterial covers the
// bootstrap crash-window path: a caller supplies in-memory key material
// generated before any store existed, and it is what gets persisted.
func TestBuildServices_ClusterCAProv_AdoptsInMemoryMaterial(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()
	hub := watch.NewHub()
	defer hub.Close()
	barrier, registry, credSvc := newTestBarrier(t)

	caCertPEM, caKeyPEM, caCert, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		ClusterCACertPEM:  caCertPEM,
		ClusterCAKeyPEM:   caKeyPEM,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	gotCert, err := node.ClusterCAProv.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate: %v", err)
	}
	if gotCert.SerialNumber.Cmp(caCert.SerialNumber) != 0 {
		t.Fatalf("adopted certificate serial %s != supplied serial %s", gotCert.SerialNumber, caCert.SerialNumber)
	}

	// A second BuildServices call over the SAME store/barrier, with no
	// material this time, must LOAD the already-adopted CA rather than
	// generating a new one -- the "ordinary restart after persistence"
	// case.
	node2, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
	})
	if err != nil {
		t.Fatalf("BuildServices (restart): %v", err)
	}
	defer func() { _ = node2.Stop(context.Background()) }()
	gotCert2, err := node2.ClusterCAProv.Certificate(context.Background())
	if err != nil {
		t.Fatalf("Certificate (restart): %v", err)
	}
	if gotCert2.SerialNumber.Cmp(caCert.SerialNumber) != 0 {
		t.Fatalf("restart generated a NEW cluster CA (serial %s) instead of loading the adopted one (%s)", gotCert2.SerialNumber, caCert.SerialNumber)
	}
}

// stubClusterCAProvider is a minimal cakey.Provider double for proving
// Infra.ClusterCAKeyProvider replaces the built-in SoftwareProvider
// entirely.
type stubClusterCAProvider struct {
	cert *x509.Certificate
}

func (s *stubClusterCAProvider) Signer(context.Context) (crypto.Signer, error) { return nil, nil }
func (s *stubClusterCAProvider) Certificate(context.Context) (*x509.Certificate, error) {
	return s.cert, nil
}

// TestBuildServices_ClusterCAProv_InjectedProviderReplacesBuiltin covers the
// HSM/TPM/KMS injection seam: when Infra.ClusterCAKeyProvider is set, it is
// used as-is and the built-in SoftwareProvider's store prefix is never
// touched.
func TestBuildServices_ClusterCAProv_InjectedProviderReplacesBuiltin(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()
	hub := watch.NewHub()
	defer hub.Close()
	barrier, registry, credSvc := newTestBarrier(t)

	_, _, caCert, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	injected := &stubClusterCAProvider{cert: caCert}

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:                 host,
		WatchHub:             hub,
		DataDir:              t.TempDir(),
		SealBarrier:          barrier,
		BarrierRegistry:      registry,
		CredentialCoreSvc:    credSvc,
		ClusterCAKeyProvider: injected,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	if node.ClusterCAProv != cakey.Provider(injected) {
		t.Fatalf("ClusterCAProv = %v, want the injected provider %v", node.ClusterCAProv, injected)
	}

	keys, err := node.KV.List(context.Background(), "__sys:cluster_ca/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("injected provider must leave __sys:cluster_ca/ untouched, found %v", keys)
	}
}

// TestBuildServices_NoClusterCAKey_LeavesClusterCAProvNil is the BYO-TLS
// proof: Infra.NoClusterCAKey with no injected provider must leave
// ClusterCAProv nil rather than stand up a built-in SoftwareProvider that
// would generate (and persist) a self-issued Key A nobody asked for. Key B
// (CertProv) is a separate trust domain and must be unaffected.
func TestBuildServices_NoClusterCAKey_LeavesClusterCAProvNil(t *testing.T) {
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
		NoClusterCAKey:    true,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	if node.ClusterCAProv != nil {
		t.Fatalf("ClusterCAProv = %v, want nil with NoClusterCAKey set and no injected provider", node.ClusterCAProv)
	}
	if node.CertProv == nil {
		t.Fatal("CertProv (Key B) is nil; NoClusterCAKey must not affect the separate login-CA trust domain")
	}

	keys, err := node.KV.List(context.Background(), "__sys:cluster_ca/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("NoClusterCAKey must leave __sys:cluster_ca/ untouched, found %v", keys)
	}
}

// TestBuildServices_NoClusterCAKey_InjectedProviderWins proves
// Infra.ClusterCAKeyProvider still wins over NoClusterCAKey: an injected
// provider is an explicit choice to serve Key A from elsewhere, and
// NoClusterCAKey only withholds the BUILT-IN provider.
func TestBuildServices_NoClusterCAKey_InjectedProviderWins(t *testing.T) {
	host := newLocalHost()
	defer func() { _ = host.Close() }()
	hub := watch.NewHub()
	defer hub.Close()
	barrier, registry, credSvc := newTestBarrier(t)

	_, _, caCert, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	injected := &stubClusterCAProvider{cert: caCert}

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:                 host,
		WatchHub:             hub,
		DataDir:              t.TempDir(),
		SealBarrier:          barrier,
		BarrierRegistry:      registry,
		CredentialCoreSvc:    credSvc,
		NoClusterCAKey:       true,
		ClusterCAKeyProvider: injected,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	if node.ClusterCAProv != cakey.Provider(injected) {
		t.Fatalf("ClusterCAProv = %v, want the injected provider %v even with NoClusterCAKey set", node.ClusterCAProv, injected)
	}
}

// newLeasedMVCCHost creates an in-memory LocalBackend over an MVCCEngine
// with a lessor attached, for a test that needs a host supporting leases
// and chunked import sessions -- a capability newLocalHost's bare KVEngine
// does not claim.
func newLeasedMVCCHost(t *testing.T) *backend.LocalBackend {
	t.Helper()
	eng, err := engine.NewMVCCEngine(memory.NewStorage(), engine.WithMVCCLessor(lease.NewLessor()))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	return backend.NewLocalBackend(eng)
}

// TestBuildServices_ImportSessionConfigWired proves Infra.ImportSession
// actually reaches the constructed Batch service (go-qrdb#576: previously
// nothing called service.WithImportSessionLimits in production, so
// config.ImportSessionConfig -- "import_session:" in YAML -- did nothing).
// A tenant limited to one concurrent session is refused a second
// ImportBegin only when that limit was actually threaded through
// BuildServices.
func TestBuildServices_ImportSessionConfigWired(t *testing.T) {
	host := newLeasedMVCCHost(t)
	defer func() { _ = host.Close() }()
	hub := watch.NewHub()
	defer hub.Close()

	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:          host,
		WatchHub:      hub,
		DataDir:       t.TempDir(),
		ImportSession: qrdbconfig.ImportSessionConfig{MaxSessionsPerTenant: 1},
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	defer func() { _ = node.Stop(context.Background()) }()

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{TenantID: "acme", Subject: "alice"})
	opts := service.ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row"}

	if _, err := node.Batch.ImportBegin(ctx, opts); err != nil {
		t.Fatalf("first ImportBegin: %v", err)
	}
	if _, err := node.Batch.ImportBegin(ctx, opts); !errors.As(err, new(*service.ImportQuotaExceededError)) {
		t.Fatalf("second ImportBegin error = %T (%v), want *service.ImportQuotaExceededError -- Infra.ImportSession was not applied", err, err)
	}
}
