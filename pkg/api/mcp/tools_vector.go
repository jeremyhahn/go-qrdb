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

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// distanceMetricMap maps user-facing distance metric names to the canonical
// string values accepted by the service layer for O(1) dispatch.
var distanceMetricMap = map[string]string{
	"cosine":      "COSINE",
	"l2":          "L2",
	"euclidean":   "L2",
	"dot_product": "DOT_PRODUCT",
	"manhattan":   "MANHATTAN",
}

// vectorIndexTypeMap maps user-facing index type names to the canonical
// string values accepted by the service layer for O(1) dispatch.
var vectorIndexTypeMap = map[string]string{
	"hnsw":     "HNSW",
	"flat":     "FLAT",
	"ivf":      "IVF",
	"ivf_flat": "IVF",
	"ivf-flat": "IVF",
}

// consistencyLevelMap maps string consistency level names to
// ConsistencyLevel constants for O(1) dispatch.
var consistencyLevelMap = map[string]service.ConsistencyLevel{
	"strong":       service.ConsistencyStrong,
	"linearizable": service.ConsistencyLinearizable,
	"eventual":     service.ConsistencyEventual,
}

// vectorTools returns the MCP tool registrations for vector operations.
func vectorTools(vectorSvc service.VectorServicer) []mcpserver.ServerTool {
	return []mcpserver.ServerTool{
		{
			Tool:    toolVectorCreateNamespace(),
			Handler: handleVectorCreateNamespace(vectorSvc),
		},
		{
			Tool:    toolVectorDeleteNamespace(),
			Handler: handleVectorDeleteNamespace(vectorSvc),
		},
		{
			Tool:    toolVectorListNamespaces(),
			Handler: handleVectorListNamespaces(vectorSvc),
		},
		{
			Tool:    toolVectorGetNamespaceInfo(),
			Handler: handleVectorGetNamespaceInfo(vectorSvc),
		},
		{
			Tool:    toolVectorUpsert(),
			Handler: handleVectorUpsert(vectorSvc),
		},
		{
			Tool:    toolVectorGet(),
			Handler: handleVectorGet(vectorSvc),
		},
		{
			Tool:    toolVectorDelete(),
			Handler: handleVectorDelete(vectorSvc),
		},
		{
			Tool:    toolVectorBatchUpsert(),
			Handler: handleVectorBatchUpsert(vectorSvc),
		},
		{
			Tool:    toolVectorBatchDelete(),
			Handler: handleVectorBatchDelete(vectorSvc),
		},
		{
			Tool:    toolVectorList(),
			Handler: handleVectorList(vectorSvc),
		},
		{
			Tool:    toolVectorUpdateMetadata(),
			Handler: handleVectorUpdateMetadata(vectorSvc),
		},
		{
			Tool:    toolVectorSearchSimilar(),
			Handler: handleVectorSearchSimilar(vectorSvc),
		},
		{
			Tool:    toolVectorSearchByMetadata(),
			Handler: handleVectorSearchByMetadata(vectorSvc),
		},
		{
			Tool:    toolVectorHybridSearch(),
			Handler: handleVectorHybridSearch(vectorSvc),
		},
		{
			Tool:    toolVectorSearchByID(),
			Handler: handleVectorSearchByID(vectorSvc),
		},
		{
			Tool:    toolVectorRebuildIndex(),
			Handler: handleVectorRebuildIndex(vectorSvc),
		},
		{
			Tool:    toolVectorGetRebuildProgress(),
			Handler: handleVectorGetRebuildProgress(vectorSvc),
		},
		{
			Tool:    toolVectorGetIndexStats(),
			Handler: handleVectorGetIndexStats(vectorSvc),
		},
		{
			Tool:    toolVectorCompactNamespace(),
			Handler: handleVectorCompactNamespace(vectorSvc),
		},
		{
			Tool:    toolVectorGetCompactionProgress(),
			Handler: handleVectorGetCompactionProgress(vectorSvc),
		},
	}
}

// ---------------------------------------------------------------------------
// Namespace management
// ---------------------------------------------------------------------------

// toolVectorCreateNamespace defines the qrdb_vector_create_namespace tool schema.
func toolVectorCreateNamespace() mcp.Tool {
	return mcp.NewTool("qrdb_vector_create_namespace",
		mcp.WithDescription("Create a new vector namespace with the specified dimensionality, distance metric, and index configuration through Raft consensus."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The unique name for the vector namespace."),
		),
		mcp.WithNumber("dimension",
			mcp.Required(),
			mcp.Description("The fixed dimensionality of vectors in this namespace."),
		),
		mcp.WithString("distance_metric",
			mcp.Required(),
			mcp.Description("The similarity measurement function: cosine, euclidean, dot_product, l2, or manhattan."),
		),
		mcp.WithString("index_type",
			mcp.Required(),
			mcp.Description("The index algorithm: hnsw, flat, or ivf."),
		),
		mcp.WithString("index_config",
			mcp.Description(`Optional JSON object of algorithm-specific index parameters. Example: {"m":"16","ef_construction":"200"}.`),
		),
		mcp.WithNumber("shard_id",
			mcp.Description("The target shard for the namespace."),
		),
	)
}

// handleVectorCreateNamespace returns a tool handler that delegates to
// vectorSvc.CreateNamespace.
func handleVectorCreateNamespace(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		namespace := mcp.ParseString(req, "namespace", "")
		if namespace == "" {
			return mcp.NewToolResultError("namespace is required"), nil
		}
		dimension := mcp.ParseUInt32(req, "dimension", 0)
		if dimension == 0 {
			return mcp.NewToolResultError("dimension must be greater than 0"), nil
		}

		distStr := mcp.ParseString(req, "distance_metric", "")
		if distStr == "" {
			return mcp.NewToolResultError("distance_metric is required"), nil
		}
		distMetric, ok := distanceMetricMap[strings.ToLower(distStr)]
		if !ok {
			return mcp.NewToolResultError("invalid distance_metric: " + distStr + "; must be one of cosine, euclidean, l2, dot_product, manhattan"), nil
		}

		idxStr := mcp.ParseString(req, "index_type", "")
		if idxStr == "" {
			return mcp.NewToolResultError("index_type is required"), nil
		}
		idxType, ok := vectorIndexTypeMap[strings.ToLower(idxStr)]
		if !ok {
			return mcp.NewToolResultError("invalid index_type: " + idxStr + "; must be one of hnsw, flat, ivf"), nil
		}

		var indexConfig map[string]string
		indexConfigStr := mcp.ParseString(req, "index_config", "")
		if indexConfigStr != "" {
			if err := json.Unmarshal([]byte(indexConfigStr), &indexConfig); err != nil {
				return mcp.NewToolResultError("invalid index_config JSON: " + err.Error()), nil
			}
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		resp, err := svc.CreateNamespace(ctx, &service.CreateNamespaceRequest{
			Namespace:      namespace,
			Dimension:      dimension,
			DistanceMetric: distMetric,
			IndexType:      idxType,
			IndexConfig:    indexConfig,
			ShardID:        shardID,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_vector_create_namespace", Err: err}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_create_namespace", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolVectorDeleteNamespace defines the qrdb_vector_delete_namespace tool schema.
func toolVectorDeleteNamespace() mcp.Tool {
	return mcp.NewTool("qrdb_vector_delete_namespace",
		mcp.WithDescription("Remove a vector namespace and all its vectors through Raft consensus."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The name of the namespace to delete."),
		),
		mcp.WithNumber("shard_id",
			mcp.Description("The target shard for the namespace."),
		),
	)
}

// handleVectorDeleteNamespace returns a tool handler that delegates to
// vectorSvc.DeleteNamespace.
func handleVectorDeleteNamespace(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		namespace := mcp.ParseString(req, "namespace", "")
		if namespace == "" {
			return mcp.NewToolResultError("namespace is required"), nil
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		resp, err := svc.DeleteNamespace(ctx, &service.DeleteNamespaceRequest{
			Namespace: namespace,
			ShardID:   shardID,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_vector_delete_namespace", Err: err}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_delete_namespace", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolVectorListNamespaces defines the qrdb_vector_list_namespaces tool schema.
func toolVectorListNamespaces() mcp.Tool {
	return mcp.NewTool("qrdb_vector_list_namespaces",
		mcp.WithDescription("Return all vector namespace names."),
		mcp.WithNumber("shard_id",
			mcp.Description("The target shard to list namespaces from."),
		),
	)
}

// handleVectorListNamespaces returns a tool handler that delegates to
// vectorSvc.ListNamespaces.
func handleVectorListNamespaces(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		resp, err := svc.ListNamespaces(ctx, &service.ListNamespacesRequest{
			ShardID: shardID,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_vector_list_namespaces", Err: err}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_list_namespaces", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolVectorGetNamespaceInfo defines the qrdb_vector_get_namespace_info tool schema.
func toolVectorGetNamespaceInfo() mcp.Tool {
	return mcp.NewTool("qrdb_vector_get_namespace_info",
		mcp.WithDescription("Return metadata about a vector namespace including dimension, distance metric, index type, and vector count."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The name of the namespace to inspect."),
		),
		mcp.WithNumber("shard_id",
			mcp.Description("The target shard for the namespace."),
		),
	)
}

// handleVectorGetNamespaceInfo returns a tool handler that delegates to
// vectorSvc.GetNamespaceInfo.
func handleVectorGetNamespaceInfo(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		namespace := mcp.ParseString(req, "namespace", "")
		if namespace == "" {
			return mcp.NewToolResultError("namespace is required"), nil
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		resp, err := svc.GetNamespaceInfo(ctx, &service.GetNamespaceInfoRequest{
			Namespace: namespace,
			ShardID:   shardID,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_vector_get_namespace_info", Err: err}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_get_namespace_info", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// Vector CRUD
// ---------------------------------------------------------------------------

// toolVectorUpsert defines the qrdb_vector_upsert tool schema.
func toolVectorUpsert() mcp.Tool {
	return mcp.NewTool("qrdb_vector_upsert",
		mcp.WithDescription("Insert or update a vector in the specified namespace through Raft consensus."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The target namespace."),
		),
		mcp.WithNumber("id",
			mcp.Required(),
			mcp.Description("The unique vector identifier (uint64)."),
		),
		mcp.WithString("vector",
			mcp.Required(),
			mcp.Description("The float32 embedding data as a JSON array of numbers. Example: [0.1, 0.2, 0.3]."),
		),
		mcp.WithString("metadata",
			mcp.Description(`Optional JSON object of key-value metadata to attach. Example: {"category":"image","label":"cat"}.`),
		),
		mcp.WithNumber("shard_id",
			mcp.Description("The target shard."),
		),
	)
}

// handleVectorUpsert returns a tool handler that delegates to
// vectorSvc.UpsertVector.
func handleVectorUpsert(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		namespace := mcp.ParseString(req, "namespace", "")
		if namespace == "" {
			return mcp.NewToolResultError("namespace is required"), nil
		}
		id := mcp.ParseUInt64(req, "id", 0)
		if id == 0 {
			return mcp.NewToolResultError("id is required and must be greater than 0"), nil
		}

		vector, err := parseFloat32Array(req, "vector")
		if err != nil {
			return mcp.NewToolResultError("invalid vector JSON: " + err.Error()), nil
		}
		if len(vector) == 0 {
			return mcp.NewToolResultError("vector is required"), nil
		}

		metadata, err := parseMetadata(req)
		if err != nil {
			return mcp.NewToolResultError("invalid metadata JSON: " + err.Error()), nil
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		resp, upsertErr := svc.UpsertVector(ctx, &service.UpsertVectorRequest{
			Namespace: namespace,
			ID:        id,
			Vector:    vector,
			Metadata:  metadata,
			ShardID:   shardID,
		})
		if upsertErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_upsert", Err: upsertErr}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_upsert", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolVectorGet defines the qrdb_vector_get tool schema.
func toolVectorGet() mcp.Tool {
	return mcp.NewTool("qrdb_vector_get",
		mcp.WithDescription("Retrieve a single vector by ID from the specified namespace."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The target namespace."),
		),
		mcp.WithNumber("id",
			mcp.Required(),
			mcp.Description("The vector identifier to retrieve (uint64)."),
		),
		mcp.WithString("consistency",
			mcp.Description("Read consistency level: strong, linearizable, or eventual. Defaults to strong."),
		),
		mcp.WithNumber("shard_id",
			mcp.Description("The target shard."),
		),
	)
}

// handleVectorGet returns a tool handler that delegates to
// vectorSvc.GetVector.
func handleVectorGet(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		namespace := mcp.ParseString(req, "namespace", "")
		if namespace == "" {
			return mcp.NewToolResultError("namespace is required"), nil
		}
		id := mcp.ParseUInt64(req, "id", 0)
		if id == 0 {
			return mcp.NewToolResultError("id is required and must be greater than 0"), nil
		}

		consistency, err := parseConsistencyLevel(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		resp, getErr := svc.GetVector(ctx, &service.GetVectorRequest{
			Namespace:       namespace,
			ID:              id,
			IncludeVector:   true,
			IncludeMetadata: true,
			Consistency:     consistency,
			ShardID:         shardID,
		})
		if getErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_get", Err: getErr}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_get", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolVectorDelete defines the qrdb_vector_delete tool schema.
func toolVectorDelete() mcp.Tool {
	return mcp.NewTool("qrdb_vector_delete",
		mcp.WithDescription("Remove a single vector by ID from the specified namespace through Raft consensus."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The target namespace."),
		),
		mcp.WithNumber("id",
			mcp.Required(),
			mcp.Description("The vector identifier to delete (uint64)."),
		),
		mcp.WithNumber("shard_id",
			mcp.Description("The target shard."),
		),
	)
}

// handleVectorDelete returns a tool handler that delegates to
// vectorSvc.DeleteVector.
func handleVectorDelete(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		namespace := mcp.ParseString(req, "namespace", "")
		if namespace == "" {
			return mcp.NewToolResultError("namespace is required"), nil
		}
		id := mcp.ParseUInt64(req, "id", 0)
		if id == 0 {
			return mcp.NewToolResultError("id is required and must be greater than 0"), nil
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		resp, err := svc.DeleteVector(ctx, &service.DeleteVectorRequest{
			Namespace: namespace,
			ID:        id,
			ShardID:   shardID,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_vector_delete", Err: err}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_delete", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// Batch operations
// ---------------------------------------------------------------------------

// vectorDataJSON is the JSON representation of a vector for batch
// upsert operations.
type vectorDataJSON struct {
	ID       uint64                 `json:"id"`
	Vector   []float32              `json:"vector"`
	Metadata map[string]interface{} `json:"metadata"`
}

// toolVectorBatchUpsert defines the qrdb_vector_batch_upsert tool schema.
func toolVectorBatchUpsert() mcp.Tool {
	return mcp.NewTool("qrdb_vector_batch_upsert",
		mcp.WithDescription("Insert or update multiple vectors in a single operation through Raft consensus."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The target namespace."),
		),
		mcp.WithString("vectors",
			mcp.Required(),
			mcp.Description(`JSON array of vector objects. Each element: {"id":123,"vector":[<floats>],"metadata":{"key":"value"}}.`),
		),
		mcp.WithNumber("shard_id",
			mcp.Description("The target shard."),
		),
	)
}

// handleVectorBatchUpsert returns a tool handler that delegates to
// vectorSvc.BatchUpsert.
func handleVectorBatchUpsert(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		namespace := mcp.ParseString(req, "namespace", "")
		if namespace == "" {
			return mcp.NewToolResultError("namespace is required"), nil
		}

		vectorsStr := mcp.ParseString(req, "vectors", "")
		if vectorsStr == "" {
			return mcp.NewToolResultError("vectors is required"), nil
		}

		var vectorsJSON []vectorDataJSON
		if err := json.Unmarshal([]byte(vectorsStr), &vectorsJSON); err != nil {
			return mcp.NewToolResultError("invalid vectors JSON: " + err.Error()), nil
		}

		if len(vectorsJSON) == 0 {
			return mcp.NewToolResultError("vectors must not be empty"), nil
		}

		vectors := make([]*service.VectorData, len(vectorsJSON))
		for i, v := range vectorsJSON {
			vectors[i] = &service.VectorData{
				ID:       v.ID,
				Vector:   v.Vector,
				Metadata: v.Metadata,
			}
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		resp, err := svc.BatchUpsert(ctx, &service.BatchUpsertRequest{
			Namespace: namespace,
			Vectors:   vectors,
			ShardID:   shardID,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_vector_batch_upsert", Err: err}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_batch_upsert", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolVectorBatchDelete defines the qrdb_vector_batch_delete tool schema.
func toolVectorBatchDelete() mcp.Tool {
	return mcp.NewTool("qrdb_vector_batch_delete",
		mcp.WithDescription("Remove multiple vectors by ID in a single operation through Raft consensus."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The target namespace."),
		),
		mcp.WithString("ids",
			mcp.Required(),
			mcp.Description(`JSON array of vector identifiers (uint64) to delete. Example: [1,2,3].`),
		),
		mcp.WithNumber("shard_id",
			mcp.Description("The target shard."),
		),
	)
}

// handleVectorBatchDelete returns a tool handler that delegates to
// vectorSvc.BatchDeleteVectors.
func handleVectorBatchDelete(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		namespace := mcp.ParseString(req, "namespace", "")
		if namespace == "" {
			return mcp.NewToolResultError("namespace is required"), nil
		}

		idsStr := mcp.ParseString(req, "ids", "")
		if idsStr == "" {
			return mcp.NewToolResultError("ids is required"), nil
		}

		var ids []uint64
		if err := json.Unmarshal([]byte(idsStr), &ids); err != nil {
			return mcp.NewToolResultError("invalid ids JSON: " + err.Error()), nil
		}

		if len(ids) == 0 {
			return mcp.NewToolResultError("ids must not be empty"), nil
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		resp, err := svc.BatchDeleteVectors(ctx, &service.BatchDeleteVectorsRequest{
			Namespace: namespace,
			IDs:       ids,
			ShardID:   shardID,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_vector_batch_delete", Err: err}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_batch_delete", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// List and metadata
// ---------------------------------------------------------------------------

// toolVectorList defines the qrdb_vector_list tool schema.
func toolVectorList() mcp.Tool {
	return mcp.NewTool("qrdb_vector_list",
		mcp.WithDescription("Return a paginated list of vectors in a namespace."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The target namespace."),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of vectors to return."),
		),
		mcp.WithString("cursor",
			mcp.Description("Opaque token for cursor-based pagination from a previous response."),
		),
		mcp.WithNumber("shard_id",
			mcp.Description("The target shard."),
		),
	)
}

// handleVectorList returns a tool handler that delegates to
// vectorSvc.ListVectors.
func handleVectorList(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		namespace := mcp.ParseString(req, "namespace", "")
		if namespace == "" {
			return mcp.NewToolResultError("namespace is required"), nil
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		resp, err := svc.ListVectors(ctx, &service.ListVectorsRequest{
			Namespace: namespace,
			Limit:     mcp.ParseInt(req, "limit", 0),
			Cursor:    mcp.ParseString(req, "cursor", ""),
			ShardID:   shardID,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_vector_list", Err: err}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_list", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolVectorUpdateMetadata defines the qrdb_vector_update_metadata tool schema.
func toolVectorUpdateMetadata() mcp.Tool {
	return mcp.NewTool("qrdb_vector_update_metadata",
		mcp.WithDescription("Update the metadata for an existing vector without modifying the vector data through Raft consensus."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The target namespace."),
		),
		mcp.WithNumber("id",
			mcp.Required(),
			mcp.Description("The vector identifier to update (uint64)."),
		),
		mcp.WithString("metadata",
			mcp.Required(),
			mcp.Description(`JSON object of new key-value metadata to set. Example: {"category":"updated","score":0.95}.`),
		),
		mcp.WithNumber("shard_id",
			mcp.Description("The target shard."),
		),
	)
}

// handleVectorUpdateMetadata returns a tool handler that delegates to
// vectorSvc.UpdateMetadata.
func handleVectorUpdateMetadata(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		namespace := mcp.ParseString(req, "namespace", "")
		if namespace == "" {
			return mcp.NewToolResultError("namespace is required"), nil
		}
		id := mcp.ParseUInt64(req, "id", 0)
		if id == 0 {
			return mcp.NewToolResultError("id is required and must be greater than 0"), nil
		}

		metadata, err := parseMetadata(req)
		if err != nil {
			return mcp.NewToolResultError("invalid metadata JSON: " + err.Error()), nil
		}
		if len(metadata) == 0 {
			return mcp.NewToolResultError("metadata is required"), nil
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		resp, updateErr := svc.UpdateMetadata(ctx, &service.UpdateVectorMetadataRequest{
			Namespace: namespace,
			ID:        id,
			Metadata:  metadata,
			ShardID:   shardID,
		})
		if updateErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_update_metadata", Err: updateErr}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_update_metadata", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// Search operations
// ---------------------------------------------------------------------------

// toolVectorSearchSimilar defines the qrdb_vector_search_similar tool schema.
func toolVectorSearchSimilar() mcp.Tool {
	return mcp.NewTool("qrdb_vector_search_similar",
		mcp.WithDescription("Perform a k-nearest-neighbor search using a query vector."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The target namespace."),
		),
		mcp.WithString("vector",
			mcp.Required(),
			mcp.Description("The query vector as a JSON array of numbers. Example: [0.1, 0.2, 0.3]."),
		),
		mcp.WithNumber("k",
			mcp.Required(),
			mcp.Description("The number of nearest neighbors to return."),
		),
		mcp.WithNumber("ef_search",
			mcp.Description("Search accuracy-speed tradeoff for HNSW indexes. Higher values yield more accurate results."),
		),
		mcp.WithNumber("min_score",
			mcp.Description("Filter results below this similarity threshold."),
		),
		mcp.WithString("consistency",
			mcp.Description("Read consistency level: strong, linearizable, or eventual. Defaults to strong."),
		),
		mcp.WithNumber("shard_id",
			mcp.Description("The target shard."),
		),
	)
}

// handleVectorSearchSimilar returns a tool handler that delegates to
// vectorSvc.SearchSimilar.
func handleVectorSearchSimilar(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		namespace := mcp.ParseString(req, "namespace", "")
		if namespace == "" {
			return mcp.NewToolResultError("namespace is required"), nil
		}

		vector, err := parseFloat32Array(req, "vector")
		if err != nil {
			return mcp.NewToolResultError("invalid vector JSON: " + err.Error()), nil
		}
		if len(vector) == 0 {
			return mcp.NewToolResultError("vector is required"), nil
		}

		k := mcp.ParseInt(req, "k", 0)
		if k <= 0 {
			return mcp.NewToolResultError("k must be greater than 0"), nil
		}

		consistency, err := parseConsistencyLevel(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		resp, searchErr := svc.SearchSimilar(ctx, &service.SearchSimilarRequest{
			Namespace:   namespace,
			QueryVector: vector,
			K:           k,
			EfSearch:    mcp.ParseInt(req, "ef_search", 0),
			MinScore:    mcp.ParseFloat32(req, "min_score", 0),
			Consistency: consistency,
			ShardID:     shardID,
		})
		if searchErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_search_similar", Err: searchErr}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_search_similar", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolVectorSearchByMetadata defines the qrdb_vector_search_by_metadata tool schema.
func toolVectorSearchByMetadata() mcp.Tool {
	return mcp.NewTool("qrdb_vector_search_by_metadata",
		mcp.WithDescription("Return vectors matching the given metadata filters."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The target namespace."),
		),
		mcp.WithString("filters",
			mcp.Required(),
			mcp.Description(`JSON object of key-value pairs that must match vector metadata. Example: {"category":"image","label":"cat"}.`),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results to return."),
		),
		mcp.WithNumber("offset",
			mcp.Description("Number of results to skip for pagination."),
		),
		mcp.WithString("consistency",
			mcp.Description("Read consistency level: strong, linearizable, or eventual. Defaults to strong."),
		),
		mcp.WithNumber("shard_id",
			mcp.Description("The target shard."),
		),
	)
}

// handleVectorSearchByMetadata returns a tool handler that delegates to
// vectorSvc.SearchByMetadata.
func handleVectorSearchByMetadata(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		namespace := mcp.ParseString(req, "namespace", "")
		if namespace == "" {
			return mcp.NewToolResultError("namespace is required"), nil
		}

		filtersStr := mcp.ParseString(req, "filters", "")
		if filtersStr == "" {
			return mcp.NewToolResultError("filters is required"), nil
		}

		var filters map[string]interface{}
		if err := json.Unmarshal([]byte(filtersStr), &filters); err != nil {
			return mcp.NewToolResultError("invalid filters JSON: " + err.Error()), nil
		}

		consistency, err := parseConsistencyLevel(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		resp, searchErr := svc.SearchByMetadata(ctx, &service.SearchByMetadataRequest{
			Namespace:   namespace,
			Filters:     filters,
			Limit:       mcp.ParseInt(req, "limit", 0),
			Offset:      mcp.ParseInt(req, "offset", 0),
			Consistency: consistency,
			ShardID:     shardID,
		})
		if searchErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_search_by_metadata", Err: searchErr}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_search_by_metadata", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolVectorHybridSearch defines the qrdb_vector_hybrid_search tool schema.
func toolVectorHybridSearch() mcp.Tool {
	return mcp.NewTool("qrdb_vector_hybrid_search",
		mcp.WithDescription("Combine vector similarity and metadata filtering for hybrid search."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The target namespace."),
		),
		mcp.WithString("vector",
			mcp.Required(),
			mcp.Description("The query vector as a JSON array of numbers. Example: [0.1, 0.2, 0.3]."),
		),
		mcp.WithString("metadata_filter",
			mcp.Required(),
			mcp.Description(`JSON object of key-value pairs that candidates must match. Example: {"category":"image"}.`),
		),
		mcp.WithNumber("k",
			mcp.Required(),
			mcp.Description("The number of results to return."),
		),
		mcp.WithNumber("ef_search",
			mcp.Description("Search accuracy-speed tradeoff for HNSW indexes."),
		),
		mcp.WithNumber("min_score",
			mcp.Description("Filter results below this similarity threshold."),
		),
		mcp.WithString("filter_operator",
			mcp.Description("How to combine metadata filters: AND or OR. Defaults to AND."),
		),
		mcp.WithString("reranking_algorithm",
			mcp.Description("The reranking strategy to apply to results."),
		),
		mcp.WithString("consistency",
			mcp.Description("Read consistency level: strong, linearizable, or eventual. Defaults to strong."),
		),
		mcp.WithNumber("shard_id",
			mcp.Description("The target shard."),
		),
	)
}

// handleVectorHybridSearch returns a tool handler that delegates to
// vectorSvc.HybridSearch.
func handleVectorHybridSearch(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		namespace := mcp.ParseString(req, "namespace", "")
		if namespace == "" {
			return mcp.NewToolResultError("namespace is required"), nil
		}

		vector, err := parseFloat32Array(req, "vector")
		if err != nil {
			return mcp.NewToolResultError("invalid vector JSON: " + err.Error()), nil
		}
		if len(vector) == 0 {
			return mcp.NewToolResultError("vector is required"), nil
		}

		filtersStr := mcp.ParseString(req, "metadata_filter", "")
		if filtersStr == "" {
			return mcp.NewToolResultError("metadata_filter is required"), nil
		}

		var metadataFilter map[string]interface{}
		if err := json.Unmarshal([]byte(filtersStr), &metadataFilter); err != nil {
			return mcp.NewToolResultError("invalid metadata_filter JSON: " + err.Error()), nil
		}

		k := mcp.ParseInt(req, "k", 0)
		if k <= 0 {
			return mcp.NewToolResultError("k must be greater than 0"), nil
		}

		consistency, err := parseConsistencyLevel(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		resp, searchErr := svc.HybridSearch(ctx, &service.HybridSearchRequest{
			Namespace:          namespace,
			QueryVector:        vector,
			MetadataFilter:     metadataFilter,
			K:                  k,
			EfSearch:           mcp.ParseInt(req, "ef_search", 0),
			MinScore:           mcp.ParseFloat32(req, "min_score", 0),
			FilterOperator:     mcp.ParseString(req, "filter_operator", ""),
			RerankingAlgorithm: mcp.ParseString(req, "reranking_algorithm", ""),
			Consistency:        consistency,
			ShardID:            shardID,
		})
		if searchErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_hybrid_search", Err: searchErr}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_hybrid_search", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolVectorSearchByID defines the qrdb_vector_search_by_id tool schema.
func toolVectorSearchByID() mcp.Tool {
	return mcp.NewTool("qrdb_vector_search_by_id",
		mcp.WithDescription("Perform a k-nearest-neighbor search using an existing vector's ID as the query, returning its most similar neighbors."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The target namespace."),
		),
		mcp.WithNumber("id",
			mcp.Required(),
			mcp.Description("The vector identifier to use as the search query (uint64)."),
		),
		mcp.WithNumber("k",
			mcp.Required(),
			mcp.Description("The number of nearest neighbors to return."),
		),
		mcp.WithString("consistency",
			mcp.Description("Read consistency level: strong, linearizable, or eventual. Defaults to strong."),
		),
		mcp.WithNumber("shard_id",
			mcp.Description("The target shard."),
		),
	)
}

// handleVectorSearchByID returns a tool handler that delegates to
// vectorSvc.SearchByID.
func handleVectorSearchByID(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		namespace := mcp.ParseString(req, "namespace", "")
		if namespace == "" {
			return mcp.NewToolResultError("namespace is required"), nil
		}
		id := mcp.ParseUInt64(req, "id", 0)
		if id == 0 {
			return mcp.NewToolResultError("id is required and must be greater than 0"), nil
		}

		k := mcp.ParseInt(req, "k", 0)
		if k <= 0 {
			return mcp.NewToolResultError("k must be greater than 0"), nil
		}

		consistency, err := parseConsistencyLevel(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		resp, searchErr := svc.SearchByID(ctx, &service.SearchByIDRequest{
			Namespace:   namespace,
			ID:          id,
			K:           k,
			Consistency: consistency,
			ShardID:     shardID,
		})
		if searchErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_search_by_id", Err: searchErr}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_search_by_id", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// Index management
// ---------------------------------------------------------------------------

// toolVectorRebuildIndex defines the qrdb_vector_rebuild_index tool schema.
func toolVectorRebuildIndex() mcp.Tool {
	return mcp.NewTool("qrdb_vector_rebuild_index",
		mcp.WithDescription("Trigger an asynchronous index rebuild for a vector namespace."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The target namespace."),
		),
		mcp.WithNumber("shard_id",
			mcp.Description("The target shard."),
		),
	)
}

// handleVectorRebuildIndex returns a tool handler that delegates to
// vectorSvc.RebuildIndex.
func handleVectorRebuildIndex(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		namespace := mcp.ParseString(req, "namespace", "")
		if namespace == "" {
			return mcp.NewToolResultError("namespace is required"), nil
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		resp, err := svc.RebuildIndex(ctx, &service.RebuildIndexRequest{
			Namespace: namespace,
			ShardID:   shardID,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_vector_rebuild_index", Err: err}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_rebuild_index", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolVectorGetRebuildProgress defines the qrdb_vector_get_rebuild_progress
// tool schema.
func toolVectorGetRebuildProgress() mcp.Tool {
	return mcp.NewTool("qrdb_vector_get_rebuild_progress",
		mcp.WithDescription("Return the current progress of an index rebuild job."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The target namespace."),
		),
		mcp.WithString("job_id",
			mcp.Required(),
			mcp.Description("The rebuild job identifier."),
		),
	)
}

// handleVectorGetRebuildProgress returns a tool handler that delegates to
// vectorSvc.GetRebuildProgress.
func handleVectorGetRebuildProgress(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		namespace := mcp.ParseString(req, "namespace", "")
		if namespace == "" {
			return mcp.NewToolResultError("namespace is required"), nil
		}
		jobID := mcp.ParseString(req, "job_id", "")
		if jobID == "" {
			return mcp.NewToolResultError("job_id is required"), nil
		}

		progress, err := svc.GetRebuildProgress(ctx, &service.GetRebuildProgressRequest{
			Namespace: namespace,
			JobID:     jobID,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_vector_get_rebuild_progress", Err: err}
		}

		data, jsonErr := json.Marshal(progress)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_get_rebuild_progress", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolVectorGetIndexStats defines the qrdb_vector_get_index_stats tool schema.
func toolVectorGetIndexStats() mcp.Tool {
	return mcp.NewTool("qrdb_vector_get_index_stats",
		mcp.WithDescription("Return statistics about a namespace's vector index including vector count, memory usage, and disk usage."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The target namespace."),
		),
		mcp.WithNumber("shard_id",
			mcp.Description("The target shard."),
		),
	)
}

// handleVectorGetIndexStats returns a tool handler that delegates to
// vectorSvc.GetIndexStats.
func handleVectorGetIndexStats(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		namespace := mcp.ParseString(req, "namespace", "")
		if namespace == "" {
			return mcp.NewToolResultError("namespace is required"), nil
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		resp, err := svc.GetIndexStats(ctx, &service.GetIndexStatsRequest{
			Namespace: namespace,
			ShardID:   shardID,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_vector_get_index_stats", Err: err}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_get_index_stats", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// Compaction
// ---------------------------------------------------------------------------

// toolVectorCompactNamespace defines the qrdb_vector_compact_namespace tool schema.
func toolVectorCompactNamespace() mcp.Tool {
	return mcp.NewTool("qrdb_vector_compact_namespace",
		mcp.WithDescription("Trigger an asynchronous compaction of a namespace's storage to reclaim space from deleted vectors."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The target namespace."),
		),
		mcp.WithNumber("shard_id",
			mcp.Description("The target shard."),
		),
	)
}

// handleVectorCompactNamespace returns a tool handler that delegates to
// vectorSvc.CompactNamespace.
func handleVectorCompactNamespace(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		namespace := mcp.ParseString(req, "namespace", "")
		if namespace == "" {
			return mcp.NewToolResultError("namespace is required"), nil
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		resp, err := svc.CompactNamespace(ctx, &service.CompactNamespaceRequest{
			Namespace: namespace,
			ShardID:   shardID,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_vector_compact_namespace", Err: err}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_compact_namespace", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolVectorGetCompactionProgress defines the qrdb_vector_get_compaction_progress
// tool schema.
func toolVectorGetCompactionProgress() mcp.Tool {
	return mcp.NewTool("qrdb_vector_get_compaction_progress",
		mcp.WithDescription("Return the current progress of a compaction job."),
		mcp.WithString("namespace",
			mcp.Required(),
			mcp.Description("The target namespace."),
		),
		mcp.WithString("operation_id",
			mcp.Required(),
			mcp.Description("The compaction operation identifier."),
		),
		mcp.WithNumber("shard_id",
			mcp.Description("The target shard."),
		),
	)
}

// handleVectorGetCompactionProgress returns a tool handler that delegates to
// vectorSvc.GetCompactionProgress.
func handleVectorGetCompactionProgress(svc service.VectorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		svc, rerr := resolveVector(ctx, svc)
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		namespace := mcp.ParseString(req, "namespace", "")
		if namespace == "" {
			return mcp.NewToolResultError("namespace is required"), nil
		}
		operationID := mcp.ParseString(req, "operation_id", "")
		if operationID == "" {
			return mcp.NewToolResultError("operation_id is required"), nil
		}

		shardID := mcp.ParseUInt64(req, "shard_id", 0)

		progress, err := svc.GetCompactionProgress(ctx, &service.GetCompactionProgressRequest{
			Namespace:   namespace,
			OperationID: operationID,
			ShardID:     shardID,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_vector_get_compaction_progress", Err: err}
		}

		data, jsonErr := json.Marshal(progress)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_vector_get_compaction_progress", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

// parseFloat32Array extracts a named parameter from a request and parses
// it as a JSON array of numbers into a []float32 slice. Returns nil
// when the parameter is absent or empty.
func parseFloat32Array(req mcp.CallToolRequest, name string) ([]float32, error) {
	raw := mcp.ParseString(req, name, "")
	if raw == "" {
		return nil, nil
	}

	var f64s []float64
	if err := json.Unmarshal([]byte(raw), &f64s); err != nil {
		return nil, err
	}

	result := make([]float32, len(f64s))
	for i, v := range f64s {
		result[i] = float32(v)
	}
	return result, nil
}

// parseMetadata extracts the optional "metadata" parameter from a request
// and parses it as a JSON object of arbitrary key-value pairs. Returns nil
// when the parameter is absent or empty.
func parseMetadata(req mcp.CallToolRequest) (map[string]interface{}, error) {
	metaStr := mcp.ParseString(req, "metadata", "")
	if metaStr == "" {
		return nil, nil
	}

	var metadata map[string]interface{}
	if err := json.Unmarshal([]byte(metaStr), &metadata); err != nil {
		return nil, err
	}
	return metadata, nil
}

// parseConsistencyLevel extracts the optional "consistency" parameter from
// a request and resolves it to a ConsistencyLevel constant. Returns
// ConsistencyStrong when the parameter is absent or empty.
func parseConsistencyLevel(req mcp.CallToolRequest) (service.ConsistencyLevel, error) {
	levelStr := mcp.ParseString(req, "consistency", "")
	if levelStr == "" {
		return service.ConsistencyStrong, nil
	}

	level, ok := consistencyLevelMap[levelStr]
	if !ok {
		return 0, &InvalidConsistencyError{Value: levelStr}
	}
	return level, nil
}

// InvalidConsistencyError is returned when an unrecognized consistency
// level string is provided.
type InvalidConsistencyError struct {
	Value string
}

// Error returns a human-readable description of the invalid consistency level.
func (e *InvalidConsistencyError) Error() string {
	return fmt.Sprintf("invalid consistency: %s; must be one of strong, linearizable, eventual", e.Value)
}
