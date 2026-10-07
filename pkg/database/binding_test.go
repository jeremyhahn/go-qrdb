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

package database

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

const (
	bindNodeDir    = "/var/lib/qrdb"
	bindTenantRoot = "/srv/pool/acme"
)

// bindTenant is the tenant record of one isolation mode, with a data root
// where the mode has one.
func bindTenant(isolation tenant.IsolationType) BindingTenant {
	t := BindingTenant{ID: "acme", Isolation: isolation}
	if isolationPlacements[isolation].tenantRoot {
		t.DataRoot = bindTenantRoot
	}
	return t
}

// TestResolveBinding_IsolationByBackend is acceptance test 2A(1): every cell
// of the tenant isolation x backend matrix resolves to the binding the
// composition table in docs/databases/architecture.md names, or to its typed
// refusal. Each expected binding is written out in full, so a cell that
// resolves to the right kind in the wrong place still fails.
func TestResolveBinding_IsolationByBackend(t *testing.T) {
	nodeRoot := filepath.Join(bindNodeDir, "tenants", "acme", "databases", "app")
	tenantRoot := filepath.Join(bindTenantRoot, "databases", "app")
	raft := func(host RaftHostScope) *RaftBinding {
		return &RaftBinding{Host: host, Replicas: 1, StateStorage: BackendPebble}
	}
	conflict := func(b BackendType, iso tenant.IsolationType) error {
		return &BackendIsolationConflictError{Name: "app", Backend: b, Isolation: string(iso)}
	}

	type cell struct {
		want    *Binding
		wantErr error
	}
	local := func(b BackendType, root string, enc EncryptorScope) cell {
		return cell{want: &Binding{TenantID: "acme", Database: "app", Kind: BindingLocal, Backend: b, DataRoot: root, Encryptor: enc}}
	}
	raftCell := func(root string, enc EncryptorScope, host RaftHostScope) cell {
		return cell{want: &Binding{TenantID: "acme", Database: "app", Kind: BindingRaft, Backend: BackendRaft, DataRoot: root, Encryptor: enc, Raft: raft(host)}}
	}

	matrix := map[tenant.IsolationType]map[BackendType]cell{
		tenant.IsolationPrefix: {
			BackendMemory:   local(BackendMemory, "", EncryptorNode),
			BackendFile:     local(BackendFile, nodeRoot, EncryptorNode),
			BackendPebble:   local(BackendPebble, nodeRoot, EncryptorNode),
			BackendObjstore: local(BackendObjstore, nodeRoot, EncryptorNode),
			BackendRaft:     raftCell(nodeRoot, EncryptorNode, RaftHostSystem),
		},
		tenant.IsolationBarrier: {
			BackendMemory:   local(BackendMemory, "", EncryptorTenant),
			BackendFile:     local(BackendFile, nodeRoot, EncryptorTenant),
			BackendPebble:   local(BackendPebble, nodeRoot, EncryptorTenant),
			BackendObjstore: local(BackendObjstore, nodeRoot, EncryptorTenant),
			BackendRaft:     raftCell(nodeRoot, EncryptorTenant, RaftHostSystem),
		},
		tenant.IsolationVolume: {
			BackendMemory:   local(BackendMemory, "", EncryptorTenant),
			BackendFile:     {wantErr: conflict(BackendFile, tenant.IsolationVolume)},
			BackendPebble:   local(BackendPebble, tenantRoot, EncryptorTenant),
			BackendObjstore: {wantErr: conflict(BackendObjstore, tenant.IsolationVolume)},
			BackendRaft:     raftCell(tenantRoot, EncryptorTenant, RaftHostTenant),
		},
		tenant.IsolationRaft: {
			BackendMemory:   local(BackendMemory, "", EncryptorTenant),
			BackendFile:     {wantErr: conflict(BackendFile, tenant.IsolationRaft)},
			BackendPebble:   local(BackendPebble, tenantRoot, EncryptorTenant),
			BackendObjstore: {wantErr: conflict(BackendObjstore, tenant.IsolationRaft)},
			BackendRaft:     raftCell(tenantRoot, EncryptorTenant, RaftHostSystem),
		},
	}

	// The matrix must cover every isolation mode and every backend the
	// package knows: a mode or backend added later without a row fails here
	// rather than going untested.
	if len(matrix) != len(isolationPlacements) {
		t.Fatalf("matrix covers %d isolation modes, the binding table has %d", len(matrix), len(isolationPlacements))
	}
	for iso, row := range matrix {
		if len(row) != len(validBackends) {
			t.Fatalf("%s row covers %d backends, %d are valid", iso, len(row), len(validBackends))
		}
		for backend, c := range row {
			t.Run(string(iso)+"/"+string(backend), func(t *testing.T) {
				cfg := &DatabaseConfig{Name: "app", Backend: backend}
				got, err := ResolveBinding(cfg, bindTenant(iso), BindingNode{DataDir: bindNodeDir, RaftMode: true})
				if c.wantErr != nil {
					if !reflect.DeepEqual(err, c.wantErr) {
						t.Fatalf("err = %#v, want %#v", err, c.wantErr)
					}
					if got != nil {
						t.Fatalf("refused cell returned a binding: %+v", got)
					}
					return
				}
				if err != nil {
					t.Fatalf("ResolveBinding: %v", err)
				}
				if !reflect.DeepEqual(got, c.want) {
					t.Fatalf("binding = %+v (raft %+v), want %+v (raft %+v)", got, got.Raft, c.want, c.want.Raft)
				}
				if got.TenantBarrierBound() != (c.want.Encryptor == EncryptorTenant) {
					t.Fatalf("TenantBarrierBound = %v for encryptor %s", got.TenantBarrierBound(), got.Encryptor)
				}
			})
		}
	}
}

// TestResolveBinding_Refusals covers every typed refusal of the "Combinations
// that are refused" table, each in a cell that would otherwise resolve.
func TestResolveBinding_Refusals(t *testing.T) {
	raftNode := BindingNode{DataDir: bindNodeDir, RaftMode: true}
	cases := []struct {
		name    string
		cfg     *DatabaseConfig
		tenant  BindingTenant
		node    BindingNode
		wantErr error
	}{
		{
			name:    "raft on a standalone node",
			cfg:     &DatabaseConfig{Name: "app", Backend: BackendRaft},
			tenant:  bindTenant(tenant.IsolationPrefix),
			node:    BindingNode{DataDir: bindNodeDir},
			wantErr: &RaftBackendUnavailableError{Name: "app"},
		},
		{
			name:    "replicas on pebble",
			cfg:     &DatabaseConfig{Name: "app", Backend: BackendPebble, Replicas: 3},
			tenant:  bindTenant(tenant.IsolationPrefix),
			node:    raftNode,
			wantErr: &RaftOptionsRequireRaftBackendError{Name: "app", Backend: BackendPebble, Option: "replicas"},
		},
		{
			name:    "dedicated wal on file",
			cfg:     &DatabaseConfig{Name: "app", Backend: BackendFile, DedicatedWAL: true},
			tenant:  bindTenant(tenant.IsolationPrefix),
			node:    raftNode,
			wantErr: &RaftOptionsRequireRaftBackendError{Name: "app", Backend: BackendFile, Option: "dedicated_wal"},
		},
		{
			name:    "cluster mode on memory",
			cfg:     &DatabaseConfig{Name: "app", Backend: BackendMemory, Raft: &DatabaseRaftConfig{ClusterMode: RaftDistributed}},
			tenant:  bindTenant(tenant.IsolationPrefix),
			node:    raftNode,
			wantErr: &RaftOptionsRequireRaftBackendError{Name: "app", Backend: BackendMemory, Option: "raft.clusterMode"},
		},
		{
			name:    "discovery on objstore",
			cfg:     &DatabaseConfig{Name: "app", Backend: BackendObjstore, Raft: &DatabaseRaftConfig{Discovery: DiscoveryStatic}},
			tenant:  bindTenant(tenant.IsolationBarrier),
			node:    raftNode,
			wantErr: &RaftOptionsRequireRaftBackendError{Name: "app", Backend: BackendObjstore, Option: "raft.discovery"},
		},
		{
			name:    "barrier on memory",
			cfg:     &DatabaseConfig{Name: "app", Backend: BackendMemory, Barrier: &DatabaseBarrierConfig{Enabled: true}},
			tenant:  bindTenant(tenant.IsolationBarrier),
			node:    raftNode,
			wantErr: &BarrierRequiresPersistentBackendError{Name: "app"},
		},
		{
			name:    "etcd on pebble",
			cfg:     &DatabaseConfig{Name: "app", Backend: BackendPebble, Protocol: ProtocolConfig{Etcd: &EtcdConfig{Enabled: true}}},
			tenant:  bindTenant(tenant.IsolationPrefix),
			node:    raftNode,
			wantErr: &EtcdRequiresRaftBackendError{Name: "app", Backend: BackendPebble},
		},
		{
			name:    "data dir outside a volume tenant's root",
			cfg:     &DatabaseConfig{Name: "app", Backend: BackendPebble, DataDir: "/srv/pool/other/databases/app"},
			tenant:  bindTenant(tenant.IsolationVolume),
			node:    raftNode,
			wantErr: &DataDirOutsideTenantRootError{Name: "app", DataDir: "/srv/pool/other/databases/app", TenantRoot: bindTenantRoot},
		},
		{
			name:    "data dir escaping a raft tenant's root",
			cfg:     &DatabaseConfig{Name: "app", Backend: BackendRaft, DataDir: bindTenantRoot + "/../other"},
			tenant:  bindTenant(tenant.IsolationRaft),
			node:    raftNode,
			wantErr: &DataDirOutsideTenantRootError{Name: "app", DataDir: bindTenantRoot + "/../other", TenantRoot: bindTenantRoot},
		},
		{
			name:    "data dir at a tenant's root itself",
			cfg:     &DatabaseConfig{Name: "app", Backend: BackendPebble, DataDir: bindTenantRoot},
			tenant:  bindTenant(tenant.IsolationRaft),
			node:    raftNode,
			wantErr: &DataDirOutsideTenantRootError{Name: "app", DataDir: bindTenantRoot, TenantRoot: bindTenantRoot},
		},
		{
			name:    "relative data dir under a tenant root",
			cfg:     &DatabaseConfig{Name: "app", Backend: BackendPebble, DataDir: "databases/app"},
			tenant:  bindTenant(tenant.IsolationVolume),
			node:    raftNode,
			wantErr: &DataDirOutsideTenantRootError{Name: "app", DataDir: "databases/app", TenantRoot: bindTenantRoot},
		},
		{
			name:    "file state storage for a raft tenant",
			cfg:     &DatabaseConfig{Name: "app", Backend: BackendRaft},
			tenant:  bindTenant(tenant.IsolationRaft),
			node:    BindingNode{DataDir: bindNodeDir, RaftMode: true, Defaults: Defaults{RaftStateStorage: BackendFile}},
			wantErr: &BackendIsolationConflictError{Name: "app", Backend: BackendFile, Isolation: string(tenant.IsolationRaft)},
		},
		{
			name:    "objstore state storage",
			cfg:     &DatabaseConfig{Name: "app", Backend: BackendRaft},
			tenant:  bindTenant(tenant.IsolationPrefix),
			node:    BindingNode{DataDir: bindNodeDir, RaftMode: true, Defaults: Defaults{RaftStateStorage: BackendObjstore}},
			wantErr: &InvalidRaftStateStorageError{Name: "app", StateStorage: BackendObjstore},
		},
		{
			name:    "unknown backend",
			cfg:     &DatabaseConfig{Name: "app", Backend: "tape"},
			tenant:  bindTenant(tenant.IsolationPrefix),
			node:    raftNode,
			wantErr: &InvalidBackendError{Backend: "tape"},
		},
		{
			name:    "unknown isolation",
			cfg:     &DatabaseConfig{Name: "app", Backend: BackendPebble},
			tenant:  BindingTenant{ID: "acme", Isolation: "shared"},
			node:    raftNode,
			wantErr: &InvalidTenantIsolationError{TenantID: "acme", Isolation: "shared"},
		},
		{
			name:    "no tenant",
			cfg:     &DatabaseConfig{Name: "app", Backend: BackendPebble},
			node:    raftNode,
			wantErr: &TenantIDRequiredError{Operation: "resolve binding"},
		},
		{
			name:    "volume tenant without a data root",
			cfg:     &DatabaseConfig{Name: "app", Backend: BackendPebble},
			tenant:  BindingTenant{ID: "acme", Isolation: tenant.IsolationVolume},
			node:    raftNode,
			wantErr: &BindingRootUnresolvedError{TenantID: "acme", Name: "app", Isolation: string(tenant.IsolationVolume)},
		},
		{
			name:    "node without a data dir",
			cfg:     &DatabaseConfig{Name: "app", Backend: BackendPebble},
			tenant:  bindTenant(tenant.IsolationPrefix),
			node:    BindingNode{RaftMode: true},
			wantErr: &BindingRootUnresolvedError{TenantID: "acme", Name: "app", Isolation: string(tenant.IsolationPrefix)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveBinding(tc.cfg, tc.tenant, tc.node)
			if !reflect.DeepEqual(err, tc.wantErr) {
				t.Fatalf("err = %#v, want %#v", err, tc.wantErr)
			}
			if got != nil {
				t.Fatalf("refusal returned a binding: %+v", got)
			}
			if err.Error() == "" {
				t.Fatal("refusal has an empty message")
			}
		})
	}
}

// TestResolveBinding_Defaults proves the node's defaults fill only what the
// record left empty: the run mode picks raft or pebble (OD-3), a node default
// overrides that, and a value the record sets is never replaced.
func TestResolveBinding_Defaults(t *testing.T) {
	prefix := bindTenant(tenant.IsolationPrefix)
	cases := []struct {
		name        string
		cfg         *DatabaseConfig
		node        BindingNode
		wantBackend BackendType
		wantRaft    *RaftBinding
	}{
		{
			name:        "raft mode defaults to raft",
			cfg:         &DatabaseConfig{Name: "app"},
			node:        BindingNode{DataDir: bindNodeDir, RaftMode: true},
			wantBackend: BackendRaft,
			wantRaft:    &RaftBinding{Host: RaftHostSystem, Replicas: 1, StateStorage: BackendPebble},
		},
		{
			name:        "standalone defaults to pebble",
			cfg:         &DatabaseConfig{Name: "app"},
			node:        BindingNode{DataDir: bindNodeDir},
			wantBackend: BackendPebble,
		},
		{
			name:        "node default backend",
			cfg:         &DatabaseConfig{Name: "app"},
			node:        BindingNode{DataDir: bindNodeDir, RaftMode: true, Defaults: Defaults{Backend: BackendFile}},
			wantBackend: BackendFile,
		},
		{
			name: "node raft defaults",
			cfg:  &DatabaseConfig{Name: "app"},
			node: BindingNode{DataDir: bindNodeDir, RaftMode: true, Defaults: Defaults{
				Replicas: 3, RaftClusterMode: RaftDistributed, RaftStateStorage: BackendMemory,
			}},
			wantBackend: BackendRaft,
			wantRaft:    &RaftBinding{Host: RaftHostSystem, Replicas: 3, ClusterMode: RaftDistributed, StateStorage: BackendMemory},
		},
		{
			name: "record wins over defaults",
			cfg:  &DatabaseConfig{Name: "app", Backend: BackendRaft, Replicas: 5, Raft: &DatabaseRaftConfig{ClusterMode: RaftLocal}},
			node: BindingNode{DataDir: bindNodeDir, RaftMode: true, Defaults: Defaults{
				Backend: BackendPebble, Replicas: 3, RaftClusterMode: RaftDistributed,
			}},
			wantBackend: BackendRaft,
			wantRaft:    &RaftBinding{Host: RaftHostSystem, Replicas: 5, ClusterMode: RaftLocal, StateStorage: BackendPebble},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveBinding(tc.cfg, prefix, tc.node)
			if err != nil {
				t.Fatalf("ResolveBinding: %v", err)
			}
			if got.Backend != tc.wantBackend {
				t.Fatalf("backend = %s, want %s", got.Backend, tc.wantBackend)
			}
			if !reflect.DeepEqual(got.Raft, tc.wantRaft) {
				t.Fatalf("raft = %+v, want %+v", got.Raft, tc.wantRaft)
			}
			if tc.cfg.Backend == "" && tc.cfg.Replicas == 0 && tc.cfg.Raft == nil {
				// Resolution is pure: the record it was given is untouched.
				if !reflect.DeepEqual(tc.cfg, &DatabaseConfig{Name: "app"}) {
					t.Fatalf("ResolveBinding mutated the record: %+v", tc.cfg)
				}
			}
		})
	}
}

// TestResolveBinding_DataDirAndBarrier covers an explicit data_dir -- taken as
// given for a tenant without a root of its own, held beneath the root for one
// with -- and a per-database barrier layered on a persistent store (OD-6).
func TestResolveBinding_DataDirAndBarrier(t *testing.T) {
	node := BindingNode{DataDir: bindNodeDir, RaftMode: true}

	got, err := ResolveBinding(&DatabaseConfig{Name: "app", Backend: BackendPebble, DataDir: "/data/app/"},
		bindTenant(tenant.IsolationPrefix), node)
	if err != nil || got.DataRoot != "/data/app" {
		t.Fatalf("prefix explicit data dir: %+v, %v", got, err)
	}

	inside := filepath.Join(bindTenantRoot, "custom", "app")
	got, err = ResolveBinding(&DatabaseConfig{Name: "app", Backend: BackendPebble, DataDir: inside},
		bindTenant(tenant.IsolationVolume), node)
	if err != nil || got.DataRoot != inside {
		t.Fatalf("volume data dir under the root: %+v, %v", got, err)
	}

	got, err = ResolveBinding(&DatabaseConfig{Name: "app", Backend: BackendFile, Barrier: &DatabaseBarrierConfig{Enabled: true}},
		bindTenant(tenant.IsolationBarrier), node)
	if err != nil || !got.DatabaseBarrier || !got.TenantBarrierBound() {
		t.Fatalf("barrier tenant with database barrier: %+v, %v", got, err)
	}

	got, err = ResolveBinding(&DatabaseConfig{Name: "app", Backend: BackendRaft, DedicatedWAL: true},
		bindTenant(tenant.IsolationRaft), node)
	if err != nil || !got.Raft.DedicatedWAL {
		t.Fatalf("raft tenant dedicated WAL: %+v, %v", got, err)
	}
	got, err = ResolveBinding(&DatabaseConfig{Name: "app", Backend: BackendRaft, DedicatedWAL: true},
		bindTenant(tenant.IsolationPrefix), node)
	if err != nil || got.Raft.DedicatedWAL {
		t.Fatalf("a dedicated WAL is a raft tenant's only: %+v, %v", got, err)
	}

	got, err = ResolveBinding(&DatabaseConfig{Name: "app", Backend: BackendRaft, Protocol: ProtocolConfig{Etcd: &EtcdConfig{Enabled: true}}},
		bindTenant(tenant.IsolationPrefix), node)
	if err != nil || got.Kind != BindingRaft {
		t.Fatalf("etcd on raft: %+v, %v", got, err)
	}
}

// TestBeneathRoot pins the containment rule a tenant's explicit data_dir is
// held to.
func TestBeneathRoot(t *testing.T) {
	cases := []struct {
		root, dir string
		want      bool
	}{
		{"/r", "/r/a", true},
		{"/r", "/r/a/b", true},
		{"/r", "/r", false},
		{"/r", "/ra", false},
		{"/r", "/", false},
		{"/r/a", "/r", false},
		{"/r", "r/a", false},
		{"r", "r/a", true},
	}
	for _, tc := range cases {
		if got := beneathRoot(tc.root, tc.dir); got != tc.want {
			t.Errorf("beneathRoot(%q, %q) = %v, want %v", tc.root, tc.dir, got, tc.want)
		}
	}
}

// TestResolveBinding_RefusalsAreTyped checks the new refusals are matched by
// errors.As, the way every transport's error table classifies them.
func TestResolveBinding_RefusalsAreTyped(t *testing.T) {
	_, err := ResolveBinding(&DatabaseConfig{Name: "app", Backend: BackendRaft}, bindTenant(tenant.IsolationPrefix), BindingNode{DataDir: bindNodeDir})
	var unavailable *RaftBackendUnavailableError
	if !errors.As(err, &unavailable) || unavailable.Name != "app" {
		t.Fatalf("err = %v, want *RaftBackendUnavailableError", err)
	}
	_, err = ResolveBinding(&DatabaseConfig{Name: "app", Backend: BackendFile}, bindTenant(tenant.IsolationVolume), BindingNode{DataDir: bindNodeDir, RaftMode: true})
	var conflict *BackendIsolationConflictError
	if !errors.As(err, &conflict) || conflict.Backend != BackendFile {
		t.Fatalf("err = %v, want *BackendIsolationConflictError", err)
	}
}
