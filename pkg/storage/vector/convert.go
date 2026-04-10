// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package vector

import (
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector/schema"
)

// toSchemaVectorValue converts a VectorValue to schema.VectorValue.
func toSchemaVectorValue(v *VectorValue) *schema.VectorValue {
	if v == nil {
		return nil
	}
	return &schema.VectorValue{
		ID:        v.ID,
		Embedding: v.Embedding,
		Metadata:  v.Metadata,
		CreatedAt: v.CreatedAt,
		UpdatedAt: v.UpdatedAt,
		Version:   v.Version,
	}
}

// fromSchemaVectorValue converts a schema.VectorValue to VectorValue.
func fromSchemaVectorValue(v *schema.VectorValue) *VectorValue {
	if v == nil {
		return nil
	}
	return &VectorValue{
		ID:        v.ID,
		Embedding: v.Embedding,
		Metadata:  v.Metadata,
		CreatedAt: v.CreatedAt,
		UpdatedAt: v.UpdatedAt,
		Version:   v.Version,
	}
}

// toSchemaNamespaceInfo converts a NamespaceInfo to schema.NamespaceInfo.
func toSchemaNamespaceInfo(n *NamespaceInfo) *schema.NamespaceInfo {
	if n == nil {
		return nil
	}
	return &schema.NamespaceInfo{
		Name:           n.Name,
		VectorCount:    n.VectorCount,
		Dimension:      n.Dimension,
		CreatedAt:      n.CreatedAt,
		UpdatedAt:      n.UpdatedAt,
		DistanceMetric: n.DistanceMetric,
		IndexType:      n.IndexType,
		IndexConfig:    n.IndexConfig,
		Metadata:       n.Metadata,
		TenantID:       n.TenantID,
		OwnerUserID:    n.OwnerUserID,
		Labels:         n.Labels,
		StorageBytes:   n.StorageBytes,
	}
}

// fromSchemaNamespaceInfo converts a schema.NamespaceInfo to NamespaceInfo.
func fromSchemaNamespaceInfo(n *schema.NamespaceInfo) *NamespaceInfo {
	if n == nil {
		return nil
	}
	return &NamespaceInfo{
		Name:           n.Name,
		VectorCount:    n.VectorCount,
		Dimension:      n.Dimension,
		CreatedAt:      n.CreatedAt,
		UpdatedAt:      n.UpdatedAt,
		DistanceMetric: n.DistanceMetric,
		IndexType:      n.IndexType,
		IndexConfig:    n.IndexConfig,
		Metadata:       n.Metadata,
		TenantID:       n.TenantID,
		OwnerUserID:    n.OwnerUserID,
		Labels:         n.Labels,
		StorageBytes:   n.StorageBytes,
	}
}
