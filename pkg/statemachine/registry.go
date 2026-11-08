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

package statemachine

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	filestore "github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	memstore "github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// Factory is a function that produces an sm.CreateFunc. Each call may
// return a fresh factory closure (e.g. for storage-backed state machines
// that need per-instance configuration).
type Factory func() sm.CreateFunc

// PersistenceType describes how a state machine persists data.
type PersistenceType string

const (
	// PersistenceMemory indicates ephemeral in-memory storage.
	PersistenceMemory PersistenceType = "memory"

	// PersistenceDisk indicates persistent on-disk storage.
	PersistenceDisk PersistenceType = "disk"

	// PersistenceHybrid indicates mixed memory and disk storage.
	PersistenceHybrid PersistenceType = "hybrid"
)

// SMKind describes the state machine interface type.
type SMKind string

const (
	// SMKindRegular is a basic serialized state machine.
	SMKindRegular SMKind = "regular"

	// SMKindDisk is a persistent disk-backed state machine.
	SMKindDisk SMKind = "disk"
)

// Capability describes a feature supported by a state machine.
type Capability string

const (
	CapabilityKV          Capability = "kv"
	CapabilityMVCC        Capability = "mvcc"
	CapabilityTimeSeries  Capability = "timeseries"
	CapabilityVector      Capability = "vector"
	CapabilityIndex       Capability = "index"
	CapabilityLease       Capability = "lease"
	CapabilityWatch       Capability = "watch"
	CapabilityTransaction Capability = "transaction"
	CapabilityBatch       Capability = "batch"
)

// Descriptor provides metadata about a registered state machine type.
//
// Capabilities is not documentation. It is what a node reports as available
// (NarrowCapabilities narrows the node's capability profile by it), and
// TestRegistryDescriptorsMatchBehaviour holds every declaration to what the
// state machine actually does.
type Descriptor struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Persistence  PersistenceType `json:"persistence"`
	Kind         SMKind          `json:"kind"`
	Capabilities []Capability    `json:"capabilities"`

	// StorageGeneric reports that the state machine composes whichever
	// storage raft.sm_storage selects (RegisterStorageGeneric).
	StorageGeneric bool `json:"storage_generic"`
}

// Has reports whether the descriptor declares c.
func (d Descriptor) Has(c Capability) bool {
	for _, have := range d.Capabilities {
		if have == c {
			return true
		}
	}
	return false
}

// entry pairs a factory with its descriptor.
type entry struct {
	factory    Factory
	descriptor Descriptor

	// generic is set for a STORAGE-GENERIC state machine — one that composes
	// whichever storage.Storage raft.sm_storage selects — and is what
	// ResolveTypedWithStorage builds it from. factory is then the same
	// construction over the zero StorageConfig.
	generic StorageGenericFactory

	// kv is set for an in-memory KVStateMachine type: the construction that
	// takes KVOptions, which ResolveTypedWithStorage passes through. factory
	// is then the same construction with none.
	kv func(opts ...KVOption) sm.CreateFunc
}

// StorageFactory creates the storage.Storage one replica of a storage-generic
// state machine composes. It is called once per (shard, replica). A replica
// whose storage cannot be opened fails to open with the error, wrapped in a
// ReplicaStorageOpenError.
type StorageFactory func(shardID, replicaID uint64) (storage.Storage, error)

// ReplicaStorageOpener opens one clustered replica's storage, set as
// StorageConfig.Replica in place of raft.sm_storage. dir is the replica's own
// directory, {Root}/sm/shard-S-replica-R, which exists when it is called. The
// opener is bound to the data-plane file system by its composer
// (node.WithReplicaStorageFactory), so the storage it opens does its I/O under
// the seal barrier. Each call returns storage owned by that one replica, and
// the replica closes it.
type ReplicaStorageOpener func(shardID, replicaID uint64, dir string) (storage.Storage, error)

// StorageGenericFactory builds a storage-generic state machine's create
// function over the storage newStorage returns for each replica, configured
// by opts.
type StorageGenericFactory func(newStorage StorageFactory, opts ...KVOption) sm.CreateFunc

// StorageConfig is where a storage-generic state machine keeps its
// per-replica storage: raft.data_dir and raft.sm_storage.
//
// It is passed to ResolveTypedWithStorage per resolution, never held in
// package state, so two nodes in one process — the SDK supports that — each
// get their own storage under their own data directory.
type StorageConfig struct {
	// Root anchors each replica's storage at {Root}/sm/shard-S-replica-R,
	// stable across restarts. Empty means a fresh temporary directory per
	// replica, which is acceptable only in tests: a production replica
	// started that way loses its state on every restart.
	Root string

	// Kind is raft.sm_storage: "pebble" (the default when empty), "file" or
	// "memory". Objstore is a snapshot/backup tier and is never a hot apply
	// path.
	Kind string

	// Encryptor is the node's at-rest encryptor. When it is enabled — the
	// seal barrier is on — every byte a replica's storage persists is
	// encrypted: pebble and file storage are opened on the encryptor's
	// barrier-driven encfs, exactly as the pebble and mvcc state machines and
	// a standalone file store are, and memory storage persists nothing. nil
	// means the barrier is off.
	Encryptor *atrest.Encryptor

	// Replica, when set, opens every replica's storage in place of Kind. A
	// state machine that carries its own storage refuses it with
	// ReplicaStorageNotApplicableError.
	Replica ReplicaStorageOpener
}

// storageKinds are the raft.sm_storage values a storage-generic state
// machine can compose.
var storageKinds = map[string]struct{}{
	"":       {},
	"pebble": {},
	"file":   {},
	"memory": {},
}

// selectsNonDefaultStorage reports whether the config names a storage other
// than the pebble default.
func (c StorageConfig) selectsNonDefaultStorage() bool {
	return c.Kind != "" && c.Kind != "pebble"
}

// Registry maps state machine type names to their Factory functions and
// descriptors. It provides O(1) lookup and is safe for concurrent use
// during application startup via a sync.RWMutex.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]entry
}

// NewRegistry creates a new empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		entries: make(map[string]entry),
	}
}

// Register adds or replaces a state machine factory for the given name.
// A default Descriptor is created with the name only.
func (r *Registry) Register(name string, factory Factory) {
	r.mu.Lock()
	r.entries[name] = entry{
		factory:    factory,
		descriptor: Descriptor{Name: name},
	}
	r.mu.Unlock()
}

// RegisterWithDescriptor adds or replaces a state machine factory with
// full metadata.
func (r *Registry) RegisterWithDescriptor(name string, factory Factory, desc Descriptor) {
	desc.Name = name
	r.mu.Lock()
	r.entries[name] = entry{factory: factory, descriptor: desc}
	r.mu.Unlock()
}

// registerKV adds or replaces an in-memory KVStateMachine type, whose
// construction takes KVOptions.
func (r *Registry) registerKV(name string, factory func(opts ...KVOption) sm.CreateFunc, desc Descriptor) {
	desc.Name = name
	r.mu.Lock()
	r.entries[name] = entry{
		factory:    func() sm.CreateFunc { return factory() },
		descriptor: desc,
		kv:         factory,
	}
	r.mu.Unlock()
}

// RegisterStorageGeneric adds or replaces a STORAGE-GENERIC state machine: one
// that composes whichever storage raft.sm_storage selects, built per
// resolution by ResolveTypedWithStorage.
func (r *Registry) RegisterStorageGeneric(name string, factory StorageGenericFactory, desc Descriptor) {
	desc.Name = name
	desc.StorageGeneric = true
	r.mu.Lock()
	r.entries[name] = entry{
		factory:    func() sm.CreateFunc { return factory(newStorageFactory(StorageConfig{})) },
		descriptor: desc,
		generic:    factory,
	}
	r.mu.Unlock()
}

// Resolve looks up the factory for the given name and returns a fresh
// sm.CreateFunc. Returns an UnknownSMTypeError if the name is not
// registered. A storage-generic state machine resolved this way composes
// the zero StorageConfig; see ResolveTypedWithStorage.
func (r *Registry) Resolve(name string) (sm.CreateFunc, error) {
	e, err := r.lookup(name)
	if err != nil {
		return nil, err
	}
	return e.factory(), nil
}

// lookup returns the entry registered under name.
func (r *Registry) lookup(name string) (entry, error) {
	r.mu.RLock()
	e, ok := r.entries[name]
	r.mu.RUnlock()
	if !ok {
		return entry{}, &UnknownSMTypeError{Name: name}
	}
	return e, nil
}

// typedName is the registry name a (type, backend) pair resolves to: the type
// alone when backend is empty, otherwise the composite key "type:backend"
// when registered, otherwise the backend name.
func (r *Registry) typedName(smType, smBackend string) string {
	if smBackend == "" {
		return smType
	}
	if compositeKey := smType + ":" + smBackend; r.Has(compositeKey) {
		return compositeKey
	}
	return smBackend
}

// ResolveTyped looks up the factory for the given SM type and backend
// combination. When backend is empty, it falls back to resolving the
// type name directly. When both are provided, it looks up the
// composite key "type:backend" first, then falls back to just backend.
func (r *Registry) ResolveTyped(smType, smBackend string) (sm.CreateFunc, error) {
	return r.Resolve(r.typedName(smType, smBackend))
}

// ResolveTypedWithStorage is ResolveTyped for a composition root that knows
// where its state machine's storage lives. A storage-generic state machine is
// built over sc; any other state machine carries its own storage, so a
// non-pebble sc.Kind is refused for it with SMStorageNotApplicableError rather
// than silently ignored, and a composer's sc.Replica factory is refused for it
// with ReplicaStorageNotApplicableError. An unknown sc.Kind is refused with
// UnknownSMStorageError.
//
// opts configure a KVStateMachine type -- the composition root's lessor, for
// one (WithKVLessor). A state machine that is not one cannot honour them, and
// is refused with KVOptionsNotApplicableError rather than built without them.
func (r *Registry) ResolveTypedWithStorage(smType, smBackend string, sc StorageConfig, opts ...KVOption) (sm.CreateFunc, error) {
	if _, ok := storageKinds[sc.Kind]; !ok {
		return nil, &UnknownSMStorageError{Kind: sc.Kind}
	}
	name := r.typedName(smType, smBackend)
	e, err := r.lookup(name)
	if err != nil {
		return nil, err
	}
	if e.generic == nil {
		if sc.Replica != nil {
			return nil, &ReplicaStorageNotApplicableError{SMType: name}
		}
		if sc.selectsNonDefaultStorage() {
			return nil, &SMStorageNotApplicableError{SMType: name, Kind: sc.Kind}
		}
		if e.kv != nil {
			return e.kv(opts...), nil
		}
		if len(opts) > 0 {
			return nil, &KVOptionsNotApplicableError{SMType: name}
		}
		return e.factory(), nil
	}
	return e.generic(newStorageFactory(sc), opts...), nil
}

// Describe returns the Descriptor for the given name. Returns
// UnknownSMTypeError if the name is not registered.
func (r *Registry) Describe(name string) (Descriptor, error) {
	r.mu.RLock()
	e, ok := r.entries[name]
	r.mu.RUnlock()
	if !ok {
		return Descriptor{}, &UnknownSMTypeError{Name: name}
	}
	return e.descriptor, nil
}

// Descriptors returns all registered descriptors sorted by name.
func (r *Registry) Descriptors() []Descriptor {
	r.mu.RLock()
	descs := make([]Descriptor, 0, len(r.entries))
	for _, e := range r.entries {
		descs = append(descs, e.descriptor)
	}
	r.mu.RUnlock()
	sort.Slice(descs, func(i, j int) bool {
		return descs[i].Name < descs[j].Name
	})
	return descs
}

// Names returns the sorted list of all registered state machine type
// names.
func (r *Registry) Names() []string {
	r.mu.RLock()
	names := make([]string, 0, len(r.entries))
	for name := range r.entries {
		names = append(names, name)
	}
	r.mu.RUnlock()
	sort.Strings(names)
	return names
}

// Has returns true if a factory is registered for the given name.
func (r *Registry) Has(name string) bool {
	r.mu.RLock()
	_, ok := r.entries[name]
	r.mu.RUnlock()
	return ok
}

// Len returns the number of registered factories.
func (r *Registry) Len() int {
	r.mu.RLock()
	n := len(r.entries)
	r.mu.RUnlock()
	return n
}

// ---------------------------------------------------------------------------
// Typed error
// ---------------------------------------------------------------------------

// UnknownSMStorageError is returned when a StorageConfig names a storage
// kind no storage-generic state machine can compose.
type UnknownSMStorageError struct {
	Kind string
}

// Error returns a human-readable description of the unknown kind.
func (e *UnknownSMStorageError) Error() string {
	return "statemachine: unknown sm_storage " + strconv.Quote(e.Kind) + "; use pebble, file or memory"
}

// KVOptionsNotApplicableError is returned when KVStateMachine options -- the
// composition root's lessor, among them -- are passed for a state machine
// that is not a KVStateMachine and so cannot honour them.
type KVOptionsNotApplicableError struct {
	SMType string
}

// Error returns a human-readable description of the refusal.
func (e *KVOptionsNotApplicableError) Error() string {
	return "statemachine: sm_type " + strconv.Quote(e.SMType) +
		" is not a KV state machine and cannot take its options (the node's lessor); use \"standard\" or another KV sm_type"
}

// SMStorageNotApplicableError is returned when a non-pebble sm_storage is
// asked of a state machine that carries its own storage.
type SMStorageNotApplicableError struct {
	SMType string
	Kind   string
}

// Error returns a human-readable description of the mismatch.
func (e *SMStorageNotApplicableError) Error() string {
	return "statemachine: sm_storage " + strconv.Quote(e.Kind) + " does not apply to sm_type " +
		strconv.Quote(e.SMType) + ", which carries its own storage; use a storage-generic sm_type (disk or fast)"
}

// ReplicaStorageNotApplicableError is returned when a composer's replica
// storage factory (StorageConfig.Replica) is set for a state machine that
// carries its own storage. Such a state machine, the clustered MVCC store
// among them, refuses the factory rather than ignore it.
type ReplicaStorageNotApplicableError struct {
	SMType string
}

// Error returns a human-readable description of the refusal.
func (e *ReplicaStorageNotApplicableError) Error() string {
	return "statemachine: a replica storage factory does not apply to sm_type " +
		strconv.Quote(e.SMType) + ", which carries its own storage; use a storage-generic sm_type (disk or fast)"
}

// ReplicaStorageOpenError is returned when one replica's storage cannot be
// opened. The replica fails to open with it and its shard does not start.
type ReplicaStorageOpenError struct {
	ShardID   uint64
	ReplicaID uint64
	Err       error
}

// Error returns a human-readable description of the failure.
func (e *ReplicaStorageOpenError) Error() string {
	return "statemachine: open storage for shard " + strconv.FormatUint(e.ShardID, 10) +
		" replica " + strconv.FormatUint(e.ReplicaID, 10) + ": " + e.Err.Error()
}

// Unwrap returns the underlying failure.
func (e *ReplicaStorageOpenError) Unwrap() error {
	return e.Err
}

// UnknownSMTypeError is returned when a state machine type name cannot
// be resolved in the registry.
type UnknownSMTypeError struct {
	Name string
}

// Error returns a human-readable description of the unknown type.
func (e *UnknownSMTypeError) Error() string {
	return "statemachine: unknown type " + strconv.Quote(e.Name)
}

// ---------------------------------------------------------------------------
// Default global registry
// ---------------------------------------------------------------------------

// defaultRegistry is the package-level registry pre-populated with all
// state machine types that can be registered without circular imports.
// Additional types (e.g. "pebble", "mvcc") are registered from their
// owning packages via the package-level Register function.
var defaultRegistry = newDefaultRegistry()

// SMKindConcurrent is a concurrent state machine with sharded reads.
const SMKindConcurrent SMKind = "concurrent"

// newDefaultRegistry creates and populates a registry with the built-in
// state machine types defined in the statemachine package.
//
// Types that live in other packages (store.NewPebbleCreateFunc,
// store.NewMVCCCreateFunc) must be registered externally to avoid
// import cycles. The cmd/qrdbd startup and store package init functions
// are the canonical registration points for those types.
func newDefaultRegistry() *Registry {
	r := NewRegistry()

	// Every state machine this package registers serves the same plane: KV,
	// the guarded single-key verbs, all-or-nothing batches, and transactions
	// decided against its own keyspace (native_txn.go, go-qrdb#136). None is
	// constructed with an index manager, time-series store, watch manager or
	// MVCC store, so none declares those; declaring one requires wiring it AND
	// the behaviour test in descriptor_truth_test.go passing for it
	// (go-qrdb#135).
	caps := func() []Capability {
		return []Capability{CapabilityKV, CapabilityBatch, CapabilityTransaction}
	}
	// Every KVStateMachine also keeps durable leases (kv_lease.go): its own
	// lessor, or the composition root's (WithKVLessor). The legacy "regular"
	// state machine does not.
	kvCaps := func() []Capability {
		return append(caps(), CapabilityLease)
	}

	// ---------------------------------------------------------------
	// Unified KV state machines (Backend-based architecture)
	// ---------------------------------------------------------------

	// "memory" and "standard" are aliases for the pure in-memory
	// KVStateMachine backed by StandardBackend.
	standardDesc := Descriptor{
		Description:  "Pure in-memory KV state machine with no persistence",
		Persistence:  PersistenceMemory,
		Kind:         SMKindRegular,
		Capabilities: kvCaps(),
	}
	r.registerKV("memory", NewKVStandardCreateFunc, standardDesc)
	r.registerKV("standard", NewKVStandardCreateFunc, standardDesc)

	// "concurrent" is the sharded in-memory KV state machine backed by
	// ConcurrentBackend, allowing concurrent Lookups during Update.
	r.registerKV("concurrent", NewKVConcurrentCreateFunc, Descriptor{
		Description:  "Sharded in-memory KV SM with concurrent reads",
		Persistence:  PersistenceMemory,
		Kind:         SMKindConcurrent,
		Capabilities: kvCaps(),
	})

	// "fast" is the 256-shard cache SM with ordered asynchronous
	// write-through to the storage raft.sm_storage selects, backed by
	// FastBackend.
	r.RegisterStorageGeneric("fast", NewKVFastCreateFunc, Descriptor{
		Description:  "High-performance 256-shard cache SM with write-through to raft.sm_storage",
		Persistence:  PersistenceHybrid,
		Kind:         SMKindRegular,
		Capabilities: kvCaps(),
	})

	// "disk" is a synchronous disk-backed KV state machine using
	// DiskBackend over the storage raft.sm_storage selects.
	r.RegisterStorageGeneric("disk", NewKVDiskCreateFunc, Descriptor{
		Description:  "Synchronous disk-backed KV SM over raft.sm_storage",
		Persistence:  PersistenceDisk,
		Kind:         SMKindDisk,
		Capabilities: kvCaps(),
	})

	// "highperf" is a high-performance in-memory concurrent SM with
	// configurable shard count (default 256). No disk persistence.
	// Designed for data-plane state (conntrack, NAT sessions).
	r.registerKV("highperf", func(opts ...KVOption) sm.CreateFunc {
		return NewKVHighPerfCreateFunc(nil, opts...)
	}, Descriptor{
		Description:  "High-performance in-memory concurrent state machine with configurable sharding",
		Persistence:  PersistenceMemory,
		Kind:         SMKindConcurrent,
		Capabilities: kvCaps(),
	})

	// ---------------------------------------------------------------
	// Legacy state machines (retained for backward compatibility)
	// ---------------------------------------------------------------

	// "regular" is the legacy RegularStateMachine. Prefer "standard".
	r.RegisterWithDescriptor("regular", func() sm.CreateFunc {
		return NewRegularCreateFunc()
	}, Descriptor{
		Description:  "Legacy pure in-memory state machine (prefer 'standard')",
		Persistence:  PersistenceMemory,
		Kind:         SMKindRegular,
		Capabilities: caps(),
	})

	return r
}

// newStorageFactory returns the StorageFactory for sc: storage on the backend
// sc.Kind selects (pebble when empty), in per-(shard, replica) directories.
// When sc.Root anchors a real data directory, paths are stable across
// restarts ({root}/sm/shard-S-replica-R) and opened with the production
// Pebble configuration; otherwise each replica gets a fresh temp directory
// with the test configuration (tests only — see StorageConfig.Root).
//
// When sc.Replica is set, the composer's factory opens the storage in that
// directory instead, handed the encryptor's file system when the barrier is on.
//
// sc.Kind has been validated by the caller. A storage that cannot be created
// is a ReplicaStorageOpenError: the create function turns it into a replica
// whose Open fails with it, so the shard does not start and nothing panics.
func newStorageFactory(sc StorageConfig) StorageFactory {
	return func(shardID, replicaID uint64) (storage.Storage, error) {
		s, err := openReplicaStorage(sc, shardID, replicaID)
		if err != nil {
			return nil, &ReplicaStorageOpenError{ShardID: shardID, ReplicaID: replicaID, Err: err}
		}
		return s, nil
	}
}

// openReplicaStorage opens the storage of one replica as sc selects it.
func openReplicaStorage(sc StorageConfig, shardID, replicaID uint64) (storage.Storage, error) {
	if sc.Replica == nil && sc.Kind == "memory" {
		return memstore.NewStorage(), nil
	}

	var dir string
	test := false
	if sc.Root != "" {
		dir = filepath.Join(sc.Root, "sm", fmt.Sprintf("shard-%d-replica-%d", shardID, replicaID))
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, &StorageError{Op: "create replica storage directory", Inner: err}
		}
	} else {
		tmp, err := os.MkdirTemp("", fmt.Sprintf("qrdb-sm-%d-%d-*", shardID, replicaID))
		if err != nil {
			return nil, &StorageError{Op: "create temporary replica storage directory", Inner: err}
		}
		dir = filepath.Join(tmp, "data")
		test = true
	}

	// A composer's opener carries the data-plane file system it was bound
	// to; it is handed the replica's directory, created here like the
	// built-in kinds'.
	if sc.Replica != nil {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, &StorageError{Op: "create replica storage directory", Inner: err}
		}
		return sc.Replica(shardID, replicaID, dir)
	}

	// Under the seal barrier every record file the file engine writes —
	// key and value, commit journal included — goes through the same
	// barrier-driven encfs, exactly as a standalone file store does.
	if sc.Kind == "file" {
		return filestore.New(dir, filestore.WithEncryptor(sc.Encryptor))
	}

	var cfg *pebblestore.Config
	if test {
		cfg = pebblestore.TestConfig(dir)
	} else {
		cfg = pebblestore.DefaultConfig(dir)
	}
	// Under the seal barrier every file Pebble writes — sstables, WAL,
	// manifest — goes through the barrier-driven encfs, as it does for
	// the pebble and mvcc state machines (store.WithEncryptor). The
	// factory runs when the replica starts, after the barrier is
	// unsealed.
	if sc.Encryptor.Enabled() {
		cfg.FS = pebblestore.WrapVFS(nil, sc.Encryptor.Wrap)
	}
	return pebblestore.New(cfg)
}

// Register adds a factory to the default registry.
func Register(name string, factory Factory) {
	defaultRegistry.Register(name, factory)
}

// RegisterWithDescriptor adds a factory with full metadata to the
// default registry.
func RegisterWithDescriptor(name string, factory Factory, desc Descriptor) {
	defaultRegistry.RegisterWithDescriptor(name, factory, desc)
}

// Resolve looks up and creates a state machine from the default registry.
func Resolve(name string) (sm.CreateFunc, error) {
	return defaultRegistry.Resolve(name)
}

// ResolveTyped looks up a state machine from the default registry using
// a type and backend combination.
func ResolveTyped(smType, smBackend string) (sm.CreateFunc, error) {
	return defaultRegistry.ResolveTyped(smType, smBackend)
}

// ResolveTypedWithStorage looks up a state machine from the default registry
// and builds it over sc with opts; see Registry.ResolveTypedWithStorage.
func ResolveTypedWithStorage(smType, smBackend string, sc StorageConfig, opts ...KVOption) (sm.CreateFunc, error) {
	return defaultRegistry.ResolveTypedWithStorage(smType, smBackend, sc, opts...)
}

// RegisterStorageGeneric adds a storage-generic state machine to the default
// registry; see Registry.RegisterStorageGeneric.
func RegisterStorageGeneric(name string, factory StorageGenericFactory, desc Descriptor) {
	defaultRegistry.RegisterStorageGeneric(name, factory, desc)
}

// Describe returns the Descriptor for a name from the default registry.
func Describe(name string) (Descriptor, error) {
	return defaultRegistry.Describe(name)
}

// Descriptors returns all registered descriptors from the default registry.
func Descriptors() []Descriptor {
	return defaultRegistry.Descriptors()
}

// Names returns all registered names from the default registry.
func Names() []string {
	return defaultRegistry.Names()
}

// Has returns true if the name is registered in the default registry.
func Has(name string) bool {
	return defaultRegistry.Has(name)
}

// Len returns the number of registered types in the default registry.
func Len() int {
	return defaultRegistry.Len()
}

// Default returns the package-level default registry. This allows
// callers to inspect or extend the registry directly when the package-
// level convenience functions are insufficient.
func Default() *Registry {
	return defaultRegistry
}

// ---------------------------------------------------------------------------
// Fast state machine factory
// ---------------------------------------------------------------------------

// NewFastCreateFunc returns an sm.CreateFunc that produces a
// FastStateMachine for each shard/replica pair. The storeFactory
// provides the backing storage for each instance. The returned
// function closes over the storage factory and options.
func NewFastCreateFunc(storeFactory func(shardID, replicaID uint64) storage.Storage, opts ...Option) sm.CreateFunc {
	return sm.NewCreateFunc(func(shardID, replicaID uint64) sm.StateMachine {
		s := storeFactory(shardID, replicaID)
		return NewFastStateMachine(shardID, replicaID, s, opts...)
	})
}
