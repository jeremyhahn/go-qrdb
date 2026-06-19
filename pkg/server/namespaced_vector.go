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

package server

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// namespacedVectorService wraps a VectorServicer so that the vector namespace
// of every request is transparently prefixed with the composite (tenant, db)
// namespace and stripped from responses. The unit of isolation for the vector
// engine is the namespace name, so prefixing it scopes each database's vectors.
//
// The underlying VectorService additionally performs context-based tenant
// scoping of its own; applying the full composite prefix (which includes the
// tenant segment) therefore double-encodes the tenant in the physical key for
// non-system tenants. This is redundant but correct — it never weakens
// isolation — and keeps a single composeNamespace helper in use across every
// engine.
type namespacedVectorService struct {
	inner  service.VectorServicer
	prefix string
}

// Compile-time interface check.
var _ service.VectorServicer = (*namespacedVectorService)(nil)

// newNamespacedVectorService creates a VectorServicer scoped to the given
// composite namespace prefix.
func newNamespacedVectorService(inner service.VectorServicer, prefix string) *namespacedVectorService {
	return &namespacedVectorService{inner: inner, prefix: prefix}
}

// strip removes the namespace prefix from a returned namespace name.
func (s *namespacedVectorService) strip(ns string) string {
	return stripStringPrefix(ns, s.prefix)
}

// CreateNamespace creates a vector namespace under the prefixed name.
func (s *namespacedVectorService) CreateNamespace(ctx context.Context, req *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
	cp := *req
	cp.Namespace = s.prefix + req.Namespace
	resp, err := s.inner.CreateNamespace(ctx, &cp)
	if err != nil {
		return nil, err
	}
	resp.Namespace = s.strip(resp.Namespace)
	return resp, nil
}

// DeleteNamespace removes the prefixed vector namespace.
func (s *namespacedVectorService) DeleteNamespace(ctx context.Context, req *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
	cp := *req
	cp.Namespace = s.prefix + req.Namespace
	resp, err := s.inner.DeleteNamespace(ctx, &cp)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// ListNamespaces lists namespaces within the namespace prefix, stripping the
// prefix from the returned names.
func (s *namespacedVectorService) ListNamespaces(ctx context.Context, req *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
	cp := *req
	cp.Prefix = s.prefix + req.Prefix
	resp, err := s.inner.ListNamespaces(ctx, &cp)
	if err != nil {
		return nil, err
	}
	for _, ns := range resp.Namespaces {
		if ns != nil {
			ns.Namespace = s.strip(ns.Namespace)
		}
	}
	return resp, nil
}

// GetNamespaceInfo retrieves metadata about the prefixed namespace.
func (s *namespacedVectorService) GetNamespaceInfo(ctx context.Context, req *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
	cp := *req
	cp.Namespace = s.prefix + req.Namespace
	resp, err := s.inner.GetNamespaceInfo(ctx, &cp)
	if err != nil {
		return nil, err
	}
	resp.Namespace = s.strip(resp.Namespace)
	return resp, nil
}

// UpsertVector inserts or updates a vector in the prefixed namespace.
func (s *namespacedVectorService) UpsertVector(ctx context.Context, req *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
	cp := *req
	cp.Namespace = s.prefix + req.Namespace
	resp, err := s.inner.UpsertVector(ctx, &cp)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// GetVector retrieves a vector from the prefixed namespace.
func (s *namespacedVectorService) GetVector(ctx context.Context, req *service.GetVectorRequest) (*service.GetVectorResponse, error) {
	cp := *req
	cp.Namespace = s.prefix + req.Namespace
	resp, err := s.inner.GetVector(ctx, &cp)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// DeleteVector removes a vector from the prefixed namespace.
func (s *namespacedVectorService) DeleteVector(ctx context.Context, req *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
	cp := *req
	cp.Namespace = s.prefix + req.Namespace
	resp, err := s.inner.DeleteVector(ctx, &cp)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// BatchUpsert inserts or updates multiple vectors in the prefixed namespace.
func (s *namespacedVectorService) BatchUpsert(ctx context.Context, req *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
	cp := *req
	cp.Namespace = s.prefix + req.Namespace
	resp, err := s.inner.BatchUpsert(ctx, &cp)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// BatchDeleteVectors removes multiple vectors from the prefixed namespace.
func (s *namespacedVectorService) BatchDeleteVectors(ctx context.Context, req *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
	cp := *req
	cp.Namespace = s.prefix + req.Namespace
	resp, err := s.inner.BatchDeleteVectors(ctx, &cp)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// ListVectors lists vectors in the prefixed namespace.
func (s *namespacedVectorService) ListVectors(ctx context.Context, req *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
	cp := *req
	cp.Namespace = s.prefix + req.Namespace
	resp, err := s.inner.ListVectors(ctx, &cp)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// UpdateMetadata updates a vector's metadata in the prefixed namespace.
func (s *namespacedVectorService) UpdateMetadata(ctx context.Context, req *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
	cp := *req
	cp.Namespace = s.prefix + req.Namespace
	resp, err := s.inner.UpdateMetadata(ctx, &cp)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// SearchSimilar performs similarity search within the prefixed namespace.
func (s *namespacedVectorService) SearchSimilar(ctx context.Context, req *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
	cp := *req
	cp.Namespace = s.prefix + req.Namespace
	resp, err := s.inner.SearchSimilar(ctx, &cp)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// SearchByMetadata performs metadata search within the prefixed namespace.
func (s *namespacedVectorService) SearchByMetadata(ctx context.Context, req *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
	cp := *req
	cp.Namespace = s.prefix + req.Namespace
	resp, err := s.inner.SearchByMetadata(ctx, &cp)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// HybridSearch performs hybrid search within the prefixed namespace.
func (s *namespacedVectorService) HybridSearch(ctx context.Context, req *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
	cp := *req
	cp.Namespace = s.prefix + req.Namespace
	resp, err := s.inner.HybridSearch(ctx, &cp)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// SearchByID performs similarity search using an existing vector's ID within
// the prefixed namespace.
func (s *namespacedVectorService) SearchByID(ctx context.Context, req *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
	cp := *req
	cp.Namespace = s.prefix + req.Namespace
	resp, err := s.inner.SearchByID(ctx, &cp)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// RebuildIndex rebuilds the index for the prefixed namespace.
func (s *namespacedVectorService) RebuildIndex(ctx context.Context, req *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
	cp := *req
	cp.Namespace = s.prefix + req.Namespace
	resp, err := s.inner.RebuildIndex(ctx, &cp)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// GetRebuildProgress retrieves rebuild progress for the prefixed namespace.
func (s *namespacedVectorService) GetRebuildProgress(ctx context.Context, req *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
	cp := *req
	cp.Namespace = s.prefix + req.Namespace
	resp, err := s.inner.GetRebuildProgress(ctx, &cp)
	if err != nil {
		return nil, err
	}
	resp.Namespace = s.strip(resp.Namespace)
	return resp, nil
}

// GetIndexStats retrieves index statistics for the prefixed namespace.
func (s *namespacedVectorService) GetIndexStats(ctx context.Context, req *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
	cp := *req
	cp.Namespace = s.prefix + req.Namespace
	resp, err := s.inner.GetIndexStats(ctx, &cp)
	if err != nil {
		return nil, err
	}
	resp.Namespace = s.strip(resp.Namespace)
	return resp, nil
}

// CompactNamespace triggers compaction for the prefixed namespace.
func (s *namespacedVectorService) CompactNamespace(ctx context.Context, req *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
	cp := *req
	cp.Namespace = s.prefix + req.Namespace
	resp, err := s.inner.CompactNamespace(ctx, &cp)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// GetCompactionProgress retrieves compaction progress for the prefixed
// namespace.
func (s *namespacedVectorService) GetCompactionProgress(ctx context.Context, req *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
	cp := *req
	cp.Namespace = s.prefix + req.Namespace
	resp, err := s.inner.GetCompactionProgress(ctx, &cp)
	if err != nil {
		return nil, err
	}
	resp.Namespace = s.strip(resp.Namespace)
	return resp, nil
}
