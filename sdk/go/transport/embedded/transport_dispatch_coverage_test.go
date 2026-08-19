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

package embedded

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// Vector methods — nil service + closed paths
//
// The full VectorServicer interface is very large and the embedded transport
// merely delegates to it. To raise coverage we exercise the guard clauses
// (closed + nil service) for every method.
// ===========================================================================

func TestEmbeddedVector_AllMethods_NilService(t *testing.T) {
	tr := newExtTransport(t)
	ctx := context.Background()
	var ne *NilServiceError

	cases := []struct {
		name string
		call func() error
	}{
		{"CreateNamespace", func() error { _, e := tr.CreateNamespace(ctx, &transport.CreateNamespaceRequest{}); return e }},
		{"DeleteNamespace", func() error { _, e := tr.DeleteNamespace(ctx, &transport.DeleteNamespaceRequest{}); return e }},
		{"ListNamespaces", func() error { _, e := tr.ListNamespaces(ctx, &transport.ListNamespacesRequest{}); return e }},
		{"GetNamespaceInfo", func() error { _, e := tr.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{}); return e }},
		{"UpsertVector", func() error { _, e := tr.UpsertVector(ctx, &transport.UpsertVectorRequest{}); return e }},
		{"GetVector", func() error { _, e := tr.GetVector(ctx, &transport.GetVectorRequest{}); return e }},
		{"DeleteVector", func() error { _, e := tr.DeleteVector(ctx, &transport.DeleteVectorRequest{}); return e }},
		{"BatchUpsert", func() error { _, e := tr.BatchUpsert(ctx, &transport.BatchUpsertRequest{}); return e }},
		{"BatchDeleteVectors", func() error {
			_, e := tr.BatchDeleteVectors(ctx, &transport.BatchDeleteVectorsRequest{})
			return e
		}},
		{"ListVectors", func() error { _, e := tr.ListVectors(ctx, &transport.ListVectorsRequest{}); return e }},
		{"UpdateMetadata", func() error {
			_, e := tr.UpdateMetadata(ctx, &transport.UpdateVectorMetadataRequest{})
			return e
		}},
		{"SearchSimilar", func() error { _, e := tr.SearchSimilar(ctx, &transport.SearchSimilarRequest{}); return e }},
		{"SearchByMetadata", func() error { _, e := tr.SearchByMetadata(ctx, &transport.SearchByMetadataRequest{}); return e }},
		{"HybridSearch", func() error { _, e := tr.HybridSearch(ctx, &transport.HybridSearchRequest{}); return e }},
		{"SearchByID", func() error { _, e := tr.SearchByID(ctx, &transport.SearchByIDRequest{}); return e }},
		{"RebuildIndex", func() error { _, e := tr.RebuildIndex(ctx, &transport.RebuildIndexRequest{}); return e }},
		{"GetRebuildProgress", func() error {
			_, e := tr.GetRebuildProgress(ctx, &transport.GetRebuildProgressRequest{})
			return e
		}},
		{"GetIndexStats", func() error { _, e := tr.GetIndexStats(ctx, &transport.GetIndexStatsRequest{}); return e }},
		{"CompactNamespace", func() error {
			_, e := tr.CompactNamespace(ctx, &transport.CompactNamespaceRequest{})
			return e
		}},
		{"GetCompactionProgress", func() error {
			_, e := tr.GetCompactionProgress(ctx, &transport.GetCompactionProgressRequest{})
			return e
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.call()
			if !errors.As(err, &ne) {
				t.Fatalf("expected NilServiceError, got %T: %v", err, err)
			}
		})
	}
}

func TestEmbeddedVector_AllMethods_Closed(t *testing.T) {
	tr := closedExtTransport(t)
	ctx := context.Background()
	var ce *transport.ClosedError

	cases := []struct {
		name string
		call func() error
	}{
		{"CreateNamespace", func() error { _, e := tr.CreateNamespace(ctx, &transport.CreateNamespaceRequest{}); return e }},
		{"DeleteNamespace", func() error { _, e := tr.DeleteNamespace(ctx, &transport.DeleteNamespaceRequest{}); return e }},
		{"ListNamespaces", func() error { _, e := tr.ListNamespaces(ctx, &transport.ListNamespacesRequest{}); return e }},
		{"GetNamespaceInfo", func() error { _, e := tr.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{}); return e }},
		{"UpsertVector", func() error { _, e := tr.UpsertVector(ctx, &transport.UpsertVectorRequest{}); return e }},
		{"GetVector", func() error { _, e := tr.GetVector(ctx, &transport.GetVectorRequest{}); return e }},
		{"DeleteVector", func() error { _, e := tr.DeleteVector(ctx, &transport.DeleteVectorRequest{}); return e }},
		{"BatchUpsert", func() error { _, e := tr.BatchUpsert(ctx, &transport.BatchUpsertRequest{}); return e }},
		{"BatchDeleteVectors", func() error {
			_, e := tr.BatchDeleteVectors(ctx, &transport.BatchDeleteVectorsRequest{})
			return e
		}},
		{"ListVectors", func() error { _, e := tr.ListVectors(ctx, &transport.ListVectorsRequest{}); return e }},
		{"UpdateMetadata", func() error {
			_, e := tr.UpdateMetadata(ctx, &transport.UpdateVectorMetadataRequest{})
			return e
		}},
		{"SearchSimilar", func() error { _, e := tr.SearchSimilar(ctx, &transport.SearchSimilarRequest{}); return e }},
		{"SearchByMetadata", func() error { _, e := tr.SearchByMetadata(ctx, &transport.SearchByMetadataRequest{}); return e }},
		{"HybridSearch", func() error { _, e := tr.HybridSearch(ctx, &transport.HybridSearchRequest{}); return e }},
		{"SearchByID", func() error { _, e := tr.SearchByID(ctx, &transport.SearchByIDRequest{}); return e }},
		{"RebuildIndex", func() error { _, e := tr.RebuildIndex(ctx, &transport.RebuildIndexRequest{}); return e }},
		{"GetRebuildProgress", func() error {
			_, e := tr.GetRebuildProgress(ctx, &transport.GetRebuildProgressRequest{})
			return e
		}},
		{"GetIndexStats", func() error { _, e := tr.GetIndexStats(ctx, &transport.GetIndexStatsRequest{}); return e }},
		{"CompactNamespace", func() error {
			_, e := tr.CompactNamespace(ctx, &transport.CompactNamespaceRequest{})
			return e
		}},
		{"GetCompactionProgress", func() error {
			_, e := tr.GetCompactionProgress(ctx, &transport.GetCompactionProgressRequest{})
			return e
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.call()
			if !errors.As(err, &ce) {
				t.Fatalf("expected ClosedError, got %T: %v", err, err)
			}
		})
	}
}

// ===========================================================================
// Dispatch handlers — exercise each handle* via Request to lift them above 0%
// ===========================================================================

func TestEmbeddedDispatch_HandlersBadRequest(t *testing.T) {
	tr := newExtTransport(t,
		WithLeaseService(&mockLeaseService{}),
		WithTimeSeriesService(&mockTSService{}),
		WithWatchService(&mockWatchService{}),
		WithCeremonyService(&mockCeremonyService{}),
		WithCeremonyCA(&mockCeremonyCA{}),
	)

	ctx := context.Background()

	// Every dispatch handler with a typed request returns InvalidRequestError
	// when the request type does not match. We exercise every handler whose
	// dispatch path isn't covered yet.
	bad := "not-the-right-type"
	cases := []string{
		"PutWithLease", "GetRevision", "GetAtRevision", "Range", "Compact",
		"ExecuteTransaction", "ExecuteBatch", "Watch", "WatchPrefix",
		"LeaseGrant", "LeaseRevoke", "LeaseKeepAlive", "LeaseTimeToLive",
		"TSWrite", "TSWriteBatch", "TSQuery", "TSAggregate", "TSWritePoint",
		"TSRichQuery", "TSDeleteSeries", "TSGetRetention", "TSSetRetention",
		"TSListMetrics",
		"CreateNamespace", "DeleteNamespace", "ListNamespaces", "GetNamespaceInfo",
		"UpsertVector", "GetVector", "DeleteVector", "BatchUpsert", "BatchDeleteVectors",
		"ListVectors", "UpdateMetadata", "SearchSimilar", "SearchByMetadata",
		"HybridSearch", "SearchByID", "RebuildIndex", "GetRebuildProgress",
		"GetIndexStats", "CompactNamespace", "GetCompactionProgress",
	}
	for _, m := range cases {
		t.Run(m, func(t *testing.T) {
			err := tr.Request(ctx, m, bad, nil)
			var ie *InvalidRequestError
			if !errors.As(err, &ie) {
				t.Fatalf("expected InvalidRequestError, got %T: %v", err, err)
			}
		})
	}
}

func TestEmbeddedDispatch_TSHandlers_Success(t *testing.T) {
	tr := newExtTransport(t, WithTimeSeriesService(&mockTSService{}))
	ctx := context.Background()

	t.Run("TSWrite", func(t *testing.T) {
		if err := tr.Request(ctx, "TSWrite", &transport.TSDataPoint{Metric: "m", TimestampNs: 1}, nil); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("TSWriteBatch", func(t *testing.T) {
		if err := tr.Request(ctx, "TSWriteBatch", &tsWriteBatchRequest{Points: []transport.TSDataPoint{{Metric: "m", TimestampNs: 1}}}, nil); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("TSQuery", func(t *testing.T) {
		var resp *transport.TSQueryResponse
		if err := tr.Request(ctx, "TSQuery", &transport.TSQueryRequest{Metric: "m"}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("TSAggregate", func(t *testing.T) {
		var resp *transport.TSAggregateResponse
		if err := tr.Request(ctx, "TSAggregate", &transport.TSAggregateRequest{Metric: "m"}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("TSWritePoint", func(t *testing.T) {
		if err := tr.Request(ctx, "TSWritePoint", &transport.TSDataPoint{Metric: "m", TimestampNs: 1}, nil); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("TSRichQuery", func(t *testing.T) {
		var resp *transport.TSRichQueryResponse
		if err := tr.Request(ctx, "TSRichQuery", &transport.TSRichQueryRequest{Metric: "m"}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("TSDeleteSeries", func(t *testing.T) {
		var resp *transport.TSDeleteResponse
		if err := tr.Request(ctx, "TSDeleteSeries", &transport.TSDeleteRequest{Metric: "m"}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("TSGetRetention", func(t *testing.T) {
		var resp *transport.TSGetRetentionResponse
		if err := tr.Request(ctx, "TSGetRetention", &transport.TSGetRetentionRequest{Metric: "m"}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("TSSetRetention", func(t *testing.T) {
		if err := tr.Request(ctx, "TSSetRetention", &transport.TSSetRetentionRequest{Metric: "m"}, nil); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
	t.Run("TSListMetrics", func(t *testing.T) {
		var resp *transport.TSListMetricsResponse
		if err := tr.Request(ctx, "TSListMetrics", &transport.TSListMetricsRequest{}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	})
}

func TestEmbeddedDispatch_WatchHandlers_Success(t *testing.T) {
	tr := newExtTransport(t, WithWatchService(&mockWatchService{}))
	ctx := context.Background()

	t.Run("Watch", func(t *testing.T) {
		var resp <-chan transport.WatchResponse
		if err := tr.Request(ctx, "Watch", &watchRequest{Key: "k"}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		for range resp {
		}
	})
	t.Run("WatchPrefix", func(t *testing.T) {
		var resp <-chan transport.WatchResponse
		if err := tr.Request(ctx, "WatchPrefix", &watchPrefixRequest{Prefix: "p"}, &resp); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		for range resp {
		}
	})
}

// ===========================================================================
// Server KV adapter — direct calls (the server.go adapter methods previously
// at 0% coverage).
// ===========================================================================

func TestEmbeddedServerKVAdapter_BasicCRUD(t *testing.T) {
	svc := newMockKVService()
	a := newServerKVAdapter(svc)
	ctx := context.Background()

	// Put + Get
	if err := a.Put(ctx, "k", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := a.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "v" {
		t.Fatalf("Get got %q", got)
	}

	// Get missing → nil, nil (per KVStore contract)
	got, err = a.Get(ctx, "missing")
	if err != nil {
		t.Fatalf("Get missing: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for missing key, got %q", got)
	}

	// Exists for existing key
	exists, err := a.Exists(ctx, "k")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Fatal("expected key to exist")
	}

	// Exists for missing key — underlying mock returns KeyNotFoundError so
	// adapter Exists returns false with that error.
	exists, err = a.Exists(ctx, "missing")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
	if exists {
		t.Fatal("expected exists=false for missing key")
	}

	// List
	keys, err := a.List(ctx, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 || keys[0] != "k" {
		t.Fatalf("List got %v", keys)
	}

	// Scan
	scanCount := 0
	if err := a.Scan(ctx, "", func(_ string, _ []byte) error {
		scanCount++
		return nil
	}); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if scanCount != 1 {
		t.Fatalf("Scan count = %d", scanCount)
	}

	// Scan with error from fn
	scanErr := errors.New("scan boom")
	if err := a.Scan(ctx, "", func(_ string, _ []byte) error {
		return scanErr
	}); !errors.Is(err, scanErr) {
		t.Fatalf("Scan should propagate fn error, got: %v", err)
	}

	// Delete
	if err := a.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

// TestEmbeddedServerKVAdapter_IndexesRefuse pins the typed refusal.
//
// This test previously asserted that every index method returned a nil error
// (and, for the queries, a nil result). It passed, and that is exactly how the
// defect survived: a green test declared "answers every index query with an
// empty result and no error" to be the intended contract, when that answer is
// indistinguishable from a maintained index with no matches. The adapter wraps
// a bare KVServicer with no index subsystem behind it, so the honest answer is
// kvstore.ErrIndexUnsupported — which lets pkg/dao plan a full scan instead of
// concluding the entity does not exist.
func TestEmbeddedServerKVAdapter_IndexesRefuse(t *testing.T) {
	svc := newMockKVService()
	a := newServerKVAdapter(svc)
	ctx := context.Background()

	if err := a.RegisterEntityIndexes("user", nil); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Fatalf("RegisterEntityIndexes = %v; want kvstore.ErrIndexUnsupported", err)
	}

	keys, err := a.QueryIndex(ctx, "x", []byte("v"))
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("QueryIndex error = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if keys != nil {
		t.Errorf("QueryIndex returned %v alongside a refusal", keys)
	}

	entries, err := a.ScanIndex(ctx, "x", []byte("a"), []byte("z"))
	if !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("ScanIndex error = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if entries != nil {
		t.Errorf("ScanIndex returned %v alongside a refusal", entries)
	}

	if _, err := a.QueryCompoundIndex(ctx, "x", [][]byte{[]byte("v")}); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("QueryCompoundIndex = %v; want kvstore.ErrIndexUnsupported", err)
	}
	if _, err := a.ScanCompoundIndex(ctx, "x", [][]byte{[]byte("p")}, []byte("a"), []byte("z")); !errors.Is(err, kvstore.ErrIndexUnsupported) {
		t.Errorf("ScanCompoundIndex = %v; want kvstore.ErrIndexUnsupported", err)
	}
}

// ===========================================================================
// SignalingError (errors.go) coverage
// ===========================================================================

func TestEmbeddedErrors_OperationError(t *testing.T) {
	// Find another zero-coverage error type. errors.go:64 corresponds to one
	// of the Error() methods. Exercise InvalidRequestError + NilServiceError
	// + UnknownMethodError.
	t.Run("InvalidRequest", func(t *testing.T) {
		e := &InvalidRequestError{Method: "Foo"}
		if !strings.Contains(e.Error(), "Foo") {
			t.Fatalf("Error() = %q", e.Error())
		}
	})
	t.Run("UnknownMethod", func(t *testing.T) {
		e := &UnknownMethodError{Method: "Foo"}
		if !strings.Contains(e.Error(), "Foo") {
			t.Fatalf("Error() = %q", e.Error())
		}
	})
}

func TestEmbeddedDispatch_UnknownMethod(t *testing.T) {
	tr := newExtTransport(t)
	err := tr.Request(context.Background(), "TotallyMadeUpMethod", nil, nil)
	var ue *UnknownMethodError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnknownMethodError, got %T: %v", err, err)
	}
}

// ===========================================================================
// MVCC/Transaction/Batch nil + closed paths
// ===========================================================================

func TestEmbeddedMVCC_AllMethods_NilService(t *testing.T) {
	tr := newExtTransport(t)
	ctx := context.Background()
	var ne *NilServiceError

	t.Run("PutWithLease", func(t *testing.T) {
		_, err := tr.PutWithLease(ctx, "k", []byte("v"), 1)
		if !errors.As(err, &ne) {
			t.Fatalf("expected NilServiceError, got %T", err)
		}
	})
	t.Run("GetRevision", func(t *testing.T) {
		_, err := tr.GetRevision(ctx, "k")
		if !errors.As(err, &ne) {
			t.Fatalf("expected NilServiceError, got %T", err)
		}
	})
	t.Run("GetAtRevision", func(t *testing.T) {
		_, err := tr.GetAtRevision(ctx, "k", 1)
		if !errors.As(err, &ne) {
			t.Fatalf("expected NilServiceError, got %T", err)
		}
	})
	t.Run("Range", func(t *testing.T) {
		_, err := tr.Range(ctx, []byte("a"), []byte("z"), transport.RangeOptions{})
		if !errors.As(err, &ne) {
			t.Fatalf("expected NilServiceError, got %T", err)
		}
	})
	t.Run("Compact", func(t *testing.T) {
		err := tr.Compact(ctx, 1)
		if !errors.As(err, &ne) {
			t.Fatalf("expected NilServiceError, got %T", err)
		}
	})
	t.Run("CurrentRevision", func(t *testing.T) {
		_, err := tr.CurrentRevision(ctx)
		if !errors.As(err, &ne) {
			t.Fatalf("expected NilServiceError, got %T", err)
		}
	})
}

func TestEmbeddedMVCC_AllMethods_Closed(t *testing.T) {
	tr := closedExtTransport(t)
	ctx := context.Background()
	var ce *transport.ClosedError

	t.Run("PutWithLease", func(t *testing.T) {
		_, err := tr.PutWithLease(ctx, "k", []byte("v"), 1)
		if !errors.As(err, &ce) {
			t.Fatalf("expected ClosedError, got %T", err)
		}
	})
	t.Run("GetRevision", func(t *testing.T) {
		_, err := tr.GetRevision(ctx, "k")
		if !errors.As(err, &ce) {
			t.Fatalf("expected ClosedError, got %T", err)
		}
	})
	t.Run("GetAtRevision", func(t *testing.T) {
		_, err := tr.GetAtRevision(ctx, "k", 1)
		if !errors.As(err, &ce) {
			t.Fatalf("expected ClosedError, got %T", err)
		}
	})
	t.Run("Range", func(t *testing.T) {
		_, err := tr.Range(ctx, []byte("a"), []byte("z"), transport.RangeOptions{})
		if !errors.As(err, &ce) {
			t.Fatalf("expected ClosedError, got %T", err)
		}
	})
	t.Run("Compact", func(t *testing.T) {
		err := tr.Compact(ctx, 1)
		if !errors.As(err, &ce) {
			t.Fatalf("expected ClosedError, got %T", err)
		}
	})
	t.Run("CurrentRevision", func(t *testing.T) {
		_, err := tr.CurrentRevision(ctx)
		if !errors.As(err, &ce) {
			t.Fatalf("expected ClosedError, got %T", err)
		}
	})
}

func TestEmbeddedTransaction_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.ExecuteTransaction(context.Background(), &transport.TransactionRequest{})
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedTransaction_Closed(t *testing.T) {
	tr := closedExtTransport(t)
	_, err := tr.ExecuteTransaction(context.Background(), &transport.TransactionRequest{})
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

func TestEmbeddedBatch_NilService(t *testing.T) {
	tr := newExtTransport(t)
	_, err := tr.ExecuteBatch(context.Background(), nil)
	var ne *NilServiceError
	if !errors.As(err, &ne) {
		t.Fatalf("expected NilServiceError, got %T", err)
	}
}

func TestEmbeddedBatch_Closed(t *testing.T) {
	tr := closedExtTransport(t)
	_, err := tr.ExecuteBatch(context.Background(), nil)
	var ce *transport.ClosedError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ClosedError, got %T", err)
	}
}

// ===========================================================================
// WatchResponse conversion (toTransportWatchResponse coverage)
// ===========================================================================

func TestEmbeddedWatch_WatchResponse_Delivery(t *testing.T) {
	w := newStubWatcher()
	// Deliver an event with both put and delete types to exercise the
	// toTransportWatchResponse conversion path.
	w.ch <- watch.WatchResponse{
		WatchID:  1,
		Revision: 5,
		Events: []*watch.Event{
			{Type: watch.EventPut, Kv: &kv.KeyValue{Key: []byte("k"), Value: []byte("v"), ModRevision: 5}},
			{Type: watch.EventDelete, Kv: &kv.KeyValue{Key: []byte("d")}},
		},
	}
	w.ch <- watch.WatchResponse{
		Canceled:     true,
		CancelReason: "test cancel",
	}
	close(w.ch)

	tr := newExtTransport(t, WithWatchService(&mockWatchService{
		watchFn: func(_ context.Context, _ string, _ ...watch.WatchOption) (watch.Watcher, error) {
			return w, nil
		},
	}))
	ch, err := tr.Watch(context.Background(), "k", transport.WatchOptions{})
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	count := 0
	for resp := range ch {
		count++
		_ = resp
	}
	if count != 2 {
		t.Fatalf("got %d responses, want 2", count)
	}
}
