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

package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ===========================================================================
// Vector success paths
// ===========================================================================

func TestRESTVector_AllMethods_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)

	// Wildcard catch-all returning valid JSON for any vector endpoint.
	rs.handle(func(r *http.Request) bool { return true }, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{"success": true, "namespace": "ns", "id": 1, "results": []interface{}{}})
	})

	ctx := context.Background()

	if _, err := rt.CreateNamespace(ctx, &transport.CreateNamespaceRequest{Namespace: "ns"}); err != nil {
		t.Fatalf("CreateNamespace: %v", err)
	}
	if _, err := rt.DeleteNamespace(ctx, &transport.DeleteNamespaceRequest{Namespace: "ns"}); err != nil {
		t.Fatalf("DeleteNamespace: %v", err)
	}
	if _, err := rt.ListNamespaces(ctx, &transport.ListNamespacesRequest{ShardID: 1}); err != nil {
		t.Fatalf("ListNamespaces: %v", err)
	}
	if _, err := rt.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{Namespace: "ns", ShardID: 1}); err != nil {
		t.Fatalf("GetNamespaceInfo: %v", err)
	}
	if _, err := rt.UpsertVector(ctx, &transport.UpsertVectorRequest{Namespace: "ns", ID: 1, Vector: []float32{0.1}}); err != nil {
		t.Fatalf("UpsertVector: %v", err)
	}
	if _, err := rt.GetVector(ctx, &transport.GetVectorRequest{Namespace: "ns", ID: 1, IncludeVector: true, IncludeMetadata: true, ShardID: 1, Consistency: 1}); err != nil {
		t.Fatalf("GetVector: %v", err)
	}
	if _, err := rt.DeleteVector(ctx, &transport.DeleteVectorRequest{Namespace: "ns", ID: 1}); err != nil {
		t.Fatalf("DeleteVector: %v", err)
	}
	if _, err := rt.BatchUpsert(ctx, &transport.BatchUpsertRequest{Namespace: "ns"}); err != nil {
		t.Fatalf("BatchUpsert: %v", err)
	}
	if _, err := rt.BatchDeleteVectors(ctx, &transport.BatchDeleteVectorsRequest{Namespace: "ns", IDs: []uint64{1}}); err != nil {
		t.Fatalf("BatchDeleteVectors: %v", err)
	}
	if _, err := rt.ListVectors(ctx, &transport.ListVectorsRequest{Namespace: "ns", Limit: 10, Cursor: "c", IncludeVectors: true, IncludeMetadata: true, ShardID: 1, Consistency: 1}); err != nil {
		t.Fatalf("ListVectors: %v", err)
	}
	if _, err := rt.UpdateMetadata(ctx, &transport.UpdateVectorMetadataRequest{Namespace: "ns", ID: 1}); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if _, err := rt.SearchSimilar(ctx, &transport.SearchSimilarRequest{Namespace: "ns", K: 5}); err != nil {
		t.Fatalf("SearchSimilar: %v", err)
	}
	if _, err := rt.SearchByMetadata(ctx, &transport.SearchByMetadataRequest{Namespace: "ns"}); err != nil {
		t.Fatalf("SearchByMetadata: %v", err)
	}
	if _, err := rt.HybridSearch(ctx, &transport.HybridSearchRequest{Namespace: "ns"}); err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	if _, err := rt.SearchByID(ctx, &transport.SearchByIDRequest{Namespace: "ns", ID: 1, K: 5}); err != nil {
		t.Fatalf("SearchByID: %v", err)
	}
	if _, err := rt.RebuildIndex(ctx, &transport.RebuildIndexRequest{Namespace: "ns"}); err != nil {
		t.Fatalf("RebuildIndex: %v", err)
	}
	if _, err := rt.GetRebuildProgress(ctx, &transport.GetRebuildProgressRequest{Namespace: "ns", JobID: "j1"}); err != nil {
		t.Fatalf("GetRebuildProgress: %v", err)
	}
	if _, err := rt.GetIndexStats(ctx, &transport.GetIndexStatsRequest{Namespace: "ns", ShardID: 1}); err != nil {
		t.Fatalf("GetIndexStats: %v", err)
	}
	if _, err := rt.CompactNamespace(ctx, &transport.CompactNamespaceRequest{Namespace: "ns"}); err != nil {
		t.Fatalf("CompactNamespace: %v", err)
	}
	if _, err := rt.GetCompactionProgress(ctx, &transport.GetCompactionProgressRequest{Namespace: "ns", OperationID: "op1"}); err != nil {
		t.Fatalf("GetCompactionProgress: %v", err)
	}
}

// ===========================================================================
// MVCC success paths
// ===========================================================================

func TestRESTMVCC_PutWithLease_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodPut, "/kv/"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{"kv": map[string]interface{}{"key": "aw==", "value": "dg==", "mod_revision": 1}})
	})
	if _, err := rt.PutWithLease(context.Background(), "k", []byte("v"), 1); err != nil {
		t.Fatalf("PutWithLease: %v", err)
	}
}

func TestRESTMVCC_GetRevision_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/kv/"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{"kv": map[string]interface{}{"key": "aw==", "value": "dg==", "mod_revision": 1}})
	})
	if _, err := rt.GetRevision(context.Background(), "k"); err != nil {
		t.Fatalf("GetRevision: %v", err)
	}
}

func TestRESTMVCC_GetAtRevision_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodGet, "/kv/"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{"kv": map[string]interface{}{"key": "aw==", "value": "dg=="}})
	})
	if _, err := rt.GetAtRevision(context.Background(), "k", 1); err != nil {
		t.Fatalf("GetAtRevision: %v", err)
	}
}

func TestRESTMVCC_Range_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/kv/range"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{"kvs": []interface{}{map[string]interface{}{"key": "YQ==", "value": "dg=="}}, "count": 1})
	})
	if _, err := rt.Range(context.Background(), []byte("a"), []byte("z"), transport.RangeOptions{Limit: 10}); err != nil {
		t.Fatalf("Range: %v", err)
	}
}

func TestRESTMVCC_Compact_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/kv/compact"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if err := rt.Compact(context.Background(), 1); err != nil {
		t.Fatalf("Compact: %v", err)
	}
}

func TestRESTMVCC_CurrentRevision_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/kv/revision"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]int64{"revision": 42})
	})
	rev, err := rt.CurrentRevision(context.Background())
	if err != nil {
		t.Fatalf("CurrentRevision: %v", err)
	}
	if rev != 42 {
		t.Fatalf("rev = %d", rev)
	}
}

func TestRESTMVCC_RestoreToRevision_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/kv/restore"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{
			"revision": 5,
			"changes": []interface{}{
				map[string]interface{}{"key": "YQ==", "action": "revert", "from_revision": 9, "to_revision": 5, "value_bytes": 3},
			},
			"applied": 1,
		})
	})
	result, err := rt.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{
		Revision: 5,
		Prefix:   []byte("prefix"),
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if result.Revision != 5 || result.Applied != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(result.Changes) != 1 || string(result.Changes[0].Key) != "a" {
		t.Fatalf("unexpected changes: %+v", result.Changes)
	}
}

// TestRESTMVCC_RestoreToRevision_BinaryRoundTrip proves an arbitrary
// byte-valued prefix, key, and response key round-trip byte-for-byte
// through the base64 (RFC 4648) JSON encoding the REST wire uses for []byte
// fields.
func TestRESTMVCC_RestoreToRevision_BinaryRoundTrip(t *testing.T) {
	binaryPrefix := []byte{0x00, 0xff, 0xfe, 'p'}
	binaryRespKey := []byte{0x00, 0xff, 0xfe, 'k'}

	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/kv/restore"), func(w http.ResponseWriter, r *http.Request) {
		var req mvccRestoreWireRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !bytes.Equal(req.Prefix, binaryPrefix) {
			t.Fatalf("server received prefix %v, want %v (binary corruption)", req.Prefix, binaryPrefix)
		}
		writeJSON(w, mvccRestoreWireResponse{
			Revision: 5,
			Changes: []*mvccRestoreKeyChangeWireResponse{
				{Key: binaryRespKey, Action: transport.RestoreActionRevert, ToRevision: 5},
			},
			Applied: 1,
		})
	})

	result, err := rt.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{
		Revision: 5,
		Prefix:   binaryPrefix,
	})
	if err != nil {
		t.Fatalf("RestoreToRevision: %v", err)
	}
	if len(result.Changes) != 1 || !bytes.Equal(result.Changes[0].Key, binaryRespKey) {
		t.Fatalf("expected round-tripped binary response key %v, got %+v", binaryRespKey, result.Changes)
	}
}

func TestRESTMVCC_RestoreToRevision_NilRequest(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	if _, err := rt.RestoreToRevision(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil request")
	}
}

// TestRESTMVCC_RestoreToRevision_GuardErrorStatus proves the client surfaces
// the server's HTTP status through transport.RequestError.StatusCode.
func TestRESTMVCC_RestoreToRevision_GuardErrorStatus(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/kv/restore"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		writeJSON(w, map[string]string{"error": "requires raft.sm_type: mvcc"})
	})

	_, err := rt.RestoreToRevision(context.Background(), &transport.RestoreRevisionRequest{
		Revision: 5, Prefix: []byte("k"),
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) {
		t.Fatalf("expected *transport.RequestError, got %T: %v", err, err)
	}
	if reqErr.StatusCode != http.StatusNotImplemented {
		t.Fatalf("StatusCode = %d, want %d", reqErr.StatusCode, http.StatusNotImplemented)
	}
}

// ===========================================================================
// Transaction success path
// ===========================================================================

func TestRESTTransaction_Execute_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodPost, "/tx"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{"succeeded": true, "revision": 1, "responses": []interface{}{}})
	})
	resp, err := rt.ExecuteTransaction(context.Background(), &transport.TransactionRequest{
		Compares: []transport.Compare{
			{Key: []byte("k1"), Target: transport.CompareValue, Result: transport.CompareEqual, Value: []byte("v")},
			{Key: []byte("k2"), Target: transport.CompareVersion, Result: transport.CompareGreater, Version: 1},
			{Key: []byte("k3"), Target: transport.CompareCreateRevision, Result: transport.CompareLess, CreateRevision: 5},
			{Key: []byte("k4"), Target: transport.CompareModRevision, Result: transport.CompareNotEqual, ModRevision: 7},
		},
		SuccessOps: []transport.Op{
			{Type: transport.OpPut, Key: []byte("k"), Value: []byte("v"), LeaseID: 1},
			{Type: transport.OpGet, Key: []byte("k")},
			{Type: transport.OpDelete, Key: []byte("k")},
			{Type: transport.OpRange, Key: []byte("a"), RangeEnd: []byte("z")},
		},
		FailureOps: []transport.Op{
			{Type: transport.OpGet, Key: []byte("x")},
		},
	})
	if err != nil {
		t.Fatalf("ExecuteTransaction: %v", err)
	}
	if !resp.Succeeded {
		t.Fatal("expected success")
	}
}

// ===========================================================================
// Tenant success paths
// ===========================================================================

func TestRESTTenant_AllMethods_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(func(r *http.Request) bool { return true }, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{
			"tenant":  map[string]interface{}{"id": "t1", "name": "test"},
			"tenants": []interface{}{map[string]interface{}{"id": "t1", "name": "test"}},
			"total":   1,
		})
	})
	ctx := context.Background()
	if _, err := rt.CreateTenant(ctx, &transport.CreateTenantRequest{Name: "test"}); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if _, err := rt.GetTenant(ctx, "t1"); err != nil {
		t.Fatalf("GetTenant: %v", err)
	}
	if _, err := rt.ListTenants(ctx); err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if _, err := rt.UpdateTenant(ctx, &transport.UpdateTenantRequest{TenantID: "t1"}); err != nil {
		t.Fatalf("UpdateTenant: %v", err)
	}
	if _, err := rt.UpdateTenantQuotas(ctx, &transport.UpdateTenantQuotasRequest{TenantID: "t1"}); err != nil {
		t.Fatalf("UpdateTenantQuotas: %v", err)
	}
}

func TestRESTTenant_DeleteTenant_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(pathPrefix(http.MethodDelete, "/v1/tenants/"), func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	if err := rt.DeleteTenant(context.Background(), "t1"); err != nil {
		t.Fatalf("DeleteTenant: %v", err)
	}
}

// TestRESTTenant_UpdateTenantEntitlements_NilRequest refuses a nil request
// with a typed error before any HTTP round trip.
func TestRESTTenant_UpdateTenantEntitlements_NilRequest(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	reached := false
	rs.handle(pathPrefix(http.MethodPut, "/v1/tenants/"), func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})

	_, err := rt.UpdateTenantEntitlements(context.Background(), nil)
	var nre *transport.NilRequestError
	var re *transport.RequestError
	if !errors.As(err, &re) || re.Method != "UpdateTenantEntitlements" || !errors.As(err, &nre) {
		t.Fatalf("UpdateTenantEntitlements(nil) = %T %v, want RequestError wrapping NilRequestError", err, err)
	}
	if reached {
		t.Fatal("a nil request reached the server")
	}
}

// ===========================================================================
// UserAuthFactor success paths
// ===========================================================================

func TestRESTUserAuthFactor_AllMethods_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(func(r *http.Request) bool { return true }, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{"factor_id": "f1", "factors": []interface{}{}, "ok": true})
	})
	ctx := context.Background()
	if _, err := rt.AddAuthFactor(ctx, &transport.AuthFactorAddRequest{UserID: 1, Type: "mtls", Credential: []byte("c")}); err != nil {
		t.Fatalf("AddAuthFactor: %v", err)
	}
	if _, err := rt.ListAuthFactors(ctx, &transport.AuthFactorListRequest{UserID: 1}); err != nil {
		t.Fatalf("ListAuthFactors: %v", err)
	}
	if _, err := rt.GetAuthFactor(ctx, &transport.AuthFactorGetRequest{FactorID: "f1"}); err != nil {
		t.Fatalf("GetAuthFactor: %v", err)
	}
	if _, err := rt.RemoveAuthFactor(ctx, &transport.AuthFactorRemoveRequest{FactorID: "f1"}); err != nil {
		t.Fatalf("RemoveAuthFactor: %v", err)
	}
	if _, err := rt.RotateAuthFactor(ctx, &transport.AuthFactorRotateRequest{OldFactorID: "f1"}); err != nil {
		t.Fatalf("RotateAuthFactor: %v", err)
	}
	if _, err := rt.RelabelAuthFactor(ctx, &transport.AuthFactorRelabelRequest{FactorID: "f1", Label: "L"}); err != nil {
		t.Fatalf("RelabelAuthFactor: %v", err)
	}
	if _, err := rt.LoginAuthFactor(ctx, &transport.AuthFactorLoginRequest{Type: "mtls", LookupKey: []byte("k")}); err != nil {
		t.Fatalf("LoginAuthFactor: %v", err)
	}
}

// ===========================================================================
// Watch (REST) - basic Watch/WatchPrefix nil tests, since SSE is hard to mock
// ===========================================================================

func TestRESTWatch_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.Watch(context.Background(), "k", transport.WatchOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRESTWatchPrefix_Closed(t *testing.T) {
	rt, _, _ := newExtTransport(t)
	_ = rt.Close()
	_, err := rt.WatchPrefix(context.Background(), "p", transport.WatchOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
}
