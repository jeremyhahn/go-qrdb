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

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// newCoverageVecSvc builds a vector service over a vecMockStorage that
// reports one system-tenant namespace, or none when namespace is empty.
// Untagged, because callers in the qrdb_vector build and in every build
// (vector_close_test.go) use it.
func newCoverageVecSvc(t *testing.T, namespace string, dimension uint32, storageBytes int64) (*vectorService, *vecMockStorage) {
	t.Helper()
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	// The scoped name used for system-tenant requests (no ctx tenant set).
	scoped := scopedNamespace(tenantid.System, namespace)

	storage.listNamespacesFn = func(ctx context.Context) ([]vector.NamespaceInfo, error) {
		if namespace == "" {
			return []vector.NamespaceInfo{}, nil
		}
		return []vector.NamespaceInfo{
			{
				Name:           scoped,
				Dimension:      dimension,
				VectorCount:    0,
				DistanceMetric: "L2",
				IndexType:      "HNSW",
				StorageBytes:   storageBytes,
				CreatedAt:      time.Now().UnixNano(),
				UpdatedAt:      time.Now().UnixNano(),
			},
		}, nil
	}

	storage.getNamespaceInfoFn = func(ctx context.Context, ns string) (*vector.NamespaceInfo, error) {
		if ns == scoped {
			return &vector.NamespaceInfo{
				Name:           scoped,
				Dimension:      dimension,
				VectorCount:    5,
				DistanceMetric: "L2",
				IndexType:      "HNSW",
				StorageBytes:   storageBytes,
				CreatedAt:      time.Now().UnixNano(),
				UpdatedAt:      time.Now().UnixNano(),
			}, nil
		}
		return nil, vector.ErrNamespaceNotFound
	}

	svc, err := NewVectorService(storage, logger)
	require.NoError(t, err)

	// Cast to concrete type for internal field access
	concrete := svc.(*vectorService)
	return concrete, storage
}
