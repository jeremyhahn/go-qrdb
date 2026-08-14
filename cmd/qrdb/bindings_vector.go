//go:build qrdb_vector

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// VectorBinding exposes vector database operations to the Wails frontend.
type VectorBinding struct {
	logger *logger.Logger
	conn   *ConnectionBinding
}

// NewVectorBinding creates a new VectorBinding backed by the given connection.
func NewVectorBinding(log *logger.Logger, conn *ConnectionBinding) *VectorBinding {
	return &VectorBinding{logger: log, conn: conn}
}

// CreateNamespace creates a new vector namespace.
func (b *VectorBinding) CreateNamespace(req *transport.CreateNamespaceRequest) (*transport.CreateNamespaceResponse, error) {
	b.logger.Debug("CreateNamespace called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("CreateNamespace failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().CreateNamespace(context.Background(), req)
	if err != nil {
		b.logger.Error("CreateNamespace failed", "error", err)
		return nil, err
	}
	b.logger.Debug("CreateNamespace completed")
	return resp, nil
}

// DeleteNamespace removes a namespace and all its vectors.
func (b *VectorBinding) DeleteNamespace(req *transport.DeleteNamespaceRequest) (*transport.DeleteNamespaceResponse, error) {
	b.logger.Debug("DeleteNamespace called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("DeleteNamespace failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().DeleteNamespace(context.Background(), req)
	if err != nil {
		b.logger.Error("DeleteNamespace failed", "error", err)
		return nil, err
	}
	b.logger.Debug("DeleteNamespace completed")
	return resp, nil
}

// ListNamespaces returns all vector namespace names.
func (b *VectorBinding) ListNamespaces(req *transport.ListNamespacesRequest) (*transport.ListNamespacesResponse, error) {
	b.logger.Debug("ListNamespaces called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("ListNamespaces failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().ListNamespaces(context.Background(), req)
	if err != nil {
		b.logger.Error("ListNamespaces failed", "error", err)
		return nil, err
	}
	b.logger.Debug("ListNamespaces completed")
	return resp, nil
}

// GetNamespaceInfo returns metadata about a namespace.
func (b *VectorBinding) GetNamespaceInfo(req *transport.GetNamespaceInfoRequest) (*transport.GetNamespaceInfoResponse, error) {
	b.logger.Debug("GetNamespaceInfo called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("GetNamespaceInfo failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().GetNamespaceInfo(context.Background(), req)
	if err != nil {
		b.logger.Error("GetNamespaceInfo failed", "error", err)
		return nil, err
	}
	b.logger.Debug("GetNamespaceInfo completed")
	return resp, nil
}

// UpsertVector inserts or updates a vector in a namespace.
func (b *VectorBinding) UpsertVector(req *transport.UpsertVectorRequest) (*transport.UpsertVectorResponse, error) {
	b.logger.Debug("UpsertVector called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("UpsertVector failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().UpsertVector(context.Background(), req)
	if err != nil {
		b.logger.Error("UpsertVector failed", "error", err)
		return nil, err
	}
	b.logger.Debug("UpsertVector completed")
	return resp, nil
}

// GetVector retrieves a single vector by ID.
func (b *VectorBinding) GetVector(req *transport.GetVectorRequest) (*transport.GetVectorResponse, error) {
	b.logger.Debug("GetVector called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("GetVector failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().GetVector(context.Background(), req)
	if err != nil {
		b.logger.Error("GetVector failed", "error", err)
		return nil, err
	}
	b.logger.Debug("GetVector completed")
	return resp, nil
}

// DeleteVector removes a single vector by ID.
func (b *VectorBinding) DeleteVector(req *transport.DeleteVectorRequest) (*transport.DeleteVectorResponse, error) {
	b.logger.Debug("DeleteVector called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("DeleteVector failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().DeleteVector(context.Background(), req)
	if err != nil {
		b.logger.Error("DeleteVector failed", "error", err)
		return nil, err
	}
	b.logger.Debug("DeleteVector completed")
	return resp, nil
}

// BatchUpsert inserts or updates multiple vectors atomically.
func (b *VectorBinding) BatchUpsert(req *transport.BatchUpsertRequest) (*transport.BatchUpsertResponse, error) {
	b.logger.Debug("BatchUpsert called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("BatchUpsert failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().BatchUpsert(context.Background(), req)
	if err != nil {
		b.logger.Error("BatchUpsert failed", "error", err)
		return nil, err
	}
	b.logger.Debug("BatchUpsert completed")
	return resp, nil
}

// BatchDeleteVectors removes multiple vectors by ID.
func (b *VectorBinding) BatchDeleteVectors(req *transport.BatchDeleteVectorsRequest) (*transport.BatchDeleteVectorsResponse, error) {
	b.logger.Debug("BatchDeleteVectors called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("BatchDeleteVectors failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().BatchDeleteVectors(context.Background(), req)
	if err != nil {
		b.logger.Error("BatchDeleteVectors failed", "error", err)
		return nil, err
	}
	b.logger.Debug("BatchDeleteVectors completed")
	return resp, nil
}

// ListVectors returns a paginated list of vectors in a namespace.
func (b *VectorBinding) ListVectors(req *transport.ListVectorsRequest) (*transport.ListVectorsResponse, error) {
	b.logger.Debug("ListVectors called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("ListVectors failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().ListVectors(context.Background(), req)
	if err != nil {
		b.logger.Error("ListVectors failed", "error", err)
		return nil, err
	}
	b.logger.Debug("ListVectors completed")
	return resp, nil
}

// UpdateMetadata updates the metadata for an existing vector.
func (b *VectorBinding) UpdateMetadata(req *transport.UpdateVectorMetadataRequest) (*transport.UpdateVectorMetadataResponse, error) {
	b.logger.Debug("UpdateMetadata called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("UpdateMetadata failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().UpdateMetadata(context.Background(), req)
	if err != nil {
		b.logger.Error("UpdateMetadata failed", "error", err)
		return nil, err
	}
	b.logger.Debug("UpdateMetadata completed")
	return resp, nil
}

// SearchSimilar performs a k-nearest-neighbor similarity search.
func (b *VectorBinding) SearchSimilar(req *transport.SearchSimilarRequest) (*transport.SearchSimilarResponse, error) {
	b.logger.Debug("SearchSimilar called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("SearchSimilar failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().SearchSimilar(context.Background(), req)
	if err != nil {
		b.logger.Error("SearchSimilar failed", "error", err)
		return nil, err
	}
	b.logger.Debug("SearchSimilar completed")
	return resp, nil
}

// SearchByMetadata returns vectors matching metadata filters.
func (b *VectorBinding) SearchByMetadata(req *transport.SearchByMetadataRequest) (*transport.SearchByMetadataResponse, error) {
	b.logger.Debug("SearchByMetadata called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("SearchByMetadata failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().SearchByMetadata(context.Background(), req)
	if err != nil {
		b.logger.Error("SearchByMetadata failed", "error", err)
		return nil, err
	}
	b.logger.Debug("SearchByMetadata completed")
	return resp, nil
}

// HybridSearch combines vector similarity and metadata filtering.
func (b *VectorBinding) HybridSearch(req *transport.HybridSearchRequest) (*transport.HybridSearchResponse, error) {
	b.logger.Debug("HybridSearch called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("HybridSearch failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().HybridSearch(context.Background(), req)
	if err != nil {
		b.logger.Error("HybridSearch failed", "error", err)
		return nil, err
	}
	b.logger.Debug("HybridSearch completed")
	return resp, nil
}

// SearchByID searches using an existing vector's ID as the query.
func (b *VectorBinding) SearchByID(req *transport.SearchByIDRequest) (*transport.SearchByIDResponse, error) {
	b.logger.Debug("SearchByID called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("SearchByID failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().SearchByID(context.Background(), req)
	if err != nil {
		b.logger.Error("SearchByID failed", "error", err)
		return nil, err
	}
	b.logger.Debug("SearchByID completed")
	return resp, nil
}

// RebuildIndex triggers an asynchronous index rebuild for a namespace.
func (b *VectorBinding) RebuildIndex(req *transport.RebuildIndexRequest) (*transport.RebuildIndexResponse, error) {
	b.logger.Debug("RebuildIndex called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("RebuildIndex failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().RebuildIndex(context.Background(), req)
	if err != nil {
		b.logger.Error("RebuildIndex failed", "error", err)
		return nil, err
	}
	b.logger.Debug("RebuildIndex completed")
	return resp, nil
}

// GetRebuildProgress returns the current progress of an index rebuild.
func (b *VectorBinding) GetRebuildProgress(req *transport.GetRebuildProgressRequest) (*transport.GetRebuildProgressResponse, error) {
	b.logger.Debug("GetRebuildProgress called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("GetRebuildProgress failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().GetRebuildProgress(context.Background(), req)
	if err != nil {
		b.logger.Error("GetRebuildProgress failed", "error", err)
		return nil, err
	}
	b.logger.Debug("GetRebuildProgress completed")
	return resp, nil
}

// GetIndexStats returns statistics about a namespace's vector index.
func (b *VectorBinding) GetIndexStats(req *transport.GetIndexStatsRequest) (*transport.GetIndexStatsResponse, error) {
	b.logger.Debug("GetIndexStats called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("GetIndexStats failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().GetIndexStats(context.Background(), req)
	if err != nil {
		b.logger.Error("GetIndexStats failed", "error", err)
		return nil, err
	}
	b.logger.Debug("GetIndexStats completed")
	return resp, nil
}

// CompactNamespace triggers compaction of a namespace's storage.
func (b *VectorBinding) CompactNamespace(req *transport.CompactNamespaceRequest) (*transport.CompactNamespaceResponse, error) {
	b.logger.Debug("CompactNamespace called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("CompactNamespace failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().CompactNamespace(context.Background(), req)
	if err != nil {
		b.logger.Error("CompactNamespace failed", "error", err)
		return nil, err
	}
	b.logger.Debug("CompactNamespace completed")
	return resp, nil
}

// GetCompactionProgress returns the current progress of a compaction job.
func (b *VectorBinding) GetCompactionProgress(req *transport.GetCompactionProgressRequest) (*transport.GetCompactionProgressResponse, error) {
	b.logger.Debug("GetCompactionProgress called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("GetCompactionProgress failed", "error", err)
		return nil, err
	}
	resp, err := cl.Vector().GetCompactionProgress(context.Background(), req)
	if err != nil {
		b.logger.Error("GetCompactionProgress failed", "error", err)
		return nil, err
	}
	b.logger.Debug("GetCompactionProgress completed")
	return resp, nil
}
