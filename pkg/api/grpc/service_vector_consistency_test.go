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

package grpc

import (
	"context"
	"testing"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// vectorConsistencyFromProto unit tests
// ---------------------------------------------------------------------------

// TestVectorConsistencyFromProto verifies that every proto ConsistencyLevel
// value maps to the correct service.ConsistencyLevel, preserving the old
// int32-field behavior where 0 meant ConsistencyStrong.
func TestVectorConsistencyFromProto(t *testing.T) {
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
			got := vectorConsistencyFromProto(tc.in)
			if got != tc.want {
				t.Errorf("vectorConsistencyFromProto(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Capturing vector service that records the consistency level passed through.
// ---------------------------------------------------------------------------

type capturingVectorService struct {
	mockVectorService
	lastGetConsistency           service.ConsistencyLevel
	lastSearchSimilarConsistency service.ConsistencyLevel
	lastSearchByMetaConsistency  service.ConsistencyLevel
	lastHybridConsistency        service.ConsistencyLevel
	lastSearchByIDConsistency    service.ConsistencyLevel
}

func (m *capturingVectorService) GetVector(_ context.Context, req *service.GetVectorRequest) (*service.GetVectorResponse, error) {
	m.lastGetConsistency = req.Consistency
	return &service.GetVectorResponse{ID: req.ID}, nil
}

func (m *capturingVectorService) SearchSimilar(_ context.Context, req *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
	m.lastSearchSimilarConsistency = req.Consistency
	return &service.SearchSimilarResponse{}, nil
}

func (m *capturingVectorService) SearchByMetadata(_ context.Context, req *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
	m.lastSearchByMetaConsistency = req.Consistency
	return &service.SearchByMetadataResponse{}, nil
}

func (m *capturingVectorService) HybridSearch(_ context.Context, req *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
	m.lastHybridConsistency = req.Consistency
	return &service.HybridSearchResponse{}, nil
}

func (m *capturingVectorService) SearchByID(_ context.Context, req *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
	m.lastSearchByIDConsistency = req.Consistency
	return &service.SearchByIDResponse{}, nil
}

// ---------------------------------------------------------------------------
// Integration-style handler tests: proto enum → service.ConsistencyLevel
// ---------------------------------------------------------------------------

// TestGRPC_VectorGet_ConsistencyMapping verifies that each proto
// ConsistencyLevel field value is correctly decoded to the service layer.
func TestGRPC_VectorGet_ConsistencyMapping(t *testing.T) {
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
			svc := &capturingVectorService{}
			client, cleanup := startTestServerFull(t, WithVector(svc))
			defer cleanup()

			_, err := client.VectorGet(context.Background(), &pb.VectorGetRequest{
				Namespace:   "ns",
				Id:          "0",
				Consistency: tc.level,
			})
			if err != nil {
				t.Fatalf("VectorGet: %v", err)
			}
			if svc.lastGetConsistency != tc.want {
				t.Errorf("consistency = %v, want %v", svc.lastGetConsistency, tc.want)
			}
		})
	}
}

// TestGRPC_VectorSearchSimilar_ConsistencyMapping verifies the consistency
// mapping for VectorSearchSimilar.
func TestGRPC_VectorSearchSimilar_ConsistencyMapping(t *testing.T) {
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
			svc := &capturingVectorService{}
			client, cleanup := startTestServerFull(t, WithVector(svc))
			defer cleanup()

			_, err := client.VectorSearchSimilar(context.Background(), &pb.VectorSearchSimilarRequest{
				Namespace:   "ns",
				Vector:      []float32{1.0, 2.0},
				K:           5,
				Consistency: tc.level,
			})
			if err != nil {
				t.Fatalf("VectorSearchSimilar: %v", err)
			}
			if svc.lastSearchSimilarConsistency != tc.want {
				t.Errorf("consistency = %v, want %v", svc.lastSearchSimilarConsistency, tc.want)
			}
		})
	}
}

// TestGRPC_VectorSearchByMetadata_ConsistencyMapping verifies the consistency
// mapping for VectorSearchByMetadata.
func TestGRPC_VectorSearchByMetadata_ConsistencyMapping(t *testing.T) {
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
			svc := &capturingVectorService{}
			client, cleanup := startTestServerFull(t, WithVector(svc))
			defer cleanup()

			_, err := client.VectorSearchByMetadata(context.Background(), &pb.VectorSearchByMetadataRequest{
				Namespace:   "ns",
				Consistency: tc.level,
			})
			if err != nil {
				t.Fatalf("VectorSearchByMetadata: %v", err)
			}
			if svc.lastSearchByMetaConsistency != tc.want {
				t.Errorf("consistency = %v, want %v", svc.lastSearchByMetaConsistency, tc.want)
			}
		})
	}
}

// TestGRPC_VectorHybridSearch_ConsistencyMapping verifies the consistency
// mapping for VectorHybridSearch.
func TestGRPC_VectorHybridSearch_ConsistencyMapping(t *testing.T) {
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
			svc := &capturingVectorService{}
			client, cleanup := startTestServerFull(t, WithVector(svc))
			defer cleanup()

			_, err := client.VectorHybridSearch(context.Background(), &pb.VectorHybridSearchRequest{
				Namespace:   "ns",
				Vector:      []float32{1.0, 2.0},
				K:           5,
				Consistency: tc.level,
			})
			if err != nil {
				t.Fatalf("VectorHybridSearch: %v", err)
			}
			if svc.lastHybridConsistency != tc.want {
				t.Errorf("consistency = %v, want %v", svc.lastHybridConsistency, tc.want)
			}
		})
	}
}

// TestGRPC_VectorSearchByID_ConsistencyMapping verifies the consistency
// mapping for VectorSearchByID.
func TestGRPC_VectorSearchByID_ConsistencyMapping(t *testing.T) {
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
			svc := &capturingVectorService{}
			client, cleanup := startTestServerFull(t, WithVector(svc))
			defer cleanup()

			_, err := client.VectorSearchByID(context.Background(), &pb.VectorSearchByIDRequest{
				Namespace:   "ns",
				Id:          "0",
				K:           5,
				Consistency: tc.level,
			})
			if err != nil {
				t.Fatalf("VectorSearchByID: %v", err)
			}
			if svc.lastSearchByIDConsistency != tc.want {
				t.Errorf("consistency = %v, want %v", svc.lastSearchByIDConsistency, tc.want)
			}
		})
	}
}
