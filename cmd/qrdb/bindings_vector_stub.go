//go:build !qrdb_vector

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
	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// VectorBinding is a stub that returns FeatureDisabledError for all
// methods when the vector feature is not compiled in.
type VectorBinding struct {
	logger *logger.Logger
	conn   *ConnectionBinding
}

// NewVectorBinding creates a new VectorBinding backed by the given connection.
func NewVectorBinding(log *logger.Logger, conn *ConnectionBinding) *VectorBinding {
	return &VectorBinding{logger: log, conn: conn}
}

// CreateNamespace returns a FeatureDisabledError.
func (b *VectorBinding) CreateNamespace(_ *transport.CreateNamespaceRequest) (*transport.CreateNamespaceResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}

// DeleteNamespace returns a FeatureDisabledError.
func (b *VectorBinding) DeleteNamespace(_ *transport.DeleteNamespaceRequest) (*transport.DeleteNamespaceResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}

// ListNamespaces returns a FeatureDisabledError.
func (b *VectorBinding) ListNamespaces(_ *transport.ListNamespacesRequest) (*transport.ListNamespacesResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}

// GetNamespaceInfo returns a FeatureDisabledError.
func (b *VectorBinding) GetNamespaceInfo(_ *transport.GetNamespaceInfoRequest) (*transport.GetNamespaceInfoResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}

// UpsertVector returns a FeatureDisabledError.
func (b *VectorBinding) UpsertVector(_ *transport.UpsertVectorRequest) (*transport.UpsertVectorResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}

// GetVector returns a FeatureDisabledError.
func (b *VectorBinding) GetVector(_ *transport.GetVectorRequest) (*transport.GetVectorResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}

// DeleteVector returns a FeatureDisabledError.
func (b *VectorBinding) DeleteVector(_ *transport.DeleteVectorRequest) (*transport.DeleteVectorResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}

// BatchUpsert returns a FeatureDisabledError.
func (b *VectorBinding) BatchUpsert(_ *transport.BatchUpsertRequest) (*transport.BatchUpsertResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}

// BatchDeleteVectors returns a FeatureDisabledError.
func (b *VectorBinding) BatchDeleteVectors(_ *transport.BatchDeleteVectorsRequest) (*transport.BatchDeleteVectorsResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}

// ListVectors returns a FeatureDisabledError.
func (b *VectorBinding) ListVectors(_ *transport.ListVectorsRequest) (*transport.ListVectorsResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}

// UpdateMetadata returns a FeatureDisabledError.
func (b *VectorBinding) UpdateMetadata(_ *transport.UpdateVectorMetadataRequest) (*transport.UpdateVectorMetadataResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}

// SearchSimilar returns a FeatureDisabledError.
func (b *VectorBinding) SearchSimilar(_ *transport.SearchSimilarRequest) (*transport.SearchSimilarResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}

// SearchByMetadata returns a FeatureDisabledError.
func (b *VectorBinding) SearchByMetadata(_ *transport.SearchByMetadataRequest) (*transport.SearchByMetadataResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}

// HybridSearch returns a FeatureDisabledError.
func (b *VectorBinding) HybridSearch(_ *transport.HybridSearchRequest) (*transport.HybridSearchResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}

// SearchByID returns a FeatureDisabledError.
func (b *VectorBinding) SearchByID(_ *transport.SearchByIDRequest) (*transport.SearchByIDResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}

// RebuildIndex returns a FeatureDisabledError.
func (b *VectorBinding) RebuildIndex(_ *transport.RebuildIndexRequest) (*transport.RebuildIndexResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}

// GetRebuildProgress returns a FeatureDisabledError.
func (b *VectorBinding) GetRebuildProgress(_ *transport.GetRebuildProgressRequest) (*transport.GetRebuildProgressResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}

// GetIndexStats returns a FeatureDisabledError.
func (b *VectorBinding) GetIndexStats(_ *transport.GetIndexStatsRequest) (*transport.GetIndexStatsResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}

// CompactNamespace returns a FeatureDisabledError.
func (b *VectorBinding) CompactNamespace(_ *transport.CompactNamespaceRequest) (*transport.CompactNamespaceResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}

// GetCompactionProgress returns a FeatureDisabledError.
func (b *VectorBinding) GetCompactionProgress(_ *transport.GetCompactionProgressRequest) (*transport.GetCompactionProgressResponse, error) {
	return nil, &FeatureDisabledError{Feature: "vector"}
}
