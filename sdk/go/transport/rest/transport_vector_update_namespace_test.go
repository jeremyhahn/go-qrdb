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
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestRESTUpdateNamespace_Success(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	var got transport.UpdateNamespaceRequest
	rs.handle(func(r *http.Request) bool { return r.URL.Path == "/vector/namespace/update" }, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]interface{}{"namespace": "ns", "requires_rebuild": true, "timestamp": 9, "success": true})
	})

	resp, err := rt.UpdateNamespace(context.Background(), &transport.UpdateNamespaceRequest{
		Namespace:   "ns",
		IndexConfig: map[string]string{"ef_search": "128"},
		Metadata:    map[string]interface{}{"owner": "search"},
	})
	if err != nil {
		t.Fatalf("UpdateNamespace: %v", err)
	}
	if !resp.Success || !resp.RequiresRebuild || resp.Timestamp != 9 {
		t.Fatalf("resp = %+v", resp)
	}
	if got.Namespace != "ns" || got.IndexConfig["ef_search"] != "128" || got.Metadata["owner"] != "search" {
		t.Fatalf("server saw %+v", got)
	}
}

// TestRESTVector_SendsTheDocumentedRequest pins, per vector RPC, the verb,
// path, query and body the transport puts on the wire, and that the answer
// decodes into the RPC's response type. A POST RPC sends its whole request
// as the JSON body; a GET RPC sends no body and every set option as a query
// parameter.
func TestRESTVector_SendsTheDocumentedRequest(t *testing.T) {
	// answer carries one distinctive value under every key a vector response
	// type decodes, so each row can prove its own field arrived.
	const answer = `{"namespace":"ns-7","success":true,"id":7,"ids":[7],"deleted_count":7,` +
		`"total_count":7,"source_id":7,"job_id":"j7","operation_id":"op7","distance_metric":"L2"}`
	type row struct {
		name, verb, path, query string
		req                     any
		call                    func(context.Context, *Transport) (any, error)
		want                    string
	}
	rows := []row{
		{"CreateNamespace", http.MethodPost, "/vector/namespace", "", &transport.CreateNamespaceRequest{Namespace: "ns", Dimension: 3},
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.CreateNamespace(ctx, &transport.CreateNamespaceRequest{Namespace: "ns", Dimension: 3})
				return field(err, func() any { return r.Namespace })
			}, "ns-7"},
		{"DeleteNamespace", http.MethodPost, "/vector/namespace/delete", "", &transport.DeleteNamespaceRequest{Namespace: "ns"},
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.DeleteNamespace(ctx, &transport.DeleteNamespaceRequest{Namespace: "ns"})
				return field(err, func() any { return r.Success })
			}, "true"},
		{"ListNamespaces", http.MethodGet, "/vector/namespaces", "shard_id=1", nil,
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.ListNamespaces(ctx, &transport.ListNamespacesRequest{ShardID: 1})
				return field(err, func() any { return r.TotalCount })
			}, "7"},
		{"GetNamespaceInfo", http.MethodGet, "/vector/namespace/info", "namespace=ns&shard_id=1", nil,
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{Namespace: "ns", ShardID: 1})
				return field(err, func() any { return r.Namespace + "/" + r.DistanceMetric })
			}, "ns-7/" + string(transport.DistanceEuclidean)},
		{"UpsertVector", http.MethodPost, "/vector/upsert", "", &transport.UpsertVectorRequest{Namespace: "ns", ID: 1, Vector: []float32{0.5}},
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.UpsertVector(ctx, &transport.UpsertVectorRequest{Namespace: "ns", ID: 1, Vector: []float32{0.5}})
				return field(err, func() any { return r.ID })
			}, "7"},
		{"GetVector", http.MethodGet, "/vector/get", "consistency=1&id=1&include_metadata=true&include_vector=true&namespace=ns&shard_id=1", nil,
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.GetVector(ctx, &transport.GetVectorRequest{Namespace: "ns", ID: 1, IncludeVector: true, IncludeMetadata: true, ShardID: 1, Consistency: 1})
				return field(err, func() any { return r.ID })
			}, "7"},
		{"DeleteVector", http.MethodPost, "/vector/delete", "", &transport.DeleteVectorRequest{Namespace: "ns", ID: 1},
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.DeleteVector(ctx, &transport.DeleteVectorRequest{Namespace: "ns", ID: 1})
				return field(err, func() any { return r.Success })
			}, "true"},
		{"BatchUpsert", http.MethodPost, "/vector/batch/upsert", "", &transport.BatchUpsertRequest{Namespace: "ns"},
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.BatchUpsert(ctx, &transport.BatchUpsertRequest{Namespace: "ns"})
				return field(err, func() any { return r.IDs })
			}, "[7]"},
		{"BatchDeleteVectors", http.MethodPost, "/vector/batch/delete", "", &transport.BatchDeleteVectorsRequest{Namespace: "ns", IDs: []uint64{1}},
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.BatchDeleteVectors(ctx, &transport.BatchDeleteVectorsRequest{Namespace: "ns", IDs: []uint64{1}})
				return field(err, func() any { return r.DeletedCount })
			}, "7"},
		{"ListVectors", http.MethodGet, "/vector/list", "consistency=1&cursor=c&id_prefix=p&include_metadata=true&include_vectors=true&limit=10&namespace=ns&shard_id=1", nil,
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.ListVectors(ctx, &transport.ListVectorsRequest{Namespace: "ns", Limit: 10, Cursor: "c", IDPrefix: "p", IncludeVectors: true, IncludeMetadata: true, ShardID: 1, Consistency: 1})
				return field(err, func() any { return r.TotalCount })
			}, "7"},
		{"UpdateMetadata", http.MethodPost, "/vector/metadata", "", &transport.UpdateVectorMetadataRequest{Namespace: "ns", ID: 1},
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.UpdateMetadata(ctx, &transport.UpdateVectorMetadataRequest{Namespace: "ns", ID: 1})
				return field(err, func() any { return r.ID })
			}, "7"},
		{"SearchSimilar", http.MethodPost, "/vector/search/similar", "", &transport.SearchSimilarRequest{Namespace: "ns", K: 5},
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.SearchSimilar(ctx, &transport.SearchSimilarRequest{Namespace: "ns", K: 5})
				return field(err, func() any { return r.Results != nil })
			}, "false"},
		{"SearchByMetadata", http.MethodPost, "/vector/search/metadata", "", &transport.SearchByMetadataRequest{Namespace: "ns"},
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.SearchByMetadata(ctx, &transport.SearchByMetadataRequest{Namespace: "ns"})
				return field(err, func() any { return r.TotalCount })
			}, "7"},
		{"HybridSearch", http.MethodPost, "/vector/search/hybrid", "", &transport.HybridSearchRequest{Namespace: "ns"},
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.HybridSearch(ctx, &transport.HybridSearchRequest{Namespace: "ns"})
				return field(err, func() any { return r.Results != nil })
			}, "false"},
		{"SearchByID", http.MethodPost, "/vector/search/by-id", "", &transport.SearchByIDRequest{Namespace: "ns", ID: 1, K: 5},
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.SearchByID(ctx, &transport.SearchByIDRequest{Namespace: "ns", ID: 1, K: 5})
				return field(err, func() any { return r.SourceID })
			}, "7"},
		{"RebuildIndex", http.MethodPost, "/vector/index/rebuild", "", &transport.RebuildIndexRequest{Namespace: "ns"},
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.RebuildIndex(ctx, &transport.RebuildIndexRequest{Namespace: "ns"})
				return field(err, func() any { return r.JobID })
			}, "j7"},
		{"GetRebuildProgress", http.MethodGet, "/vector/index/rebuild/progress", "job_id=j1&namespace=ns", nil,
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.GetRebuildProgress(ctx, &transport.GetRebuildProgressRequest{Namespace: "ns", JobID: "j1"})
				return field(err, func() any { return r.JobID })
			}, "j7"},
		{"GetIndexStats", http.MethodGet, "/vector/index/stats", "namespace=ns&shard_id=1", nil,
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.GetIndexStats(ctx, &transport.GetIndexStatsRequest{Namespace: "ns", ShardID: 1})
				return field(err, func() any { return r.Namespace })
			}, "ns-7"},
		{"CompactNamespace", http.MethodPost, "/vector/compact", "", &transport.CompactNamespaceRequest{Namespace: "ns"},
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.CompactNamespace(ctx, &transport.CompactNamespaceRequest{Namespace: "ns"})
				return field(err, func() any { return r.OperationID })
			}, "op7"},
		{"GetCompactionProgress", http.MethodGet, "/vector/compact/progress", "namespace=ns&operation_id=op1&shard_id=1", nil,
			func(ctx context.Context, rt *Transport) (any, error) {
				r, err := rt.GetCompactionProgress(ctx, &transport.GetCompactionProgressRequest{Namespace: "ns", OperationID: "op1", ShardID: 1})
				return field(err, func() any { return r.OperationID })
			}, "op7"},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			rt, rs, _ := newExtTransport(t)
			var verb, path, query string
			var body []byte
			rs.handle(func(*http.Request) bool { return true }, func(w http.ResponseWriter, r *http.Request) {
				verb, path, query = r.Method, r.URL.Path, r.URL.RawQuery
				body, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, answer)
			})
			got, err := tc.call(context.Background(), rt)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if verb != tc.verb || path != tc.path || query != tc.query {
				t.Fatalf("request = %s %s?%s, want %s %s?%s", verb, path, query, tc.verb, tc.path, tc.query)
			}
			wantBody := []byte{}
			if tc.req != nil {
				if wantBody, err = json.Marshal(tc.req); err != nil {
					t.Fatalf("marshal: %v", err)
				}
			}
			if !bytes.Equal(bytes.TrimSpace(body), wantBody) {
				t.Fatalf("body = %s, want %s", body, wantBody)
			}
			if s := fmt.Sprint(got); s != tc.want {
				t.Fatalf("decoded = %s, want %s", s, tc.want)
			}
		})
	}
}

// field returns the value read from a decoded response, or the call's error.
func field(err error, read func() any) (any, error) {
	if err != nil {
		return nil, err
	}
	return read(), nil
}

// TestRESTUpdateNamespace_TypedRefusals proves the REST client rebuilds the
// vector refusals from the error body's code, so a missing namespace is not
// read as a missing key.
func TestRESTUpdateNamespace_TypedRefusals(t *testing.T) {
	cases := map[string]struct {
		status int
		body   transport.ErrorResponse
		check  func(error) bool
	}{
		"missing namespace": {
			status: http.StatusNotFound,
			body: transport.ErrorResponse{
				Error: "namespace not found", Code: transport.ReasonVectorNamespaceNotFound,
				Metadata: (&transport.NamespaceNotFoundError{Namespace: "missing"}).Metadata(),
			},
			check: func(err error) bool {
				nf, ok := errors.AsType[*transport.NamespaceNotFoundError](err)
				return ok && nf.Namespace == "missing"
			},
		},
		"dimension mismatch": {
			status: http.StatusBadRequest,
			body: transport.ErrorResponse{
				Error: "dimension mismatch", Code: transport.ReasonVectorDimensionMismatch,
				Metadata: (&transport.DimensionMismatchError{Expected: 4, Got: 8}).Metadata(),
			},
			check: func(err error) bool {
				dm, ok := errors.AsType[*transport.DimensionMismatchError](err)
				return ok && dm.Expected == 4 && dm.Got == 8
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rt, rs, _ := newExtTransport(t)
			rs.handle(func(*http.Request) bool { return true }, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(tc.body)
			})
			_, err := rt.UpdateNamespace(context.Background(), &transport.UpdateNamespaceRequest{Namespace: "missing"})
			if !tc.check(err) {
				t.Fatalf("err = %v (%T), want the typed refusal", err, err)
			}
		})
	}
}

func TestRESTVector_Closed(t *testing.T) {
	assertClosedRefusesBeforeTheWire(t, map[string]func(context.Context, *Transport) error{
		"CreateNamespace": func(ctx context.Context, rt *Transport) error {
			_, err := rt.CreateNamespace(ctx, &transport.CreateNamespaceRequest{})
			return err
		},
		"UpdateNamespace": func(ctx context.Context, rt *Transport) error {
			_, err := rt.UpdateNamespace(ctx, &transport.UpdateNamespaceRequest{Namespace: "ns"})
			return err
		},
		"DeleteNamespace": func(ctx context.Context, rt *Transport) error {
			_, err := rt.DeleteNamespace(ctx, &transport.DeleteNamespaceRequest{})
			return err
		},
		"ListNamespaces": func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListNamespaces(ctx, &transport.ListNamespacesRequest{})
			return err
		},
		"GetNamespaceInfo": func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{})
			return err
		},
		"UpsertVector": func(ctx context.Context, rt *Transport) error {
			_, err := rt.UpsertVector(ctx, &transport.UpsertVectorRequest{})
			return err
		},
		"GetVector": func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetVector(ctx, &transport.GetVectorRequest{})
			return err
		},
		"DeleteVector": func(ctx context.Context, rt *Transport) error {
			_, err := rt.DeleteVector(ctx, &transport.DeleteVectorRequest{})
			return err
		},
		"BatchUpsert": func(ctx context.Context, rt *Transport) error {
			_, err := rt.BatchUpsert(ctx, &transport.BatchUpsertRequest{})
			return err
		},
		"BatchDeleteVectors": func(ctx context.Context, rt *Transport) error {
			_, err := rt.BatchDeleteVectors(ctx, &transport.BatchDeleteVectorsRequest{})
			return err
		},
		"ListVectors": func(ctx context.Context, rt *Transport) error {
			_, err := rt.ListVectors(ctx, &transport.ListVectorsRequest{})
			return err
		},
		"UpdateMetadata": func(ctx context.Context, rt *Transport) error {
			_, err := rt.UpdateMetadata(ctx, &transport.UpdateVectorMetadataRequest{})
			return err
		},
		"SearchSimilar": func(ctx context.Context, rt *Transport) error {
			_, err := rt.SearchSimilar(ctx, &transport.SearchSimilarRequest{})
			return err
		},
		"SearchByMetadata": func(ctx context.Context, rt *Transport) error {
			_, err := rt.SearchByMetadata(ctx, &transport.SearchByMetadataRequest{})
			return err
		},
		"HybridSearch": func(ctx context.Context, rt *Transport) error {
			_, err := rt.HybridSearch(ctx, &transport.HybridSearchRequest{})
			return err
		},
		"SearchByID": func(ctx context.Context, rt *Transport) error {
			_, err := rt.SearchByID(ctx, &transport.SearchByIDRequest{})
			return err
		},
		"RebuildIndex": func(ctx context.Context, rt *Transport) error {
			_, err := rt.RebuildIndex(ctx, &transport.RebuildIndexRequest{})
			return err
		},
		"GetRebuildProgress": func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetRebuildProgress(ctx, &transport.GetRebuildProgressRequest{})
			return err
		},
		"GetIndexStats": func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetIndexStats(ctx, &transport.GetIndexStatsRequest{})
			return err
		},
		"CompactNamespace": func(ctx context.Context, rt *Transport) error {
			_, err := rt.CompactNamespace(ctx, &transport.CompactNamespaceRequest{})
			return err
		},
		"GetCompactionProgress": func(ctx context.Context, rt *Transport) error {
			_, err := rt.GetCompactionProgress(ctx, &transport.GetCompactionProgressRequest{})
			return err
		},
	})
}
