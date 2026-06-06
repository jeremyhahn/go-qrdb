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

//go:build !integration

package quic

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// vectorConsistencyFromWire unit tests
// ---------------------------------------------------------------------------

// TestVectorConsistencyFromWire verifies that every proto ConsistencyLevel
// value maps to the correct service.ConsistencyLevel, preserving the old
// int32-field behavior where 0 meant ConsistencyStrong.
func TestVectorConsistencyFromWire(t *testing.T) {
	tests := []struct {
		name string
		in   pb.ConsistencyLevel
		want service.ConsistencyLevel
	}{
		{
			name: "unspecified maps to strong (backward compat: old int32 0)",
			in:   pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED,
			want: service.ConsistencyStrong,
		},
		{
			name: "strong maps to ConsistencyStrong",
			in:   pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG,
			want: service.ConsistencyStrong,
		},
		{
			name: "linearizable maps to ConsistencyLinearizable",
			in:   pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE,
			want: service.ConsistencyLinearizable,
		},
		{
			name: "eventual maps to ConsistencyEventual",
			in:   pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL,
			want: service.ConsistencyEventual,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := vectorConsistencyFromWire(tc.in)
			if got != tc.want {
				t.Errorf("vectorConsistencyFromWire(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Handler-level consistency routing tests (all 5 affected messages)
// ---------------------------------------------------------------------------

// TestHandleVectorGet_ConsistencyMapping verifies that each proto
// ConsistencyLevel is correctly decoded and passed to the vector service.
func TestHandleVectorGet_ConsistencyMapping(t *testing.T) {
	tests := []struct {
		name  string
		level pb.ConsistencyLevel
		want  service.ConsistencyLevel
	}{
		{"unspecified→strong", pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED, service.ConsistencyStrong},
		{"strong→strong", pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG, service.ConsistencyStrong},
		{"linearizable→linearizable", pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE, service.ConsistencyLinearizable},
		{"eventual→eventual", pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL, service.ConsistencyEventual},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var captured service.ConsistencyLevel
			mock := &mockVectorService{
				getVectorFn: func(_ context.Context, req *service.GetVectorRequest) (*service.GetVectorResponse, error) {
					captured = req.Consistency
					return &service.GetVectorResponse{}, nil
				},
			}
			srv := newVectorServer(mock)

			payload, err := proto.Marshal(&pb.VectorGetRequest{
				Namespace:   "ns",
				Id:          "0",
				Consistency: tc.level,
			})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if _, err := srv.handleVectorGet(context.Background(), payload); err != nil {
				t.Fatalf("handleVectorGet: %v", err)
			}
			if captured != tc.want {
				t.Errorf("consistency = %v, want %v", captured, tc.want)
			}
		})
	}
}

// TestHandleVectorSearchSimilar_ConsistencyMapping verifies the consistency
// mapping for handleVectorSearchSimilar.
func TestHandleVectorSearchSimilar_ConsistencyMapping(t *testing.T) {
	tests := []struct {
		name  string
		level pb.ConsistencyLevel
		want  service.ConsistencyLevel
	}{
		{"unspecified→strong", pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED, service.ConsistencyStrong},
		{"strong→strong", pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG, service.ConsistencyStrong},
		{"linearizable→linearizable", pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE, service.ConsistencyLinearizable},
		{"eventual→eventual", pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL, service.ConsistencyEventual},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var captured service.ConsistencyLevel
			mock := &mockVectorService{
				searchSimilarFn: func(_ context.Context, req *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
					captured = req.Consistency
					return &service.SearchSimilarResponse{}, nil
				},
			}
			srv := newVectorServer(mock)

			payload, err := proto.Marshal(&pb.VectorSearchSimilarRequest{
				Namespace:   "ns",
				Vector:      []float32{1.0, 2.0},
				K:           5,
				Consistency: tc.level,
			})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if _, err := srv.handleVectorSearchSimilar(context.Background(), payload); err != nil {
				t.Fatalf("handleVectorSearchSimilar: %v", err)
			}
			if captured != tc.want {
				t.Errorf("consistency = %v, want %v", captured, tc.want)
			}
		})
	}
}

// TestHandleVectorSearchByMetadata_ConsistencyMapping verifies the consistency
// mapping for handleVectorSearchByMetadata.
func TestHandleVectorSearchByMetadata_ConsistencyMapping(t *testing.T) {
	tests := []struct {
		name  string
		level pb.ConsistencyLevel
		want  service.ConsistencyLevel
	}{
		{"unspecified→strong", pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED, service.ConsistencyStrong},
		{"strong→strong", pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG, service.ConsistencyStrong},
		{"linearizable→linearizable", pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE, service.ConsistencyLinearizable},
		{"eventual→eventual", pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL, service.ConsistencyEventual},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var captured service.ConsistencyLevel
			mock := &mockVectorService{
				searchByMetadataFn: func(_ context.Context, req *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
					captured = req.Consistency
					return &service.SearchByMetadataResponse{}, nil
				},
			}
			srv := newVectorServer(mock)

			payload, err := proto.Marshal(&pb.VectorSearchByMetadataRequest{
				Namespace:   "ns",
				Consistency: tc.level,
			})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if _, err := srv.handleVectorSearchByMetadata(context.Background(), payload); err != nil {
				t.Fatalf("handleVectorSearchByMetadata: %v", err)
			}
			if captured != tc.want {
				t.Errorf("consistency = %v, want %v", captured, tc.want)
			}
		})
	}
}

// TestHandleVectorHybridSearch_ConsistencyMapping verifies the consistency
// mapping for handleVectorHybridSearch.
func TestHandleVectorHybridSearch_ConsistencyMapping(t *testing.T) {
	tests := []struct {
		name  string
		level pb.ConsistencyLevel
		want  service.ConsistencyLevel
	}{
		{"unspecified→strong", pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED, service.ConsistencyStrong},
		{"strong→strong", pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG, service.ConsistencyStrong},
		{"linearizable→linearizable", pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE, service.ConsistencyLinearizable},
		{"eventual→eventual", pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL, service.ConsistencyEventual},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var captured service.ConsistencyLevel
			mock := &mockVectorService{
				hybridSearchFn: func(_ context.Context, req *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
					captured = req.Consistency
					return &service.HybridSearchResponse{}, nil
				},
			}
			srv := newVectorServer(mock)

			payload, err := proto.Marshal(&pb.VectorHybridSearchRequest{
				Namespace:   "ns",
				Vector:      []float32{1.0, 2.0},
				K:           5,
				Consistency: tc.level,
			})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if _, err := srv.handleVectorHybridSearch(context.Background(), payload); err != nil {
				t.Fatalf("handleVectorHybridSearch: %v", err)
			}
			if captured != tc.want {
				t.Errorf("consistency = %v, want %v", captured, tc.want)
			}
		})
	}
}

// TestHandleVectorSearchByID_ConsistencyMapping verifies the consistency
// mapping for handleVectorSearchByID.
func TestHandleVectorSearchByID_ConsistencyMapping(t *testing.T) {
	tests := []struct {
		name  string
		level pb.ConsistencyLevel
		want  service.ConsistencyLevel
	}{
		{"unspecified→strong", pb.ConsistencyLevel_CONSISTENCY_LEVEL_UNSPECIFIED, service.ConsistencyStrong},
		{"strong→strong", pb.ConsistencyLevel_CONSISTENCY_LEVEL_STRONG, service.ConsistencyStrong},
		{"linearizable→linearizable", pb.ConsistencyLevel_CONSISTENCY_LEVEL_LINEARIZABLE, service.ConsistencyLinearizable},
		{"eventual→eventual", pb.ConsistencyLevel_CONSISTENCY_LEVEL_EVENTUAL, service.ConsistencyEventual},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var captured service.ConsistencyLevel
			mock := &mockVectorService{
				searchByIDFn: func(_ context.Context, req *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
					captured = req.Consistency
					return &service.SearchByIDResponse{}, nil
				},
			}
			srv := newVectorServer(mock)

			payload, err := proto.Marshal(&pb.VectorSearchByIDRequest{
				Namespace:   "ns",
				Id:          "0",
				K:           5,
				Consistency: tc.level,
			})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if _, err := srv.handleVectorSearchByID(context.Background(), payload); err != nil {
				t.Fatalf("handleVectorSearchByID: %v", err)
			}
			if captured != tc.want {
				t.Errorf("consistency = %v, want %v", captured, tc.want)
			}
		})
	}
}
