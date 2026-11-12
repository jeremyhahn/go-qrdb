//go:build raft

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

// Package statemachine provides Raft state machine implementations.
package statemachine

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/vmihailenco/msgpack/v5"

	"github.com/jeremyhahn/go-qrdb/pkg/api"
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	vectorStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
)

// VectorConsensusFSM is a state machine for vector operation consensus with
// direct storage application. It handles Raft consensus for vector operations
// and applies changes directly to the vector storage, ensuring all nodes
// maintain consistent vector data.
//
// This architecture ensures:
// - All nodes receive and apply the same operations in the same order
// - Each node maintains its own HNSW index for fast similarity search
// - Cross-node replication works properly via Raft consensus.
type VectorConsensusFSM struct {
	shardID   uint64
	replicaID uint64
	logger    *logger.Logger

	// Vector storage for persistent data
	storage vectorStorage.Storage

	// HNSW indexes per namespace (each node maintains its own)
	hnswIndexes map[string]*hnsw.HNSWIndex
	indexMu     sync.RWMutex

	// Tracking for operations
	opCount   atomic.Uint64
	lastIndex atomic.Uint64

	// Namespace metadata for snapshot
	mu         sync.RWMutex
	namespaces map[string]*ConsensusNamespaceInfo
}

// ConsensusNamespaceInfo stores minimal namespace metadata for the consensus state machine.
type ConsensusNamespaceInfo struct {
	Name      string `msgpack:"name"`
	Dimension uint32 `msgpack:"dim"`
	// DistanceMetric is the metric the namespace was created with, so an
	// index built lazily or after a snapshot install keeps it. A snapshot
	// written before it was tracked decodes it empty (namespaceMetricLocked).
	DistanceMetric string `msgpack:"metric,omitempty"`
}

// VectorShardID is the default shard ID for vector operations.
const VectorShardID uint64 = 2

// NewVectorConsensusFSM creates a new vector consensus state machine.
// This version is used when no vector storage is available (for backward compatibility).
func NewVectorConsensusFSM(shardID, replicaID uint64, log *logger.Logger) sm.StateMachine {
	return NewVectorConsensusFSMWithStorage(shardID, replicaID, nil, log)
}

// NewVectorConsensusFSMWithStorage creates a new vector consensus state machine with storage.
// This is the primary constructor that enables cross-node replication.
func NewVectorConsensusFSMWithStorage(shardID, replicaID uint64, storage vectorStorage.Storage, log *logger.Logger) sm.StateMachine {
	if log == nil {
		log, _ = logger.New(logger.Default())
	}

	fsm := &VectorConsensusFSM{
		shardID:     shardID,
		replicaID:   replicaID,
		logger:      log.WithField("component", "vector-consensus-fsm"),
		storage:     storage,
		hnswIndexes: make(map[string]*hnsw.HNSWIndex),
		namespaces:  make(map[string]*ConsensusNamespaceInfo),
	}

	// If storage is available, load existing namespaces
	if storage != nil {
		if err := fsm.loadNamespaces(); err != nil {
			fsm.logger.WithError(err).Warn("Failed to load namespaces during FSM init")
		}
	}

	return fsm
}

// loadNamespaces loads existing namespaces from storage and initializes HNSW indexes.
func (v *VectorConsensusFSM) loadNamespaces() error {
	if v.storage == nil {
		return nil
	}

	ctx := context.Background()
	namespaces, err := v.storage.ListNamespaces(ctx)
	if err != nil {
		return api.NewOperationFailedError("VectorConsensusFSM.loadNamespaces", err)
	}

	v.logger.WithField("count", len(namespaces)).Debug("Loading namespaces from storage")

	for _, ns := range namespaces {
		// Track namespace metadata
		v.mu.Lock()
		v.namespaces[ns.Name] = &ConsensusNamespaceInfo{
			Name:           ns.Name,
			Dimension:      ns.Dimension,
			DistanceMetric: ns.DistanceMetric,
		}
		v.mu.Unlock()

		v.rebuildIndexFromStorage(ctx, ns.Name, ns.Dimension, ns.DistanceMetric)
	}

	return nil
}

// rebuildIndexFromStorage replaces the namespace's HNSW index with one built
// under its metric and fills it with every vector storage holds for it. A
// vector whose length is not the namespace dimension is skipped, so a stray
// record cannot fix the index at the wrong dimension. A failure is logged and
// leaves the namespace without an index, which the next upsert builds lazily.
func (v *VectorConsensusFSM) rebuildIndexFromStorage(ctx context.Context, namespace string, dimension uint32, distanceMetric string) {
	vectors, err := v.storage.ScanNamespace(ctx, namespace, nil)
	if err != nil {
		v.logger.WithFields(map[string]interface{}{
			"namespace": namespace,
			"error":     err,
		}).Warn("Failed to scan vectors for namespace")
		return
	}

	if err := v.createHNSWIndex(namespace, distanceMetric); err != nil {
		v.logger.WithFields(map[string]interface{}{
			"namespace": namespace,
			"error":     err,
		}).Warn("Failed to create HNSW index for namespace")
		return
	}

	v.indexMu.RLock()
	index := v.hnswIndexes[namespace]
	v.indexMu.RUnlock()

	loaded := 0
	for _, vec := range vectors {
		if dimension != 0 && len(vec.Embedding) != int(dimension) {
			v.logger.WithFields(map[string]interface{}{
				"namespace": namespace,
				"vector_id": vec.ID,
				"dimension": len(vec.Embedding),
				"want":      dimension,
			}).Warn("Skipping stored vector of the wrong dimension")
			continue
		}
		if err := index.Insert(vec.ID, vec.Embedding); err != nil {
			v.logger.WithFields(map[string]interface{}{
				"namespace": namespace,
				"vector_id": vec.ID,
				"error":     err,
			}).Warn("Failed to insert vector into HNSW index")
			continue
		}
		loaded++
	}

	v.logger.WithFields(map[string]interface{}{
		"namespace": namespace,
		"vectors":   loaded,
	}).Debug("Loaded namespace with vectors")
}

// createHNSWIndex creates a new HNSW index for the given namespace. An empty
// metric name is L2; an unknown one is an *hnsw.UnknownDistanceMetricError
// and builds no index, rather than one under a metric nobody asked for.
func (v *VectorConsensusFSM) createHNSWIndex(namespace, distanceMetric string) error {
	metric, err := hnsw.ParseDistanceMetric(distanceMetric)
	if err != nil {
		return err
	}

	config := &hnsw.Config{
		M:              16,
		MMax:           32,
		EfConstruction: 200,
		EfSearch:       50,
		DistanceMetric: metric,
	}

	index, err := hnsw.NewHNSWIndex(config)
	if err != nil {
		return err
	}

	v.indexMu.Lock()
	v.hnswIndexes[namespace] = index
	v.indexMu.Unlock()

	return nil
}

// namespaceMetricLocked returns the distance metric namespace was created
// with: the one its tracked metadata carries, else the one its storage record
// carries, else "" (which createHNSWIndex reads as L2, the create default).
// The caller holds v.mu.
func (v *VectorConsensusFSM) namespaceMetricLocked(namespace string) string {
	if info, ok := v.namespaces[namespace]; ok && info.DistanceMetric != "" {
		return info.DistanceMetric
	}
	if v.storage != nil {
		if info, err := v.storage.GetNamespaceInfo(context.Background(), namespace); err == nil && info != nil {
			return info.DistanceMetric
		}
	}
	return ""
}

// namespaceMetric is namespaceMetricLocked for a caller that does not hold
// v.mu.
func (v *VectorConsensusFSM) namespaceMetric(namespace string) string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.namespaceMetricLocked(namespace)
}

// Update applies vector commands from the Raft log.
// This method is called on ALL nodes (including followers) when log entries are committed.
// It applies the operations to local storage and HNSW index.
func (v *VectorConsensusFSM) Update(_ context.Context, entries []sm.Entry, results []sm.Result) error {
	// The Update contract sizes results to entries. Reslicing states that
	// length here, before any entry is applied, so every results[i] below is
	// in range by construction rather than by the caller's say-so.
	results = results[:len(entries)]
	for i, entry := range entries {
		v.opCount.Add(1)
		v.lastIndex.Store(entry.Index)

		result, err := v.applyEntry(entry.Cmd)

		// An operation this state machine does not implement is a refusal, not
		// a fault: it is encoded into the result payload rather than halting
		// the apply loop (go-qrdb#520). Anything else is returned, so a
		// replica that has diverged stops.
		resolved, stop, fatal := ResolveApplyError(nil, entry.Index, result, err)
		if stop {
			return fatal
		}
		results[i] = resolved
	}

	return nil
}

// applyEntry decodes one committed command and applies it to storage and
// the HNSW index, returning the handler's result and error for
// ResolveApplyError to classify.
func (v *VectorConsensusFSM) applyEntry(data []byte) (sm.Result, error) {
	// Empty commands are config changes (ADD/REMOVE node) - no-op
	if len(data) == 0 {
		return sm.Result{}, nil
	}

	// Decode the VectorCommand
	// DEFENSIVE: If decode fails, log and return success to prevent cluster-wide panic.
	// This can happen if an invalid command somehow reaches this shard, which should
	// not occur in normal operation but we handle it gracefully to maintain stability.
	cmd, err := DecodeVectorCommand(data)
	if err != nil {
		v.logger.WithFields(map[string]interface{}{
			"error":       err,
			"data_len":    len(data),
			"first_bytes": fmt.Sprintf("%x", data[:min(16, len(data))]),
		}).Warn("Failed to decode vector command - skipping entry to maintain cluster stability")
		// Return success to prevent panicking
		// All nodes will skip this entry, maintaining consistency
		return sm.Result{Value: 0}, nil
	}

	// Apply the operation to storage and HNSW index
	ctx := context.Background()
	switch cmd.Op {
	case OpCreateNamespace:
		return v.applyCreateNamespace(ctx, cmd)

	case OpDeleteNamespace:
		return v.applyDeleteNamespace(ctx, cmd)

	case OpUpsertVector:
		return v.applyUpsertVector(ctx, cmd)

	case OpDeleteVector:
		return v.applyDeleteVector(ctx, cmd)

	case OpBatchUpsert:
		return v.applyBatchUpsert(ctx, cmd)

	case OpUpdateMetadata:
		return v.applyUpdateMetadata(ctx, cmd)

	case OpUpdateNamespace:
		return v.applyUpdateNamespace(ctx, cmd)

	default:
		// For read-only operations, just return success
		v.logger.WithField("op", cmd.Op.String()).Debug("Read-only operation, skipping apply")
		return sm.Result{Value: 1}, nil
	}
}

// applyCreateNamespace creates a namespace in storage and initializes HNSW index.
func (v *VectorConsensusFSM) applyCreateNamespace(ctx context.Context, cmd *VectorCommand) (sm.Result, error) {
	// Determine distance metric, default to L2
	distanceMetric := cmd.DistanceMetric
	if distanceMetric == "" {
		distanceMetric = "L2"
	}

	// Determine index type, default to HNSW
	indexType := cmd.IndexType
	if indexType == "" {
		indexType = "HNSW"
	}

	v.logger.WithFields(map[string]interface{}{
		"namespace":       cmd.Namespace,
		"dimension":       cmd.Dimension,
		"distance_metric": distanceMetric,
		"index_type":      indexType,
	}).Debug("Applying CreateNamespace")

	// Track namespace metadata
	v.mu.Lock()
	v.namespaces[cmd.Namespace] = &ConsensusNamespaceInfo{
		Name:           cmd.Namespace,
		Dimension:      cmd.Dimension,
		DistanceMetric: distanceMetric,
	}
	v.mu.Unlock()

	// Apply to storage if available
	if v.storage != nil {
		now := time.Now().UnixNano()
		info := &vectorStorage.NamespaceInfo{
			Name:           cmd.Namespace,
			Dimension:      cmd.Dimension,
			VectorCount:    0,
			DistanceMetric: distanceMetric,
			IndexType:      indexType,
			IndexConfig:    cmd.IndexParams,
			Metadata:       cmd.NamespaceMetadata,
			CreatedAt:      now,
			UpdatedAt:      now,
			TenantID:       cmd.TenantID,
			OwnerUserID:    cmd.OwnerUserID,
		}

		if err := v.storage.CreateNamespace(ctx, info); err != nil {
			// Namespace may already exist from a previous apply - this is OK
			v.logger.WithFields(map[string]interface{}{
				"namespace": cmd.Namespace,
				"error":     err,
			}).Debug("Namespace creation skipped (may already exist)")
		}
	}

	// Create HNSW index
	if err := v.createHNSWIndex(cmd.Namespace, distanceMetric); err != nil {
		v.logger.WithFields(map[string]interface{}{
			"namespace": cmd.Namespace,
			"error":     err,
		}).Warn("Failed to create HNSW index")
	}

	v.logger.WithField("namespace", cmd.Namespace).Info("Namespace created via Raft consensus")
	return sm.Result{Value: 1}, nil
}

// applyDeleteNamespace removes a namespace from storage and HNSW index.
func (v *VectorConsensusFSM) applyDeleteNamespace(ctx context.Context, cmd *VectorCommand) (sm.Result, error) {
	v.logger.WithField("namespace", cmd.Namespace).Debug("Applying DeleteNamespace")

	// Remove namespace metadata
	v.mu.Lock()
	delete(v.namespaces, cmd.Namespace)
	v.mu.Unlock()

	// Apply to storage if available
	if v.storage != nil {
		if err := v.storage.DeleteNamespace(ctx, cmd.Namespace); err != nil {
			v.logger.WithFields(map[string]interface{}{
				"namespace": cmd.Namespace,
				"error":     err,
			}).Debug("Namespace deletion error (may not exist)")
		}
	}

	// Remove HNSW index
	v.indexMu.Lock()
	delete(v.hnswIndexes, cmd.Namespace)
	v.indexMu.Unlock()

	v.logger.WithField("namespace", cmd.Namespace).Info("Namespace deleted via Raft consensus")
	return sm.Result{Value: 1}, nil
}

// applyUpsertVector inserts or updates a vector in storage and HNSW index.
func (v *VectorConsensusFSM) applyUpsertVector(ctx context.Context, cmd *VectorCommand) (sm.Result, error) {
	v.logger.WithFields(map[string]interface{}{
		"namespace": cmd.Namespace,
		"id":        cmd.ID,
	}).Debug("Applying UpsertVector")

	// Apply to storage if available
	if v.storage != nil {
		// Convert metadata from map[string]interface{} to map[string]string
		metadata := make(map[string]string)
		for k, val := range cmd.Metadata {
			metadata[k] = vectorStorage.EncodeMetadataValue(val)
		}

		now := time.Now().UnixNano()
		vec := &vectorStorage.VectorValue{
			ID:        cmd.ID,
			Embedding: cmd.Vector,
			Metadata:  metadata,
			CreatedAt: now,
			UpdatedAt: now,
			Version:   1,
		}

		if err := v.storage.UpsertVector(ctx, cmd.Namespace, vec); err != nil {
			v.logger.WithFields(map[string]interface{}{
				"namespace": cmd.Namespace,
				"id":        cmd.ID,
				"error":     err,
			}).Error("Failed to upsert vector to storage")
			return sm.Result{}, err
		}
	}

	// Update HNSW index
	v.indexMu.RLock()
	index, exists := v.hnswIndexes[cmd.Namespace]
	v.indexMu.RUnlock()

	if exists {
		if err := index.Insert(cmd.ID, cmd.Vector); err != nil {
			v.logger.WithFields(map[string]interface{}{
				"namespace": cmd.Namespace,
				"id":        cmd.ID,
				"error":     err,
			}).Warn("Failed to insert vector into HNSW index")
		}
	} else {
		// Create HNSW index if it doesn't exist (lazy initialization)
		if err := v.createHNSWIndex(cmd.Namespace, v.namespaceMetric(cmd.Namespace)); err == nil {
			v.indexMu.RLock()
			index = v.hnswIndexes[cmd.Namespace]
			v.indexMu.RUnlock()
			if index != nil {
				if insertErr := index.Insert(cmd.ID, cmd.Vector); insertErr != nil {
					v.logger.WithFields(map[string]interface{}{
						"namespace": cmd.Namespace,
						"id":        cmd.ID,
						"error":     insertErr,
					}).Warn("Failed to insert vector into newly created HNSW index")
				}
			}
		}
	}

	v.logger.WithFields(map[string]interface{}{
		"namespace": cmd.Namespace,
		"id":        cmd.ID,
	}).Debug("Vector upserted via Raft consensus")
	return sm.Result{Value: 1}, nil
}

// applyDeleteVector removes a vector from storage and HNSW index.
func (v *VectorConsensusFSM) applyDeleteVector(ctx context.Context, cmd *VectorCommand) (sm.Result, error) {
	v.logger.WithFields(map[string]interface{}{
		"namespace": cmd.Namespace,
		"id":        cmd.ID,
		"ids":       len(cmd.IDs),
	}).Debug("Applying DeleteVector")

	// Handle batch delete
	if len(cmd.IDs) > 0 {
		if v.storage != nil {
			if err := v.storage.BatchDeleteVectors(ctx, cmd.Namespace, cmd.IDs); err != nil {
				v.logger.WithFields(map[string]interface{}{
					"namespace": cmd.Namespace,
					"ids":       len(cmd.IDs),
					"error":     err,
				}).Error("Failed to batch delete vectors from storage")
			}
		}

		// Update HNSW index
		v.indexMu.RLock()
		index, exists := v.hnswIndexes[cmd.Namespace]
		v.indexMu.RUnlock()

		if exists {
			for _, id := range cmd.IDs {
				if err := index.Delete(id); err != nil {
					v.logger.WithFields(map[string]interface{}{
						"namespace": cmd.Namespace,
						"id":        id,
						"error":     err,
					}).Warn("Failed to delete vector from HNSW index")
				}
			}
		}
		return sm.Result{Value: 1}, nil
	}

	// Handle single delete
	if v.storage != nil {
		if err := v.storage.DeleteVector(ctx, cmd.Namespace, cmd.ID); err != nil {
			v.logger.WithFields(map[string]interface{}{
				"namespace": cmd.Namespace,
				"id":        cmd.ID,
				"error":     err,
			}).Error("Failed to delete vector from storage")
		}
	}

	// Update HNSW index
	v.indexMu.RLock()
	index, exists := v.hnswIndexes[cmd.Namespace]
	v.indexMu.RUnlock()

	if exists {
		if err := index.Delete(cmd.ID); err != nil {
			v.logger.WithFields(map[string]interface{}{
				"namespace": cmd.Namespace,
				"id":        cmd.ID,
				"error":     err,
			}).Warn("Failed to delete vector from HNSW index")
		}
	}

	v.logger.WithFields(map[string]interface{}{
		"namespace": cmd.Namespace,
		"id":        cmd.ID,
	}).Debug("Vector deleted via Raft consensus")
	return sm.Result{Value: 1}, nil
}

// applyBatchUpsert inserts or updates multiple vectors in storage and HNSW index.
func (v *VectorConsensusFSM) applyBatchUpsert(ctx context.Context, cmd *VectorCommand) (sm.Result, error) {
	v.logger.WithFields(map[string]interface{}{
		"namespace": cmd.Namespace,
		"count":     len(cmd.IDs),
	}).Debug("Applying BatchUpsert")

	if len(cmd.IDs) != len(cmd.Vectors) {
		return sm.Result{}, api.NewLengthMismatchError("VectorConsensusFSM.applyBatchUpsert", "IDs", len(cmd.IDs), "Vectors", len(cmd.Vectors))
	}

	// Apply to storage if available
	if v.storage != nil {
		now := time.Now().UnixNano()
		vecs := make([]*vectorStorage.VectorValue, len(cmd.IDs))
		for i := range cmd.IDs {
			vecs[i] = &vectorStorage.VectorValue{
				ID:        cmd.IDs[i],
				Embedding: cmd.Vectors[i],
				Metadata:  make(map[string]string),
				CreatedAt: now,
				UpdatedAt: now,
				Version:   1,
			}
		}

		if err := v.storage.BatchUpsertVectors(ctx, cmd.Namespace, vecs); err != nil {
			v.logger.WithFields(map[string]interface{}{
				"namespace": cmd.Namespace,
				"count":     len(cmd.IDs),
				"error":     err,
			}).Error("Failed to batch upsert vectors to storage")
			return sm.Result{}, err
		}
	}

	// Update HNSW index
	v.indexMu.RLock()
	index, exists := v.hnswIndexes[cmd.Namespace]
	v.indexMu.RUnlock()

	if exists {
		for i := range cmd.IDs {
			if err := index.Insert(cmd.IDs[i], cmd.Vectors[i]); err != nil {
				v.logger.WithFields(map[string]interface{}{
					"namespace": cmd.Namespace,
					"id":        cmd.IDs[i],
					"error":     err,
				}).Warn("Failed to insert vector into HNSW index during batch")
			}
		}
	} else {
		// Create HNSW index if it doesn't exist (lazy initialization)
		if err := v.createHNSWIndex(cmd.Namespace, v.namespaceMetric(cmd.Namespace)); err == nil {
			v.indexMu.RLock()
			index = v.hnswIndexes[cmd.Namespace]
			v.indexMu.RUnlock()
			if index != nil {
				for i := range cmd.IDs {
					if insertErr := index.Insert(cmd.IDs[i], cmd.Vectors[i]); insertErr != nil {
						v.logger.WithFields(map[string]interface{}{
							"namespace": cmd.Namespace,
							"id":        cmd.IDs[i],
							"error":     insertErr,
						}).Warn("Failed to insert vector into newly created HNSW index during batch")
					}
				}
			}
		}
	}

	v.logger.WithFields(map[string]interface{}{
		"namespace": cmd.Namespace,
		"count":     len(cmd.IDs),
	}).Debug("Batch upsert completed via Raft consensus")
	return sm.Result{Value: uint64(len(cmd.IDs))}, nil
}

// applyUpdateMetadata updates metadata for a vector.
func (v *VectorConsensusFSM) applyUpdateMetadata(ctx context.Context, cmd *VectorCommand) (sm.Result, error) {
	v.logger.WithFields(map[string]interface{}{
		"namespace": cmd.Namespace,
		"id":        cmd.ID,
	}).Debug("Applying UpdateMetadata")

	// Get existing vector
	if v.storage == nil {
		return sm.Result{Value: 1}, nil
	}

	vec, err := v.storage.GetVector(ctx, cmd.Namespace, cmd.ID)
	if err != nil {
		v.logger.WithFields(map[string]interface{}{
			"namespace": cmd.Namespace,
			"id":        cmd.ID,
			"error":     err,
		}).Error("Failed to get vector for metadata update")
		return sm.Result{}, err
	}

	// Update metadata
	if cmd.Merge {
		for k, val := range cmd.Metadata {
			vec.Metadata[k] = vectorStorage.EncodeMetadataValue(val)
		}
	} else {
		vec.Metadata = make(map[string]string)
		for k, val := range cmd.Metadata {
			vec.Metadata[k] = vectorStorage.EncodeMetadataValue(val)
		}
	}

	vec.UpdatedAt = time.Now().UnixNano()
	vec.Version++

	if err := v.storage.UpsertVector(ctx, cmd.Namespace, vec); err != nil {
		v.logger.WithFields(map[string]interface{}{
			"namespace": cmd.Namespace,
			"id":        cmd.ID,
			"error":     err,
		}).Error("Failed to update vector metadata")
		return sm.Result{}, err
	}

	v.logger.WithFields(map[string]interface{}{
		"namespace": cmd.Namespace,
		"id":        cmd.ID,
	}).Debug("Metadata updated via Raft consensus")
	return sm.Result{Value: 1}, nil
}

// applyUpdateNamespace changes a namespace's index parameters and metadata in
// place, merging each key of the command over the stored value, and keeps
// every vector. A missing namespace or a changed dimension is refused on its
// merits, identically on every replica, so neither halts the apply loop.
func (v *VectorConsensusFSM) applyUpdateNamespace(ctx context.Context, cmd *VectorCommand) (sm.Result, error) {
	v.logger.WithField("namespace", cmd.Namespace).Debug("Applying UpdateNamespace")

	if v.storage == nil {
		return sm.Result{Value: 1}, nil
	}

	info, err := v.storage.GetNamespaceInfo(ctx, cmd.Namespace)
	if err != nil {
		if errors.Is(err, vectorStorage.ErrNamespaceNotFound) {
			return sm.Result{}, &vector.NamespaceNotFoundError{Namespace: cmd.Namespace}
		}
		return sm.Result{}, err
	}
	if cmd.Dimension != 0 && cmd.Dimension != info.Dimension {
		return sm.Result{}, &vector.DimensionMismatchError{
			Namespace: cmd.Namespace,
			Got:       int(cmd.Dimension),
			Want:      info.Dimension,
		}
	}

	metadata := make(map[string]string, len(cmd.Metadata))
	for k, val := range cmd.Metadata {
		metadata[k] = fmt.Sprintf("%v", val)
	}
	update, requiresRebuild := vectorStorage.MergeNamespaceUpdate(info, cmd.IndexParams, metadata, cmd.Timestamp)
	if err := v.storage.UpdateNamespace(ctx, cmd.Namespace, update); err != nil {
		return sm.Result{}, err
	}

	data, err := (&UpdateNamespaceResult{Updated: true, RequiresRebuild: requiresRebuild}).Encode()
	if err != nil {
		return sm.Result{}, err
	}

	v.logger.WithFields(map[string]interface{}{
		"namespace":        cmd.Namespace,
		"requires_rebuild": requiresRebuild,
	}).Info("Namespace updated via Raft consensus")
	return sm.Result{Value: 1, Data: data}, nil
}

// Lookup performs a read-only query on the state machine.
// For vectors, we only support namespace lookups here.
// Actual vector queries go through VectorService directly.
func (v *VectorConsensusFSM) Lookup(_ context.Context, query interface{}) (interface{}, error) {
	// Support namespace list query
	if query == nil {
		v.mu.RLock()
		defer v.mu.RUnlock()

		namespaces := make([]string, 0, len(v.namespaces))
		for name := range v.namespaces {
			namespaces = append(namespaces, name)
		}
		return namespaces, nil
	}

	// Support namespace info query by name
	if name, ok := query.(string); ok {
		v.mu.RLock()
		defer v.mu.RUnlock()

		if info, exists := v.namespaces[name]; exists {
			return info, nil
		}
		return nil, nil
	}

	return nil, api.NewInvalidQueryTypeError("VectorConsensusFSM.Lookup", "string or nil", fmt.Sprintf("%T", query))
}

// SaveSnapshot saves the current state to a snapshot.
func (v *VectorConsensusFSM) SaveSnapshot(_ context.Context, w io.Writer, _ <-chan struct{}) error {
	v.mu.RLock()
	defer v.mu.RUnlock()

	// Encode namespace metadata
	data, err := msgpack.Marshal(v.namespaces)
	if err != nil {
		return api.NewSnapshotOperationError("VectorConsensusFSM.SaveSnapshot", "marshal namespaces", err)
	}

	// Write length prefix
	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(len(data))) // #nosec G115 -- msgpack output size bounded by namespace count
	if _, err := w.Write(lenBuf); err != nil {
		return err
	}

	// Write data
	if _, err := w.Write(data); err != nil {
		return err
	}

	return nil
}

// RecoverFromSnapshot restores state from a snapshot.
func (v *VectorConsensusFSM) RecoverFromSnapshot(ctx context.Context, r io.Reader, _ <-chan struct{}) error {
	// Read length prefix
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(r, lenBuf); err != nil {
		return err
	}
	dataLen := binary.BigEndian.Uint32(lenBuf)

	// Read data
	data := make([]byte, dataLen)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}

	// Decode namespace metadata into a fresh map: the snapshot is the whole
	// state, so a namespace it does not name must not survive the install.
	namespaces := make(map[string]*ConsensusNamespaceInfo)
	if err := msgpack.Unmarshal(data, &namespaces); err != nil {
		return api.NewSnapshotOperationError("VectorConsensusFSM.RecoverFromSnapshot", "unmarshal namespaces", err)
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	v.namespaces = namespaces

	v.indexMu.Lock()
	v.hnswIndexes = make(map[string]*hnsw.HNSWIndex)
	v.indexMu.Unlock()

	// Rebuild every namespace's index from the vectors storage holds.
	if v.storage != nil {
		for name, info := range v.namespaces {
			v.rebuildIndexFromStorage(ctx, name, info.Dimension, v.namespaceMetricLocked(name))
		}
	}

	return nil
}

// Close closes the state machine.
func (v *VectorConsensusFSM) Close(_ context.Context) error {
	v.logger.Info("Closing vector consensus state machine")
	return nil
}
