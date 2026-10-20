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

package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/invite"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// homedTenant is one isolated tenant the confinement probes write for.
type homedTenant struct {
	name      string
	id        string
	isolation tenant.IsolationType
	// policy is the tenant's own policy; empty inherits the floor.
	policy tenant.TenantIsolationPolicy
	// floor is the cluster floor set before the tenant is created.
	floor tenant.ClusterIsolationPolicy
}

// homedTenants are tenants whose records must live in their home store and
// nowhere in the system store: every isolated type, whatever its policy. The
// policy decides what an operator may do, never where a record lives.
var homedTenants = []homedTenant{
	{name: "strict raft", id: "vault", isolation: tenant.IsolationRaft, policy: tenant.TenantIsolationPolicyStrict},
	{name: "operator-access raft", id: "ledger", isolation: tenant.IsolationRaft, floor: tenant.ClusterIsolationPolicyOperatorAccess},
	{name: "barrier under a strict floor", id: "safe", isolation: tenant.IsolationBarrier, floor: tenant.ClusterIsolationPolicyStrict},
	{name: "operator-access volume", id: "archive", isolation: tenant.IsolationVolume, floor: tenant.ClusterIsolationPolicyOperatorAccess},
}

// mapHomeSource is a tenant.HomeStoreSource whose every home store is the
// daemon's own guarded adapter over one map, standing in for the tenant's
// home store.
type mapHomeSource struct {
	data  map[string][]byte
	store kvstore.KVStore
}

func newMapHomeSource(t *testing.T) *mapHomeSource {
	t.Helper()
	data := make(map[string][]byte)
	store, err := newGuardedKVServiceAdapter(newMockKVStore(data), newMockTransactionServicer(data), nil, nil)
	if err != nil {
		t.Fatalf("home store adapter: %v", err)
	}
	return &mapHomeSource{data: data, store: store}
}

func (m *mapHomeSource) EnsureHome(context.Context, string) error { return nil }

func (m *mapHomeSource) HomeStore(context.Context, string) (kvstore.KVStore, error) {
	return m.store, nil
}

var _ tenant.HomeStoreSource = (*mapHomeSource)(nil)

// homeFixture is the daemon's IAM wiring over a system store map, with one
// isolated tenant whose home store is a second map.
type homeFixture struct {
	tenantID string
	system   map[string][]byte
	home     *mapHomeSource
	mgmt     *mgmtBootstrapResult
}

// newHomeFixture boots the daemon's IAM wiring and creates tc's tenant,
// unsealed, with its home store attached.
func newHomeFixture(t *testing.T, tc homedTenant) *homeFixture {
	t.Helper()
	ctx := context.Background()

	strategy, err := seal.NewSoftwareStrategy(ctx, []byte("tenant-home-system-pass"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	sysBarrier, err := seal.NewStorageBarrier(reattachTestLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "system/root-key"}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := sysBarrier.Initialize(ctx, seal.Credentials{}); err != nil {
		t.Fatalf("system Initialize: %v", err)
	}
	reg, _, allocator := sealtest.PooledRegistry(t, sysBarrier)
	svc, err := tenant.NewTenantService(seal.NewMemoryBackend(), reg, reattachTestLogger(),
		tenant.WithDefaultEntitlements(&quota.Entitlements{
			AllowedIsolationModes: []string{
				quota.IsolationModePrefix, quota.IsolationModeBarrier,
				quota.IsolationModeVolume, quota.IsolationModeRaft,
			},
			AllowDedicatedRaft: true,
			AllowVolumeStorage: true,
			AllowedVolumePools: []string{"default"},
		}),
		tenant.WithVolumeAllocator(allocator))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	if tc.floor != "" {
		if err := svc.SetClusterIsolationPolicy(ctx, tc.floor); err != nil {
			t.Fatalf("SetClusterIsolationPolicy(%s): %v", tc.floor, err)
		}
	}
	var opts []tenant.CreateTenantOption
	if tc.policy != "" {
		opts = append(opts, tenant.WithIsolationPolicy(tc.policy))
	}
	if _, err := svc.CreateTenant(ctx, tc.id, tc.name, tc.isolation, opts...); err != nil {
		t.Fatalf("CreateTenant(%s): %v", tc.name, err)
	}
	if err := reg.InitializeTenant(ctx, tc.id, seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant(%s): %v", tc.name, err)
	}

	system := make(map[string][]byte)
	mock := newMockKVStore(system)
	mgmt, err := bootstrapUserRoleStores(ctx, mock, mock, newMockTransactionServicer(system), &mockRaftNode{}, testLeaderWait, 1, 1,
		svc, false, reg, seal.StrategyPassphrase, nil, auth.MTLSBindingEnrolled, nil, nil, nil)
	if err != nil {
		t.Fatalf("bootstrapUserRoleStores: %v", err)
	}
	router, ok := mgmt.storeResolver.(tenant.HomeRouter)
	if !ok {
		t.Fatalf("the daemon's store resolver %T cannot route home stores", mgmt.storeResolver)
	}
	home := newMapHomeSource(t)
	router.SetHomeStoreSource(home)
	return &homeFixture{tenantID: tc.id, system: system, home: home, mgmt: mgmt}
}

// forEachHomedTenant runs fn once per homedTenants case, on a fresh fixture.
func forEachHomedTenant(t *testing.T, fn func(t *testing.T, f *homeFixture)) {
	t.Helper()
	for _, tc := range homedTenants {
		t.Run(tc.name, func(t *testing.T) {
			fn(t, newHomeFixture(t, tc))
		})
	}
}

// ctx returns a context bound to the fixture's tenant.
func (f *homeFixture) ctx() context.Context {
	return tenant.WithTenantID(context.Background(), f.tenantID)
}

// assertConfined fails unless every key is in the tenant's home store and in
// none of the forms the system store could hold a tenant's record under: as
// is, in the tenant's prefix keyspace, or in the tenant's or the system
// tenant's identity namespace. Each probe is one exact key.
func (f *homeFixture) assertConfined(t *testing.T, family string, keys ...string) {
	t.Helper()
	if len(keys) == 0 {
		t.Fatalf("%s: no keys to probe", family)
	}
	for _, key := range keys {
		if _, ok := f.home.data[key]; !ok {
			t.Errorf("%s: %q is not in the tenant's home store", family, key)
		}
		for _, form := range []string{
			key,
			f.tenantID + "/" + key,
			tenant.IdentityNamespace(f.tenantID) + key,
			tenant.IdentityNamespace("system") + key,
		} {
			if _, ok := f.system[form]; ok {
				t.Errorf("%s: %q is in the system store", family, form)
			}
		}
	}
}

// userIDSegment is the fixed-width big-endian key segment the user store
// encodes an ID as.
func userIDSegment(id uint64) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], id)
	return string(b[:])
}

func TestTenantHome_IAMRecordsStayOutOfSystemStore(t *testing.T) {
	forEachHomedTenant(t, func(t *testing.T, f *homeFixture) {
		ctx := f.ctx()
		r, err := f.mgmt.servedRoleSvc.CreateRole(ctx, &role.CreateRoleRequest{
			Name: "reader", TenantID: f.tenantID, Permissions: []permission.Permission{permission.KVGet},
		})
		if err != nil {
			t.Fatalf("CreateRole: %v", err)
		}
		f.assertConfined(t, "role",
			"__sys:roles/id/"+r.ID,
			"__sys:roles/tenant/"+f.tenantID+"/"+r.ID,
			"__sys:role_idx/name/"+f.tenantID+"/reader",
		)

		u, err := f.mgmt.servedUserSvc.CreateUser(ctx, &user.CreateUserRequest{
			Username: "alice", Email: "alice@db.example.com", TenantID: f.tenantID, RoleIDs: []string{r.ID},
		})
		if err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		if len(u.RoleIDs) != 1 || u.RoleIDs[0] != r.ID {
			t.Fatalf("user roles = %v, want the binding to %s", u.RoleIDs, r.ID)
		}
		// The role binding is part of the user record, so the primary key covers it.
		f.assertConfined(t, "user",
			user.PrimaryRecordKey(u.ID),
			"__sys:users/tenant/"+f.tenantID+"/"+userIDSegment(u.ID),
			"__sys:user_idx/username/"+f.tenantID+"/alice",
			"__sys:user_idx/email/"+f.tenantID+"/"+user.NormalizeEmail("alice@db.example.com"),
		)

		// The served services read back what they wrote, from the home store.
		if got, err := f.mgmt.servedUserSvc.GetUser(ctx, u.ID); err != nil || got.Username != "alice" {
			t.Fatalf("GetUser = %v, %v; want alice from the home store", got, err)
		}
	})
}

// TestTenantHome_IdentityIndexHoldsNoEntry: an isolated tenant's ids are
// resolved in its own tenant, so creating its users and roles claims nothing
// in the system shard's identity index. The system tenant's own role, created
// the same way, does claim — the probe key is the one a claim would occupy.
func TestTenantHome_IdentityIndexHoldsNoEntry(t *testing.T) {
	forEachHomedTenant(t, func(t *testing.T, f *homeFixture) {
		ctx := f.ctx()
		indexKey := func(kind tenant.IdentityKind, id string) string {
			return tenant.IdentityNamespace("system") + "__sys:identity_idx/tenant/" + string(kind) + "/" + id
		}

		r, err := f.mgmt.servedRoleSvc.CreateRole(ctx, &role.CreateRoleRequest{
			Name: "reader", TenantID: f.tenantID, Permissions: []permission.Permission{permission.KVGet},
		})
		if err != nil {
			t.Fatalf("CreateRole: %v", err)
		}
		u, err := f.mgmt.servedUserSvc.CreateUser(ctx, &user.CreateUserRequest{
			Username: "alice", Email: "alice@db.example.com", TenantID: f.tenantID,
		})
		if err != nil {
			t.Fatalf("CreateUser: %v", err)
		}
		for _, key := range []string{
			indexKey(tenant.IdentityRole, r.ID),
			indexKey(tenant.IdentityUser, tenant.IdentityID(u.ID)),
		} {
			if _, ok := f.system[key]; ok {
				t.Errorf("the system store holds the isolated tenant's identity claim %q", key)
			}
		}

		// Deleting them releases nothing and fails nothing: there is no claim.
		if err := f.mgmt.servedUserSvc.DeleteUser(ctx, u.ID); err != nil {
			t.Fatalf("DeleteUser: %v", err)
		}
		if err := f.mgmt.servedRoleSvc.DeleteRole(ctx, r.ID); err != nil {
			t.Fatalf("DeleteRole: %v", err)
		}

		sysCtx := tenant.WithTenantID(context.Background(), "system")
		sr, err := f.mgmt.servedRoleSvc.CreateRole(sysCtx, &role.CreateRoleRequest{
			Name: "system-reader", TenantID: "system", Permissions: []permission.Permission{permission.KVGet},
		})
		if err != nil {
			t.Fatalf("CreateRole(system): %v", err)
		}
		if _, ok := f.system[indexKey(tenant.IdentityRole, sr.ID)]; !ok {
			t.Fatalf("the system tenant's role claim is not at %q; the probe key is wrong", indexKey(tenant.IdentityRole, sr.ID))
		}
	})
}

func TestTenantHome_DEKKeyringStaysOutOfSystemStore(t *testing.T) {
	forEachHomedTenant(t, func(t *testing.T, f *homeFixture) {
		// The software key-wrap provider resolves the tenant ID as a key file
		// inside the key directory: provision one for this tenant.
		keyDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(keyDir, f.tenantID), bytes.Repeat([]byte{0x42}, dek.KeySize), 0o600); err != nil {
			t.Fatal(err)
		}
		keyring, err := dek.NewKeyring(f.mgmt.storeResolver, dek.WithKeyDir(keyDir))
		if err != nil {
			t.Fatalf("NewKeyring: %v", err)
		}
		if _, err := keyring.Rotate(f.ctx(), f.tenantID); err != nil {
			t.Fatalf("Rotate: %v", err)
		}
		f.assertConfined(t, "dek keyring", "__sys:dek/keyring")
	})
}

func TestTenantHome_InvitationsStayOutOfSystemStore(t *testing.T) {
	forEachHomedTenant(t, func(t *testing.T, f *homeFixture) {
		ctx := f.ctx()
		invites, err := invite.NewTenantStore(f.mgmt.storeResolver)
		if err != nil {
			t.Fatalf("NewTenantStore: %v", err)
		}
		before := make(map[string]bool, len(f.home.data))
		for k := range f.home.data {
			before[k] = true
		}
		token, _, err := invites.Create(ctx, "42", f.tenantID, time.Hour)
		if err != nil {
			t.Fatalf("Create invitation: %v", err)
		}
		// The invitation's keys are the DAO's own encoding: take each one the
		// home store gained, then probe the system store for exactly that key.
		var written []string
		for k := range f.home.data {
			if !before[k] {
				written = append(written, k)
			}
		}
		f.assertConfined(t, "invitation", written...)

		if _, err := invites.Lookup(ctx, f.tenantID, token); err != nil {
			t.Fatalf("Lookup from the home store: %v", err)
		}
	})
}

// TestTenantHome_TenantCAStaysOutOfSystemStore: an isolated tenant's CA key
// and certificate, every certificate it issues and every revocation of one
// are the tenant's records. The system store holds only the published CA
// certificate relying parties trust a presented chain against.
func TestTenantHome_TenantCAStaysOutOfSystemStore(t *testing.T) {
	forEachHomedTenant(t, func(t *testing.T, f *homeFixture) {
		ctx := f.ctx()
		sysStore, err := newGuardedKVServiceAdapter(newMockKVStore(f.system), newMockTransactionServicer(f.system), nil, nil)
		if err != nil {
			t.Fatalf("system store adapter: %v", err)
		}
		guarded, ok := sysStore.(kvstore.MultiGuardedBatchKVStore)
		if !ok {
			t.Fatalf("system store %T has no guarded batch writes", sysStore)
		}
		strategy, err := seal.NewSoftwareStrategy(ctx, []byte("tenant-home-cert-pass"))
		if err != nil {
			t.Fatalf("NewSoftwareStrategy: %v", err)
		}
		barrier, err := seal.NewStorageBarrier(reattachTestLogger(), seal.NewMemoryBackend(),
			seal.BarrierConfig{RootKeyPath: "cert/root-key"}, strategy)
		if err != nil {
			t.Fatalf("NewStorageBarrier: %v", err)
		}
		if err := barrier.Initialize(ctx, seal.Credentials{}); err != nil {
			t.Fatalf("barrier Initialize: %v", err)
		}
		prov := cert.NewDefaultProvider(guarded, barrier, cert.WithTrustDomain(authtest.ClientTrustDomain))
		prov.SetStoreResolver(f.mgmt.storeResolver)

		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		issued, err := prov.IssueClientCert(ctx, &cert.IssueRequest{
			Username: "alice", TenantID: f.tenantID, PublicKey: &key.PublicKey, TTL: time.Hour,
		})
		if err != nil {
			t.Fatalf("IssueClientCert: %v", err)
		}
		if _, err := prov.RevokeCert(ctx, issued.Serial); err != nil {
			t.Fatalf("RevokeCert: %v", err)
		}

		f.assertConfined(t, "tenant CA",
			"__sys:cert_ca/"+f.tenantID+"/material",
			"__sys:cert_ca/"+f.tenantID+"/certificate",
			"__sys:certs/"+f.tenantID+"/"+issued.Serial,
			"__sys:certs_revoked/"+issued.Serial,
		)
		if _, ok := f.system["__sys:cert_ca_pub/"+f.tenantID+"/certificate"]; !ok {
			t.Error("the tenant's CA certificate is not published in the system store")
		}
		if revoked, err := prov.IsRevoked(ctx, issued.Serial); err != nil || !revoked {
			t.Errorf("IsRevoked = %v, %v; want the home marker to answer", revoked, err)
		}
	})
}
