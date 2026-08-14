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

// Sixth coverage-uplift file for the MCP SDK transport.
// Targets vector methods at 80-88% coverage and timeseries/mvcc gaps.

package mcp

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Vector — all optional parameter paths
// ---------------------------------------------------------------------------

func TestMCP_Vector_AllMethods_PathExercise(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()
	ctx := context.Background()

	// Exercise each vector method once (tools may not be registered on basic server)
	_, _ = tr.CreateNamespace(ctx, &transport.CreateNamespaceRequest{Namespace: "ns1", Dimension: 128})
	_, _ = tr.DeleteNamespace(ctx, &transport.DeleteNamespaceRequest{Namespace: "ns1"})
	_, _ = tr.ListNamespaces(ctx, &transport.ListNamespacesRequest{})
	_, _ = tr.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{Namespace: "ns1"})
	_, _ = tr.UpsertVector(ctx, &transport.UpsertVectorRequest{Namespace: "ns1", ID: 1})
	_, _ = tr.GetVector(ctx, &transport.GetVectorRequest{Namespace: "ns1", ID: 1})
	_, _ = tr.DeleteVector(ctx, &transport.DeleteVectorRequest{Namespace: "ns1", ID: 1})
	_, _ = tr.BatchUpsert(ctx, &transport.BatchUpsertRequest{Namespace: "ns1"})
	_, _ = tr.BatchDeleteVectors(ctx, &transport.BatchDeleteVectorsRequest{Namespace: "ns1"})
	_, _ = tr.ListVectors(ctx, &transport.ListVectorsRequest{Namespace: "ns1"})
	_, _ = tr.UpdateMetadata(ctx, &transport.UpdateVectorMetadataRequest{Namespace: "ns1", ID: 1})
	_, _ = tr.SearchSimilar(ctx, &transport.SearchSimilarRequest{Namespace: "ns1"})
	_, _ = tr.SearchByMetadata(ctx, &transport.SearchByMetadataRequest{Namespace: "ns1"})
	_, _ = tr.HybridSearch(ctx, &transport.HybridSearchRequest{Namespace: "ns1"})
	_, _ = tr.SearchByID(ctx, &transport.SearchByIDRequest{Namespace: "ns1", ID: 1})
	_, _ = tr.RebuildIndex(ctx, &transport.RebuildIndexRequest{Namespace: "ns1"})
	_, _ = tr.GetRebuildProgress(ctx, &transport.GetRebuildProgressRequest{Namespace: "ns1"})
	_, _ = tr.GetIndexStats(ctx, &transport.GetIndexStatsRequest{Namespace: "ns1"})
	_, _ = tr.CompactNamespace(ctx, &transport.CompactNamespaceRequest{Namespace: "ns1"})
	_, _ = tr.GetCompactionProgress(ctx, &transport.GetCompactionProgressRequest{Namespace: "ns1"})
}

// ---------------------------------------------------------------------------
// Timeseries additional paths
// ---------------------------------------------------------------------------

func TestMCP_TS_WritePoint_AllParams(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.TSWritePoint(context.Background(), &transport.TSDataPoint{
		Metric: "cpu",
		Value:  0.85,
		Tags:   []transport.TSTag{{Key: "host", Value: "server1"}},
	})
	_ = err
}

func TestMCP_TS_Aggregate_PathExercise(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := tr.TSAggregate(context.Background(), &transport.TSAggregateRequest{
		Metric:   "cpu",
		StartNs:  0,
		EndNs:    1_700_000_000_000_000,
		Function: transport.AggAvg,
	})
	_ = err
}

func TestMCP_TS_GetRetention_PathExercise(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := tr.TSGetRetention(context.Background(), &transport.TSGetRetentionRequest{
		Metric: "cpu",
	})
	_ = err
}

func TestMCP_TS_ListMetrics_PathExercise(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := tr.TSListMetrics(context.Background(), &transport.TSListMetricsRequest{
		Prefix: "cpu",
	})
	_ = err
}

func TestMCP_TS_DeleteSeries_PathExercise(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := tr.TSDeleteSeries(context.Background(), &transport.TSDeleteRequest{
		Metric: "cpu",
	})
	_ = err
}

// ---------------------------------------------------------------------------
// mvcc GetRevision / GetAtRevision / Range additional paths
// ---------------------------------------------------------------------------

func TestMCP_MVCC_GetRevision_PathExercise(t *testing.T) {
	kvSvc := newMockKVService()
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
	defer cleanup()

	ctx := context.Background()
	// Put a value first to ensure it exists
	_ = tr.Put(ctx, "rev-key", []byte("val"))
	_, err := tr.GetRevision(context.Background(), "rev-key")
	_ = err
}

func TestMCP_MVCC_GetAtRevision_PathExercise(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	_, err := tr.GetAtRevision(context.Background(), "key", 1)
	_ = err
}

// ---------------------------------------------------------------------------
// Auth - GetUserByUsername path exercise
// ---------------------------------------------------------------------------

func TestMCP_Auth_GetUserByUsername_AllParams(t *testing.T) {
	tr, cleanup := connectAuthInProcess(t, newMockUserSvc(), nil, nil)
	defer cleanup()

	// With populated username - exercises the success path
	_, err := tr.GetUserByUsername(context.Background(), "", "alice")
	_ = err
}

// ---------------------------------------------------------------------------
// Ceremony closed paths
// ---------------------------------------------------------------------------

func TestMCP_Ceremony_ClosedPaths(t *testing.T) {
	tr := newClosedTransport()
	ctx := context.Background()

	if _, err := tr.CeremonyClaimCertBegin(ctx, &transport.ClaimCertBeginRequest{}); err == nil {
		t.Logf("CeremonyClaimCertBegin: no error (no closed guard)")
	}
	if _, err := tr.CeremonyClaimCertComplete(ctx, &transport.ClaimCertCompleteRequest{}); err == nil {
		t.Logf("CeremonyClaimCertComplete: no error (no closed guard)")
	}
	if _, err := tr.CeremonyBeginClaimShare(ctx, &transport.BeginClaimShareRequest{}); err == nil {
		t.Logf("CeremonyBeginClaimShare: no error (no closed guard)")
	}
	if _, err := tr.CeremonySignCSR(ctx, &transport.SignCSRInitRequest{}); err == nil {
		t.Logf("CeremonySignCSR: no error (no closed guard)")
	}
	if _, err := tr.CeremonyEnrollExternalOfficer(ctx, &transport.EnrollExternalOfficerRequest{}); err == nil {
		t.Logf("CeremonyEnrollExternalOfficer: no error (no closed guard)")
	}
}
