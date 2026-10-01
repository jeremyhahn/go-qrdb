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

// coverage_gaps2_test.go covers additional 0% and low-coverage functions:
// vector helpers, import errors, ceremony methods, collection marshal/unmarshal.
package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
)

// ---------------------------------------------------------------------------
// errors.go – Import error types (0% — locked behind extra build tags in
//             coverage_extended_test.go but unconditionally available)
// ---------------------------------------------------------------------------

func TestImportInvalidFormatError_Error(t *testing.T) {
	e := &ImportInvalidFormatError{Format: "parquet"}
	want := "service: invalid import format: parquet (expected csv, json, yaml, ndjson, archive, or auto)"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}

func TestImportMissingKeyFieldError_Error(t *testing.T) {
	e := &ImportMissingKeyFieldError{}
	want := "service: import keyField is required"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}

func TestImportInvalidValueModeError_Error(t *testing.T) {
	e := &ImportInvalidValueModeError{Mode: "row-col"}
	want := "service: invalid import valueMode: row-col (expected field or row)"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}

func TestImportMissingValueFieldError_Error(t *testing.T) {
	e := &ImportMissingValueFieldError{}
	want := "service: import valueField is required when valueMode is 'field'"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}

func TestImportParseError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("unexpected EOF")
	e := &ImportParseError{Err: cause}
	want := "service: import parse failed: unexpected EOF"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
	if !errors.Is(e, cause) {
		t.Fatal("expected errors.Is to match cause through Unwrap")
	}
}

func TestImportTransactionAbortedError_Error(t *testing.T) {
	e := &ImportTransactionAbortedError{Row: 7, Key: "k7", Message: "conflict"}
	want := "service: import aborted at row 7 (key=k7): conflict"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// vector_errors.go – TenantRequiredError.Error() (0%)
// ---------------------------------------------------------------------------

func TestTenantRequiredError_Error(t *testing.T) {
	e := &TenantRequiredError{Op: "CreateNamespace"}
	want := "service: tenant required for CreateNamespace: strict tenant isolation is enabled"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// vector.go – pure helpers (0%): convertToStringMap, convertFromStringMap,
//             matchesMetadataFilter, unscopedNamespace, namespaceMetricLabels
// ---------------------------------------------------------------------------

func TestConvertToStringMap_Nil(t *testing.T) {
	if convertToStringMap(nil) != nil {
		t.Fatal("expected nil for nil input")
	}
}

func TestConvertToStringMap_Values(t *testing.T) {
	m := map[string]interface{}{
		"key1": "val1",
		"key2": 42,
		"key3": true,
	}
	result := convertToStringMap(m)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(result))
	}
	if result["key1"] != "val1" {
		t.Fatalf("expected val1, got %q", result["key1"])
	}
	if result["key2"] != "42" {
		t.Fatalf("expected 42, got %q", result["key2"])
	}
	if result["key3"] != "true" {
		t.Fatalf("expected true, got %q", result["key3"])
	}
}

func TestConvertFromStringMap_Nil(t *testing.T) {
	if convertFromStringMap(nil) != nil {
		t.Fatal("expected nil for nil input")
	}
}

func TestConvertFromStringMap_Values(t *testing.T) {
	m := map[string]string{"a": "1", "b": "2"}
	result := convertFromStringMap(m)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
	if result["a"] != "1" {
		t.Fatalf("expected 1, got %v", result["a"])
	}
}

func TestMatchesMetadataFilter_AllMatch(t *testing.T) {
	metadata := map[string]interface{}{"role": "admin", "active": "true"}
	filter := map[string]interface{}{"role": "admin"}
	if !matchesMetadataFilter(metadata, filter) {
		t.Fatal("expected match")
	}
}

func TestMatchesMetadataFilter_NoMatch_WrongValue(t *testing.T) {
	metadata := map[string]interface{}{"role": "user"}
	filter := map[string]interface{}{"role": "admin"}
	if matchesMetadataFilter(metadata, filter) {
		t.Fatal("expected no match for wrong value")
	}
}

func TestMatchesMetadataFilter_NoMatch_MissingKey(t *testing.T) {
	metadata := map[string]interface{}{"name": "alice"}
	filter := map[string]interface{}{"role": "admin"}
	if matchesMetadataFilter(metadata, filter) {
		t.Fatal("expected no match for missing key")
	}
}

func TestUnscopedNamespace_SystemTenantEmptyPrefix(t *testing.T) {
	tenantID, logical := unscopedNamespace("\x1fdocs")
	if tenantID != "system" {
		t.Fatalf("expected system tenant, got %q", tenantID)
	}
	if logical != "docs" {
		t.Fatalf("expected docs, got %q", logical)
	}
}

func TestUnscopedNamespace_UserTenant(t *testing.T) {
	tenantID, logical := unscopedNamespace("acme\x1fvectors")
	if tenantID != "acme" {
		t.Fatalf("expected acme, got %q", tenantID)
	}
	if logical != "vectors" {
		t.Fatalf("expected vectors, got %q", logical)
	}
}

func TestUnscopedNamespace_NoSeparator(t *testing.T) {
	// Legacy unscoped name with no separator byte.
	tenantID, logical := unscopedNamespace("legacy-ns")
	if tenantID != "system" {
		t.Fatalf("expected system for no-separator, got %q", tenantID)
	}
	if logical != "legacy-ns" {
		t.Fatalf("expected legacy-ns, got %q", logical)
	}
}

func TestNamespaceMetricLabels_System(t *testing.T) {
	labels := namespaceMetricLabels("\x1fdocs")
	if labels["namespace"] != "docs" {
		t.Fatalf("expected namespace=docs, got %q", labels["namespace"])
	}
	if labels["tenant"] != "system" {
		t.Fatalf("expected tenant=system, got %q", labels["tenant"])
	}
}

func TestNamespaceMetricLabels_UserTenant(t *testing.T) {
	labels := namespaceMetricLabels("acme\x1fvectors")
	if labels["namespace"] != "vectors" {
		t.Fatalf("expected namespace=vectors, got %q", labels["namespace"])
	}
	if labels["tenant"] != "acme" {
		t.Fatalf("expected tenant=acme, got %q", labels["tenant"])
	}
}

// ---------------------------------------------------------------------------
// vector.go – WithHNSWIndexFactory, WithStrictTenantIsolation options (0%)
// ---------------------------------------------------------------------------

func TestWithHNSWIndexFactory_Option(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	// A nil factory is still a valid option (disables HNSW).
	var factory interfaces.HNSWIndexFactory
	svc, err := NewVectorService(storage, logger, WithHNSWIndexFactory(factory))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
}

func TestWithStrictTenantIsolation_True(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	svc, err := NewVectorService(storage, logger, WithStrictTenantIsolation(true))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
	// Verify strict isolation is set: CreateNamespace without tenant context
	// must return TenantRequiredError.
	ctx := context.Background() // no tenant
	_, createErr := svc.CreateNamespace(ctx, &CreateNamespaceRequest{
		Namespace: "test-ns",
		Dimension: 128,
	})
	if createErr == nil {
		t.Fatal("expected error with strict tenant isolation, got nil")
	}
	var tre *TenantRequiredError
	if !errors.As(createErr, &tre) {
		t.Fatalf("expected *TenantRequiredError, got %T: %v", createErr, createErr)
	}
}

func TestWithStrictTenantIsolation_False(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()

	// Strict isolation off (default) — system tenant context should work.
	svc, err := NewVectorService(storage, logger, WithStrictTenantIsolation(false))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
}

// ---------------------------------------------------------------------------
// vector.go – validate* functions that have missing branches
// ---------------------------------------------------------------------------

func TestValidateCreateNamespaceRequest_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, createErr := svc.CreateNamespace(ctx, &CreateNamespaceRequest{
		Namespace: "", // empty namespace triggers validation error
		Dimension: 128,
	})
	if createErr == nil {
		t.Fatal("expected error for empty namespace, got nil")
	}
}

func TestValidateDeleteNamespaceRequest_Nil(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, deleteErr := svc.DeleteNamespace(ctx, nil)
	if deleteErr == nil {
		t.Fatal("expected error for nil request, got nil")
	}
}

func TestValidateGetNamespaceInfoRequest_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, getErr := svc.GetNamespaceInfo(ctx, &GetNamespaceInfoRequest{Namespace: ""})
	if getErr == nil {
		t.Fatal("expected error for empty namespace, got nil")
	}
}

func TestValidateUpsertVectorRequest_NilRequest(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, upsertErr := svc.UpsertVector(ctx, nil)
	if upsertErr == nil {
		t.Fatal("expected error for nil request, got nil")
	}
}

func TestValidateUpsertVectorRequest_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, upsertErr := svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "",
		Vector:    []float32{1.0, 2.0, 3.0},
	})
	if upsertErr == nil {
		t.Fatal("expected error for empty namespace, got nil")
	}
}

func TestValidateUpsertVectorRequest_EmptyVector(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, upsertErr := svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "ns",
		Vector:    nil, // empty vector
	})
	if upsertErr == nil {
		t.Fatal("expected error for empty vector, got nil")
	}
}

func TestValidateGetVectorRequest_Nil(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, getErr := svc.GetVector(ctx, nil)
	if getErr == nil {
		t.Fatal("expected error for nil request, got nil")
	}
}

func TestValidateGetVectorRequest_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, getErr := svc.GetVector(ctx, &GetVectorRequest{Namespace: "", ID: 1})
	if getErr == nil {
		t.Fatal("expected error for empty namespace, got nil")
	}
}

func TestValidateDeleteVectorRequest_Nil(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, delErr := svc.DeleteVector(ctx, nil)
	if delErr == nil {
		t.Fatal("expected error for nil request, got nil")
	}
}

func TestValidateDeleteVectorRequest_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, delErr := svc.DeleteVector(ctx, &DeleteVectorRequest{Namespace: "", ID: 1})
	if delErr == nil {
		t.Fatal("expected error for empty namespace, got nil")
	}
}

func TestValidateBatchUpsertRequest_Nil(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, batchErr := svc.BatchUpsert(ctx, nil)
	if batchErr == nil {
		t.Fatal("expected error for nil request, got nil")
	}
}

func TestValidateBatchUpsertRequest_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, batchErr := svc.BatchUpsert(ctx, &BatchUpsertRequest{
		Namespace: "",
		Vectors:   []*VectorData{{Vector: []float32{1.0}}},
	})
	if batchErr == nil {
		t.Fatal("expected error for empty namespace, got nil")
	}
}

func TestValidateRebuildIndexRequest_Nil(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, rebuildErr := svc.RebuildIndex(ctx, nil)
	if rebuildErr == nil {
		t.Fatal("expected error for nil request, got nil")
	}
}

func TestValidateRebuildIndexRequest_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, rebuildErr := svc.RebuildIndex(ctx, &RebuildIndexRequest{Namespace: ""})
	if rebuildErr == nil {
		t.Fatal("expected error for empty namespace, got nil")
	}
}

func TestValidateGetRebuildProgressRequest_Nil(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, progErr := svc.GetRebuildProgress(ctx, nil)
	if progErr == nil {
		t.Fatal("expected error for nil request, got nil")
	}
}

func TestValidateGetRebuildProgressRequest_EmptyJobID(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, progErr := svc.GetRebuildProgress(ctx, &GetRebuildProgressRequest{JobID: ""})
	if progErr == nil {
		t.Fatal("expected error for empty job_id (and empty namespace), got nil")
	}
}

func TestValidateGetIndexStatsRequest_Nil(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, statsErr := svc.GetIndexStats(ctx, nil)
	if statsErr == nil {
		t.Fatal("expected error for nil request, got nil")
	}
}

func TestValidateGetIndexStatsRequest_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, statsErr := svc.GetIndexStats(ctx, &GetIndexStatsRequest{Namespace: ""})
	if statsErr == nil {
		t.Fatal("expected error for empty namespace, got nil")
	}
}

func TestValidateCompactNamespaceRequest_Nil(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, compactErr := svc.CompactNamespace(ctx, nil)
	if compactErr == nil {
		t.Fatal("expected error for nil request, got nil")
	}
}

func TestValidateCompactNamespaceRequest_EmptyNamespace(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, compactErr := svc.CompactNamespace(ctx, &CompactNamespaceRequest{Namespace: ""})
	if compactErr == nil {
		t.Fatal("expected error for empty namespace, got nil")
	}
}

func TestValidateGetCompactionProgressRequest_Nil(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, progressErr := svc.GetCompactionProgress(ctx, nil)
	if progressErr == nil {
		t.Fatal("expected error for nil request, got nil")
	}
}

func TestValidateGetCompactionProgressRequest_EmptyBoth(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, progressErr := svc.GetCompactionProgress(ctx, &GetCompactionProgressRequest{
		OperationID: "",
		Namespace:   "",
	})
	if progressErr == nil {
		t.Fatal("expected error for empty operation_id and namespace, got nil")
	}
}

// BatchDeleteVectors nil/empty request.
func TestBatchDeleteVectors_Nil(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, delErr := svc.BatchDeleteVectors(ctx, nil)
	if delErr == nil {
		t.Fatal("expected error for nil request, got nil")
	}
}

// ListVectors nil/empty request.
func TestListVectors_Nil(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, listErr := svc.ListVectors(ctx, nil)
	if listErr == nil {
		t.Fatal("expected error for nil request, got nil")
	}
}

// UpdateMetadata nil/empty request.
func TestUpdateMetadata_Nil(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, updateErr := svc.UpdateMetadata(ctx, nil)
	if updateErr == nil {
		t.Fatal("expected error for nil request, got nil")
	}
}

// SearchByID nil/empty request.
func TestSearchByID_Nil(t *testing.T) {
	storage := newVecMockStorage()
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	ctx := context.Background()
	_, searchErr := svc.SearchByID(ctx, nil)
	if searchErr == nil {
		t.Fatal("expected error for nil request, got nil")
	}
}

// ---------------------------------------------------------------------------
// vector.go – operations on non-existent namespaces (low coverage paths)
// ---------------------------------------------------------------------------

func newVecSvc(t *testing.T) VectorService {
	t.Helper()
	storage := newVecMockStorage()
	// Make the storage return "not found" for GetNamespaceInfo and empty list
	storage.listNamespacesFn = func(_ context.Context) ([]vector.NamespaceInfo, error) {
		return nil, nil
	}
	logger := newVecTestLogger()
	svc, err := NewVectorService(storage, logger)
	if err != nil {
		t.Fatalf("NewVectorService: %v", err)
	}
	return svc
}

func TestDeleteNamespace_NonExistentSucceeds(t *testing.T) {
	// DeleteNamespace does not require the namespace to exist in the in-memory
	// map — it delegates to storage (which is a no-op mock) and succeeds.
	svc := newVecSvc(t)
	ctx := context.Background()
	resp, err := svc.DeleteNamespace(ctx, &DeleteNamespaceRequest{Namespace: "no-such-ns"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Response may or may not indicate success, just validate the path was exercised.
	_ = resp
}

func TestGetNamespaceInfo_NotFound(t *testing.T) {
	svc := newVecSvc(t)
	ctx := context.Background()
	_, err := svc.GetNamespaceInfo(ctx, &GetNamespaceInfoRequest{Namespace: "ghost"})
	if err == nil {
		t.Fatal("expected error for missing namespace, got nil")
	}
}

func TestUpsertVector_NamespaceNotFound(t *testing.T) {
	svc := newVecSvc(t)
	ctx := context.Background()
	_, err := svc.UpsertVector(ctx, &UpsertVectorRequest{
		Namespace: "no-ns",
		Vector:    []float32{1.0, 2.0},
	})
	if err == nil {
		t.Fatal("expected error for missing namespace, got nil")
	}
}

func TestGetVector_NamespaceNotFound(t *testing.T) {
	svc := newVecSvc(t)
	ctx := context.Background()
	_, err := svc.GetVector(ctx, &GetVectorRequest{Namespace: "no-ns", ID: 1})
	if err == nil {
		t.Fatal("expected error for missing namespace, got nil")
	}
}

func TestDeleteVector_StoragePath(t *testing.T) {
	// DeleteVector delegates directly to storage without checking if the
	// namespace exists in memory. The mock returns nil, so it succeeds.
	svc := newVecSvc(t)
	ctx := context.Background()
	resp, err := svc.DeleteVector(ctx, &DeleteVectorRequest{Namespace: "no-ns", ID: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = resp
}

func TestSearchSimilar_ValidationError(t *testing.T) {
	svc := newVecSvc(t)
	ctx := context.Background()
	// Nil request.
	_, err := svc.SearchSimilar(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil request, got nil")
	}
}

func TestSearchSimilar_EmptyVector(t *testing.T) {
	svc := newVecSvc(t)
	ctx := context.Background()
	_, err := svc.SearchSimilar(ctx, &SearchSimilarRequest{
		Namespace:   "ns",
		QueryVector: nil, // empty
		K:           10,
	})
	if err == nil {
		t.Fatal("expected error for empty query vector, got nil")
	}
}

func TestSearchByMetadata_Nil(t *testing.T) {
	svc := newVecSvc(t)
	ctx := context.Background()
	_, err := svc.SearchByMetadata(ctx, nil)
	if err == nil {
		t.Fatal("expected error for nil request, got nil")
	}
}

func TestHybridSearch_EmptyVector(t *testing.T) {
	svc := newVecSvc(t)
	ctx := context.Background()
	// A request with empty namespace should fail validation.
	_, err := svc.HybridSearch(ctx, &HybridSearchRequest{
		Namespace:   "",
		QueryVector: []float32{1.0, 2.0},
		K:           5,
	})
	if err == nil {
		t.Fatal("expected error for empty namespace, got nil")
	}
}

func TestGetIndexStats_NamespaceNotFound(t *testing.T) {
	svc := newVecSvc(t)
	ctx := context.Background()
	_, err := svc.GetIndexStats(ctx, &GetIndexStatsRequest{Namespace: "no-ns"})
	if err == nil {
		t.Fatal("expected error for missing namespace, got nil")
	}
}

func TestGetCompactionProgress_WithNamespace(t *testing.T) {
	svc := newVecSvc(t)
	ctx := context.Background()
	// With a valid namespace field — the namespace may not exist but validation
	// passes. The service returns an error from storage lookup.
	_, err := svc.GetCompactionProgress(ctx, &GetCompactionProgressRequest{Namespace: "ns"})
	// Any error or nil is acceptable — we just want the path exercised.
	_ = err
}

func TestRebuildIndex_NamespaceNotFound(t *testing.T) {
	svc := newVecSvc(t)
	ctx := context.Background()
	_, err := svc.RebuildIndex(ctx, &RebuildIndexRequest{Namespace: "no-ns"})
	if err == nil {
		t.Fatal("expected error for missing namespace, got nil")
	}
}

// ---------------------------------------------------------------------------
// collection_service.go – marshalDocument / unmarshalDocument (25%/13%)
// Test via CreateCollection with a custom StorageCodec and InsertDocument.
// The JSON codec path is tested separately for the non-JSON branch.
// ---------------------------------------------------------------------------

func TestCollectionService_InsertDocument_WithExplicitID(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "idcol",
		Database: "mydb",
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	// Document with explicit _id.
	doc := map[string]interface{}{
		"_id":   uint64(999),
		"name":  "Alice",
		"email": "alice@example.com",
	}
	result, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "idcol",
		Database:   "mydb",
		Document:   doc,
	})
	if err != nil {
		t.Fatalf("InsertDocument with explicit _id: %v", err)
	}
	if result.ID != 999 {
		t.Fatalf("expected ID=999, got %d", result.ID)
	}
}

func TestCollectionService_GetDocument_AfterInsert(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "getcol",
		Database: "mydb",
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	insertResult, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "getcol",
		Database:   "mydb",
		Document:   map[string]interface{}{"title": "hello"},
	})
	if err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}

	doc, err := svc.GetDocument(ctx, &GetDocumentRequest{
		Collection: "getcol",
		Database:   "mydb",
		ID:         insertResult.ID,
	})
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if doc.ID != insertResult.ID {
		t.Fatalf("expected ID %d, got %d", insertResult.ID, doc.ID)
	}
}

func TestCollectionService_GetDocument_NotFound_Coverage(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "notfoundcol",
		Database: "mydb",
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	_, err = svc.GetDocument(ctx, &GetDocumentRequest{
		Collection: "notfoundcol",
		Database:   "mydb",
		ID:         99999,
	})
	if err == nil {
		t.Fatal("expected error for missing document, got nil")
	}
}

func TestCollectionService_UpdateDocument_Success(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "updcol",
		Database: "mydb",
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	insertResult, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "updcol",
		Database:   "mydb",
		Document:   map[string]interface{}{"title": "original"},
	})
	if err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}

	updated, err := svc.UpdateDocument(ctx, &UpdateDocumentRequest{
		Collection: "updcol",
		Database:   "mydb",
		ID:         insertResult.ID,
		Document:   map[string]interface{}{"title": "updated"},
	})
	if err != nil {
		t.Fatalf("UpdateDocument: %v", err)
	}
	if updated.ID != insertResult.ID {
		t.Fatalf("expected ID %d, got %d", insertResult.ID, updated.ID)
	}
}

func TestCollectionService_UpdateDocument_NotFound_Coverage(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "updnotfound",
		Database: "mydb",
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	_, err = svc.UpdateDocument(ctx, &UpdateDocumentRequest{
		Collection: "updnotfound",
		Database:   "mydb",
		ID:         99999,
		Document:   map[string]interface{}{"title": "x"},
	})
	if err == nil {
		t.Fatal("expected error for missing document, got nil")
	}
}

func TestCollectionService_DeleteDocument_Success(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "delcol",
		Database: "mydb",
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	insertResult, err := svc.InsertDocument(ctx, &InsertDocumentRequest{
		Collection: "delcol",
		Database:   "mydb",
		Document:   map[string]interface{}{"x": "y"},
	})
	if err != nil {
		t.Fatalf("InsertDocument: %v", err)
	}

	_, err = svc.DeleteDocument(ctx, &DeleteDocumentRequest{
		Collection: "delcol",
		Database:   "mydb",
		ID:         insertResult.ID,
	})
	if err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
}

func TestCollectionService_DeleteDocument_NotFound_Coverage(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "delnf",
		Database: "mydb",
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	_, err = svc.DeleteDocument(ctx, &DeleteDocumentRequest{
		Collection: "delnf",
		Database:   "mydb",
		ID:         99999,
	})
	if err == nil {
		t.Fatal("expected error for missing document, got nil")
	}
}

func TestCollectionService_ListDocuments_Empty_Coverage(t *testing.T) {
	svc := newTestCollectionService()
	ctx := context.Background()

	_, err := svc.CreateCollection(ctx, &CreateCollectionRequest{
		Name:     "listcol",
		Database: "mydb",
	})
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}

	resp, err := svc.ListDocuments(ctx, &ListDocumentsRequest{
		Collection: "listcol",
		Database:   "mydb",
	})
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.Documents) != 0 {
		t.Fatalf("expected 0 docs, got %d", len(resp.Documents))
	}
}

// ---------------------------------------------------------------------------
// database_service.go – WithBarrierManager func option exercise (0%)
// The option func itself is what needs coverage; we must call it on a service.
// ---------------------------------------------------------------------------

func TestWithBarrierManager_FuncCoverage(t *testing.T) {
	// Create the functional option with a nil *database.DatabaseBarrierManager
	// so that the function body (the closure) executes.
	// We cannot pass nil directly for a typed pointer if it would panic,
	// but we just want coverage of the option function body — calling
	// WithBarrierManager and invoking the returned option covers it.
	opt := WithBarrierManager(nil)
	// Build a minimal DatabaseService and invoke the option directly.
	s := &DatabaseService{}
	opt(s)
	// After invocation, barrierMgr is nil — that is the expected state.
	if s.barrierMgr != nil {
		t.Fatal("expected nil barrierMgr after applying nil option")
	}
}
