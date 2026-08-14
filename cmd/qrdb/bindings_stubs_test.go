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

//go:build !qrdb_ts && !qrdb_vector && !qrdb_embedded

package main

import (
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// TimeSeriesBinding stub tests
// ---------------------------------------------------------------------------

func TestTimeSeriesBinding_Stub_Write(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	err := b.Write(nil)
	if err == nil {
		t.Fatal("expected error from Write stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "time-series" {
		t.Fatalf("expected Feature %q, got %q", "time-series", fde.Feature)
	}
}

func TestTimeSeriesBinding_Stub_Write_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	err := b.Write(nil)
	expected := "qrdb: feature disabled: time-series"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestTimeSeriesBinding_Stub_WriteBatch(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	err := b.WriteBatch(nil)
	if err == nil {
		t.Fatal("expected error from WriteBatch stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "time-series" {
		t.Fatalf("expected Feature %q, got %q", "time-series", fde.Feature)
	}
}

func TestTimeSeriesBinding_Stub_WriteBatch_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	err := b.WriteBatch(nil)
	expected := "qrdb: feature disabled: time-series"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestTimeSeriesBinding_Stub_Query(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	resp, err := b.Query(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from Query stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "time-series" {
		t.Fatalf("expected Feature %q, got %q", "time-series", fde.Feature)
	}
}

func TestTimeSeriesBinding_Stub_Query_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	_, err := b.Query(nil)
	expected := "qrdb: feature disabled: time-series"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestTimeSeriesBinding_Stub_Aggregate(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	resp, err := b.Aggregate(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from Aggregate stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "time-series" {
		t.Fatalf("expected Feature %q, got %q", "time-series", fde.Feature)
	}
}

func TestTimeSeriesBinding_Stub_Aggregate_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	_, err := b.Aggregate(nil)
	expected := "qrdb: feature disabled: time-series"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestTimeSeriesBinding_Stub_WritePoint(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	err := b.WritePoint(nil)
	if err == nil {
		t.Fatal("expected error from WritePoint stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "time-series" {
		t.Fatalf("expected Feature %q, got %q", "time-series", fde.Feature)
	}
}

func TestTimeSeriesBinding_Stub_WritePoint_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	err := b.WritePoint(nil)
	expected := "qrdb: feature disabled: time-series"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestTimeSeriesBinding_Stub_RichQuery(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	resp, err := b.RichQuery(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from RichQuery stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "time-series" {
		t.Fatalf("expected Feature %q, got %q", "time-series", fde.Feature)
	}
}

func TestTimeSeriesBinding_Stub_RichQuery_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	_, err := b.RichQuery(nil)
	expected := "qrdb: feature disabled: time-series"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestTimeSeriesBinding_Stub_DeleteSeries(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	resp, err := b.DeleteSeries(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from DeleteSeries stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "time-series" {
		t.Fatalf("expected Feature %q, got %q", "time-series", fde.Feature)
	}
}

func TestTimeSeriesBinding_Stub_DeleteSeries_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	_, err := b.DeleteSeries(nil)
	expected := "qrdb: feature disabled: time-series"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestTimeSeriesBinding_Stub_GetRetention(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	resp, err := b.GetRetention(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from GetRetention stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "time-series" {
		t.Fatalf("expected Feature %q, got %q", "time-series", fde.Feature)
	}
}

func TestTimeSeriesBinding_Stub_GetRetention_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	_, err := b.GetRetention(nil)
	expected := "qrdb: feature disabled: time-series"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestTimeSeriesBinding_Stub_SetRetention(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	err := b.SetRetention(nil)
	if err == nil {
		t.Fatal("expected error from SetRetention stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "time-series" {
		t.Fatalf("expected Feature %q, got %q", "time-series", fde.Feature)
	}
}

func TestTimeSeriesBinding_Stub_SetRetention_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	err := b.SetRetention(nil)
	expected := "qrdb: feature disabled: time-series"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestTimeSeriesBinding_Stub_ListMetrics(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	resp, err := b.ListMetrics(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from ListMetrics stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "time-series" {
		t.Fatalf("expected Feature %q, got %q", "time-series", fde.Feature)
	}
}

func TestTimeSeriesBinding_Stub_ListMetrics_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTimeSeriesBinding(testLogger(t), conn)
	_, err := b.ListMetrics(nil)
	expected := "qrdb: feature disabled: time-series"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

// ---------------------------------------------------------------------------
// VectorBinding stub tests
// ---------------------------------------------------------------------------

func TestVectorBinding_Stub_CreateNamespace(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.CreateNamespace(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from CreateNamespace stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_CreateNamespace_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.CreateNamespace(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestVectorBinding_Stub_DeleteNamespace(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.DeleteNamespace(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from DeleteNamespace stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_DeleteNamespace_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.DeleteNamespace(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestVectorBinding_Stub_ListNamespaces(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.ListNamespaces(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from ListNamespaces stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_ListNamespaces_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.ListNamespaces(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestVectorBinding_Stub_GetNamespaceInfo(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.GetNamespaceInfo(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from GetNamespaceInfo stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_GetNamespaceInfo_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.GetNamespaceInfo(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestVectorBinding_Stub_UpsertVector(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.UpsertVector(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from UpsertVector stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_UpsertVector_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.UpsertVector(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestVectorBinding_Stub_GetVector(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.GetVector(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from GetVector stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_GetVector_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.GetVector(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestVectorBinding_Stub_DeleteVector(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.DeleteVector(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from DeleteVector stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_DeleteVector_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.DeleteVector(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestVectorBinding_Stub_BatchUpsert(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.BatchUpsert(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from BatchUpsert stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_BatchUpsert_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.BatchUpsert(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestVectorBinding_Stub_BatchDeleteVectors(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.BatchDeleteVectors(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from BatchDeleteVectors stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_BatchDeleteVectors_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.BatchDeleteVectors(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestVectorBinding_Stub_ListVectors(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.ListVectors(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from ListVectors stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_ListVectors_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.ListVectors(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestVectorBinding_Stub_UpdateMetadata(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.UpdateMetadata(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from UpdateMetadata stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_UpdateMetadata_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.UpdateMetadata(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestVectorBinding_Stub_SearchSimilar(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.SearchSimilar(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from SearchSimilar stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_SearchSimilar_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.SearchSimilar(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestVectorBinding_Stub_SearchByMetadata(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.SearchByMetadata(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from SearchByMetadata stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_SearchByMetadata_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.SearchByMetadata(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestVectorBinding_Stub_HybridSearch(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.HybridSearch(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from HybridSearch stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_HybridSearch_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.HybridSearch(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestVectorBinding_Stub_SearchByID(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.SearchByID(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from SearchByID stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_SearchByID_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.SearchByID(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestVectorBinding_Stub_RebuildIndex(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.RebuildIndex(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from RebuildIndex stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_RebuildIndex_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.RebuildIndex(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestVectorBinding_Stub_GetRebuildProgress(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.GetRebuildProgress(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from GetRebuildProgress stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_GetRebuildProgress_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.GetRebuildProgress(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestVectorBinding_Stub_GetIndexStats(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.GetIndexStats(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from GetIndexStats stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_GetIndexStats_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.GetIndexStats(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestVectorBinding_Stub_CompactNamespace(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.CompactNamespace(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from CompactNamespace stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_CompactNamespace_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.CompactNamespace(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}

func TestVectorBinding_Stub_GetCompactionProgress(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	resp, err := b.GetCompactionProgress(nil)
	if resp != nil {
		t.Fatalf("expected nil response, got %v", resp)
	}
	if err == nil {
		t.Fatal("expected error from GetCompactionProgress stub")
	}
	var fde *FeatureDisabledError
	if !errors.As(err, &fde) {
		t.Fatalf("expected FeatureDisabledError, got %T: %v", err, err)
	}
	if fde.Feature != "vector" {
		t.Fatalf("expected Feature %q, got %q", "vector", fde.Feature)
	}
}

func TestVectorBinding_Stub_GetCompactionProgress_ErrorMessage(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewVectorBinding(testLogger(t), conn)
	_, err := b.GetCompactionProgress(nil)
	expected := "qrdb: feature disabled: vector"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}
