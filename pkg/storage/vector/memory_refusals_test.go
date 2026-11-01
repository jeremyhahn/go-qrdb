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

package vector

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemoryVectorStorage_canceledContextRefuses(t *testing.T) {
	tests := []struct {
		name string
		call func(ctx context.Context, s *MemoryVectorStorage) error
	}{
		{"UpsertVector", func(ctx context.Context, s *MemoryVectorStorage) error {
			return s.UpsertVector(ctx, "docs", &VectorValue{ID: 2})
		}},
		{"GetVector", func(ctx context.Context, s *MemoryVectorStorage) error {
			_, err := s.GetVector(ctx, "docs", 1)
			return err
		}},
		{"DeleteVector", func(ctx context.Context, s *MemoryVectorStorage) error {
			return s.DeleteVector(ctx, "docs", 1)
		}},
		{"BatchUpsertVectors", func(ctx context.Context, s *MemoryVectorStorage) error {
			return s.BatchUpsertVectors(ctx, "docs", []*VectorValue{{ID: 2}})
		}},
		{"BatchDeleteVectors", func(ctx context.Context, s *MemoryVectorStorage) error {
			return s.BatchDeleteVectors(ctx, "docs", []uint64{1})
		}},
		{"ScanByMetadata", func(ctx context.Context, s *MemoryVectorStorage) error {
			_, err := s.ScanByMetadata(ctx, "docs", "k", "v", nil)
			return err
		}},
		{"ScanByMultipleFilters", func(ctx context.Context, s *MemoryVectorStorage) error {
			_, err := s.ScanByMultipleFilters(ctx, "docs", []MetadataFilter{{Field: "k", Value: "v", Operator: FilterEqual}}, nil)
			return err
		}},
		{"ScanByMultipleFilters without filters", func(ctx context.Context, s *MemoryVectorStorage) error {
			_, err := s.ScanByMultipleFilters(ctx, "docs", nil, nil)
			return err
		}},
		{"ScanNamespace", func(ctx context.Context, s *MemoryVectorStorage) error {
			_, err := s.ScanNamespace(ctx, "docs", nil)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewMemoryVectorStorage()
			createUpdateNamespace(t, s)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			require.ErrorIs(t, tt.call(ctx, s), ErrContextCanceled)

			// The refused call changed nothing.
			vecs, err := s.ScanNamespace(context.Background(), "docs", nil)
			require.NoError(t, err)
			require.Len(t, vecs, 1)
			require.Equal(t, uint64(1), vecs[0].ID)
		})
	}
}

func TestApplyScanOptions(t *testing.T) {
	results := []*VectorValue{{ID: 1}, {ID: 2}, {ID: 3}, {ID: 4}}
	tests := []struct {
		name string
		opts *ScanOptions
		want []uint64
	}{
		{name: "nil options", opts: nil, want: []uint64{1, 2, 3, 4}},
		{name: "zero options", opts: &ScanOptions{}, want: []uint64{1, 2, 3, 4}},
		{name: "limit", opts: &ScanOptions{Limit: 2}, want: []uint64{1, 2}},
		{name: "limit past end", opts: &ScanOptions{Limit: 9}, want: []uint64{1, 2, 3, 4}},
		{name: "offset", opts: &ScanOptions{Offset: 3}, want: []uint64{4}},
		{name: "offset and limit", opts: &ScanOptions{Offset: 1, Limit: 2}, want: []uint64{2, 3}},
		{name: "offset past end", opts: &ScanOptions{Offset: 4, Limit: 1}, want: []uint64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := make([]uint64, 0, len(tt.want))
			for _, v := range applyScanOptions(results, tt.opts) {
				got = append(got, v.ID)
			}
			require.Equal(t, tt.want, got)
		})
	}
}

func TestMemoryVectorStorage_ScanNamespaceLimit(t *testing.T) {
	s := NewMemoryVectorStorage()
	ctx := context.Background()
	createUpdateNamespace(t, s)
	require.NoError(t, s.BatchUpsertVectors(ctx, "docs", []*VectorValue{{ID: 3}, {ID: 2}}))

	vecs, err := s.ScanNamespace(ctx, "docs", &ScanOptions{Limit: 2})
	require.NoError(t, err)
	require.Len(t, vecs, 2)
	require.Equal(t, uint64(1), vecs[0].ID)
	require.Equal(t, uint64(2), vecs[1].ID)
}
