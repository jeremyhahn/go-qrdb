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

package quic

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mockVectorService is a configurable mock for service.VectorServicer.
type mockVectorService struct {
	createNamespaceFn       func(ctx context.Context, req *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error)
	updateNamespaceFn       func(ctx context.Context, req *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error)
	deleteNamespaceFn       func(ctx context.Context, req *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error)
	listNamespacesFn        func(ctx context.Context, req *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error)
	getNamespaceInfoFn      func(ctx context.Context, req *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error)
	upsertVectorFn          func(ctx context.Context, req *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error)
	getVectorFn             func(ctx context.Context, req *service.GetVectorRequest) (*service.GetVectorResponse, error)
	deleteVectorFn          func(ctx context.Context, req *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error)
	batchUpsertFn           func(ctx context.Context, req *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error)
	batchDeleteVectorsFn    func(ctx context.Context, req *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error)
	listVectorsFn           func(ctx context.Context, req *service.ListVectorsRequest) (*service.ListVectorsResponse, error)
	updateMetadataFn        func(ctx context.Context, req *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error)
	searchSimilarFn         func(ctx context.Context, req *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error)
	searchByMetadataFn      func(ctx context.Context, req *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error)
	hybridSearchFn          func(ctx context.Context, req *service.HybridSearchRequest) (*service.HybridSearchResponse, error)
	searchByIDFn            func(ctx context.Context, req *service.SearchByIDRequest) (*service.SearchByIDResponse, error)
	rebuildIndexFn          func(ctx context.Context, req *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error)
	getRebuildProgressFn    func(ctx context.Context, req *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error)
	getIndexStatsFn         func(ctx context.Context, req *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error)
	compactNamespaceFn      func(ctx context.Context, req *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error)
	getCompactionProgressFn func(ctx context.Context, req *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error)
}

func (m *mockVectorService) UpdateNamespace(ctx context.Context, req *service.UpdateNamespaceRequest) (*service.UpdateNamespaceResponse, error) {
	if m.updateNamespaceFn != nil {
		return m.updateNamespaceFn(ctx, req)
	}
	return &service.UpdateNamespaceResponse{Namespace: req.Namespace, RequiresRebuild: len(req.IndexConfig) > 0, Timestamp: 1000, Success: true}, nil
}

func (m *mockVectorService) CreateNamespace(ctx context.Context, req *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
	return m.createNamespaceFn(ctx, req)
}

func (m *mockVectorService) DeleteNamespace(ctx context.Context, req *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
	return m.deleteNamespaceFn(ctx, req)
}

func (m *mockVectorService) ListNamespaces(ctx context.Context, req *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
	return m.listNamespacesFn(ctx, req)
}

func (m *mockVectorService) GetNamespaceInfo(ctx context.Context, req *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
	return m.getNamespaceInfoFn(ctx, req)
}

func (m *mockVectorService) UpsertVector(ctx context.Context, req *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	return m.upsertVectorFn(ctx, req)
}

func (m *mockVectorService) GetVector(ctx context.Context, req *service.GetVectorRequest) (*service.GetVectorResponse, error) {
	return m.getVectorFn(ctx, req)
}

func (m *mockVectorService) DeleteVector(ctx context.Context, req *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
	return m.deleteVectorFn(ctx, req)
}

func (m *mockVectorService) BatchUpsert(ctx context.Context, req *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
	return m.batchUpsertFn(ctx, req)
}

func (m *mockVectorService) BatchDeleteVectors(ctx context.Context, req *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
	return m.batchDeleteVectorsFn(ctx, req)
}

func (m *mockVectorService) ListVectors(ctx context.Context, req *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
	return m.listVectorsFn(ctx, req)
}

func (m *mockVectorService) UpdateMetadata(ctx context.Context, req *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
	return m.updateMetadataFn(ctx, req)
}

func (m *mockVectorService) SearchSimilar(ctx context.Context, req *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
	return m.searchSimilarFn(ctx, req)
}

func (m *mockVectorService) SearchByMetadata(ctx context.Context, req *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
	return m.searchByMetadataFn(ctx, req)
}

func (m *mockVectorService) HybridSearch(ctx context.Context, req *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
	return m.hybridSearchFn(ctx, req)
}

func (m *mockVectorService) SearchByID(ctx context.Context, req *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
	return m.searchByIDFn(ctx, req)
}

func (m *mockVectorService) RebuildIndex(ctx context.Context, req *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
	return m.rebuildIndexFn(ctx, req)
}

func (m *mockVectorService) GetRebuildProgress(ctx context.Context, req *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
	return m.getRebuildProgressFn(ctx, req)
}

func (m *mockVectorService) GetIndexStats(ctx context.Context, req *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
	return m.getIndexStatsFn(ctx, req)
}

func (m *mockVectorService) CompactNamespace(ctx context.Context, req *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
	return m.compactNamespaceFn(ctx, req)
}

func (m *mockVectorService) GetCompactionProgress(ctx context.Context, req *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
	return m.getCompactionProgressFn(ctx, req)
}
