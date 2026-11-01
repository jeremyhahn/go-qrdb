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

package engine

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sort"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/vector"
)

// ---------------------------------------------------------------------------
// VectorIndexManager: the in-memory ANN index half of
// docs/vectors/engineering.md "Target: the vector engine" -- "Apply and the
// index", "Checkpoints", "Open".
//
// No state here is authoritative. Every namespace's live index is a
// REBUILDABLE CACHE derived from the replicated records a VectorIndexManager
// never writes to: losing it (a missing or corrupt checkpoint, a process
// that never warmed one up) costs a rebuild, never data -- see Warm and
// RebuildNamespace, both of which read ONLY through the given
// storage.Storage, the same store every vector command commits into.
// ---------------------------------------------------------------------------

// vectorIndexState reports whether a namespace's live index can answer a
// search right now.
type vectorIndexState byte

const (
	vectorIndexWarming vectorIndexState = iota
	vectorIndexReady
)

// vectorNamespaceIndex is one namespace's live index and its readiness.
type vectorNamespaceIndex struct {
	mu    sync.RWMutex
	state vectorIndexState
	live  vector.LiveIndex
	rec   vector.NamespaceRecord
}

// VectorIndexManager owns every namespace's in-memory ANN index for one
// engine instance. It is constructed once per engine and driven two ways:
// ApplyEffects, called after every committed batch that touched a vector
// namespace (see MVCCEngine.ApplyVectorIndexEffects and
// KVEngine.adaptVectorHandler); and Warm, called once by the engine's owner
// before the engine serves vector reads.
type VectorIndexManager struct {
	mu   sync.RWMutex
	byNS map[string]*vectorNamespaceIndex

	// dir is the vector-index checkpoint directory under the binding's data
	// root. Empty means this binding persists no checkpoints (a memory
	// binding, or any caller that did not configure one): every Warm is a
	// full rebuild, which is always correct, only slower to become ready.
	dir string
	enc *atrest.Encryptor
}

// NewVectorIndexManager constructs an index manager. dir empty disables
// checkpoint persistence entirely (Warm always rebuilds from records, and
// Checkpoint is a no-op); enc nil is a valid, no-op encryptor (see
// atrest.New), not a disabled one -- pass the binding's real encryptor to
// get checkpoints encrypted at rest.
func NewVectorIndexManager(dir string, enc *atrest.Encryptor) *VectorIndexManager {
	if enc == nil {
		enc = atrest.New(nil)
	}
	return &VectorIndexManager{
		byNS: make(map[string]*vectorNamespaceIndex),
		dir:  dir,
		enc:  enc,
	}
}

func (m *VectorIndexManager) get(namespace string) *vectorNamespaceIndex {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.byNS[namespace]
}

func (m *VectorIndexManager) set(namespace string, ns *vectorNamespaceIndex) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byNS[namespace] = ns
}

func (m *VectorIndexManager) delete(namespace string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.byNS, namespace)
}

// ApplyEffects applies every effect to its namespace's live index, in order.
// An index error on one effect marks that namespace for rebuild (its state
// becomes vectorIndexWarming) and is otherwise swallowed: the committed
// entry that produced the effect already succeeded and must not be
// retracted (docs/vectors/engineering.md "Apply and the index", point 3).
// Safe to call with a nil receiver or an empty slice.
func (m *VectorIndexManager) ApplyEffects(effects []vector.IndexEffect) {
	if m == nil {
		return
	}
	for _, e := range effects {
		m.applyOne(e)
	}
}

func (m *VectorIndexManager) applyOne(e vector.IndexEffect) {
	switch e.Op {
	case vector.IndexEffectNamespaceCreate:
		live, err := vector.NewLiveIndex(e.Namespace, e.Record)
		ns := &vectorNamespaceIndex{rec: e.Record, state: vectorIndexReady}
		if err != nil {
			ns.state = vectorIndexWarming
		} else {
			ns.live = live
		}
		m.set(e.Namespace, ns)

	case vector.IndexEffectNamespaceDelete:
		m.delete(e.Namespace)
		m.removeCheckpoints(e.Namespace)

	case vector.IndexEffectUpsert:
		ns := m.get(e.Namespace)
		if ns == nil {
			return // not warmed into this instance; a later Warm will pick it up
		}
		ns.mu.Lock()
		if ns.live != nil {
			if err := ns.live.Upsert(e.ID, e.Embedding); err != nil {
				ns.state = vectorIndexWarming
			}
		}
		ns.mu.Unlock()

	case vector.IndexEffectDelete:
		ns := m.get(e.Namespace)
		if ns == nil {
			return
		}
		ns.mu.Lock()
		if ns.live != nil {
			if err := ns.live.Delete(e.ID); err != nil {
				ns.state = vectorIndexWarming
			}
		}
		ns.mu.Unlock()

	case vector.IndexEffectReconfigure:
		// A reconfigure changes the index's own geometry (type, parameters),
		// which no live index can be incrementally migrated to: the old
		// index is discarded and the namespace is marked for rebuild rather
		// than left serving stale-geometry results.
		live, err := vector.NewLiveIndex(e.Namespace, e.Record)
		ns := &vectorNamespaceIndex{rec: e.Record, state: vectorIndexWarming}
		if err == nil {
			ns.live = live
		}
		m.set(e.Namespace, ns)
	}
}

// Search returns the k nearest neighbours of query in namespace's live
// index. It answers *vector.NamespaceNotFoundError when this manager holds
// no entry for namespace, *service.VectorIndexWarmingError when the entry
// exists but is not yet ready, and *service.VectorIndexUntrainedError when
// the index is ready but is an IVF index short of its training floor: that
// waits on more vectors being written, not on time, so it is a failed
// precondition rather than a retry.
func (m *VectorIndexManager) Search(namespace string, query []float32, k int) ([]vector.IndexHit, error) {
	ns := m.get(namespace)
	if ns == nil {
		return nil, &vector.NamespaceNotFoundError{Namespace: namespace}
	}
	ns.mu.RLock()
	defer ns.mu.RUnlock()
	if ns.state != vectorIndexReady || ns.live == nil {
		return nil, &service.VectorIndexWarmingError{Namespace: namespace}
	}
	hits, err := ns.live.Search(query, k)
	if err != nil {
		if errors.Is(err, vector.ErrIndexNotReady) {
			return nil, &service.VectorIndexUntrainedError{Namespace: namespace}
		}
		return nil, err
	}
	return hits, nil
}

// Warm populates the index manager from every namespace record in stg: for
// each, it tries a checkpoint and falls back to a full rebuild from the
// vector records (docs/vectors/engineering.md "Open"). Call once, before
// the engine serves vector reads. Safe to call with a nil receiver.
func (m *VectorIndexManager) Warm(stg storage.Storage) error {
	if m == nil {
		return nil
	}
	namespaces, err := listVectorNamespaces(stg)
	if err != nil {
		return err
	}
	for name, rec := range namespaces {
		if err := m.warmNamespace(stg, name, rec); err != nil {
			return err
		}
	}
	return nil
}

// RebuildNamespace discards namespace's live index, if any, and rebuilds it
// from stg: a checkpoint, or a full scan of the namespace's vector records.
// This is the recovery path an index error during ApplyEffects leaves a
// namespace waiting for, and the explicit entry point for the "delete the
// checkpoint and prove no data loss" guarantee: a checkpoint is only ever a
// cache of what RebuildNamespace can always reconstruct from the replicated
// store.
func (m *VectorIndexManager) RebuildNamespace(stg storage.Storage, namespace string) error {
	rec, err := readNamespaceRecord(stg, namespace)
	if err != nil {
		return err
	}
	if rec == nil {
		m.delete(namespace)
		return nil
	}
	return m.warmNamespace(stg, namespace, *rec)
}

func (m *VectorIndexManager) warmNamespace(stg storage.Storage, name string, rec vector.NamespaceRecord) error {
	live, err := vector.NewLiveIndex(name, rec)
	if err != nil {
		return err
	}
	ns := &vectorNamespaceIndex{rec: rec, live: live, state: vectorIndexWarming}
	m.set(name, ns)

	if m.loadCheckpoint(stg, name, rec, live) {
		ns.mu.Lock()
		ns.state = vectorIndexReady
		ns.mu.Unlock()
		return nil
	}

	if err := rebuildIndexFromStore(stg, name, live); err != nil {
		return err
	}
	ns.mu.Lock()
	ns.state = vectorIndexReady
	ns.mu.Unlock()
	return nil
}

// Checkpoint writes namespace's current live index to disk, if this manager
// has a checkpoint directory configured and the namespace's index is ready.
// It is a no-op, not an error, when there is nothing to write: no directory
// configured (the "memory binding writes none" rule), the namespace is
// unknown to this manager, or its index is still warming or has nothing
// checkpoint-worthy yet (an untrained IVF namespace). Safe to call with a
// nil receiver.
func (m *VectorIndexManager) Checkpoint(namespace string) error {
	if m == nil || m.dir == "" {
		return nil
	}
	ns := m.get(namespace)
	if ns == nil {
		return nil
	}
	ns.mu.RLock()
	defer ns.mu.RUnlock()
	if ns.state != vectorIndexReady || ns.live == nil {
		return nil
	}
	return m.writeCheckpoint(namespace, ns.rec, ns.live)
}

// CheckpointAll calls Checkpoint for every namespace this manager currently
// holds. Safe to call with a nil receiver.
func (m *VectorIndexManager) CheckpointAll() error {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	names := make([]string, 0, len(m.byNS))
	for name := range m.byNS {
		names = append(names, name)
	}
	m.mu.RUnlock()
	sort.Strings(names)
	for _, name := range names {
		if err := m.Checkpoint(name); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Store scans: rebuild-from-records and namespace discovery. Both read ONLY
// through the given storage.Storage, never through a batch -- the index
// manager has no apply-time state of its own.
// ---------------------------------------------------------------------------

// listVectorNamespaces returns every namespace record in stg, keyed by name.
func listVectorNamespaces(stg storage.Storage) (map[string]vector.NamespaceRecord, error) {
	prefix := vector.NamespacePrefix()
	it := stg.NewIterator(&storage.IteratorOptions{LowerBound: prefix, UpperBound: prefixEndBytes(prefix)})
	defer func() { _ = it.Close() }()

	out := make(map[string]vector.NamespaceRecord)
	for it.SeekToFirst(); it.Valid(); it.Next() {
		name := string(it.Key()[len(prefix):])
		rec, err := vector.DecodeNamespaceRecord(it.Value())
		if err != nil {
			return nil, err
		}
		out[name] = rec
	}
	return out, it.Error()
}

// rebuildIndexFromStore inserts every vector record in namespace into live,
// in ascending ID order (the order a storage iterator yields them in: see
// appendIDHex's ordering-preserving hex encoding), so a from-scratch rebuild
// builds the identical index on every replica.
func rebuildIndexFromStore(stg storage.Storage, namespace string, live vector.LiveIndex) error {
	prefix := vector.VectorPrefix(namespace)
	it := stg.NewIterator(&storage.IteratorOptions{LowerBound: prefix, UpperBound: prefixEndBytes(prefix)})
	defer func() { _ = it.Close() }()

	for it.SeekToFirst(); it.Valid(); it.Next() {
		id, err := parseVectorID(it.Key())
		if err != nil {
			return err
		}
		rec, err := vector.DecodeVectorRecord(it.Value())
		if err != nil {
			return err
		}
		if err := live.Upsert(id, rec.Embedding); err != nil {
			return err
		}
	}
	return it.Error()
}

// parseVectorID recovers the vector ID from the trailing 16 hex digits of a
// key built by vector.VectorKey.
func parseVectorID(key []byte) (uint64, error) {
	if len(key) < 16 {
		return 0, &vector.InvalidRecordError{Record: "vector key", Reason: "too short to carry an ID"}
	}
	var raw [8]byte
	if _, err := hex.Decode(raw[:], key[len(key)-16:]); err != nil {
		return 0, &vector.InvalidRecordError{Record: "vector key", Reason: "ID segment is not hex: " + err.Error()}
	}
	return binary.BigEndian.Uint64(raw[:]), nil
}
