//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package hnsw

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/api"
)

const (
	// Key prefixes for PebbleDB.
	prefixNode       = "hnsw:node:"
	prefixMetadata   = "hnsw:meta:"
	prefixEntryPoint = "hnsw:meta:entry"
	prefixMaxLayer   = "hnsw:meta:maxlayer"
	prefixDimensions = "hnsw:meta:dimensions"
)

// SerializedNode represents a node in serialized form.
type SerializedNode struct {
	ID          uint64
	Vector      []float32
	Layer       int
	Connections map[int][]uint64
}

// Save persists the index to PebbleDB.
func (idx *HNSWIndex) Save(db *pebble.DB) error {
	batch := db.NewBatch()
	defer func() { _ = batch.Close() }()

	// Save metadata
	if err := idx.saveMetadata(batch); err != nil {
		return api.NewHNSWPersistenceError("hnsw.Save", "save metadata", err)
	}

	// Save all nodes
	idx.nodes.Range(func(key, value interface{}) bool {
		node, ok := value.(*HNSWNode)
		if !ok {
			return true
		}
		if err := idx.saveNode(batch, node); err != nil {
			// Store error and stop iteration
			return false
		}
		return true
	})

	// Commit batch
	if err := batch.Commit(pebble.Sync); err != nil {
		return api.NewHNSWPersistenceError("hnsw.Save", "commit batch", err)
	}

	return nil
}

// Load restores the index from PebbleDB.
func (idx *HNSWIndex) Load(db *pebble.DB) error {
	// Load metadata
	if err := idx.loadMetadata(db); err != nil {
		return api.NewHNSWPersistenceError("hnsw.Load", "load metadata", err)
	}

	// Load all nodes
	iter, err := db.NewIter(&pebble.IterOptions{
		LowerBound: []byte(prefixNode),
		UpperBound: []byte(prefixNode + "\xff"),
	})
	if err != nil {
		return api.NewHNSWPersistenceError("hnsw.Load", "create iterator", err)
	}
	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		node, err := idx.deserializeNode(iter.Value())
		if err != nil {
			return api.NewHNSWPersistenceError("hnsw.Load", "deserialize node", err)
		}

		idx.nodes.Store(node.ID, node)
		idx.nodeCount.Add(1)
	}

	if err := iter.Error(); err != nil {
		return api.NewIteratorError("hnsw.Load", err)
	}

	return nil
}

// saveMetadata saves index metadata to the database.
func (idx *HNSWIndex) saveMetadata(batch *pebble.Batch) error {
	// Save entry point
	entryPoint := idx.entryPoint.Load()
	if err := batch.Set([]byte(prefixEntryPoint), uint64ToBytes(entryPoint), pebble.Sync); err != nil {
		return err
	}

	// Save max layer
	maxLayer := idx.maxLayer.Load()
	if err := batch.Set([]byte(prefixMaxLayer), int32ToBytes(maxLayer), pebble.Sync); err != nil {
		return err
	}

	// Save dimensions
	if err := batch.Set([]byte(prefixDimensions), intToBytes(int(idx.dimensions.Load())), pebble.Sync); err != nil {
		return err
	}

	return nil
}

// loadMetadata loads index metadata from the database.
func (idx *HNSWIndex) loadMetadata(db *pebble.DB) error {
	// Load entry point
	entryPointBytes, epCloser, err := db.Get([]byte(prefixEntryPoint))
	if err != nil && !errors.Is(err, pebble.ErrNotFound) {
		return err
	}
	if epCloser != nil {
		defer func() { _ = epCloser.Close() }()
	}
	if len(entryPointBytes) > 0 {
		idx.entryPoint.Store(bytesToUint64(entryPointBytes))
	}

	// Load max layer
	maxLayerBytes, mlCloser, err := db.Get([]byte(prefixMaxLayer))
	if err != nil && !errors.Is(err, pebble.ErrNotFound) {
		return err
	}
	if mlCloser != nil {
		defer func() { _ = mlCloser.Close() }()
	}
	if len(maxLayerBytes) > 0 {
		idx.maxLayer.Store(bytesToInt32(maxLayerBytes))
	}

	// Load dimensions
	dimensionsBytes, dimCloser, err := db.Get([]byte(prefixDimensions))
	if err != nil && !errors.Is(err, pebble.ErrNotFound) {
		return err
	}
	if dimCloser != nil {
		defer func() { _ = dimCloser.Close() }()
	}
	if len(dimensionsBytes) > 0 {
		idx.dimensions.Store(int32(bytesToInt(dimensionsBytes))) //nolint:gosec // G115: Safe conversion - dimension from persisted data
	}

	return nil
}

// saveNode saves a single node to the database.
func (idx *HNSWIndex) saveNode(batch *pebble.Batch, node *HNSWNode) error {
	key := []byte(prefixNode + fmt.Sprintf("%020d", node.ID))

	data, err := idx.serializeNode(node)
	if err != nil {
		return err
	}

	return batch.Set(key, data, pebble.Sync)
}

// serializeNode converts a node to bytes using gob encoding.
func (idx *HNSWIndex) serializeNode(node *HNSWNode) ([]byte, error) {
	node.mu.RLock()
	defer node.mu.RUnlock()

	sn := &SerializedNode{
		ID:          node.ID,
		Vector:      node.Vector,
		Layer:       node.Layer,
		Connections: make(map[int][]uint64),
	}

	// Copy connections
	for layer, conns := range node.Connections {
		sn.Connections[layer] = make([]uint64, len(conns))
		copy(sn.Connections[layer], conns)
	}

	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(sn); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// deserializeNode converts bytes back to a node.
func (idx *HNSWIndex) deserializeNode(data []byte) (*HNSWNode, error) {
	var sn SerializedNode

	buf := bytes.NewBuffer(data)
	dec := gob.NewDecoder(buf)
	if err := dec.Decode(&sn); err != nil {
		return nil, err
	}

	node := &HNSWNode{
		ID:          sn.ID,
		Vector:      sn.Vector,
		Layer:       sn.Layer,
		Connections: sn.Connections,
	}

	return node, nil
}

// Snapshot creates a snapshot of the index.
func (idx *HNSWIndex) Snapshot(w io.Writer) error {
	// Create a map to hold all nodes
	snapshot := make(map[uint64]*SerializedNode)

	idx.nodes.Range(func(key, value interface{}) bool {
		node, ok := value.(*HNSWNode)
		if !ok {
			return true
		}
		node.mu.RLock()

		sn := &SerializedNode{
			ID:          node.ID,
			Vector:      make([]float32, len(node.Vector)),
			Layer:       node.Layer,
			Connections: make(map[int][]uint64),
		}

		copy(sn.Vector, node.Vector)

		for layer, conns := range node.Connections {
			sn.Connections[layer] = make([]uint64, len(conns))
			copy(sn.Connections[layer], conns)
		}

		node.mu.RUnlock()

		snapshot[node.ID] = sn
		return true
	})

	// Encode snapshot
	enc := gob.NewEncoder(w)
	return enc.Encode(snapshot)
}

// Restore restores the index from a snapshot.
func (idx *HNSWIndex) Restore(r io.Reader) error {
	var snapshot map[uint64]*SerializedNode

	dec := gob.NewDecoder(r)
	if err := dec.Decode(&snapshot); err != nil {
		return err
	}

	// Clear existing nodes
	idx.nodes = sync.Map{}
	idx.nodeCount.Store(0)

	// Restore nodes
	var maxLayer int32 = -1
	var entryPoint uint64

	for id, sn := range snapshot {
		node := &HNSWNode{
			ID:          sn.ID,
			Vector:      sn.Vector,
			Layer:       sn.Layer,
			Connections: sn.Connections,
		}

		idx.nodes.Store(id, node)
		idx.nodeCount.Add(1)

		if int32(node.Layer) > maxLayer { //nolint:gosec // G115: Safe conversion - layer value bounded
			maxLayer = int32(node.Layer) //nolint:gosec // G115: Safe conversion - layer value bounded
			entryPoint = node.ID
		}

		// Set dimensions from first node
		if idx.dimensions.Load() == 0 {
			idx.dimensions.Store(int32(len(node.Vector))) //nolint:gosec // G115: Safe conversion - vector length bounded
		}
	}

	idx.maxLayer.Store(maxLayer)
	idx.entryPoint.Store(entryPoint)

	return nil
}

// Helper functions for byte conversion.
func uint64ToBytes(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func bytesToUint64(b []byte) uint64 {
	return binary.BigEndian.Uint64(b)
}

func int32ToBytes(v int32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(v)) //nolint:gosec // Safe conversion
	return b
}

func bytesToInt32(b []byte) int32 {
	return int32(binary.BigEndian.Uint32(b)) //nolint:gosec // G115: Safe conversion - reading persisted int32
}

func intToBytes(v int) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(v)) //nolint:gosec // G115: Safe conversion - int to uint32 for persistence
	return b
}

func bytesToInt(b []byte) int {
	return int(binary.BigEndian.Uint32(b))
}
