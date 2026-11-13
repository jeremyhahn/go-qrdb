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

package database

import (
	"path/filepath"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// BindingKind is the kind of host that applies a binding's writes.
type BindingKind string

const (
	// BindingLocal is an engine-direct local host over the database's own
	// store: memory, file, pebble or objstore. It is not replicated.
	BindingLocal BindingKind = "local"

	// BindingRaft is the database's own Raft group, whose state machine runs
	// the same engine over its own state storage.
	BindingRaft BindingKind = "raft"
)

// bindingKinds maps every backend a database can record to the kind of host
// that applies its writes.
var bindingKinds = map[BackendType]BindingKind{
	BackendMemory:   BindingLocal,
	BackendFile:     BindingLocal,
	BackendPebble:   BindingLocal,
	BackendObjstore: BindingLocal,
	BackendRaft:     BindingRaft,
}

// EncryptorScope names whose key encrypts a binding's store at rest.
type EncryptorScope string

const (
	// EncryptorNode is the node's at-rest encryptor. Only a prefix-isolated
	// tenant, which by definition has no key of its own, is bound to it.
	EncryptorNode EncryptorScope = "node"

	// EncryptorTenant is the owning tenant's barrier. Sealing it fails every
	// engine of the database closed.
	EncryptorTenant EncryptorScope = "tenant"
)

// RaftHostScope names the Raft host a raft binding's group runs on.
type RaftHostScope string

const (
	// RaftHostSystem is the node's system host and its shared log (or, for a
	// raft-isolated tenant that opted in, the tenant's dedicated WAL).
	RaftHostSystem RaftHostScope = "system"

	// RaftHostTenant is a volume-isolated tenant's own host, its log under
	// the tenant's data root.
	RaftHostTenant RaftHostScope = "tenant"
)

// isolationPlacement is what a tenant isolation mode decides about every
// binding of the tenant: where its store lives, whose key encrypts it, and
// which host runs a raft binding's group.
type isolationPlacement struct {
	// tenantRoot places the store under the tenant's own data root rather
	// than the node's.
	tenantRoot bool
	encryptor  EncryptorScope
	raftHost   RaftHostScope
}

// isolationPlacements is the tenant isolation x backend composition table of
// docs/databases/architecture.md, one row per isolation mode.
var isolationPlacements = map[tenant.IsolationType]isolationPlacement{
	tenant.IsolationPrefix:  {tenantRoot: false, encryptor: EncryptorNode, raftHost: RaftHostSystem},
	tenant.IsolationBarrier: {tenantRoot: false, encryptor: EncryptorTenant, raftHost: RaftHostSystem},
	tenant.IsolationVolume:  {tenantRoot: true, encryptor: EncryptorTenant, raftHost: RaftHostTenant},
	tenant.IsolationRaft:    {tenantRoot: true, encryptor: EncryptorTenant, raftHost: RaftHostSystem},
}

// nameVisibleBackends are the backends whose key names are file or object
// names, visible to anyone who can list the directory or bucket. A tenant
// whose contract is that its data lives under its own root and key may not
// use them (OD-7, ruled B), as a database backend or as a raft group's state
// storage.
var nameVisibleBackends = map[BackendType]struct{}{
	BackendFile:     {},
	BackendObjstore: {},
}

// raftStateStorages are the stores a raft binding's state machine may run
// over (OD-8, ruled A).
var raftStateStorages = map[BackendType]struct{}{
	BackendPebble: {},
	BackendFile:   {},
	BackendMemory: {},
}

// defaultBackendByRunMode is the backend a create that leaves it empty gets
// when the node sets no default (OD-3, ruled A): raft in raft mode, pebble in
// standalone mode.
var defaultBackendByRunMode = map[bool]BackendType{
	true:  BackendRaft,
	false: BackendPebble,
}

// DefaultRaftStateStorage is the state storage of a raft binding when neither
// the database nor the node names one.
const DefaultRaftStateStorage = BackendPebble

// Defaults are the node's database defaults: the values a create fills into a
// field the caller left empty. They are applied once, at creation, and the
// result is recorded, so an existing database never consults them again.
type Defaults struct {
	// Backend is the default storage backend. Empty selects the run mode's
	// default (raft in raft mode, pebble in standalone mode).
	Backend BackendType
	// Replicas is the default replication factor of a raft database. Zero
	// selects one.
	Replicas int
	// RaftClusterMode is the default cluster mode of a raft database.
	RaftClusterMode RaftClusterMode
	// RaftStateStorage is the default state storage of a raft database's
	// group. Empty selects DefaultRaftStateStorage.
	RaftStateStorage BackendType
	// RunMode is the node's run mode. A create whose backend neither the
	// request nor Backend names records the run mode's default backend
	// (raft in raft mode, pebble in standalone mode). Empty records none.
	RunMode RunMode
}

// BindingNode is the node-level input to ResolveBinding.
type BindingNode struct {
	// DataDir is the node's data root, under which prefix- and
	// barrier-isolated tenants' database stores live.
	DataDir string
	// RaftMode reports whether the node hosts Raft groups at all.
	RaftMode bool
	// Defaults fill the fields a database record left empty.
	Defaults Defaults
}

// BindingTenant is the owning tenant's input to ResolveBinding.
type BindingTenant struct {
	// ID is the tenant's identifier.
	ID string
	// Isolation is the tenant's isolation mode. Empty is prefix, the
	// tenant service's default.
	Isolation tenant.IsolationType
	// DataRoot is a volume- or raft-isolated tenant's dedicated data root,
	// resolved on this node. It is unused for prefix and barrier tenants.
	DataRoot string
}

// RaftBinding is the raft half of a binding: where its group runs and what
// its state machine stores into.
type RaftBinding struct {
	// Host is the Raft host the database's group runs on.
	Host RaftHostScope
	// DedicatedWAL places the group's log in the raft-isolated tenant's own
	// WAL rather than the system host's shared log.
	DedicatedWAL bool
	// Replicas is the group's replication factor.
	Replicas int
	// ClusterMode is the group's cluster topology.
	ClusterMode RaftClusterMode
	// StateStorage is the store the group's state machine runs the engine
	// over: pebble, file or memory.
	StateStorage BackendType
}

// Binding is the resolved, concrete placement of one (tenant, database): the
// store every engine of the database is built over, where it lives, and whose
// key encrypts it. It is computed by ResolveBinding and never by anything
// else.
type Binding struct {
	// TenantID is the owning tenant.
	TenantID string
	// Database is the database name.
	Database string
	// Kind is the kind of host that applies the database's writes.
	Kind BindingKind
	// Backend is the database's storage backend.
	Backend BackendType
	// DataRoot is the directory of the database's own store. It is empty
	// for a memory store, which has nothing at rest.
	DataRoot string
	// Encryptor names whose key encrypts the store at rest.
	Encryptor EncryptorScope
	// DatabaseBarrier reports a per-database barrier layered on the store
	// (OD-6, ruled A): sealing it, like sealing the tenant's, fails every
	// engine of the database closed.
	DatabaseBarrier bool
	// Raft is set for a raft binding only.
	Raft *RaftBinding
}

// TenantBarrierBound reports whether sealing the owning tenant's barrier
// fails this binding closed: true for every tenant whose key encrypts it.
func (b *Binding) TenantBarrierBound() bool {
	return b.Encryptor == EncryptorTenant
}

// ResolveBinding computes the storage binding of a database from its record,
// its owning tenant's record and the node's defaults, and nothing else. It is
// a pure function: it performs no I/O and opens nothing. A combination the
// design refuses is returned as its typed error, before anything is created,
// and never degrades to another backend or to the control-plane store.
func ResolveBinding(cfg *DatabaseConfig, t BindingTenant, node BindingNode) (*Binding, error) {
	if t.ID == "" {
		return nil, &TenantIDRequiredError{Operation: "resolve binding"}
	}
	isolation := t.Isolation
	if isolation == "" {
		isolation = tenant.IsolationPrefix
	}
	placement, ok := isolationPlacements[isolation]
	if !ok {
		return nil, &InvalidTenantIsolationError{TenantID: t.ID, Isolation: string(isolation)}
	}

	backend := cfg.Backend
	if backend == "" {
		backend = node.Defaults.Backend
	}
	if backend == "" {
		backend = defaultBackendByRunMode[node.RaftMode]
	}
	kind, ok := bindingKinds[backend]
	if !ok {
		return nil, &InvalidBackendError{Backend: string(backend)}
	}

	if kind == BindingRaft && !node.RaftMode {
		return nil, &RaftBackendUnavailableError{Name: cfg.Name}
	}
	if kind != BindingRaft {
		if option := raftOptionSet(cfg); option != "" {
			return nil, &RaftOptionsRequireRaftBackendError{Name: cfg.Name, Backend: backend, Option: option}
		}
	}
	if err := refuseNameVisible(cfg.Name, backend, isolation); err != nil {
		return nil, err
	}
	barrier := cfg.Barrier != nil && cfg.Barrier.Enabled
	if barrier && backend == BackendMemory {
		return nil, &BarrierRequiresPersistentBackendError{Name: cfg.Name}
	}
	if cfg.Protocol.Etcd != nil && cfg.Protocol.Etcd.Enabled && kind != BindingRaft {
		return nil, &EtcdRequiresRaftBackendError{Name: cfg.Name, Backend: backend}
	}

	b := &Binding{
		TenantID:        t.ID,
		Database:        cfg.Name,
		Kind:            kind,
		Backend:         backend,
		Encryptor:       placement.encryptor,
		DatabaseBarrier: barrier,
	}
	if kind == BindingRaft {
		rb, err := resolveRaftBinding(cfg, isolation, placement, node.Defaults)
		if err != nil {
			return nil, err
		}
		b.Raft = rb
	}
	// A memory store has nothing at rest, so it has no directory -- and no
	// node or tenant root is required to place it.
	if backend != BackendMemory {
		root, err := bindingRoot(cfg, t, isolation, placement, node.DataDir)
		if err != nil {
			return nil, err
		}
		b.DataRoot = root
	}
	return b, nil
}

// raftOptionSet names the first raft-only option a database record sets, or
// returns "" when it sets none.
func raftOptionSet(cfg *DatabaseConfig) string {
	if cfg.Replicas != 0 {
		return "replicas"
	}
	if cfg.DedicatedWAL {
		return "dedicated_wal"
	}
	r := cfg.Raft
	if r == nil {
		return ""
	}
	switch {
	case r.ClusterMode != "":
		return "raft.clusterMode"
	case r.ShardID != 0:
		return "raft.shardId"
	case r.ReplicaID != 0:
		return "raft.replicaId"
	case r.RaftAddress != "":
		return "raft.raftAddress"
	case len(r.Peers) != 0:
		return "raft.peers"
	case r.LocalNodeCount != 0:
		return "raft.localNodeCount"
	case r.StateStorage != "":
		return "raft.stateStorage"
	case r.Discovery != "" || r.Static != nil || r.Multicast != nil || r.DNS != nil || r.Token != nil:
		return "raft.discovery"
	}
	return ""
}

// refuseNameVisible refuses a backend whose key names are visible outside the
// tenant's encryption for a tenant whose contract forbids it (OD-7).
func refuseNameVisible(name string, backend BackendType, isolation tenant.IsolationType) error {
	if !isolationPlacements[isolation].tenantRoot {
		return nil
	}
	if _, visible := nameVisibleBackends[backend]; visible {
		return &BackendIsolationConflictError{Name: name, Backend: backend, Isolation: string(isolation)}
	}
	return nil
}

// bindingRoot computes the directory of a database's store: its explicit
// data_dir, held under the tenant's root where the tenant has one, or the
// isolation mode's default.
func bindingRoot(cfg *DatabaseConfig, t BindingTenant, isolation tenant.IsolationType, placement isolationPlacement, nodeDataDir string) (string, error) {
	explicit := strings.TrimSpace(cfg.DataDir)
	if explicit != "" && !placement.tenantRoot {
		return filepath.Clean(explicit), nil
	}
	base := t.DataRoot
	if !placement.tenantRoot {
		base = ""
		if strings.TrimSpace(nodeDataDir) != "" {
			base = filepath.Join(nodeDataDir, "tenants", t.ID)
		}
	}
	if strings.TrimSpace(base) == "" {
		return "", &BindingRootUnresolvedError{TenantID: t.ID, Name: cfg.Name, Isolation: string(isolation)}
	}
	if explicit == "" {
		return filepath.Join(base, "databases", cfg.Name), nil
	}
	dir := filepath.Clean(explicit)
	if !beneathRoot(filepath.Clean(base), dir) {
		return "", &DataDirOutsideTenantRootError{Name: cfg.Name, DataDir: cfg.DataDir, TenantRoot: base}
	}
	return dir, nil
}

// beneathRoot reports whether dir lies strictly beneath root: the root itself
// holds the tenant's other state and is never a database's store. Both must
// be cleaned; a relative path is never beneath an absolute root.
func beneathRoot(root, dir string) bool {
	if filepath.IsAbs(root) != filepath.IsAbs(dir) {
		return false
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// resolveRaftBinding computes a raft binding's host, log and state storage
// from the database record, its tenant's isolation and the node's defaults.
func resolveRaftBinding(cfg *DatabaseConfig, isolation tenant.IsolationType, placement isolationPlacement, d Defaults) (*RaftBinding, error) {
	rb := &RaftBinding{
		Host:         placement.raftHost,
		DedicatedWAL: cfg.DedicatedWAL && isolation == tenant.IsolationRaft,
		Replicas:     cfg.Replicas,
		StateStorage: d.RaftStateStorage,
	}
	// The record's state storage, resolved at create, wins over the node's.
	if cfg.Raft != nil && cfg.Raft.StateStorage != "" {
		rb.StateStorage = cfg.Raft.StateStorage
	}
	if rb.Replicas == 0 {
		rb.Replicas = d.Replicas
	}
	if rb.Replicas == 0 {
		rb.Replicas = 1
	}
	if cfg.Raft != nil {
		rb.ClusterMode = cfg.Raft.ClusterMode
	}
	if rb.ClusterMode == "" {
		rb.ClusterMode = d.RaftClusterMode
	}
	if rb.StateStorage == "" {
		rb.StateStorage = DefaultRaftStateStorage
	}
	if _, ok := raftStateStorages[rb.StateStorage]; !ok {
		return nil, &InvalidRaftStateStorageError{Name: cfg.Name, StateStorage: rb.StateStorage}
	}
	if err := refuseNameVisible(cfg.Name, rb.StateStorage, isolation); err != nil {
		return nil, err
	}
	return rb, nil
}
