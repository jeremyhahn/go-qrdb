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
	"context"
	"encoding/json"
	"errors"
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
