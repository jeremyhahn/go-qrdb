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
	"errors"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRESTVector_ReportsSDKIndexTypes proves the REST transport reports the
// server's canonical index-type name as the SDK constant, the value the
// protobuf transports return for the same namespace.
func TestRESTVector_ReportsSDKIndexTypes(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(func(r *http.Request) bool { return true }, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{
			"namespace":       "ns",
			"index_type":      "IVF",
			"distance_metric": "L2",
			"namespaces": []interface{}{
				map[string]interface{}{"namespace": "a", "index_type": "FLAT"},
				map[string]interface{}{"namespace": "b", "index_type": "HNSW"},
			},
		})
	})
	ctx := context.Background()

	info, err := rt.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("GetNamespaceInfo: %v", err)
	}
	if info.IndexType != transport.IndexIVFFlat {
		t.Errorf("GetNamespaceInfo index type: %q, want %q", info.IndexType, transport.IndexIVFFlat)
	}
	stats, err := rt.GetIndexStats(ctx, &transport.GetIndexStatsRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("GetIndexStats: %v", err)
	}
	if stats.IndexType != transport.IndexIVFFlat {
		t.Errorf("GetIndexStats index type: %q, want %q", stats.IndexType, transport.IndexIVFFlat)
	}
	list, err := rt.ListNamespaces(ctx, &transport.ListNamespacesRequest{})
	if err != nil {
		t.Fatalf("ListNamespaces: %v", err)
	}
	if len(list.Namespaces) != 2 {
		t.Fatalf("namespaces: %d, want 2", len(list.Namespaces))
	}
	if list.Namespaces[0].IndexType != transport.IndexFlat || list.Namespaces[1].IndexType != transport.IndexHNSW {
		t.Errorf("ListNamespaces index types: %q, %q", list.Namespaces[0].IndexType, list.Namespaces[1].IndexType)
	}
}

// TestRESTVector_UnknownIndexTypePassesThrough proves a name the SDK does not
// know reaches the caller as the server sent it.
func TestRESTVector_UnknownIndexTypePassesThrough(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(func(r *http.Request) bool { return true }, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{"namespace": "ns", "index_type": "DISKANN", "distance_metric": "L2"})
	})
	info, err := rt.GetNamespaceInfo(context.Background(), &transport.GetNamespaceInfoRequest{Namespace: "ns"})
	if err != nil {
		t.Fatalf("GetNamespaceInfo: %v", err)
	}
	if info.IndexType != "DISKANN" {
		t.Errorf("index type: %q, want DISKANN", info.IndexType)
	}
}

// TestRESTVector_ReportsSDKDistanceMetrics proves a metric the server reports
// in the case it was created with comes back as the SDK constant the
// protobuf transports return.
func TestRESTVector_ReportsSDKDistanceMetrics(t *testing.T) {
	for reported, want := range map[string]transport.DistanceMetric{
		"L2":        transport.DistanceEuclidean,
		"MANHATTAN": transport.DistanceManhattan,
		"manhattan": transport.DistanceManhattan,
		"cosine":    transport.DistanceCosine,
	} {
		rt, rs, _ := newExtTransport(t)
		rs.handle(func(r *http.Request) bool { return true }, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, map[string]interface{}{"namespace": "ns", "distance_metric": reported})
		})
		info, err := rt.GetNamespaceInfo(context.Background(), &transport.GetNamespaceInfoRequest{Namespace: "ns"})
		if err != nil {
			t.Fatalf("GetNamespaceInfo with %q: %v", reported, err)
		}
		if info.DistanceMetric != want {
			t.Errorf("GetNamespaceInfo with %q: metric %q, want %q", reported, info.DistanceMetric, want)
		}
	}
}

// TestRESTVector_UnknownDistanceMetricRefused proves a metric the SDK does
// not know is refused with the typed error, never read as cosine.
func TestRESTVector_UnknownDistanceMetricRefused(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(func(r *http.Request) bool { return true }, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]interface{}{"namespace": "ns", "distance_metric": "CHEBYSHEV"})
	})
	info, err := rt.GetNamespaceInfo(context.Background(), &transport.GetNamespaceInfoRequest{Namespace: "ns"})
	var unknown *transport.UnknownDistanceMetricError
	if !errors.As(err, &unknown) || unknown.Metric != "CHEBYSHEV" {
		t.Fatalf("GetNamespaceInfo = %+v, %v; want *UnknownDistanceMetricError{CHEBYSHEV}", info, err)
	}
}
