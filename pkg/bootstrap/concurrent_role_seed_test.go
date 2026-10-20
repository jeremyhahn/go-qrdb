// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// This file is package bootstrap_test (external), not bootstrap (internal,
// like the rest of this directory's _test.go files), because
// TestProvisionTenant_ConcurrentCallersConverge needs
// pkg/server/lifecycle.NewGuardedKVStoreAdapter, and lifecycle imports
// bootstrap (pending_admin.go, for CompletePendingTenantAdmin): an internal
// test file importing lifecycle would close that into
// bootstrap(test) -> lifecycle -> bootstrap, which the Go toolchain refuses
// as an import cycle. An external test package is a separate compilation
// unit that imports bootstrap like any other caller, so it can also import
// lifecycle without closing the cycle. Every symbol this file needs from
// bootstrap is already exported for exactly this reason.
package bootstrap_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"sync"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	raftbackend "github.com/jeremyhahn/go-qrdb/pkg/backend/raft"
	"github.com/jeremyhahn/go-qrdb/pkg/bootstrap"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/raftharness"
)

// discardLogger returns a logger this file's assertions never read, mirroring
// bootstrap_test.go's own helper of the same name -- unexported, and
// unreachable from this separate package, so it is reproduced rather than
// shared.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// validAdminCSR returns a syntactically and cryptographically valid CSR PEM,
// mirroring bootstrap_test.go's own helper of the same name -- unexported,
// and unreachable from this separate package, so it is reproduced rather
// than shared. planAdmin parses and signature-checks the CSR (cert.ParseCSR)
// before anything is written, so every cert-mode ProvisionTenant call here
// needs a real one rather than a placeholder string.
func validAdminCSR(t *testing.T) []byte {
	t.Helper()
	_, csrPEM, err := certtest.NewCSR("irrelevant-csr-subject")
	if err != nil {
		t.Fatalf("certtest.NewCSR: %v", err)
	}
	return csrPEM
}

// realCertPEM generates a valid self-signed certificate PEM, mirroring
// bootstrap_test.go's own helper of the same name.
func realCertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "admin"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// concurrentFakeSigner is bootstrap_test.go's fakeSigner, reproduced here for
// the same unexported-and-unreachable reason as discardLogger: it satisfies
// firstboot.CertSigner (ProvisionTenant's Signer seam) without importing
// firstboot, since Go does not require importing an interface's defining
// package to implement it structurally.
type concurrentFakeSigner struct {
	cert []byte
}

func (s *concurrentFakeSigner) SignAdminCSR(_ context.Context, _, _ string, _ []byte) ([]byte, error) {
	return s.cert, nil
}

// concurrentFakeBinder is bootstrap_test.go's fakeBinder, reproduced here for
// the same reason as concurrentFakeSigner; it satisfies firstboot.CertBinder
// (ProvisionTenant's Binder seam).
type concurrentFakeBinder struct{}

func (b *concurrentFakeBinder) BindAdminCert(_ context.Context, _ string, _ uint64, _ string) error {
	return nil
}

// concurrentFakeTenants is fakeTenants made safe for concurrent callers: the
// FIRST CreateTenant call for an id creates it; every other call — from
// another goroutine racing the same tenant, exactly as N nodes' own
// ProvisionTenant calls would — gets tenant.ErrTenantAlreadyExists, exactly
// as the real tenant.TenantServicer answers a racing create.
type concurrentFakeTenants struct {
	mu      sync.Mutex
	created map[string]bool
}

func (f *concurrentFakeTenants) CreateTenant(_ context.Context, id, name string, isolationType tenant.IsolationType, _ ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.created == nil {
		f.created = map[string]bool{}
	}
	if f.created[id] {
		return nil, tenant.ErrTenantAlreadyExists
	}
	f.created[id] = true
	return &tenant.TenantInfo{ID: id, Name: name, IsolationType: isolationType}, nil
}

// concurrentFakeUsers is fakeUsers made safe for concurrent callers: the
// FIRST CreateUser call for a tenant+username pair creates it; every other
// call gets user.UsernameExistsError, exactly as the real user.UserServicer
// answers a racing create.
type concurrentFakeUsers struct {
	mu      sync.Mutex
	nextID  uint64
	created map[string]bool
}

func (f *concurrentFakeUsers) CreateUser(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := req.TenantID + "/" + req.Username
	if f.created == nil {
		f.created = map[string]bool{}
	}
	if f.created[key] {
		return nil, &user.UsernameExistsError{Username: req.Username, TenantID: req.TenantID}
	}
	f.created[key] = true
	f.nextID++
	return &user.User{ID: f.nextID, Username: req.Username, TenantID: req.TenantID, RoleIDs: req.RoleIDs, Lifecycle: user.LifecycleActive}, nil
}

// TestProvisionTenant_ConcurrentCallersConverge is the regression test for
// the firstboot role race's second root cause: seedTenantRoles, reached
// through ProvisionTenant, racing ANOTHER concurrent ProvisionTenant call
// for the SAME new tenant under the SAME deterministic built-in role ids.
//
// Runs over a REAL go-quicraft cluster (raftharness), not a fake, for the
// same reason TestRegisterBuiltInRoles_ConcurrentSeedersConverge
// (pkg/firstboot) and TestReconcileBuiltInRoles_ConcurrentCallersConverge
// (pkg/role) do: the defect is a visibility-lag race between a create
// collision (reported authoritatively by the Raft LEADER) and the
// verifying read's own StaleRead, which a single-process fake cannot model.
// Tenants/Users are concurrency-safe fakes (the race under test is
// specifically about ROLES); Roles is a REAL, WithAtomicIdentity role.Store
// per caller, one per cluster host, exactly as production composes it.
func TestProvisionTenant_ConcurrentCallersConverge(t *testing.T) {
	const callers = 3
	const shardID = uint64(9201)
	const tenantID = "concurrent-tenant"

	c := raftharness.NewCluster(t, callers)
	members := make(map[uint64]string, callers)
	for i := range callers {
		members[uint64(i+1)] = c.Addrs[i]
	}
	for i := range callers {
		if err := c.Hosts[i].StartShard(members, false, c.CreateFn(), quicraftconfig.Config{
			ShardID: shardID, ReplicaID: uint64(i + 1), MemberIdentities: c.Identities,
		}); err != nil {
			t.Fatalf("StartShard(host %d): %v", i, err)
		}
	}
	waitShardReadableWithin(t, c.Hosts, shardID, extraShardReadyBound)

	roleSvcs := make([]role.RoleServicer, callers)
	for i := range callers {
		host := raftbackend.New(c.Hosts[i])
		guarded, err := lifecycle.NewGuardedKVStoreAdapter(
			service.NewKVService(host, shardID),
			service.NewIndexService(host, shardID),
			service.NewTransactionService(host, shardID),
		)
		if err != nil {
			t.Fatalf("NewGuardedKVStoreAdapter(host %d): %v", i, err)
		}
		idx, err := tenant.NewIdentityIndex(guarded)
		if err != nil {
			t.Fatalf("NewIdentityIndex(host %d): %v", i, err)
		}
		svc, err := role.NewStore(guarded, role.WithIdentityIndex(idx), role.WithAtomicIdentity())
		if err != nil {
			t.Fatalf("role.NewStore(host %d): %v", i, err)
		}
		roleSvcs[i] = svc
	}

	tenants := &concurrentFakeTenants{}
	users := &concurrentFakeUsers{}

	adminCSRPEM := validAdminCSR(t)
	signer := &concurrentFakeSigner{cert: realCertPEM(t)}
	binder := &concurrentFakeBinder{}

	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// A caller re-runs a provisioning an election cut short: the
			// re-run is idempotent, and that is the contract a caller of a
			// non-exclusive ProvisionTenant has on a leader change.
			_, err := service.SettleLeadership(context.Background(), provisionSettle, func(ctx context.Context) (*bootstrap.TenantProvisionResult, error) {
				return bootstrap.ProvisionTenant(ctx, bootstrap.TenantProvisionConfig{
					TenantID:    tenantID,
					Isolation:   tenant.IsolationPrefix,
					AdminCSRPEM: adminCSRPEM,
				}, bootstrap.Dependencies{
					Tenants: tenants, Users: users, Roles: roleSvcs[i],
					Signer: signer, Binder: binder, Logger: discardLogger(),
					TrustDomain: "db.example.com",
				})
			})
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: ProvisionTenant: %v", i, err)
		}
	}
	if t.Failed() {
		return
	}

	for _, want := range role.BuiltInRoles(tenantID) {
		for i := range callers {
			got, err := service.SettleLeadership(context.Background(), readSettle, func(ctx context.Context) (*role.Role, error) {
				return roleSvcs[i].GetRoleByName(ctx, tenantID, want.Name)
			})
			if err != nil {
				t.Fatalf("host %d: GetRoleByName(%q): %v", i, want.Name, err)
			}
			if got.ID != want.ID || got.TenantID != want.TenantID || got.System != want.System {
				t.Errorf("host %d: role %q = %+v, want canonical %+v", i, want.Name, got, want)
			}
		}
	}
}

// extraShardReadyBound bounds waitShardReadableWithin's wait for THIS
// test's own second shard (every raftharness.NewCluster host already forms
// and waits out shard.ShardKV's own election under raftharness.Bound, 2
// minutes, before this test starts). Longer than raftharness.Bound: under
// `make coverage`'s instrumentation overhead and full-suite parallel load,
// a SECOND election on the same hosts was observed to miss that window
// (two observed timeouts in pkg/firstboot's identical test shape, each
// resolving on a bare retry under lighter load) -- a genuine host-contention
// risk, not a defect in the election itself, so the fix is more patience,
// not a different algorithm.
const extraShardReadyBound = 5 * time.Minute

// shardReadyPollAttempt bounds a single readiness probe inside
// waitShardReadableWithin, rather than handing a probe the whole `bound`:
// SyncGetShardMembership blocks for its entire context budget while no
// leader has confirmed a read index yet, so a probe sized to `bound` turns
// the retry loop into one very long call -- a single slow election round
// consumes the whole budget and the wait fails having tried exactly once.
// The two timeouts extraShardReadyBound's doc records were exactly that: not
// "needs more patience" but "never got a second try." A short per-attempt
// timeout lets the loop send a fresh ReadIndex request throughout the whole
// bound window instead of spending it on a single attempt.
const shardReadyPollAttempt = 2 * time.Second

// provisionSettle waits out an election that cuts a caller's provisioning
// short (a leader that misses its check-quorum window on a loaded host steps
// down mid-proposal and answers "not leader ... it may still commit"): the
// caller re-runs ProvisionTenant, which is idempotent when not Exclusive.
// An attempt gets the whole budget because it is a chain of Raft writes
// that each answer promptly on a leader change; extraShardReadyBound is the
// hang guard, not a speed assertion.
var provisionSettle = service.SettlePolicy{
	Budget:  extraShardReadyBound,
	Attempt: extraShardReadyBound,
	Poll:    100 * time.Millisecond,
}

// readSettle waits out an election under the convergence reads, with a
// short attempt for the reason shardReadyPollAttempt records.
var readSettle = service.SettlePolicy{
	Budget:  extraShardReadyBound,
	Attempt: shardReadyPollAttempt,
	Poll:    100 * time.Millisecond,
}

// waitShardReadableWithin is raftharness.Cluster.WaitReadable with a wait
// bound this test controls directly, rather than the package's own fixed
// raftharness.Bound -- see extraShardReadyBound.
func waitShardReadableWithin(t testing.TB, hosts []*quicraft.Host, shardID uint64, bound time.Duration) {
	t.Helper()
	deadline := time.Now().Add(bound)
	for _, host := range hosts {
		for {
			ctx, cancel := context.WithTimeout(context.Background(), shardReadyPollAttempt)
			_, err := host.SyncGetShardMembership(ctx, shardID)
			cancel()
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("waitShardReadableWithin: shard %d membership never became readable: %v", shardID, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}
