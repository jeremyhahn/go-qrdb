//go:build !integration

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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// vectorTools registration test
// ---------------------------------------------------------------------------

func TestVectorTools_Registration(t *testing.T) {
	svc := &mockVectorService{}
	tools := vectorTools(svc)

	expectedNames := []string{
		"qrdb_vector_create_namespace",
		"qrdb_vector_update_namespace",
		"qrdb_vector_delete_namespace",
		"qrdb_vector_list_namespaces",
		"qrdb_vector_get_namespace_info",
		"qrdb_vector_upsert",
		"qrdb_vector_get",
		"qrdb_vector_delete",
		"qrdb_vector_batch_upsert",
		"qrdb_vector_batch_delete",
		"qrdb_vector_list",
		"qrdb_vector_update_metadata",
		"qrdb_vector_search_similar",
		"qrdb_vector_search_by_metadata",
		"qrdb_vector_hybrid_search",
		"qrdb_vector_search_by_id",
		"qrdb_vector_rebuild_index",
		"qrdb_vector_get_rebuild_progress",
		"qrdb_vector_get_index_stats",
		"qrdb_vector_compact_namespace",
		"qrdb_vector_get_compaction_progress",
	}

	registered := make(map[string]bool, len(tools))
	for _, tool := range tools {
		registered[tool.Tool.Name] = true
	}

	for _, name := range expectedNames {
		if !registered[name] {
			t.Errorf("tool %q not registered", name)
		}
	}

	if len(tools) != len(expectedNames) {
		t.Errorf("expected %d tools, got %d", len(expectedNames), len(tools))
	}
}

func TestVectorTools_RegistrationReturnsNonEmptySlice(t *testing.T) {
	svc := &mockVectorService{}
	tools := vectorTools(svc)
	if len(tools) == 0 {
		t.Fatal("vectorTools returned empty slice")
	}
	for _, tool := range tools {
		if tool.Tool.Name == "" {
			t.Error("tool has empty name")
		}
		if tool.Handler == nil {
			t.Errorf("tool %q has nil handler", tool.Tool.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// CreateNamespace
// ---------------------------------------------------------------------------

func TestHandleVectorCreateNamespace_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_create_namespace")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace":       "embeddings",
		"dimension":       float64(128),
		"distance_metric": "cosine",
		"index_type":      "hnsw",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result")
	}
}

func TestHandleVectorCreateNamespace_MissingNamespace(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_create_namespace")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"dimension": float64(128),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing namespace")
	}
}

func TestHandleVectorCreateNamespace_MissingDimension(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_create_namespace")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing dimension")
	}
}

func TestHandleVectorCreateNamespace_InvalidDistanceMetric(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_create_namespace")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace":       "embeddings",
		"dimension":       float64(128),
		"distance_metric": "invalid_metric",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid distance metric")
	}
}

func TestHandleVectorCreateNamespace_InvalidIndexType(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_create_namespace")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace":  "embeddings",
		"dimension":  float64(128),
		"index_type": "invalid_type",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid index type")
	}
}

func TestHandleVectorCreateNamespace_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		createNamespaceFn: func(_ context.Context, _ *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
			return nil, errors.New("already exists")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_create_namespace")

	_, err := handler(context.Background(), makeReq(map[string]any{
		"namespace":       "embeddings",
		"dimension":       float64(128),
		"distance_metric": "cosine",
		"index_type":      "hnsw",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

func TestHandleVectorCreateNamespace_WithAllOptions(t *testing.T) {
	var captured *service.CreateNamespaceRequest
	svc := &mockVectorService{
		createNamespaceFn: func(_ context.Context, req *service.CreateNamespaceRequest) (*service.CreateNamespaceResponse, error) {
			captured = req
			return &service.CreateNamespaceResponse{Namespace: req.Namespace, Dimension: req.Dimension, Success: true}, nil
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_create_namespace")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace":       "embeddings",
		"dimension":       float64(256),
		"distance_metric": "l2",
		"index_type":      "flat",
		"metadata":        `{"env":"test"}`,
		"index_config":    `{"m":"16","ef_construction":"200"}`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("unexpected error result")
	}
	if captured == nil {
		t.Fatal("service not called")
	}
	// MCP forwards the lowercase-normalized metric/index verbatim to the
	// service, matching REST/gRPC/QUIC/Unix (transport parity): the input "l2"
	// and "flat" reach the service as "l2"/"flat", not the uppercased "L2"/"FLAT".
	if captured.DistanceMetric != "l2" {
		t.Errorf("expected distance metric l2, got %q", captured.DistanceMetric)
	}
	if captured.IndexType != "flat" {
		t.Errorf("expected index type flat, got %q", captured.IndexType)
	}
	if captured.Dimension != 256 {
		t.Errorf("expected dimension 256, got %d", captured.Dimension)
	}
}

func TestHandleVectorCreateNamespace_InvalidMetadata(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_create_namespace")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"dimension": float64(128),
		"metadata":  "not-json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid metadata")
	}
}

func TestHandleVectorCreateNamespace_InvalidIndexConfig(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_create_namespace")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace":    "embeddings",
		"dimension":    float64(128),
		"index_config": "not-json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid index config")
	}
}

// ---------------------------------------------------------------------------
// DeleteNamespace
// ---------------------------------------------------------------------------

func TestHandleVectorDeleteNamespace_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_delete_namespace")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result")
	}
}

func TestHandleVectorDeleteNamespace_MissingNamespace(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_delete_namespace")

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing namespace")
	}
}

func TestHandleVectorDeleteNamespace_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		deleteNamespaceFn: func(_ context.Context, _ *service.DeleteNamespaceRequest) (*service.DeleteNamespaceResponse, error) {
			return nil, errors.New("not found")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_delete_namespace")

	_, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "missing",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// ListNamespaces
// ---------------------------------------------------------------------------

func TestHandleVectorListNamespaces_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_list_namespaces")

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var resp service.ListNamespacesResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.TotalCount != 1 {
		t.Errorf("expected 1 namespace, got %d", resp.TotalCount)
	}
}

func TestHandleVectorListNamespaces_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		listNamespacesFn: func(_ context.Context, _ *service.ListNamespacesRequest) (*service.ListNamespacesResponse, error) {
			return nil, errors.New("internal error")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_list_namespaces")

	_, err := handler(context.Background(), makeReq(map[string]any{}))
	if err == nil {
		t.Fatal("expected error from service")
	}
}

// ---------------------------------------------------------------------------
// GetNamespaceInfo
// ---------------------------------------------------------------------------

func TestHandleVectorGetNamespaceInfo_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_get_namespace_info")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var resp service.GetNamespaceInfoResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.Namespace != "embeddings" {
		t.Errorf("expected namespace 'embeddings', got %q", resp.Namespace)
	}
}

func TestHandleVectorGetNamespaceInfo_MissingNamespace(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_get_namespace_info")

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing namespace")
	}
}

func TestHandleVectorGetNamespaceInfo_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		getNamespaceInfoFn: func(_ context.Context, _ *service.GetNamespaceInfoRequest) (*service.GetNamespaceInfoResponse, error) {
			return nil, errors.New("not found")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_get_namespace_info")

	_, err := handler(context.Background(), makeReq(map[string]any{"namespace": "missing"}))
	if err == nil {
		t.Fatal("expected error from service")
	}
}

// ---------------------------------------------------------------------------
// UpsertVector
// ---------------------------------------------------------------------------

func TestHandleVectorUpsert_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_upsert")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"id":        float64(42),
		"vector":    "[1.0, 2.0, 3.0]",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	var resp service.UpsertVectorResponse
	if jsonErr := json.Unmarshal([]byte(text), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.ID != 42 {
		t.Errorf("expected ID 42, got %d", resp.ID)
	}
}

func TestHandleVectorUpsert_MissingNamespace(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_upsert")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"id":     float64(42),
		"vector": "[1.0]",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing namespace")
	}
}

func TestHandleVectorUpsert_MissingVector(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_upsert")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"id":        float64(42),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing vector")
	}
}

func TestHandleVectorUpsert_InvalidVector(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_upsert")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"id":        float64(42),
		"vector":    "not-json-array",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid vector")
	}
}

func TestHandleVectorUpsert_WithMetadata(t *testing.T) {
	var captured *service.UpsertVectorRequest
	svc := &mockVectorService{
		upsertVectorFn: func(_ context.Context, req *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
			captured = req
			return &service.UpsertVectorResponse{ID: req.ID, Version: 1, Success: true}, nil
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_upsert")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"id":        float64(42),
		"vector":    "[1.0, 2.0]",
		"metadata":  `{"key":"value"}`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("unexpected error result")
	}
	if captured == nil {
		t.Fatal("service not called")
	}
	if captured.Metadata == nil || captured.Metadata["key"] != "value" {
		t.Errorf("metadata not passed correctly: %v", captured.Metadata)
	}
}

func TestHandleVectorUpsert_InvalidMetadata(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_upsert")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"id":        float64(42),
		"vector":    "[1.0]",
		"metadata":  "not-json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid metadata")
	}
}

func TestHandleVectorUpsert_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		upsertVectorFn: func(_ context.Context, _ *service.UpsertVectorRequest) (*service.UpsertVectorResponse, error) {
			return nil, errors.New("raft timeout")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_upsert")

	_, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"id":        float64(42),
		"vector":    "[1.0]",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
}

// ---------------------------------------------------------------------------
// GetVector
// ---------------------------------------------------------------------------

func TestHandleVectorGet_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_get")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"id":        float64(42),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result")
	}
}

func TestHandleVectorGet_MissingNamespace(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_get")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"id": float64(42),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing namespace")
	}
}

func TestHandleVectorGet_WithConsistency(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_get")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace":   "embeddings",
		"id":          float64(42),
		"consistency": "eventual",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("unexpected error result")
	}
}

func TestHandleVectorGet_InvalidConsistency(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_get")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace":   "embeddings",
		"id":          float64(42),
		"consistency": "invalid_level",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid consistency")
	}
}

func TestHandleVectorGet_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		getVectorFn: func(_ context.Context, _ *service.GetVectorRequest) (*service.GetVectorResponse, error) {
			return nil, errors.New("not found")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_get")

	_, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"id":        float64(42),
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
}

// ---------------------------------------------------------------------------
// DeleteVector
// ---------------------------------------------------------------------------

func TestHandleVectorDelete_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_delete")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"id":        float64(42),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result")
	}
}

func TestHandleVectorDelete_MissingNamespace(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_delete")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"id": float64(42),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing namespace")
	}
}

func TestHandleVectorDelete_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		deleteVectorFn: func(_ context.Context, _ *service.DeleteVectorRequest) (*service.DeleteVectorResponse, error) {
			return nil, errors.New("not found")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_delete")

	_, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"id":        float64(42),
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
}

// ---------------------------------------------------------------------------
// BatchUpsert
// ---------------------------------------------------------------------------

func TestHandleVectorBatchUpsert_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_batch_upsert")

	vectors := `[{"id":1,"vector":[1.0,2.0]},{"id":2,"vector":[3.0,4.0]}]`
	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"vectors":   vectors,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result")
	}
}

func TestHandleVectorBatchUpsert_MissingNamespace(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_batch_upsert")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"vectors": `[{"id":1,"vector":[1.0]}]`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing namespace")
	}
}

func TestHandleVectorBatchUpsert_MissingVectors(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_batch_upsert")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing vectors")
	}
}

func TestHandleVectorBatchUpsert_InvalidVectorsJSON(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_batch_upsert")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"vectors":   "not-json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid vectors JSON")
	}
}

func TestHandleVectorBatchUpsert_EmptyVectors(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_batch_upsert")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"vectors":   "[]",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for empty vectors")
	}
}

func TestHandleVectorBatchUpsert_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		batchUpsertFn: func(_ context.Context, _ *service.BatchUpsertRequest) (*service.BatchUpsertResponse, error) {
			return nil, errors.New("raft timeout")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_batch_upsert")

	_, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"vectors":   `[{"id":1,"vector":[1.0]}]`,
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
}

// ---------------------------------------------------------------------------
// BatchDelete
// ---------------------------------------------------------------------------

func TestHandleVectorBatchDelete_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_batch_delete")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"ids":       "[1, 2, 3]",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result")
	}
}

func TestHandleVectorBatchDelete_MissingNamespace(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_batch_delete")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"ids": "[1]",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing namespace")
	}
}

func TestHandleVectorBatchDelete_MissingIDs(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_batch_delete")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing ids")
	}
}

func TestHandleVectorBatchDelete_InvalidIDsJSON(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_batch_delete")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"ids":       "not-json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid IDs JSON")
	}
}

func TestHandleVectorBatchDelete_EmptyIDs(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_batch_delete")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"ids":       "[]",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for empty ids")
	}
}

func TestHandleVectorBatchDelete_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		batchDeleteVectorsFn: func(_ context.Context, _ *service.BatchDeleteVectorsRequest) (*service.BatchDeleteVectorsResponse, error) {
			return nil, errors.New("raft timeout")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_batch_delete")

	_, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"ids":       "[1, 2]",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
}

// ---------------------------------------------------------------------------
// ListVectors
// ---------------------------------------------------------------------------

func TestHandleVectorList_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_list")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result")
	}
}

func TestHandleVectorList_MissingNamespace(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_list")

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing namespace")
	}
}

func TestHandleVectorList_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		listVectorsFn: func(_ context.Context, _ *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
			return nil, errors.New("internal error")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_list")

	_, err := handler(context.Background(), makeReq(map[string]any{"namespace": "embeddings"}))
	if err == nil {
		t.Fatal("expected error from service")
	}
}

func TestHandleVectorList_WithLimitAndCursor(t *testing.T) {
	var captured *service.ListVectorsRequest
	svc := &mockVectorService{
		listVectorsFn: func(_ context.Context, req *service.ListVectorsRequest) (*service.ListVectorsResponse, error) {
			captured = req
			return &service.ListVectorsResponse{Vectors: []*service.VectorData{}, TotalCount: 0}, nil
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_list")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"limit":     float64(50),
		"cursor":    "abc123",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("unexpected error result")
	}
	if captured == nil {
		t.Fatal("service not called")
	}
	if captured.Limit != 50 {
		t.Errorf("expected limit 50, got %d", captured.Limit)
	}
	if captured.Cursor != "abc123" {
		t.Errorf("expected cursor abc123, got %q", captured.Cursor)
	}
}

// ---------------------------------------------------------------------------
// UpdateMetadata
// ---------------------------------------------------------------------------

func TestHandleVectorUpdateMetadata_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_update_metadata")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"id":        float64(42),
		"metadata":  `{"key":"value"}`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result")
	}
}

func TestHandleVectorUpdateMetadata_MissingNamespace(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_update_metadata")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"id":       float64(42),
		"metadata": `{"key":"value"}`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing namespace")
	}
}

func TestHandleVectorUpdateMetadata_MissingMetadata(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_update_metadata")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"id":        float64(42),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing metadata")
	}
}

func TestHandleVectorUpdateMetadata_InvalidMetadata(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_update_metadata")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"id":        float64(42),
		"metadata":  "not-json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid metadata")
	}
}

func TestHandleVectorUpdateMetadata_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		updateMetadataFn: func(_ context.Context, _ *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
			return nil, errors.New("not found")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_update_metadata")

	_, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"id":        float64(42),
		"metadata":  `{"key":"value"}`,
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
}

func TestHandleVectorUpdateMetadata_CaptureRequest(t *testing.T) {
	var captured *service.UpdateVectorMetadataRequest
	svc := &mockVectorService{
		updateMetadataFn: func(_ context.Context, req *service.UpdateVectorMetadataRequest) (*service.UpdateVectorMetadataResponse, error) {
			captured = req
			return &service.UpdateVectorMetadataResponse{ID: req.ID, Version: 2, Success: true}, nil
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_update_metadata")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"id":        float64(42),
		"metadata":  `{"key":"value","num":123}`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Error("unexpected error result")
	}
	if captured == nil {
		t.Fatal("service not called")
	}
	if captured.Namespace != "embeddings" {
		t.Errorf("expected namespace 'embeddings', got %q", captured.Namespace)
	}
	if captured.ID != 42 {
		t.Errorf("expected ID 42, got %d", captured.ID)
	}
	if captured.Metadata["key"] != "value" {
		t.Errorf("expected metadata key=value, got %v", captured.Metadata["key"])
	}
}

// ---------------------------------------------------------------------------
// SearchSimilar
// ---------------------------------------------------------------------------

func TestHandleVectorSearchSimilar_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_search_similar")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"vector":    "[1.0, 2.0, 3.0]",
		"k":         float64(10),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result")
	}
}

func TestHandleVectorSearchSimilar_MissingNamespace(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_search_similar")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"vector": "[1.0]",
		"k":      float64(10),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing namespace")
	}
}

func TestHandleVectorSearchSimilar_MissingVector(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_search_similar")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"k":         float64(10),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing vector")
	}
}

func TestHandleVectorSearchSimilar_InvalidVector(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_search_similar")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"vector":    "not-json",
		"k":         float64(10),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid query vector")
	}
}

func TestHandleVectorSearchSimilar_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		searchSimilarFn: func(_ context.Context, _ *service.SearchSimilarRequest) (*service.SearchSimilarResponse, error) {
			return nil, errors.New("internal error")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_search_similar")

	_, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"vector":    "[1.0]",
		"k":         float64(10),
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
}

func TestHandleVectorSearchSimilar_InvalidConsistency(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_search_similar")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace":   "embeddings",
		"vector":      "[1.0]",
		"k":           float64(10),
		"consistency": "bad",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid consistency")
	}
}

// ---------------------------------------------------------------------------
// SearchByMetadata
// ---------------------------------------------------------------------------

func TestHandleVectorSearchByMetadata_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_search_by_metadata")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"filters":   `{"color":"red"}`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result")
	}
}

func TestHandleVectorSearchByMetadata_MissingNamespace(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_search_by_metadata")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"filters": `{"color":"red"}`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing namespace")
	}
}

func TestHandleVectorSearchByMetadata_MissingFilters(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_search_by_metadata")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing filters")
	}
}

func TestHandleVectorSearchByMetadata_InvalidFilters(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_search_by_metadata")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"filters":   "not-json",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid filters")
	}
}

func TestHandleVectorSearchByMetadata_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		searchByMetadataFn: func(_ context.Context, _ *service.SearchByMetadataRequest) (*service.SearchByMetadataResponse, error) {
			return nil, errors.New("internal error")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_search_by_metadata")

	_, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"filters":   `{"color":"red"}`,
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
}

func TestHandleVectorSearchByMetadata_InvalidConsistency(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_search_by_metadata")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace":   "embeddings",
		"filters":     `{"color":"red"}`,
		"consistency": "bad",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid consistency")
	}
}

// ---------------------------------------------------------------------------
// HybridSearch
// ---------------------------------------------------------------------------

func TestHandleVectorHybridSearch_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_hybrid_search")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace":       "embeddings",
		"vector":          "[1.0, 2.0]",
		"k":               float64(5),
		"metadata_filter": `{"color":"red"}`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result")
	}
}

func TestHandleVectorHybridSearch_MissingNamespace(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_hybrid_search")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"vector":          "[1.0]",
		"k":               float64(5),
		"metadata_filter": `{"color":"red"}`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing namespace")
	}
}

func TestHandleVectorHybridSearch_MissingVector(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_hybrid_search")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace":       "embeddings",
		"k":               float64(5),
		"metadata_filter": `{"color":"red"}`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing query_vector")
	}
}

func TestHandleVectorHybridSearch_InvalidVector(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_hybrid_search")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace":       "embeddings",
		"vector":          "bad",
		"k":               float64(5),
		"metadata_filter": `{"color":"red"}`,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid query vector")
	}
}

func TestHandleVectorHybridSearch_InvalidConsistency(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_hybrid_search")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace":       "embeddings",
		"vector":          "[1.0]",
		"k":               float64(5),
		"metadata_filter": `{"color":"red"}`,
		"consistency":     "bad",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid consistency")
	}
}

func TestHandleVectorHybridSearch_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		hybridSearchFn: func(_ context.Context, _ *service.HybridSearchRequest) (*service.HybridSearchResponse, error) {
			return nil, errors.New("internal error")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_hybrid_search")

	_, err := handler(context.Background(), makeReq(map[string]any{
		"namespace":       "embeddings",
		"vector":          "[1.0]",
		"k":               float64(5),
		"metadata_filter": `{"color":"red"}`,
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
}

// ---------------------------------------------------------------------------
// SearchByID
// ---------------------------------------------------------------------------

func TestHandleVectorSearchByID_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_search_by_id")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"id":        float64(42),
		"k":         float64(10),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result")
	}
}

func TestHandleVectorSearchByID_MissingNamespace(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_search_by_id")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"id": float64(42),
		"k":  float64(10),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing namespace")
	}
}

func TestHandleVectorSearchByID_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		searchByIDFn: func(_ context.Context, _ *service.SearchByIDRequest) (*service.SearchByIDResponse, error) {
			return nil, errors.New("not found")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_search_by_id")

	_, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"id":        float64(42),
		"k":         float64(10),
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
}

func TestHandleVectorSearchByID_InvalidConsistency(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_search_by_id")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace":   "embeddings",
		"id":          float64(42),
		"k":           float64(10),
		"consistency": "bad",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid consistency")
	}
}

// ---------------------------------------------------------------------------
// RebuildIndex
// ---------------------------------------------------------------------------

func TestHandleVectorRebuildIndex_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_rebuild_index")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result")
	}
}

func TestHandleVectorRebuildIndex_MissingNamespace(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_rebuild_index")

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing namespace")
	}
}

func TestHandleVectorRebuildIndex_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		rebuildIndexFn: func(_ context.Context, _ *service.RebuildIndexRequest) (*service.RebuildIndexResponse, error) {
			return nil, errors.New("busy")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_rebuild_index")

	_, err := handler(context.Background(), makeReq(map[string]any{"namespace": "embeddings"}))
	if err == nil {
		t.Fatal("expected error from service")
	}
}

// ---------------------------------------------------------------------------
// GetRebuildProgress
// ---------------------------------------------------------------------------

func TestHandleVectorGetRebuildProgress_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_get_rebuild_progress")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"job_id":    "job-1",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result")
	}
}

func TestHandleVectorGetRebuildProgress_MissingNamespace(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_get_rebuild_progress")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"job_id": "job-1",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing namespace")
	}
}

func TestHandleVectorGetRebuildProgress_MissingJobID(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_get_rebuild_progress")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing job_id")
	}
}

func TestHandleVectorGetRebuildProgress_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		getRebuildProgressFn: func(_ context.Context, _ *service.GetRebuildProgressRequest) (*service.GetRebuildProgressResponse, error) {
			return nil, errors.New("not found")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_get_rebuild_progress")

	_, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
		"job_id":    "job-1",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
}

// ---------------------------------------------------------------------------
// GetIndexStats
// ---------------------------------------------------------------------------

func TestHandleVectorGetIndexStats_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_get_index_stats")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result")
	}
}

func TestHandleVectorGetIndexStats_MissingNamespace(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_get_index_stats")

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing namespace")
	}
}

func TestHandleVectorGetIndexStats_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		getIndexStatsFn: func(_ context.Context, _ *service.GetIndexStatsRequest) (*service.GetIndexStatsResponse, error) {
			return nil, errors.New("not found")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_get_index_stats")

	_, err := handler(context.Background(), makeReq(map[string]any{"namespace": "embeddings"}))
	if err == nil {
		t.Fatal("expected error from service")
	}
}

// ---------------------------------------------------------------------------
// CompactNamespace
// ---------------------------------------------------------------------------

func TestHandleVectorCompactNamespace_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_compact_namespace")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result")
	}
}

func TestHandleVectorCompactNamespace_MissingNamespace(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_compact_namespace")

	result, err := handler(context.Background(), makeReq(map[string]any{}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing namespace")
	}
}

func TestHandleVectorCompactNamespace_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		compactNamespaceFn: func(_ context.Context, _ *service.CompactNamespaceRequest) (*service.CompactNamespaceResponse, error) {
			return nil, errors.New("busy")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_compact_namespace")

	_, err := handler(context.Background(), makeReq(map[string]any{"namespace": "embeddings"}))
	if err == nil {
		t.Fatal("expected error from service")
	}
}

// ---------------------------------------------------------------------------
// GetCompactionProgress
// ---------------------------------------------------------------------------

func TestHandleVectorGetCompactionProgress_Success(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_get_compaction_progress")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace":    "embeddings",
		"operation_id": "op-1",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := resultText(t, result)
	if text == "" {
		t.Error("expected non-empty result")
	}
}

func TestHandleVectorGetCompactionProgress_MissingNamespace(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_get_compaction_progress")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"operation_id": "op-1",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing namespace")
	}
}

func TestHandleVectorGetCompactionProgress_MissingOperationID(t *testing.T) {
	svc := &mockVectorService{}
	handler := findVectorHandler(t, svc, "qrdb_vector_get_compaction_progress")

	result, err := handler(context.Background(), makeReq(map[string]any{
		"namespace": "embeddings",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing operation_id")
	}
}

func TestHandleVectorGetCompactionProgress_ServiceError(t *testing.T) {
	svc := &mockVectorService{
		getCompactionProgressFn: func(_ context.Context, _ *service.GetCompactionProgressRequest) (*service.GetCompactionProgressResponse, error) {
			return nil, errors.New("not found")
		},
	}
	handler := findVectorHandler(t, svc, "qrdb_vector_get_compaction_progress")

	_, err := handler(context.Background(), makeReq(map[string]any{
		"namespace":    "embeddings",
		"operation_id": "op-1",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
}

// ---------------------------------------------------------------------------
// Helper function tests: parseFloat32Array, parseMetadata, parseConsistencyLevel
// ---------------------------------------------------------------------------

func TestParseFloat32Array_Empty(t *testing.T) {
	req := makeReq(map[string]any{})
	result, err := parseFloat32Array(req, "vector")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestParseFloat32Array_Valid(t *testing.T) {
	req := makeReq(map[string]any{"vector": "[1.5, 2.5, 3.5]"})
	result, err := parseFloat32Array(req, "vector")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(result))
	}
	if result[0] != 1.5 || result[1] != 2.5 || result[2] != 3.5 {
		t.Errorf("unexpected values: %v", result)
	}
}

func TestParseFloat32Array_Invalid(t *testing.T) {
	req := makeReq(map[string]any{"vector": "not-json"})
	_, err := parseFloat32Array(req, "vector")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseMetadata_Empty(t *testing.T) {
	req := makeReq(map[string]any{})
	result, err := parseMetadata(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil, got %v", result)
	}
}

func TestParseMetadata_Valid(t *testing.T) {
	req := makeReq(map[string]any{"metadata": `{"key":"value","num":42}`})
	result, err := parseMetadata(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["key"] != "value" {
		t.Errorf("expected key=value, got %v", result["key"])
	}
}

func TestParseMetadata_Invalid(t *testing.T) {
	req := makeReq(map[string]any{"metadata": "not-json"})
	_, err := parseMetadata(req)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParseConsistencyLevel_Empty(t *testing.T) {
	req := makeReq(map[string]any{})
	level, err := parseConsistencyLevel(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if level != service.ConsistencyStrong {
		t.Errorf("expected ConsistencyStrong, got %v", level)
	}
}

func TestParseConsistencyLevel_Strong(t *testing.T) {
	req := makeReq(map[string]any{"consistency": "strong"})
	level, err := parseConsistencyLevel(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if level != service.ConsistencyStrong {
		t.Errorf("expected ConsistencyStrong, got %v", level)
	}
}

func TestParseConsistencyLevel_Eventual(t *testing.T) {
	req := makeReq(map[string]any{"consistency": "eventual"})
	level, err := parseConsistencyLevel(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if level != service.ConsistencyEventual {
		t.Errorf("expected ConsistencyEventual, got %v", level)
	}
}

func TestParseConsistencyLevel_Linearizable(t *testing.T) {
	req := makeReq(map[string]any{"consistency": "linearizable"})
	level, err := parseConsistencyLevel(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if level != service.ConsistencyLinearizable {
		t.Errorf("expected ConsistencyLinearizable, got %v", level)
	}
}

func TestParseConsistencyLevel_Invalid(t *testing.T) {
	req := makeReq(map[string]any{"consistency": "invalid_level"})
	_, err := parseConsistencyLevel(req)
	if err == nil {
		t.Fatal("expected error for invalid consistency level")
	}
	var invErr *InvalidConsistencyError
	if !errors.As(err, &invErr) {
		t.Errorf("expected *InvalidConsistencyError, got %T", err)
	}
	if invErr.Value != "invalid_level" {
		t.Errorf("expected value 'invalid_level', got %q", invErr.Value)
	}
}

func TestInvalidConsistencyError_Error(t *testing.T) {
	err := &InvalidConsistencyError{Value: "bogus"}
	expected := "invalid consistency: bogus; must be one of strong, linearizable, eventual"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestInvalidConsistencyError_ErrorDifferentValue(t *testing.T) {
	err := &InvalidConsistencyError{Value: "weak"}
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
}

// ---------------------------------------------------------------------------
// Server options: WithVector, WithUserService, WithRoleService, WithCertProvider
// ---------------------------------------------------------------------------

func TestWithVector(t *testing.T) {
	svc := &mockVectorService{}
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	s := NewServer(kvSvc, adminSvc, ":0", WithVector(svc))
	if s.vectorSvc != svc {
		t.Error("WithVector did not set vectorSvc")
	}
}

func TestWithVector_ToolsRegistered(t *testing.T) {
	svc := &mockVectorService{}
	kvSvc := newMockKVService()
	adminSvc := newMockAdminService()
	s := NewServer(kvSvc, adminSvc, ":0", WithVector(svc))
	tools := s.MCPServer().ListTools()
	if _, ok := tools["qrdb_vector_create_namespace"]; !ok {
		t.Error("vector tools not registered")
	}
}

// ---------------------------------------------------------------------------
// handleScan tests (was at 8.3% coverage)
// ---------------------------------------------------------------------------

func TestHandleScan_Success(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["app/a"] = []byte("1")
	kvSvc.store["app/b"] = []byte("2")
	handler := handleScan(kvSvc)

	req := buildCallToolRequest(t, "qrdb_scan", map[string]any{
		"prefix": "app/",
	})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleScan: %v", err)
	}

	// Values are base64-encoded (RFC 4648); map[string][]byte decodes them
	// back to raw bytes automatically.
	text := resultText(t, result)
	var out map[string][]byte
	if jsonErr := json.Unmarshal([]byte(text), &out); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if len(out) != 2 {
		t.Errorf("expected 2 pairs, got %d: %v", len(out), out)
	}
	if string(out["app/a"]) != "1" {
		t.Errorf("expected app/a=1, got %q", out["app/a"])
	}
}

func TestHandleScan_EmptyPrefix(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["x"] = []byte("1")
	kvSvc.store["y"] = []byte("2")
	handler := handleScan(kvSvc)

	req := buildCallToolRequest(t, "qrdb_scan", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleScan: %v", err)
	}

	text := resultText(t, result)
	var out map[string]string
	if jsonErr := json.Unmarshal([]byte(text), &out); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if len(out) != 2 {
		t.Errorf("expected 2 pairs, got %d", len(out))
	}
}
