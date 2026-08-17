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

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
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
type Descriptor struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Persistence  PersistenceType `json:"persistence"`
	Kind         SMKind          `json:"kind"`
	Capabilities []Capability    `json:"capabilities"`
}

// entry pairs a factory with its descriptor.
type entry struct {
	factory    Factory
	descriptor Descriptor
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

// Resolve looks up the factory for the given name and returns a fresh
// sm.CreateFunc. Returns an UnknownSMTypeError if the name is not
// registered.
func (r *Registry) Resolve(name string) (sm.CreateFunc, error) {
	r.mu.RLock()
	e, ok := r.entries[name]
	r.mu.RUnlock()
	if !ok {
		return nil, &UnknownSMTypeError{Name: name}
	}
	return e.factory(), nil
}

// ResolveTyped looks up the factory for the given SM type and backend
// combination. When backend is empty, it falls back to resolving the
// type name directly. When both are provided, it looks up the
// composite key "type:backend" first, then falls back to just backend.
func (r *Registry) ResolveTyped(smType, smBackend string) (sm.CreateFunc, error) {
	if smBackend == "" {
		return r.Resolve(smType)
	}
	// Try composite key first (e.g., "kv:standard").
	compositeKey := smType + ":" + smBackend
	if r.Has(compositeKey) {
		return r.Resolve(compositeKey)
	}
	// Fall back to just the backend name.
	return r.Resolve(smBackend)
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

	// ---------------------------------------------------------------
	// Unified KV state machines (Backend-based architecture)
	// ---------------------------------------------------------------

	// "memory" and "standard" are aliases for the pure in-memory
	// KVStateMachine backed by StandardBackend.
	standardFactory := func() sm.CreateFunc {
		return NewKVStandardCreateFunc()
	}
	standardDesc := Descriptor{
		Description: "Pure in-memory KV state machine with no persistence",
		Persistence: PersistenceMemory,
		Kind:        SMKindRegular,
		Capabilities: []Capability{
			CapabilityKV, CapabilityBatch, CapabilityMVCC,
			CapabilityTransaction, CapabilityLease,
			CapabilityIndex, CapabilityTimeSeries,
		},
	}
	r.RegisterWithDescriptor("memory", standardFactory, standardDesc)
	r.RegisterWithDescriptor("standard", standardFactory, standardDesc)

	// "concurrent" is the sharded in-memory KV state machine backed by
	// ConcurrentBackend, allowing concurrent Lookups during Update.
	r.RegisterWithDescriptor("concurrent", func() sm.CreateFunc {
		return NewKVConcurrentCreateFunc()
	}, Descriptor{
		Description: "Sharded in-memory KV SM with concurrent reads",
		Persistence: PersistenceMemory,
		Kind:        SMKindConcurrent,
		Capabilities: []Capability{
			CapabilityKV, CapabilityBatch, CapabilityMVCC,
			CapabilityTransaction, CapabilityLease,
			CapabilityIndex, CapabilityTimeSeries,
		},
	})

	// "fast" is the high-performance 256-shard cache SM with async
	// PebbleDB write-through, backed by FastBackend.
	r.RegisterWithDescriptor("fast", func() sm.CreateFunc {
		return NewKVFastCreateFunc(defaultPebbleStorageFactory())
	}, Descriptor{
		Description: "High-performance 256-shard cache SM with PebbleDB write-through",
		Persistence: PersistenceHybrid,
		Kind:        SMKindRegular,
		Capabilities: []Capability{
			CapabilityKV, CapabilityBatch, CapabilityMVCC,
			CapabilityTransaction, CapabilityLease,
			CapabilityIndex, CapabilityTimeSeries,
		},
	})

	// "disk" is a synchronous disk-backed KV state machine using
	// DiskBackend with direct PebbleDB I/O.
	r.RegisterWithDescriptor("disk", func() sm.CreateFunc {
		return NewKVDiskCreateFunc(defaultPebbleStorageFactory())
	}, Descriptor{
		Description: "Synchronous disk-backed KV SM with direct PebbleDB I/O",
		Persistence: PersistenceDisk,
		Kind:        SMKindDisk,
		Capabilities: []Capability{
			CapabilityKV, CapabilityBatch, CapabilityMVCC,
			CapabilityTransaction, CapabilityLease,
			CapabilityIndex, CapabilityTimeSeries,
		},
	})

	// "highperf" is a high-performance in-memory concurrent SM with
	// configurable shard count (default 256). No disk persistence.
	// Designed for data-plane state (conntrack, NAT sessions).
	r.RegisterWithDescriptor("highperf", func() sm.CreateFunc {
		return NewKVHighPerfCreateFunc(nil)
	}, Descriptor{
		Description: "High-performance in-memory concurrent state machine with configurable sharding",
		Persistence: PersistenceMemory,
		Kind:        SMKindConcurrent,
		Capabilities: []Capability{
			CapabilityKV, CapabilityBatch, CapabilityMVCC,
			CapabilityTransaction, CapabilityLease,
			CapabilityIndex, CapabilityTimeSeries,
		},
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
		Capabilities: []Capability{CapabilityKV},
	})

	return r
}

// dataDirRoot, when set via SetDataDirRoot, anchors the default storage
// factory's per-replica directories under the deployment's real data
// directory so they are DURABLE and REUSED across restarts.
var (
	dataDirMu   sync.RWMutex
	dataDirRoot string
)

// SetDataDirRoot anchors the default registry storage factory under dir
// (typically the daemon's --data-dir). Composition roots MUST call this
// before resolving a disk-persistent state machine from the registry;
// without it the factory falls back to a fresh temp directory per process,
// which is acceptable only for tests — a production replica started that
// way loses its state on every restart.
func SetDataDirRoot(dir string) {
	dataDirMu.Lock()
	dataDirRoot = dir
	dataDirMu.Unlock()
}

// defaultPebbleStorageFactory returns a storage factory that creates
// PebbleDB storage instances in per-(shard, replica) directories. When
// SetDataDirRoot has anchored a real data directory, paths are stable
// across restarts ({root}/sm/shard-S-replica-R) and opened with the
// production Pebble configuration; otherwise it falls back to a fresh
// temp directory with the test configuration (tests only — see
// SetDataDirRoot).
func defaultPebbleStorageFactory() func(shardID, replicaID uint64) storage.Storage {
	return func(shardID, replicaID uint64) storage.Storage {
		dataDirMu.RLock()
		root := dataDirRoot
		dataDirMu.RUnlock()

		var cfg *pebblestore.Config
		if root != "" {
			dir := filepath.Join(root, "sm", fmt.Sprintf("shard-%d-replica-%d", shardID, replicaID))
			if err := os.MkdirAll(dir, 0o750); err != nil {
				panic("statemachine: failed to create sm storage dir: " + err.Error())
			}
			cfg = pebblestore.DefaultConfig(dir)
		} else {
			dir, err := os.MkdirTemp("", fmt.Sprintf("qrdb-sm-%d-%d-*", shardID, replicaID))
			if err != nil {
				panic("statemachine: failed to create temp dir for storage: " + err.Error())
			}
			cfg = pebblestore.TestConfig(filepath.Join(dir, "data"))
		}
		s, err := pebblestore.New(cfg)
		if err != nil {
			panic("statemachine: failed to create pebble storage: " + err.Error())
		}
		return s
	}
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
