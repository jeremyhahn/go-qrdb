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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// TestVecBatchUpsert_IVFRefusesWrongDimensionBeforeWriting drives the
// case where one wrong-length vector in a batch, once stored, would sit in
// the IVF index's pending set and keep every later training attempt from
// succeeding: the batch is refused whole, nothing is written, and the
// namespace keeps training and searching.
func TestVecBatchUpsert_IVFRefusesWrongDimensionBeforeWriting(t *testing.T) {
	ctx := context.Background()
	svc, err := NewVectorService(vector.NewMemoryVectorStorage(), newVecTestLogger())
	require.NoError(t, err)
	createIndexedNamespace(t, svc, "ns", "ivf_flat", map[string]string{"num_centroids": "2"}, []float32{0, 0})

	resp, err := svc.BatchUpsert(ctx, &BatchUpsertRequest{Namespace: "ns", Vectors: []*VectorData{
		{ID: 2, Vector: []float32{10, 10}},
		{ID: 3, Vector: []float32{1, 2, 3}},
	}})
	assert.Nil(t, resp)
	mismatch := requireErrorAs[*DimensionMismatchError](t, err)
	assert.Equal(t, 2, mismatch.Expected)
	assert.Equal(t, 3, mismatch.Actual)

	stats, err := svc.GetIndexStats(ctx, &GetIndexStatsRequest{Namespace: "ns", ShardID: 1})
	require.NoError(t, err)
	assert.Equal(t, uint64(1), stats.VectorCount, "no vector of a refused batch is written")

	_, err = svc.BatchUpsert(ctx, &BatchUpsertRequest{Namespace: "ns", Vectors: []*VectorData{
		{ID: 2, Vector: []float32{10, 10}},
		{ID: 3, Vector: []float32{9, 9}},
	}})
	require.NoError(t, err)

	found, err := svc.SearchSimilar(ctx, &SearchSimilarRequest{
		Namespace: "ns", QueryVector: []float32{10, 10}, K: 1, ShardID: 1,
	})
	require.NoError(t, err)
	require.Len(t, found.Results, 1)
	assert.Equal(t, uint64(2), found.Results[0].ID)
}

// infoFailingStorage is a vector.Storage whose namespace metadata cannot be
// read while every other operation, batch writes included, succeeds.
type infoFailingStorage struct {
	vector.Storage
	err error
}

func (s *infoFailingStorage) GetNamespaceInfo(context.Context, string) (*vector.NamespaceInfo, error) {
	return nil, s.err
}

// TestVecBatchUpsert_RefusesWhenNamespaceInfoUnreadable pins that a batch
// whose namespace dimension cannot be read is not written: without the
// dimension, a batch of the wrong length validates against itself.
func TestVecBatchUpsert_RefusesWhenNamespaceInfoUnreadable(t *testing.T) {
	ctx := context.Background()
	backing := vector.NewMemoryVectorStorage()
	setup, err := NewVectorService(backing, newVecTestLogger())
	require.NoError(t, err)
	createIndexedNamespace(t, setup, "ns", "flat", nil, []float32{0, 0})

	storage := &infoFailingStorage{Storage: backing, err: vector.ErrNamespaceNotFound}
	svc, err := NewVectorService(storage, newVecTestLogger())
	require.NoError(t, err)

	resp, err := svc.BatchUpsert(ctx, &BatchUpsertRequest{Namespace: "ns", Vectors: []*VectorData{
		{ID: 2, Vector: []float32{1, 2, 3}},
		{ID: 3, Vector: []float32{4, 5, 6}},
	}})
	assert.Nil(t, resp)
	notFound := requireErrorAs[*NamespaceNotFoundError](t, err)
	assert.Equal(t, "ns", notFound.Namespace)

	_, err = backing.GetVector(ctx, "ns", 2)
	assert.Error(t, err, "no vector of a refused batch is written")
}

// TestVecBatchUpsert_RefusesUnknownNamespace pins the same refusal through
// a real storage: a batch into a namespace that does not exist is an error.
func TestVecBatchUpsert_RefusesUnknownNamespace(t *testing.T) {
	svc, err := NewVectorService(vector.NewMemoryVectorStorage(), newVecTestLogger())
	require.NoError(t, err)

	resp, err := svc.BatchUpsert(context.Background(), &BatchUpsertRequest{Namespace: "missing", Vectors: []*VectorData{
		{ID: 1, Vector: []float32{1, 2}},
	}})
	assert.Nil(t, resp)
	requireErrorAs[*NamespaceNotFoundError](t, err)
}

func TestValidateBatchDimensions_NamespaceDimension(t *testing.T) {
	require.NoError(t, validateBatchDimensions([]*VectorData{
		{ID: 1, Vector: []float32{1, 2}}, {ID: 2, Vector: []float32{3, 4}},
	}, 2))

	mismatch := requireErrorAs[*DimensionMismatchError](t, validateBatchDimensions([]*VectorData{
		{ID: 1, Vector: []float32{1, 2, 3}},
	}, 2))
	assert.Equal(t, 2, mismatch.Expected)
	assert.Equal(t, 3, mismatch.Actual)
}

func TestValidateBatchDimensions_BatchAgreesWithItself(t *testing.T) {
	require.NoError(t, validateBatchDimensions([]*VectorData{
		{ID: 1, Vector: []float32{1, 2, 3}}, {ID: 2, Vector: []float32{4, 5, 6}},
	}, 0))

	mismatch := requireErrorAs[*DimensionMismatchError](t, validateBatchDimensions([]*VectorData{
		{ID: 1, Vector: []float32{1, 2, 3}}, {ID: 2, Vector: []float32{4, 5}},
	}, 0))
	assert.Equal(t, 3, mismatch.Expected)
	assert.Equal(t, 2, mismatch.Actual)
}

func TestValidateBatchDimensions_RefusesNilEntry(t *testing.T) {
	requireErrorAs[*InvalidVectorError](t, validateBatchDimensions([]*VectorData{
		{ID: 1, Vector: []float32{1, 2}}, nil,
	}, 2))
}
