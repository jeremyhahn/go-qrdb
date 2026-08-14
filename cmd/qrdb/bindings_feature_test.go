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

//go:build qrdb_ts && qrdb_vector

package main

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// TimeSeriesBinding not-connected tests (real implementation, not stub)
// ---------------------------------------------------------------------------

func TestTimeSeriesBinding_Write_NotConnected(t *testing.T) {
	b := NewTimeSeriesBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	err := b.Write(nil)
	assertFeatureNotConnected(t, err)
}

func TestTimeSeriesBinding_WriteBatch_NotConnected(t *testing.T) {
	b := NewTimeSeriesBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	err := b.WriteBatch(nil)
	assertFeatureNotConnected(t, err)
}

func TestTimeSeriesBinding_Query_NotConnected(t *testing.T) {
	b := NewTimeSeriesBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.Query(nil)
	assertFeatureNotConnected(t, err)
}

func TestTimeSeriesBinding_Aggregate_NotConnected(t *testing.T) {
	b := NewTimeSeriesBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.Aggregate(nil)
	assertFeatureNotConnected(t, err)
}

func TestTimeSeriesBinding_WritePoint_NotConnected(t *testing.T) {
	b := NewTimeSeriesBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	err := b.WritePoint(nil)
	assertFeatureNotConnected(t, err)
}

func TestTimeSeriesBinding_RichQuery_NotConnected(t *testing.T) {
	b := NewTimeSeriesBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.RichQuery(nil)
	assertFeatureNotConnected(t, err)
}

func TestTimeSeriesBinding_DeleteSeries_NotConnected(t *testing.T) {
	b := NewTimeSeriesBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.DeleteSeries(nil)
	assertFeatureNotConnected(t, err)
}

func TestTimeSeriesBinding_GetRetention_NotConnected(t *testing.T) {
	b := NewTimeSeriesBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.GetRetention(nil)
	assertFeatureNotConnected(t, err)
}

func TestTimeSeriesBinding_SetRetention_NotConnected(t *testing.T) {
	b := NewTimeSeriesBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	err := b.SetRetention(nil)
	assertFeatureNotConnected(t, err)
}

func TestTimeSeriesBinding_ListMetrics_NotConnected(t *testing.T) {
	b := NewTimeSeriesBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.ListMetrics(nil)
	assertFeatureNotConnected(t, err)
}

// ---------------------------------------------------------------------------
// VectorBinding not-connected tests (real implementation, not stub)
// ---------------------------------------------------------------------------

func TestVectorBinding_CreateNamespace_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.CreateNamespace(nil)
	assertFeatureNotConnected(t, err)
}

func TestVectorBinding_DeleteNamespace_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.DeleteNamespace(nil)
	assertFeatureNotConnected(t, err)
}

func TestVectorBinding_ListNamespaces_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.ListNamespaces(nil)
	assertFeatureNotConnected(t, err)
}

func TestVectorBinding_GetNamespaceInfo_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.GetNamespaceInfo(nil)
	assertFeatureNotConnected(t, err)
}

func TestVectorBinding_UpsertVector_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.UpsertVector(nil)
	assertFeatureNotConnected(t, err)
}

func TestVectorBinding_GetVector_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.GetVector(nil)
	assertFeatureNotConnected(t, err)
}

func TestVectorBinding_DeleteVector_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.DeleteVector(nil)
	assertFeatureNotConnected(t, err)
}

func TestVectorBinding_BatchUpsert_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.BatchUpsert(nil)
	assertFeatureNotConnected(t, err)
}

func TestVectorBinding_BatchDeleteVectors_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.BatchDeleteVectors(nil)
	assertFeatureNotConnected(t, err)
}

func TestVectorBinding_ListVectors_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.ListVectors(nil)
	assertFeatureNotConnected(t, err)
}

func TestVectorBinding_UpdateMetadata_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.UpdateMetadata(nil)
	assertFeatureNotConnected(t, err)
}

func TestVectorBinding_SearchSimilar_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.SearchSimilar(nil)
	assertFeatureNotConnected(t, err)
}

func TestVectorBinding_SearchByMetadata_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.SearchByMetadata(nil)
	assertFeatureNotConnected(t, err)
}

func TestVectorBinding_HybridSearch_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.HybridSearch(nil)
	assertFeatureNotConnected(t, err)
}

func TestVectorBinding_SearchByID_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.SearchByID(nil)
	assertFeatureNotConnected(t, err)
}

func TestVectorBinding_RebuildIndex_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.RebuildIndex(nil)
	assertFeatureNotConnected(t, err)
}

func TestVectorBinding_GetRebuildProgress_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.GetRebuildProgress(nil)
	assertFeatureNotConnected(t, err)
}

func TestVectorBinding_GetIndexStats_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.GetIndexStats(nil)
	assertFeatureNotConnected(t, err)
}

func TestVectorBinding_CompactNamespace_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.CompactNamespace(nil)
	assertFeatureNotConnected(t, err)
}

func TestVectorBinding_GetCompactionProgress_NotConnected(t *testing.T) {
	b := NewVectorBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	_, err := b.GetCompactionProgress(nil)
	assertFeatureNotConnected(t, err)
}

// assertFeatureNotConnected verifies err is a *NotConnectedError.
func assertFeatureNotConnected(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var nce *NotConnectedError
	if !errors.As(err, &nce) {
		t.Fatalf("expected *NotConnectedError, got %T: %v", err, err)
	}
}
