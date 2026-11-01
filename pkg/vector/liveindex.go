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

package vector

import (
	"bytes"
	"errors"
	"sort"
	"strconv"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/vector/flat"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/ivf"
)

// IndexHit is one ranked result from a LiveIndex search, in the vector
// package's own ID space -- never the string IDs an individual index
// package (IVF) may use internally.
type IndexHit struct {
	ID       uint64
	Distance float32
}

// LiveIndex is the uniform, in-memory ANN index facade the engine's index
// manager drives, hiding the three concrete index packages' differing
// native APIs (hnsw.HNSWIndex, ivf.Index, flat.Index) behind one shape keyed
// by the uint64 vector ID the replicated store itself uses.
type LiveIndex interface {
	// Upsert inserts id, or replaces it if already present. Replacing is not
	// every concrete index's native operation (HNSW and IVF refuse a
	// duplicate ID), so Upsert is defined as delete-then-insert, making
	// every implementation idempotent under replay.
	Upsert(id uint64, embedding []float32) error

	// Delete removes id. Deleting an ID that is not present is a no-op
	// success, so callers (replay, the post-commit hook recovering from a
	// prior partial apply) never need to check existence first.
	Delete(id uint64) error

	// Search returns up to k nearest neighbours of query. It returns
	// ErrIndexNotReady when the index exists but cannot yet answer a
	// search (an IVF index that has not reached its training floor), and
	// hnsw.ErrEmptyIndex when an HNSW or FLAT index holds no vectors.
	Search(query []float32, k int) ([]IndexHit, error)

	// Count returns the number of vectors currently held.
	Count() int64

	// Marshal serializes the index for a checkpoint. It returns
	// ErrIndexNotReady when there is nothing checkpoint-worthy yet.
	Marshal() ([]byte, error)

	// Unmarshal restores the index from bytes Marshal produced.
	Unmarshal(data []byte) error
}

// ErrIndexNotReady is returned by LiveIndex.Search and LiveIndex.Marshal
// when the underlying index cannot yet serve the request -- currently only
// an IVF index that has not accumulated enough vectors to train. It is a
// sentinel the engine's index manager checks for; it is never returned
// across a service boundary (that boundary wraps it in
// service.VectorIndexUntrainedError).
var ErrIndexNotReady = &indexNotReadyError{}

type indexNotReadyError struct{}

func (e *indexNotReadyError) Error() string { return "vector: index not ready" }

// hnswDistanceMetrics maps each record metric to the hnsw package's own enum,
// which the HNSW and flat live indexes take.
var hnswDistanceMetrics = map[DistanceMetric]hnsw.DistanceMetric{
	DistanceCosine:     hnsw.CosineDistance,
	DistanceEuclidean:  hnsw.L2Distance,
	DistanceDotProduct: hnsw.DotProductDistance,
	DistanceManhattan:  hnsw.ManhattanDistance,
}

// ivfDistanceMetrics maps each record metric to the ivf package's own enum.
var ivfDistanceMetrics = map[DistanceMetric]ivf.DistanceMetric{
	DistanceCosine:     ivf.CosineDistance,
	DistanceEuclidean:  ivf.L2Distance,
	DistanceDotProduct: ivf.DotProductDistance,
	DistanceManhattan:  ivf.ManhattanDistance,
}

// liveIndexBuilders maps each record index type to the constructor of its
// live index.
var liveIndexBuilders = map[IndexType]func(namespace string, rec NamespaceRecord) (LiveIndex, error){
	IndexHNSW: newHNSWLiveIndex,
	IndexFlat: newFlatLiveIndex,
	IndexIVFFlat: func(namespace string, rec NamespaceRecord) (LiveIndex, error) {
		return newIVFLiveIndex(namespace, rec), nil
	},
}

// NewLiveIndex constructs an empty LiveIndex for a namespace whose geometry
// is rec, and whose deterministic draws (HNSW layer, IVF k-means seed) are
// salted with namespace. A zero dimension, or a metric or index type this
// build does not recognise, is refused with InvalidDimensionError,
// InvalidDistanceMetricError or InvalidIndexTypeError: building some other
// index would answer searches from a geometry the namespace never had.
func NewLiveIndex(namespace string, rec NamespaceRecord) (LiveIndex, error) {
	if rec.Dimension == 0 {
		return nil, &InvalidDimensionError{Namespace: namespace}
	}
	if _, ok := hnswDistanceMetrics[rec.DistanceMetric]; !ok {
		return nil, &InvalidDistanceMetricError{Namespace: namespace, Metric: rec.DistanceMetric}
	}
	build, ok := liveIndexBuilders[rec.IndexType]
	if !ok {
		return nil, &InvalidIndexTypeError{Namespace: namespace, IndexType: rec.IndexType}
	}
	return build(namespace, rec)
}

func newHNSWLiveIndex(namespace string, rec NamespaceRecord) (LiveIndex, error) {
	cfg := hnsw.DefaultConfig()
	cfg.DistanceMetric = hnswDistanceMetrics[rec.DistanceMetric]
	idx, err := hnsw.NewHNSWIndex(cfg)
	if err != nil {
		return nil, err
	}
	return &hnswLiveIndex{namespace: namespace, idx: idx, ml: cfg.Ml}, nil
}

func newFlatLiveIndex(_ string, rec NamespaceRecord) (LiveIndex, error) {
	return &flatLiveIndex{idx: flat.NewIndex(hnswDistanceMetrics[rec.DistanceMetric])}, nil
}

// ---------------------------------------------------------------------------
// HNSW
// ---------------------------------------------------------------------------

type hnswLiveIndex struct {
	namespace string
	idx       *hnsw.HNSWIndex
	ml        float64
}

func (l *hnswLiveIndex) Upsert(id uint64, embedding []float32) error {
	_ = l.idx.Delete(id) // idempotent: ignore "not found"
	layer := DeterministicLayer(l.namespace, id, l.ml)
	return l.idx.InsertAtLayer(id, embedding, layer)
}

func (l *hnswLiveIndex) Delete(id uint64) error {
	_ = l.idx.Delete(id) // idempotent: ignore "not found"
	return nil
}

func (l *hnswLiveIndex) Search(query []float32, k int) ([]IndexHit, error) {
	results, err := l.idx.Search(query, k)
	if err != nil {
		return nil, err
	}
	hits := make([]IndexHit, len(results))
	for i, r := range results {
		hits[i] = IndexHit{ID: r.ID, Distance: r.Distance}
	}
	return hits, nil
}

func (l *hnswLiveIndex) Count() int64 { return l.idx.Count() }

func (l *hnswLiveIndex) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	if err := l.idx.Snapshot(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (l *hnswLiveIndex) Unmarshal(data []byte) error {
	return l.idx.Restore(bytes.NewReader(data))
}

// ---------------------------------------------------------------------------
// Flat
// ---------------------------------------------------------------------------

type flatLiveIndex struct {
	idx *flat.Index
}

func (l *flatLiveIndex) Upsert(id uint64, embedding []float32) error {
	// flat.Index.Insert is already an upsert (see its doc comment).
	return l.idx.Insert(id, embedding)
}

func (l *flatLiveIndex) Delete(id uint64) error {
	_ = l.idx.Delete(id) // idempotent: ignore "not found"
	return nil
}

// Search answers an empty index with hnsw.ErrEmptyIndex, as the HNSW live
// index does, so a caller tells an empty index apart from a failed search
// without knowing which index type it holds.
func (l *flatLiveIndex) Search(query []float32, k int) ([]IndexHit, error) {
	results, err := l.idx.Search(query, k)
	if errors.Is(err, flat.ErrEmptyIndex) {
		return nil, hnsw.ErrEmptyIndex
	}
	if err != nil {
		return nil, err
	}
	hits := make([]IndexHit, len(results))
	for i, r := range results {
		hits[i] = IndexHit{ID: r.ID, Distance: r.Distance}
	}
	return hits, nil
}

func (l *flatLiveIndex) Count() int64 { return l.idx.Count() }

func (l *flatLiveIndex) Marshal() ([]byte, error) { return l.idx.Marshal() }

func (l *flatLiveIndex) Unmarshal(data []byte) error { return l.idx.Unmarshal(data) }

// ---------------------------------------------------------------------------
// IVF
// ---------------------------------------------------------------------------

// defaultIVFCentroids is the phase-2 default partition count for a
// namespace that does not set "num_centroids" in its index parameters. It
// is deliberately conservative (the ivf package's own production default is
// 256): a replicated namespace trains as soon as it has this many vectors,
// and a default sized for a standalone tuned deployment would leave most
// namespaces "warming" indefinitely in ordinary tests and small databases.
const defaultIVFCentroids = 8

// ivfLiveIndex adapts ivf.Index, which requires Train before any vector can
// be added, to LiveIndex's incremental Upsert/Delete shape. Vectors are
// buffered until the namespace has accumulated numCentroids of them, at
// which point the index trains once, deterministically (vector.DeterministicSeed),
// and takes every buffered vector. Search and Marshal return
// ErrIndexNotReady before that point -- a real, documented limit of this
// phase: an IVF namespace with fewer than numCentroids vectors never
// becomes searchable until more arrive (see docs/vectors/engineering.md
// "Target: determinism" and the vector-replicated phase-2 report).
type ivfLiveIndex struct {
	mu           sync.Mutex
	namespace    string
	rec          NamespaceRecord
	numCentroids int
	index        ivf.Index
	pendingIDs   map[uint64][]float32
}

func newIVFLiveIndex(namespace string, rec NamespaceRecord) *ivfLiveIndex {
	n := defaultIVFCentroids
	if v, ok := rec.IndexParams["num_centroids"]; ok {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}
	return &ivfLiveIndex{
		namespace:    namespace,
		rec:          rec,
		numCentroids: n,
		pendingIDs:   make(map[uint64][]float32),
	}
}

// sortedPendingIDs returns the buffered IDs in ascending order, so training
// and the subsequent Add calls happen in a deterministic sequence rather
// than Go's randomized map iteration order.
func (l *ivfLiveIndex) sortedPendingIDs() []uint64 {
	ids := make([]uint64, 0, len(l.pendingIDs))
	for id := range l.pendingIDs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (l *ivfLiveIndex) train() error {
	ids := l.sortedPendingIDs()
	vectors := make([][]float32, len(ids))
	for i, id := range ids {
		vectors[i] = l.pendingIDs[id]
	}
	centroids := l.numCentroids
	if centroids > len(vectors) {
		centroids = len(vectors)
	}
	cfg := &ivf.Config{
		NumCentroids:   centroids,
		Dimensions:     int(l.rec.Dimension),
		MaxIterations:  25,
		Convergence:    0.0001,
		InitMethod:     "kmeans++",
		DefaultNProbe:  centroids,
		DistanceMetric: ivfDistanceMetrics[l.rec.DistanceMetric],
		Seed:           DeterministicSeed(l.namespace, l.rec.TrainingGeneration),
	}
	index, err := ivf.NewIndex(cfg)
	if err != nil {
		return err
	}
	if err := index.Train(vectors); err != nil {
		return err
	}
	for _, id := range ids {
		if err := index.Add(strconv.FormatUint(id, 10), l.pendingIDs[id], nil); err != nil {
			return err
		}
	}
	l.index = index
	l.pendingIDs = nil
	return nil
}

func (l *ivfLiveIndex) Upsert(id uint64, embedding []float32) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.index == nil {
		l.pendingIDs[id] = embedding
		if len(l.pendingIDs) >= l.numCentroids {
			return l.train()
		}
		return nil
	}

	key := strconv.FormatUint(id, 10)
	_ = l.index.Remove(key) // idempotent: ignore "not found"
	return l.index.Add(key, embedding, nil)
}

func (l *ivfLiveIndex) Delete(id uint64) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.index == nil {
		delete(l.pendingIDs, id)
		return nil
	}
	_ = l.index.Remove(strconv.FormatUint(id, 10)) // idempotent: ignore "not found"
	return nil
}

func (l *ivfLiveIndex) Search(query []float32, k int) ([]IndexHit, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.index == nil {
		return nil, ErrIndexNotReady
	}
	results, err := l.index.Search(query, k, 0)
	if err != nil {
		return nil, err
	}
	hits := make([]IndexHit, 0, len(results))
	for _, r := range results {
		id, convErr := strconv.ParseUint(r.ID, 10, 64)
		if convErr != nil {
			continue
		}
		hits = append(hits, IndexHit{ID: id, Distance: r.Distance})
	}
	return hits, nil
}

func (l *ivfLiveIndex) Count() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.index == nil {
		return int64(len(l.pendingIDs))
	}
	return int64(l.index.Size())
}

func (l *ivfLiveIndex) Marshal() ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.index == nil {
		return nil, ErrIndexNotReady
	}
	return l.index.Marshal()
}

func (l *ivfLiveIndex) Unmarshal(data []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// DefaultConfig is a placeholder to satisfy ivf.NewIndex's config
	// validation; Unmarshal immediately overwrites idx.config wholesale from
	// the persisted bytes, so the real configuration this namespace trained
	// with (centroid count included) is what ends up in effect, not this
	// one.
	cfg := ivf.DefaultConfig(int(l.rec.Dimension))
	cfg.DistanceMetric = ivfDistanceMetrics[l.rec.DistanceMetric]
	index, err := ivf.NewIndex(cfg)
	if err != nil {
		return err
	}
	if err := index.Unmarshal(data); err != nil {
		return err
	}
	l.index = index
	l.pendingIDs = nil
	return nil
}
