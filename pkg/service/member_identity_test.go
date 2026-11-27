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

package service

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

var (
	identityOne     = "spki:" + strings.Repeat("11", 32)
	identityTwo     = "spki:" + strings.Repeat("22", 32)
	identityNewNode = "spki:" + strings.Repeat("44", 32)
)

// clusteredMockHost is a host WITH a transport whose system shard has
// committed replicas 1 and 2 with identities one and two.
func clusteredMockHost() *mockHost {
	return &mockHost{
		nodeIdentity: identityOne,
		memberships: map[uint64]*quicraft.Membership{
			shard.ShardKV: {
				Nodes:      map[uint64]string{1: "one.example:1", 2: "two.example:1"},
				Identities: map[uint64]string{1: identityOne, 2: identityTwo},
			},
		},
	}
}

func TestAdminService_AddMember_Identity(t *testing.T) {
	tests := []struct {
		name     string
		host     func() *mockHost
		replica  uint64
		addr     string
		override string
		want     string
		refusal  *MemberIdentityRefusal
	}{
		{name: "committed identity of a known replica", host: clusteredMockHost, replica: 2, addr: "two.example:1", want: identityTwo},
		{name: "override for a brand-new node", host: clusteredMockHost, replica: 4, addr: "four.example:1", override: identityNewNode, want: identityNewNode},
		{name: "override matching the committed identity", host: clusteredMockHost, replica: 2, addr: "two.example:1", override: identityTwo, want: identityTwo},
		{name: "override contradicting the committed identity", host: clusteredMockHost, replica: 2, addr: "two.example:1", override: identityNewNode, refusal: refusalPtr(IdentityConflict)},
		{name: "malformed override", host: clusteredMockHost, replica: 4, addr: "four.example:1", override: "spki:short", refusal: refusalPtr(IdentityInvalid)},
		{name: "brand-new node without override", host: clusteredMockHost, replica: 4, addr: "four.example:1", refusal: refusalPtr(IdentityUnknown)},
		{name: "membership unreadable", host: func() *mockHost {
			h := clusteredMockHost()
			h.membershipErr = errors.New("no leader")
			return h
		}, replica: 4, addr: "four.example:1", refusal: refusalPtr(IdentityMembershipUnavailable)},
		{name: "address committed with two identities", host: func() *mockHost {
			h := clusteredMockHost()
			h.memberships[shard.ShardKV].Observers = map[uint64]string{3: "two.example:1"}
			h.memberships[shard.ShardKV].Identities[3] = identityNewNode
			return h
		}, replica: 4, addr: "two.example:1", refusal: refusalPtr(IdentityConflict)},
		{name: "replica committed at another address", host: clusteredMockHost, replica: 2, addr: "one.example:1", refusal: refusalPtr(IdentityConflict)},
		{name: "transport-less host commits none", host: func() *mockHost { return &mockHost{} }, replica: 4, addr: "four.example:1", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			host := tc.host()
			// The proposal itself is not under test: stop it with a sentinel
			// once the identity it was given has been recorded.
			host.requestAddNodeFn = func(context.Context, uint64, uint64, string) (*quicraft.RequestState, error) {
				return nil, errProposalStopped
			}
			svc := NewAdminService(host)
			err := svc.AddMember(context.Background(), shard.ShardKV, tc.replica, tc.addr, tc.override)
			if tc.refusal != nil {
				var mie *MemberIdentityError
				if !errors.As(err, &mie) || mie.Refusal() != *tc.refusal || mie.ReplicaID != tc.replica {
					t.Fatalf("AddMember = %v, want *MemberIdentityError with refusal %d", err, *tc.refusal)
				}
				if len(host.addedIdentities) != 0 {
					t.Fatalf("a refused add must propose nothing, proposed %v", host.addedIdentities)
				}
				return
			}
			if !errors.Is(err, errProposalStopped) {
				t.Fatalf("AddMember = %v, want the add to reach the proposal", err)
			}
			if len(host.addedIdentities) != 1 || host.addedIdentities[0] != tc.want {
				t.Fatalf("proposed identities %v, want [%q]", host.addedIdentities, tc.want)
			}
		})
	}
}

func TestMemberIdentityError_Error(t *testing.T) {
	_, err := ResolveMemberIdentity(context.Background(), clusteredMockHost(), shard.ShardKV, 4, "four.example:1", "")
	if err == nil || !strings.HasPrefix(err.Error(), "service: member identity: ") ||
		!strings.Contains(err.Error(), "qrdbd node-identity --cert") {
		t.Fatalf("err = %v, want the refusal to say how to get the identity", err)
	}
}

var errProposalStopped = errors.New("proposal stopped by the test")

func refusalPtr(r MemberIdentityRefusal) *MemberIdentityRefusal { return &r }

// TestAdminService_StartShard_MemberIdentities pins that StartShard builds
// Config.MemberIdentities the way the dedicated-shard factory does: the local
// replica's is the host's own, every other member's is its host's committed
// identity, a founder refuses a member it cannot resolve, a joiner keeps only
// the anchors it can, and a transport-less host states none.
func TestAdminService_StartShard_MemberIdentities(t *testing.T) {
	founders := map[uint64]string{1: "one.example:1", 2: "two.example:1"}
	withStranger := map[uint64]string{1: "one.example:1", 2: "two.example:1", 3: "four.example:1"}
	tests := []struct {
		name    string
		host    func() *mockHost
		members map[uint64]string
		join    bool
		want    map[uint64]string
		refusal *MemberIdentityRefusal
	}{
		{name: "founder with committed peers", host: clusteredMockHost, members: founders,
			want: map[uint64]string{1: identityOne, 2: identityTwo}},
		{name: "founder with a peer no membership names", host: clusteredMockHost, members: withStranger,
			refusal: refusalPtr(IdentityUnknown)},
		{name: "joiner keeps the anchors it can resolve", host: clusteredMockHost, members: withStranger, join: true,
			want: map[uint64]string{1: identityOne, 2: identityTwo}},
		{name: "transport-less host states none", host: func() *mockHost { return &mockHost{} }, members: founders},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			host := tc.host()
			var started *config.Config
			host.startShardFn = func(_ map[uint64]string, _ bool, _ sm.CreateFunc, cfg config.Config) error {
				started = &cfg
				return nil
			}
			svc := NewAdminService(host, WithStateMachineFactory(dummyCreateFunc))
			err := svc.StartShard(context.Background(), &StartShardRequest{
				ShardID: 100, ReplicaID: 1, Members: tc.members, Join: tc.join,
			})
			if tc.refusal != nil {
				var mie *MemberIdentityError
				if !errors.As(err, &mie) || mie.Refusal() != *tc.refusal {
					t.Fatalf("StartShard = %v, want *MemberIdentityError with refusal %d", err, *tc.refusal)
				}
				if started != nil {
					t.Fatal("a refused start must not start the shard")
				}
				return
			}
			if err != nil || started == nil {
				t.Fatalf("StartShard = %v, want the shard started", err)
			}
			if !maps.Equal(started.MemberIdentities, tc.want) {
				t.Fatalf("MemberIdentities = %v, want %v", started.MemberIdentities, tc.want)
			}
		})
	}
}

// listedDatabases is a TenantDatabaseLister over a fixed catalog.
type listedDatabases []database.DatabaseMeta

func (l listedDatabases) TenantRecords(context.Context, string) ([]database.DatabaseMeta, error) {
	return l, nil
}

// TestNodeKeyLogDatabases_JudgedByDataRoute: the probe lists every database
// whose data is served from a node-keyed log -- a shared-WAL raft group and
// a database served from the shared system shard -- and spares only a
// dedicated-WAL raft group and a database served from its own barrier store.
func TestNodeKeyLogDatabases_JudgedByDataRoute(t *testing.T) {
	on := &database.DatabaseBarrierConfig{Enabled: true}
	lister := listedDatabases{
		{TenantID: "bt", Name: "raft-shared", Backend: database.BackendRaft},
		{TenantID: "bt", Name: "raft-dedicated", Backend: database.BackendRaft, DedicatedWAL: true},
		{TenantID: "bt", Name: "pebble-shared", Backend: database.BackendPebble},
		{TenantID: "bt", Name: "memory-shared", Backend: database.BackendMemory},
		{TenantID: "bt", Name: "pebble-dir-no-barrier", Backend: database.BackendPebble, DataDir: "/d/1"},
		{TenantID: "bt", Name: "pebble-barrier-shared", Backend: database.BackendPebble, Barrier: on},
		{TenantID: "bt", Name: "pebble-barrier-own", Backend: database.BackendPebble, DataDir: "/d/2", Barrier: on},
		{TenantID: "other", Name: "foreign", Backend: database.BackendPebble},
	}
	got, err := NewNodeKeyLogProbe(lister).NodeKeyLogDatabases(context.Background(), "bt")
	if err != nil {
		t.Fatalf("NodeKeyLogDatabases: %v", err)
	}
	want := []string{"raft-shared", "pebble-shared", "memory-shared", "pebble-dir-no-barrier", "pebble-barrier-shared"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NodeKeyLogDatabases = %v, want %v", got, want)
	}
}

// unopenableCatalog mirrors database.Manager for a tenant whose shared-WAL
// database is recorded in the catalog but does not open on this node:
// ListForTenant logs the open failure and leaves the database out, while
// TenantRecords reports the catalog record as written.
type unopenableCatalog struct{ records []database.DatabaseMeta }

func (c unopenableCatalog) ListForTenant(context.Context) ([]database.DatabaseInfo, error) {
	return nil, nil
}

func (c unopenableCatalog) TenantRecords(_ context.Context, tenantID string) ([]database.DatabaseMeta, error) {
	var out []database.DatabaseMeta
	for _, r := range c.records {
		if r.TenantID == tenantID {
			out = append(out, r)
		}
	}
	return out, nil
}

// TestNodeKeyLogDatabases_CountsUnopenableRecords pins the strict-isolation
// probe to the catalog: a database that cannot open (a transient failure or
// an unreleased stale entry) still writes the node-keyed log once it opens
// again, so it must still refuse a tightening to strict.
func TestNodeKeyLogDatabases_CountsUnopenableRecords(t *testing.T) {
	c := unopenableCatalog{records: []database.DatabaseMeta{
		{TenantID: "bt", Name: "broken", Backend: database.BackendRaft},
		{TenantID: "bt", Name: "own-wal", Backend: database.BackendRaft, DedicatedWAL: true},
		{TenantID: "other", Name: "foreign", Backend: database.BackendRaft},
	}}
	got, err := NewNodeKeyLogProbe(c).NodeKeyLogDatabases(context.Background(), "bt")
	if err != nil {
		t.Fatalf("NodeKeyLogDatabases: %v", err)
	}
	if want := []string{"broken"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NodeKeyLogDatabases = %v, want %v: an unopenable shared-WAL database must still refuse strict", got, want)
	}
}

// sharedShardKeys is a system-shard KV double: ListAll returns the stored
// keys under prefix, or err.
type sharedShardKeys struct {
	keys []string
	err  error
}

func (s sharedShardKeys) ListAll(_ context.Context, prefix string) ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	var out []string
	for _, k := range s.keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

// TestNodeKeyLogDatabases_ReportsImplicitDefaultNamespaceWithData: the
// implicit default database has no catalog record, but the tenant's keys in
// its shared namespace are on the system shard's node-keyed WAL, so a strict
// tightening must see it. Another tenant's keys there do not count.
func TestNodeKeyLogDatabases_ReportsImplicitDefaultNamespaceWithData(t *testing.T) {
	kv := sharedShardKeys{keys: []string{
		tenant.ComposeNamespace("bt", tenant.DefaultDatabase) + "k1",
		tenant.ComposeNamespace("other", "appdb") + "k2",
	}}
	probe := NewNodeKeyLogProbe(unopenableCatalog{}, WithSharedNamespaces(NewKVSharedNamespaceReader(kv), tenant.DefaultDatabase, "appdb"))
	got, err := probe.NodeKeyLogDatabases(context.Background(), "bt")
	if err != nil {
		t.Fatalf("NodeKeyLogDatabases: %v", err)
	}
	if want := []string{tenant.DefaultDatabase}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NodeKeyLogDatabases = %v, want %v", got, want)
	}

	// A read error fails the probe: the tightening fails closed.
	failing := NewNodeKeyLogProbe(unopenableCatalog{}, WithSharedNamespaces(NewKVSharedNamespaceReader(sharedShardKeys{err: errors.New("shard unavailable")}), tenant.DefaultDatabase))
	if _, err := failing.NodeKeyLogDatabases(context.Background(), "bt"); err == nil {
		t.Fatal("NodeKeyLogDatabases over a failing shard read = nil error, want the read error")
	}
}

// TestNodeKeyLogDatabases_ReadsCallTimeSharedNames proves the probe reads
// the names WithSharedNamespaceNames lists -- the SQL default database and
// the shared registry's databases -- when it runs, not when it is built,
// and dedupes them against the fixed names.
func TestNodeKeyLogDatabases_ReadsCallTimeSharedNames(t *testing.T) {
	kv := sharedShardKeys{keys: []string{
		tenant.ComposeNamespace("bt", "sqldefault") + "k1",
		tenant.ComposeNamespace("bt", "registered") + "k2",
		tenant.ComposeNamespace("other", "sqldefault") + "k3",
	}}
	var names []string
	probe := NewNodeKeyLogProbe(unopenableCatalog{},
		WithSharedNamespaces(NewKVSharedNamespaceReader(kv), tenant.DefaultDatabase),
		WithSharedNamespaceNames(func() []string { return names }))

	got, err := probe.NodeKeyLogDatabases(context.Background(), "bt")
	if err != nil || len(got) != 0 {
		t.Fatalf("NodeKeyLogDatabases before any name is listed = (%v, %v), want none", got, err)
	}

	names = []string{"sqldefault", "registered", tenant.DefaultDatabase, "sqldefault", ""}
	got, err = probe.NodeKeyLogDatabases(context.Background(), "bt")
	if err != nil {
		t.Fatalf("NodeKeyLogDatabases: %v", err)
	}
	if want := []string{"sqldefault", "registered"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("NodeKeyLogDatabases = %v, want %v", got, want)
	}

	// A tenant with no data under those names is not reported.
	got, err = probe.NodeKeyLogDatabases(context.Background(), "clean")
	if err != nil || len(got) != 0 {
		t.Fatalf("NodeKeyLogDatabases(clean) = (%v, %v), want none", got, err)
	}

	// Without a shared reader the names are not read.
	bare := NewNodeKeyLogProbe(unopenableCatalog{}, WithSharedNamespaceNames(func() []string { return names }))
	if got, err := bare.NodeKeyLogDatabases(context.Background(), "bt"); err != nil || len(got) != 0 {
		t.Fatalf("NodeKeyLogDatabases without a shared reader = (%v, %v), want none", got, err)
	}

	// A read error on a call-time name fails the probe closed.
	failing := NewNodeKeyLogProbe(unopenableCatalog{},
		WithSharedNamespaces(NewKVSharedNamespaceReader(sharedShardKeys{err: errors.New("shard unavailable")})),
		WithSharedNamespaceNames(func() []string { return []string{"sqldefault"} }))
	if _, err := failing.NodeKeyLogDatabases(context.Background(), "bt"); err == nil {
		t.Fatal("NodeKeyLogDatabases over a failing shard read = nil error, want the read error")
	}
}

// TestNodeKeyLogDatabases_IgnoresEmptyDefaultNamespace: a tenant that never
// wrote its implicit default database does not block a strict tightening.
func TestNodeKeyLogDatabases_IgnoresEmptyDefaultNamespace(t *testing.T) {
	kv := sharedShardKeys{keys: []string{tenant.ComposeNamespace("other", tenant.DefaultDatabase) + "k"}}
	probe := NewNodeKeyLogProbe(unopenableCatalog{}, WithSharedNamespaces(NewKVSharedNamespaceReader(kv), tenant.DefaultDatabase))
	got, err := probe.NodeKeyLogDatabases(context.Background(), "bt")
	if err != nil {
		t.Fatalf("NodeKeyLogDatabases: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("NodeKeyLogDatabases = %v, want none", got)
	}
}

// sharedVectorNamespaces is a fake of the system shard's vector service: it
// answers ListNamespaces with fixed names, ignoring the request's prefix so
// the reader's own filter is what is tested.
type sharedVectorNamespaces struct {
	names []string
	err   error
}

func (v sharedVectorNamespaces) ListNamespaces(context.Context, *ListNamespacesRequest) (*ListNamespacesResponse, error) {
	if v.err != nil {
		return nil, v.err
	}
	resp := &ListNamespacesResponse{}
	for _, n := range v.names {
		resp.Namespaces = append(resp.Namespaces, &NamespaceInfo{Namespace: n})
	}
	return resp, nil
}

// TestNodeKeyLogProbe_VectorOnlyTenantRefusesStrict: a barrier tenant whose
// only data in the implicit default database is a vector namespace has no
// catalog record and no KV key, yet its vectors are on the shared node-keyed
// log, so the probe reports the database; a tenant with no vectors there is
// not reported, and a vector read error fails the probe closed.
func TestNodeKeyLogProbe_VectorOnlyTenantRefusesStrict(t *testing.T) {
	vec := sharedVectorNamespaces{names: []string{
		tenant.ComposeNamespace("bt", tenant.DefaultDatabase) + "embeddings",
		tenant.ComposeNamespace("other", "appdb") + "docs",
	}}
	reader := NewKVSharedNamespaceReader(sharedShardKeys{}, WithSharedVectorNamespaces(vec))
	probe := NewNodeKeyLogProbe(unopenableCatalog{}, WithSharedNamespaces(reader, tenant.DefaultDatabase, "appdb"))

	got, err := probe.NodeKeyLogDatabases(context.Background(), "bt")
	if err != nil {
		t.Fatalf("NodeKeyLogDatabases: %v", err)
	}
	if len(got) != 1 || got[0] != tenant.DefaultDatabase {
		t.Fatalf("vector-only tenant: got %v, want [%s]", got, tenant.DefaultDatabase)
	}

	clean, err := probe.NodeKeyLogDatabases(context.Background(), "clean")
	if err != nil {
		t.Fatalf("NodeKeyLogDatabases clean: %v", err)
	}
	if len(clean) != 0 {
		t.Fatalf("clean tenant: got %v, want none", clean)
	}

	failing := NewNodeKeyLogProbe(unopenableCatalog{}, WithSharedNamespaces(
		NewKVSharedNamespaceReader(sharedShardKeys{}, WithSharedVectorNamespaces(sharedVectorNamespaces{err: errors.New("vector store unavailable")})),
		tenant.DefaultDatabase))
	if _, err := failing.NodeKeyLogDatabases(context.Background(), "bt"); err == nil {
		t.Fatal("a vector read error must fail the probe")
	}
}

// TestCreateForTenant_BarrierPebbleBlocksTightening: a barrier tenant's
// pebble database is served from the shared system shard, so once created
// the probe a tightening consults lists it.
func TestCreateForTenant_BarrierPebbleBlocksTightening(t *testing.T) {
	info := &tenant.TenantInfo{ID: "bt", IsolationType: tenant.IsolationBarrier}
	l := &tighteningLookup{info: info}
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := newReplicaDatabaseService(t, info, f)
	svc.(*DatabaseService).tenantInfo = l
	ctx := tenant.WithTenantID(context.Background(), "bt")

	cfg := testDBConfig("app")
	cfg.Backend = database.BackendPebble
	if _, err := svc.CreateForTenant(ctx, cfg); err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	dbs, err := NewNodeKeyLogProbe(svc.(*DatabaseService).manager).NodeKeyLogDatabases(context.Background(), "bt")
	if err != nil || len(dbs) != 1 || dbs[0] != "app" {
		t.Fatalf("NodeKeyLogDatabases = (%v, %v), want [app]", dbs, err)
	}
}

// TestCreateForTenant_BarrierPebbleRolledBackUnderATighteningMarker: a
// barrier tenant's pebble create concurrent with a tightening to strict is
// refused and rolled back, as a shared-WAL raft create is.
func TestCreateForTenant_BarrierPebbleRolledBackUnderATighteningMarker(t *testing.T) {
	info := &tenant.TenantInfo{ID: "bt", IsolationType: tenant.IsolationBarrier}
	l := &tighteningLookup{info: info, marker: true}
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxTotalReplicas: 10, MaxDedicatedWALGroups: 5}}
	svc := newReplicaDatabaseService(t, info, f)
	svc.(*DatabaseService).tenantInfo = l
	ctx := tenant.WithTenantID(context.Background(), "bt")

	cfg := testDBConfig("app")
	cfg.Backend = database.BackendPebble
	_, err := svc.CreateForTenant(ctx, cfg)
	var refused *database.StrictTenantDedicatedWALRequiredError
	if !errors.As(err, &refused) || refused.Name != "app" || refused.Tenant != "bt" {
		t.Fatalf("CreateForTenant(barrier, pebble) under a tightening marker = %v, want *StrictTenantDedicatedWALRequiredError", err)
	}
	if got, gerr := svc.GetForTenant(ctx, "app"); gerr == nil {
		t.Fatalf("refused create left %+v in the catalog", got)
	}
}
