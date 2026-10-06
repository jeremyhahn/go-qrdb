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
	"math"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/vector"
)

// ---------------------------------------------------------------------------
// Vector engine: the apply-side half of docs/vectors/engineering.md "Target:
// the vector engine".
//
// Every function here reads and writes through the caller's storage.Batch
// only, never through a side store, so a vector command is exactly as
// replicated and exactly as atomic as the database binding that proposes it
// -- the Pebble-backed node-local store this replaces had neither (go-qrdb
// vector-replicated audit, 2026-10-02). They are plain functions, not
// MVCCEngine or KVEngine methods, so both engines call the identical logic
// (buildUpdateHandlers in mvcc_engine.go, buildExtHandlers in kv_engine.go)
// and cannot drift apart on what a vector command does to the store.
//
// A validation failure is returned as a plain Go error, using one of
// pkg/vector's typed errors. That is enough for it to be treated as a
// deterministic refusal rather than a fault: every one of those types
// implements protocol.RefusalError (see pkg/vector/errors.go), which is the
// declaration statemachine.ResolveApplyError reads -- the single
// classification point both MVCCEngine.UpdateEntries and KVEngine.Update
// already call after every handler, vector or not. A refusal is encoded into
// the result and consensus advances; anything that does NOT declare itself a
// refusal (a storage Get/Set/Delete/iterator failure, a decode error on bytes
// already committed) propagates and halts Apply, which is the correct
// outcome for a fault no rule can attribute to the request. Handlers here
// therefore never pre-encode a result themselves -- that would flatten the
// one case (an error bubbling up through upsertOne/deleteOne) where the SAME
// return site can be either a refusal or a fault, into always a refusal.
// ---------------------------------------------------------------------------

// defaultMutationLogWindow is the number of trailing mutation-log entries a
// namespace keeps. It is a node setting with this default
// (docs/vectors/engineering.md "Target: the vector engine", "Mutation log").
const defaultMutationLogWindow = 50000

// metadataIndexPresence is the value written at every metadata-index key. A
// metadata-index entry's existence is the only thing a search reads --
// keys.go's layout carries the field, value, and ID in the KEY -- so every
// entry shares this one sentinel byte rather than an empty value, which the
// storage.Reader contract cannot tell apart from "key does not exist" (see
// storage.Reader.Get's doc comment).
var metadataIndexPresence = []byte{1}

// readNamespaceRecord reads and decodes namespace's record through r, which
// must be the caller's storage.Batch so the read observes the batch's own
// pending writes. It returns (nil, nil) when the namespace does not exist.
func readNamespaceRecord(r storage.Reader, namespace string) (*vector.NamespaceRecord, error) {
	data, err := r.Get(vector.NamespaceKey(namespace))
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	rec, err := vector.DecodeNamespaceRecord(data)
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// putNamespaceRecord encodes and writes rec into batch.
func putNamespaceRecord(batch storage.Batch, namespace string, rec vector.NamespaceRecord) error {
	data, err := vector.EncodeNamespaceRecord(rec)
	if err != nil {
		return err
	}
	return batch.Set(vector.NamespaceKey(namespace), data)
}

// appendMutationLog advances rec's sequence by one, writes a mutation-log
// entry at the new sequence, and trims the entry that just fell outside the
// retained window -- all deterministically, from rec and the entry alone, so
// every replica trims the identical key. rec.Seq is updated in place; the
// caller persists rec itself (putNamespaceRecord).
func appendMutationLog(batch storage.Batch, namespace string, rec *vector.NamespaceRecord, op vector.MutationOp, id uint64) error {
	rec.Seq++
	entry := vector.EncodeMutationLogEntry(vector.MutationLogEntry{Op: op, ID: id})
	if err := batch.Set(vector.MutationLogKey(namespace, rec.Seq), entry); err != nil {
		return err
	}
	if rec.Seq > defaultMutationLogWindow {
		return batch.Delete(vector.MutationLogKey(namespace, rec.Seq-defaultMutationLogWindow))
	}
	return nil
}

// deleteKeyRange deletes every key in batch carrying prefix. It reads the
// keys to delete through batch itself (a Reader), so a namespace delete
// removes records an earlier command in the same apply batch just wrote, not
// only ones already committed.
func deleteKeyRange(batch storage.Batch, prefix []byte) error {
	it := batch.NewIterator(&storage.IteratorOptions{
		LowerBound: prefix,
		UpperBound: prefixEndBytes(prefix),
		OnlyKeys:   true,
	})
	defer func() { _ = it.Close() }()

	var keys [][]byte
	for it.SeekToFirst(); it.Valid(); it.Next() {
		key := make([]byte, len(it.Key()))
		copy(key, it.Key())
		keys = append(keys, key)
	}
	if err := it.Error(); err != nil {
		return err
	}
	for _, key := range keys {
		if err := batch.Delete(key); err != nil {
			return err
		}
	}
	return nil
}

// replaceMetadataIndex removes oldMeta's entries for id and writes newMeta's,
// so a vector's metadata index always reflects its current record, never a
// union of its past and present metadata.
func replaceMetadataIndex(batch storage.Batch, namespace string, id uint64, oldMeta, newMeta map[string]string) error {
	for field, value := range oldMeta {
		// Comma-ok, not a bare map read: an ABSENT field in newMeta and a
		// field PRESENT in newMeta with the empty string are both read as ""
		// by a bare `newMeta[field]`, so a field whose value is genuinely ""
		// in both old and new was indistinguishable from a field removed
		// entirely -- the removed field's stale index entry was never
		// deleted (go-qrdb vector-replicated review, B4).
		if newValue, ok := newMeta[field]; ok && newValue == value {
			continue // unchanged pair; leave its index entry alone
		}
		if err := batch.Delete(vector.MetadataKey(namespace, field, value, id)); err != nil {
			return err
		}
	}
	for field, value := range newMeta {
		// Same hazard in the other direction: a field ADDED with the empty
		// string value read as "already present" against an old map that
		// never had the field at all, so its new index entry was never
		// written.
		if oldValue, ok := oldMeta[field]; ok && oldValue == value {
			continue // already present from the loop above's skip
		}
		if err := batch.Set(vector.MetadataKey(namespace, field, value, id), metadataIndexPresence); err != nil {
			return err
		}
	}
	return nil
}

// vectorGeometry validates a namespace's dimension, distance metric, and
// index type together, the three fields a create fixes for the namespace's
// lifetime.
func vectorGeometry(namespace string, dimension uint32, metric, indexType byte) error {
	if dimension == 0 {
		return &vector.InvalidDimensionError{Namespace: namespace}
	}
	if !vector.ValidDistanceMetric(metric) {
		return &vector.InvalidDistanceMetricError{Namespace: namespace, Metric: metric}
	}
	if !vector.ValidIndexType(indexType) {
		return &vector.InvalidIndexTypeError{Namespace: namespace, IndexType: indexType}
	}
	return nil
}

// applyVectorNamespaceCreate applies protocol.OpVectorNamespaceCreate.
func applyVectorNamespaceCreate(batch storage.Batch, payload []byte) (sm.Result, error) {
	p, err := protocol.DecodeVectorNamespaceCreate(payload)
	if err != nil {
		return sm.Result{}, err
	}
	if !vector.ValidNamespaceName(p.Name) {
		return sm.Result{}, &vector.InvalidNamespaceNameError{Namespace: p.Name}
	}
	if err := vectorGeometry(p.Name, p.Dimension, p.DistanceMetric, p.IndexType); err != nil {
		return sm.Result{}, err
	}
	existing, err := readNamespaceRecord(batch, p.Name)
	if err != nil {
		return sm.Result{}, err
	}
	if existing != nil {
		return sm.Result{}, &vector.NamespaceExistsError{Namespace: p.Name}
	}

	rec := vector.NamespaceRecord{
		Dimension:          p.Dimension,
		DistanceMetric:     p.DistanceMetric,
		IndexType:          p.IndexType,
		IndexParams:        p.IndexParams,
		Seq:                0,
		NextID:             1,
		TrainingGeneration: 0,
		CreatedAtUnixNano:  p.CreatedAtUnixNano,
	}
	if err := putNamespaceRecord(batch, p.Name, rec); err != nil {
		return sm.Result{}, err
	}
	return sm.Result{Value: 1}, nil
}

// applyVectorNamespaceDelete applies protocol.OpVectorNamespaceDelete. A
// namespace already absent is a no-op success, like handleIndexDrop: a
// cleanup proposal retried after its own success must not stall consensus.
func applyVectorNamespaceDelete(batch storage.Batch, payload []byte) (sm.Result, error) {
	namespace, err := protocol.DecodeVectorNamespaceDelete(payload)
	if err != nil {
		return sm.Result{}, err
	}
	if !vector.ValidNamespaceName(namespace) {
		return sm.Result{}, &vector.InvalidNamespaceNameError{Namespace: namespace}
	}
	existing, err := readNamespaceRecord(batch, namespace)
	if err != nil {
		return sm.Result{}, err
	}
	if existing == nil {
		return sm.Result{Value: 1}, nil
	}
	for _, prefix := range [][]byte{
		vector.VectorPrefix(namespace),
		vector.MetadataPrefix(namespace),
		vector.MutationLogPrefix(namespace),
	} {
		if err := deleteKeyRange(batch, prefix); err != nil {
			return sm.Result{}, err
		}
	}
	if err := batch.Delete(vector.NamespaceKey(namespace)); err != nil {
		return sm.Result{}, err
	}
	return sm.Result{Value: 1}, nil
}

// allocateID resolves the ID one upsert item writes at, and the namespace's
// NextID counter after that write, from an input NextID and the item's own
// (HasID, ID) -- deterministically and without mutating anything, so it can
// be run twice: once in a batch's pre-validation pass (simulated, against a
// local copy of NextID) and once for real (against rec.NextID) when
// upsertOne stages the write it validated. Running the identical allocation
// twice is what lets a batch upsert refuse an item that would overflow the
// allocator WITHOUT any earlier item in the same batch having staged a
// write yet (go-qrdb vector-replicated review, B2's invariant, applied to
// N1's new refusal).
func allocateID(namespace string, nextID uint64, item protocol.VectorUpsertParams) (id, newNextID uint64, err error) {
	if !item.HasID {
		if nextID == math.MaxUint64 {
			return 0, 0, &vector.IDOverflowError{Namespace: namespace}
		}
		return nextID, nextID + 1, nil
	}
	if item.ID == math.MaxUint64 {
		// NextID = id+1 would wrap to 0, which this namespace would then
		// hand out as a FRESH auto-allocated ID -- colliding with whatever
		// this explicit upsert just wrote at MaxUint64, or reusing ID 0 if
		// it was never otherwise assigned (go-qrdb vector-replicated
		// review, N1).
		return 0, 0, &vector.IDOverflowError{Namespace: namespace}
	}
	if item.ID >= nextID {
		// An explicit ID at or past the allocator keeps every replica's
		// allocator ahead of every ID ever stored, so a later omitted-ID
		// upsert can never collide with one a caller supplied explicitly.
		return item.ID, item.ID + 1, nil
	}
	return item.ID, nextID, nil
}

// upsertOne validates and stages one vector write into batch against rec (the
// namespace record, already read), allocating an ID from rec.NextID when the
// item omits one. It does not persist rec; the caller does, once, after every
// item in the command has staged successfully -- see applyVectorUpsert and
// applyVectorBatchUpsert.
func upsertOne(batch storage.Batch, namespace string, rec *vector.NamespaceRecord, item protocol.VectorUpsertParams, timestamp int64) error {
	if len(item.Embedding) != int(rec.Dimension) {
		return &vector.DimensionMismatchError{Namespace: namespace, Got: len(item.Embedding), Want: rec.Dimension}
	}

	id, newNextID, err := allocateID(namespace, rec.NextID, item)
	if err != nil {
		return err
	}
	rec.NextID = newNextID

	var oldMeta map[string]string
	createdAt := timestamp
	existing, err := batch.Get(vector.VectorKey(namespace, id))
	if err != nil {
		return err
	}
	if existing != nil {
		old, err := vector.DecodeVectorRecord(existing)
		if err != nil {
			return err
		}
		oldMeta = old.Metadata
		createdAt = old.CreatedAtUnixNano
	}

	record := vector.VectorRecord{
		Embedding:         item.Embedding,
		Metadata:          item.Metadata,
		CreatedAtUnixNano: createdAt,
		UpdatedAtUnixNano: timestamp,
	}
	data, err := vector.EncodeVectorRecord(record)
	if err != nil {
		return err
	}
	if err := batch.Set(vector.VectorKey(namespace, id), data); err != nil {
		return err
	}
	if err := replaceMetadataIndex(batch, namespace, id, oldMeta, item.Metadata); err != nil {
		return err
	}
	return appendMutationLog(batch, namespace, rec, vector.MutationUpsert, id)
}

// applyVectorUpsert applies protocol.OpVectorUpsert.
func applyVectorUpsert(batch storage.Batch, payload []byte) (sm.Result, error) {
	namespace, timestamp, item, err := protocol.DecodeVectorUpsert(payload)
	if err != nil {
		return sm.Result{}, err
	}
	if !vector.ValidNamespaceName(namespace) {
		return sm.Result{}, &vector.InvalidNamespaceNameError{Namespace: namespace}
	}
	rec, err := readNamespaceRecord(batch, namespace)
	if err != nil {
		return sm.Result{}, err
	}
	if rec == nil {
		return sm.Result{}, &vector.NamespaceNotFoundError{Namespace: namespace}
	}

	assignedID := item.ID
	if !item.HasID {
		assignedID = rec.NextID
	}
	if err := upsertOne(batch, namespace, rec, item, timestamp); err != nil {
		return sm.Result{}, err
	}
	if err := putNamespaceRecord(batch, namespace, *rec); err != nil {
		return sm.Result{}, err
	}
	return sm.Result{Value: assignedID}, nil
}

// applyVectorBatchUpsert applies protocol.OpVectorBatchUpsert. Every item is
// validated before any of them is staged into batch, so a batch that fails
// partway writes nothing: the namespace record, the mutation log, and every
// vector and metadata key are exactly as they were before the command
// (docs/vectors/engineering.md "Target: acceptance tests", "Atomicity").
func applyVectorBatchUpsert(batch storage.Batch, payload []byte) (sm.Result, error) {
	p, err := protocol.DecodeVectorBatchUpsert(payload)
	if err != nil {
		return sm.Result{}, err
	}
	if !vector.ValidNamespaceName(p.Name) {
		return sm.Result{}, &vector.InvalidNamespaceNameError{Namespace: p.Name}
	}
	rec, err := readNamespaceRecord(batch, p.Name)
	if err != nil {
		return sm.Result{}, err
	}
	if rec == nil {
		return sm.Result{}, &vector.NamespaceNotFoundError{Namespace: p.Name}
	}
	// Simulated against a local copy of NextID, in lockstep with upsertOne's
	// own allocation (allocateID), so a batch whose LATER item would
	// overflow the allocator is refused before the EARLIER items in the
	// same batch stage any write -- the same atomicity hazard B2 fixed for
	// "vector not found", applied here to N1's new overflow refusal.
	nextID := rec.NextID
	for _, item := range p.Vectors {
		if len(item.Embedding) != int(rec.Dimension) {
			return sm.Result{}, &vector.DimensionMismatchError{Namespace: p.Name, Got: len(item.Embedding), Want: rec.Dimension}
		}
		var err error
		if _, nextID, err = allocateID(p.Name, nextID, item); err != nil {
			return sm.Result{}, err
		}
	}

	for _, item := range p.Vectors {
		if err := upsertOne(batch, p.Name, rec, item, p.TimestampUnixNano); err != nil {
			return sm.Result{}, err
		}
	}
	if err := putNamespaceRecord(batch, p.Name, *rec); err != nil {
		return sm.Result{}, err
	}
	return sm.Result{Value: uint64(len(p.Vectors))}, nil
}

// deleteOne validates and stages the removal of one vector from batch against
// rec, appending a mutation-log entry. It does not persist rec.
func deleteOne(batch storage.Batch, namespace string, rec *vector.NamespaceRecord, id uint64) error {
	existing, err := batch.Get(vector.VectorKey(namespace, id))
	if err != nil {
		return err
	}
	if existing == nil {
		return &vector.VectorNotFoundError{Namespace: namespace, ID: id}
	}
	old, err := vector.DecodeVectorRecord(existing)
	if err != nil {
		return err
	}
	if err := batch.Delete(vector.VectorKey(namespace, id)); err != nil {
		return err
	}
	for field, value := range old.Metadata {
		if err := batch.Delete(vector.MetadataKey(namespace, field, value, id)); err != nil {
			return err
		}
	}
	return appendMutationLog(batch, namespace, rec, vector.MutationDelete, id)
}

// applyVectorDelete applies protocol.OpVectorDelete.
func applyVectorDelete(batch storage.Batch, payload []byte) (sm.Result, error) {
	namespace, id, err := protocol.DecodeVectorDelete(payload)
	if err != nil {
		return sm.Result{}, err
	}
	if !vector.ValidNamespaceName(namespace) {
		return sm.Result{}, &vector.InvalidNamespaceNameError{Namespace: namespace}
	}
	rec, err := readNamespaceRecord(batch, namespace)
	if err != nil {
		return sm.Result{}, err
	}
	if rec == nil {
		return sm.Result{}, &vector.NamespaceNotFoundError{Namespace: namespace}
	}
	if err := deleteOne(batch, namespace, rec, id); err != nil {
		return sm.Result{}, err
	}
	if err := putNamespaceRecord(batch, namespace, *rec); err != nil {
		return sm.Result{}, err
	}
	return sm.Result{Value: 1}, nil
}

// applyVectorBatchDelete applies protocol.OpVectorBatchDelete. Every ID is
// checked to exist before any of them is staged into batch, for the same
// atomicity reason as applyVectorBatchUpsert.
func applyVectorBatchDelete(batch storage.Batch, payload []byte) (sm.Result, error) {
	namespace, ids, err := protocol.DecodeVectorBatchDelete(payload)
	if err != nil {
		return sm.Result{}, err
	}
	if !vector.ValidNamespaceName(namespace) {
		return sm.Result{}, &vector.InvalidNamespaceNameError{Namespace: namespace}
	}
	rec, err := readNamespaceRecord(batch, namespace)
	if err != nil {
		return sm.Result{}, err
	}
	if rec == nil {
		return sm.Result{}, &vector.NamespaceNotFoundError{Namespace: namespace}
	}
	// Every ID is checked to exist, AND no ID may repeat, before any of them
	// is staged into batch. The duplicate check is not an optimization: the
	// existence check alone passes for a repeated ID (it is found present
	// twice, since nothing has deleted it yet), so without this the first
	// occurrence's delete would stage successfully and the second
	// occurrence's delete would then refuse "not found" against a record
	// its OWN sibling in this same command just removed -- a refused
	// command that still deleted the vector (go-qrdb vector-replicated
	// review, B2).
	seen := make(map[uint64]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			return sm.Result{}, &vector.DuplicateIDError{Namespace: namespace, ID: id}
		}
		seen[id] = struct{}{}
		exists, err := batch.Get(vector.VectorKey(namespace, id))
		if err != nil {
			return sm.Result{}, err
		}
		if exists == nil {
			return sm.Result{}, &vector.VectorNotFoundError{Namespace: namespace, ID: id}
		}
	}

	for _, id := range ids {
		if err := deleteOne(batch, namespace, rec, id); err != nil {
			return sm.Result{}, err
		}
	}
	if err := putNamespaceRecord(batch, namespace, *rec); err != nil {
		return sm.Result{}, err
	}
	return sm.Result{Value: uint64(len(ids))}, nil
}

// applyVectorUpdateMetadata applies protocol.OpVectorUpdateMetadata: it
// replaces a vector's metadata and its index entries without touching the
// embedding.
func applyVectorUpdateMetadata(batch storage.Batch, payload []byte) (sm.Result, error) {
	p, err := protocol.DecodeVectorUpdateMetadata(payload)
	if err != nil {
		return sm.Result{}, err
	}
	if !vector.ValidNamespaceName(p.Name) {
		return sm.Result{}, &vector.InvalidNamespaceNameError{Namespace: p.Name}
	}
	rec, err := readNamespaceRecord(batch, p.Name)
	if err != nil {
		return sm.Result{}, err
	}
	if rec == nil {
		return sm.Result{}, &vector.NamespaceNotFoundError{Namespace: p.Name}
	}
	existing, err := batch.Get(vector.VectorKey(p.Name, p.ID))
	if err != nil {
		return sm.Result{}, err
	}
	if existing == nil {
		return sm.Result{}, &vector.VectorNotFoundError{Namespace: p.Name, ID: p.ID}
	}
	old, err := vector.DecodeVectorRecord(existing)
	if err != nil {
		return sm.Result{}, err
	}

	record := vector.VectorRecord{
		Embedding:         old.Embedding,
		Metadata:          p.Metadata,
		CreatedAtUnixNano: old.CreatedAtUnixNano,
		UpdatedAtUnixNano: p.TimestampUnixNano,
	}
	data, err := vector.EncodeVectorRecord(record)
	if err != nil {
		return sm.Result{}, err
	}
	if err := batch.Set(vector.VectorKey(p.Name, p.ID), data); err != nil {
		return sm.Result{}, err
	}
	if err := replaceMetadataIndex(batch, p.Name, p.ID, old.Metadata, p.Metadata); err != nil {
		return sm.Result{}, err
	}
	if err := appendMutationLog(batch, p.Name, rec, vector.MutationUpdate, p.ID); err != nil {
		return sm.Result{}, err
	}
	if err := putNamespaceRecord(batch, p.Name, *rec); err != nil {
		return sm.Result{}, err
	}
	return sm.Result{Value: 1}, nil
}

// applyVectorIndexReconfigure applies protocol.OpVectorIndexReconfigure: it
// records new index parameters and advances the namespace's training
// generation. It has no index to rebuild yet -- the in-memory ANN index and
// its post-commit apply hook are a later phase of this program
// (docs/vectors/engineering.md "Target: the vector engine", "Apply and the
// index") -- so today it only updates the record every replica will rebuild
// its own index from once that hook exists.
func applyVectorIndexReconfigure(batch storage.Batch, payload []byte) (sm.Result, error) {
	p, err := protocol.DecodeVectorIndexReconfigure(payload)
	if err != nil {
		return sm.Result{}, err
	}
	if !vector.ValidNamespaceName(p.Name) {
		return sm.Result{}, &vector.InvalidNamespaceNameError{Namespace: p.Name}
	}
	rec, err := readNamespaceRecord(batch, p.Name)
	if err != nil {
		return sm.Result{}, err
	}
	if rec == nil {
		return sm.Result{}, &vector.NamespaceNotFoundError{Namespace: p.Name}
	}
	if !vector.ValidIndexType(p.IndexType) {
		return sm.Result{}, &vector.InvalidIndexTypeError{Namespace: p.Name, IndexType: p.IndexType}
	}
	rec.IndexType = p.IndexType
	rec.IndexParams = p.IndexParams
	rec.TrainingGeneration++
	if err := putNamespaceRecord(batch, p.Name, *rec); err != nil {
		return sm.Result{}, err
	}
	return sm.Result{Value: rec.TrainingGeneration}, nil
}
