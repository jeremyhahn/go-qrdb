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

//go:build qrdb_vector && !integration

package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// VectorParseError
// ---------------------------------------------------------------------------

func TestVectorParseError_Error(t *testing.T) {
	cause := errors.New("strconv error")
	e := &VectorParseError{Value: "abc", Err: cause}
	msg := e.Error()
	if !strings.Contains(msg, "abc") {
		t.Errorf("expected Value %q in error message %q", "abc", msg)
	}
	if !strings.Contains(msg, "strconv error") {
		t.Errorf("expected cause in error message %q", msg)
	}
}

func TestVectorParseError_Unwrap(t *testing.T) {
	cause := errors.New("underlying cause")
	e := &VectorParseError{Value: "x", Err: cause}
	if !errors.Is(e, cause) {
		t.Error("expected errors.Is to find wrapped cause")
	}
}

// ---------------------------------------------------------------------------
// JSONParseError
// ---------------------------------------------------------------------------

func TestJSONParseError_Error(t *testing.T) {
	cause := errors.New("json decode failed")
	e := &JSONParseError{Input: `{bad}`, Err: cause}
	msg := e.Error()
	if !strings.Contains(msg, "json decode failed") {
		t.Errorf("expected cause in error message %q", msg)
	}
}

func TestJSONParseError_Unwrap(t *testing.T) {
	cause := errors.New("root cause")
	e := &JSONParseError{Input: "x", Err: cause}
	if !errors.Is(e, cause) {
		t.Error("expected errors.Is to find wrapped cause")
	}
}

// ---------------------------------------------------------------------------
// InvalidDistanceMetricError
// ---------------------------------------------------------------------------

func TestInvalidDistanceMetricError_Error(t *testing.T) {
	e := &InvalidDistanceMetricError{Value: "hamming"}
	msg := e.Error()
	if !strings.Contains(msg, "hamming") {
		t.Errorf("expected Value %q in error message %q", "hamming", msg)
	}
}

func TestInvalidDistanceMetricError_Error_ContainsPrefix(t *testing.T) {
	e := &InvalidDistanceMetricError{Value: "xyz"}
	if !strings.HasPrefix(e.Error(), "invalid distance metric:") {
		t.Errorf("expected prefix in error message %q", e.Error())
	}
}

// ---------------------------------------------------------------------------
// InvalidIndexTypeError
// ---------------------------------------------------------------------------

func TestInvalidIndexTypeError_Error(t *testing.T) {
	e := &InvalidIndexTypeError{Value: "lsh"}
	msg := e.Error()
	if !strings.Contains(msg, "lsh") {
		t.Errorf("expected Value %q in error message %q", "lsh", msg)
	}
}

func TestInvalidIndexTypeError_Error_ContainsPrefix(t *testing.T) {
	e := &InvalidIndexTypeError{Value: "tree"}
	if !strings.HasPrefix(e.Error(), "invalid index type:") {
		t.Errorf("expected prefix in error message %q", e.Error())
	}
}

// ---------------------------------------------------------------------------
// InvalidConsistencyLevelError
// ---------------------------------------------------------------------------

func TestInvalidConsistencyLevelError_Error(t *testing.T) {
	e := &InvalidConsistencyLevelError{Value: "strong"}
	msg := e.Error()
	if !strings.Contains(msg, "strong") {
		t.Errorf("expected Value %q in error message %q", "strong", msg)
	}
}

func TestInvalidConsistencyLevelError_Error_ContainsPrefix(t *testing.T) {
	e := &InvalidConsistencyLevelError{Value: "eventual"}
	if !strings.HasPrefix(e.Error(), "invalid consistency level:") {
		t.Errorf("expected prefix in error message %q", e.Error())
	}
}

// ---------------------------------------------------------------------------
// InvalidVectorIDError
// ---------------------------------------------------------------------------

func TestInvalidVectorIDError_Error(t *testing.T) {
	e := &InvalidVectorIDError{Input: "abc"}
	msg := e.Error()
	if !strings.Contains(msg, "abc") {
		t.Errorf("expected Input %q in error message %q", "abc", msg)
	}
}

func TestInvalidVectorIDError_Error_ContainsPrefix(t *testing.T) {
	e := &InvalidVectorIDError{Input: "xyz"}
	if !strings.HasPrefix(e.Error(), "invalid vector ID:") {
		t.Errorf("expected prefix in error message %q", e.Error())
	}
}

// ---------------------------------------------------------------------------
// parseFloatVector
// ---------------------------------------------------------------------------

func TestParseFloatVector_Valid(t *testing.T) {
	vec, err := parseFloatVector("1.0,2.5,3.14")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(vec))
	}
	if vec[0] != 1.0 {
		t.Errorf("vec[0] = %v, want 1.0", vec[0])
	}
}

func TestParseFloatVector_Invalid(t *testing.T) {
	_, err := parseFloatVector("1.0,notanumber,3.0")
	if err == nil {
		t.Fatal("expected error for invalid float")
	}
	var vpe *VectorParseError
	if !errors.As(err, &vpe) {
		t.Fatalf("expected *VectorParseError, got %T", err)
	}
}

func TestParseFloatVector_SingleElement(t *testing.T) {
	vec, err := parseFloatVector("42.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vec) != 1 {
		t.Fatalf("expected 1 element, got %d", len(vec))
	}
}

func TestParseFloatVector_WhitespaceHandled(t *testing.T) {
	vec, err := parseFloatVector(" 1.0 , 2.0 , 3.0 ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vec) != 3 {
		t.Fatalf("expected 3 elements, got %d", len(vec))
	}
}

// ---------------------------------------------------------------------------
// parseJSONMap
// ---------------------------------------------------------------------------

func TestParseJSONMap_Valid(t *testing.T) {
	m, err := parseJSONMap(`{"key":"value","count":1}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m["key"] != "value" {
		t.Errorf("m[key] = %v, want %q", m["key"], "value")
	}
}

func TestParseJSONMap_Invalid(t *testing.T) {
	_, err := parseJSONMap("{invalid json}")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	var jpe *JSONParseError
	if !errors.As(err, &jpe) {
		t.Fatalf("expected *JSONParseError, got %T", err)
	}
}

func TestParseJSONMap_EmptyObject(t *testing.T) {
	m, err := parseJSONMap(`{}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("expected empty map, got %d keys", len(m))
	}
}

// ---------------------------------------------------------------------------
// parseDistanceMetric
// ---------------------------------------------------------------------------

func TestParseDistanceMetric_Cosine(t *testing.T) {
	dm, err := parseDistanceMetric("cosine")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dm != transport.DistanceCosine {
		t.Errorf("got %q, want %q", dm, transport.DistanceCosine)
	}
}

func TestParseDistanceMetric_Euclidean(t *testing.T) {
	dm, err := parseDistanceMetric("euclidean")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dm != transport.DistanceEuclidean {
		t.Errorf("got %q, want %q", dm, transport.DistanceEuclidean)
	}
}

func TestParseDistanceMetric_DotProduct(t *testing.T) {
	dm, err := parseDistanceMetric("dot-product")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dm != transport.DistanceDotProduct {
		t.Errorf("got %q, want %q", dm, transport.DistanceDotProduct)
	}
}

func TestParseDistanceMetric_Invalid(t *testing.T) {
	_, err := parseDistanceMetric("hamming")
	if err == nil {
		t.Fatal("expected error for unknown distance metric")
	}
	var ide *InvalidDistanceMetricError
	if !errors.As(err, &ide) {
		t.Fatalf("expected *InvalidDistanceMetricError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// parseIndexType
// ---------------------------------------------------------------------------

func TestParseIndexType_HNSW(t *testing.T) {
	it, err := parseIndexType("hnsw")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if it != transport.IndexHNSW {
		t.Errorf("got %q, want %q", it, transport.IndexHNSW)
	}
}

func TestParseIndexType_Flat(t *testing.T) {
	it, err := parseIndexType("flat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if it != transport.IndexFlat {
		t.Errorf("got %q, want %q", it, transport.IndexFlat)
	}
}

func TestParseIndexType_IVFFlat(t *testing.T) {
	it, err := parseIndexType("ivf-flat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if it != transport.IndexIVFFlat {
		t.Errorf("got %q, want %q", it, transport.IndexIVFFlat)
	}
}

func TestParseIndexType_Invalid(t *testing.T) {
	_, err := parseIndexType("annoy")
	if err == nil {
		t.Fatal("expected error for unknown index type")
	}
	var iite *InvalidIndexTypeError
	if !errors.As(err, &iite) {
		t.Fatalf("expected *InvalidIndexTypeError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// parseConsistencyLevel
// ---------------------------------------------------------------------------

func TestParseConsistencyLevel_Empty(t *testing.T) {
	cl, err := parseConsistencyLevel("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cl != 0 {
		t.Errorf("expected 0, got %d", cl)
	}
}

func TestParseConsistencyLevel_Default(t *testing.T) {
	cl, err := parseConsistencyLevel("default")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cl != 0 {
		t.Errorf("expected 0, got %d", cl)
	}
}

func TestParseConsistencyLevel_Leader(t *testing.T) {
	cl, err := parseConsistencyLevel("leader")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cl != 1 {
		t.Errorf("expected 1, got %d", cl)
	}
}

func TestParseConsistencyLevel_Any(t *testing.T) {
	cl, err := parseConsistencyLevel("any")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cl != 2 {
		t.Errorf("expected 2, got %d", cl)
	}
}

func TestParseConsistencyLevel_Invalid(t *testing.T) {
	_, err := parseConsistencyLevel("strong")
	if err == nil {
		t.Fatal("expected error for unknown consistency level")
	}
	var icle *InvalidConsistencyLevelError
	if !errors.As(err, &icle) {
		t.Fatalf("expected *InvalidConsistencyLevelError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// parseVectorID
// ---------------------------------------------------------------------------

func TestParseVectorID_Valid(t *testing.T) {
	id, err := parseVectorID("42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 42 {
		t.Errorf("expected 42, got %d", id)
	}
}

func TestParseVectorID_Zero(t *testing.T) {
	id, err := parseVectorID("0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != 0 {
		t.Errorf("expected 0, got %d", id)
	}
}

func TestParseVectorID_Invalid(t *testing.T) {
	_, err := parseVectorID("notanumber")
	if err == nil {
		t.Fatal("expected error for non-numeric ID")
	}
	var ivide *InvalidVectorIDError
	if !errors.As(err, &ivide) {
		t.Fatalf("expected *InvalidVectorIDError, got %T", err)
	}
}

func TestParseVectorID_NegativeString(t *testing.T) {
	_, err := parseVectorID("-1")
	if err == nil {
		t.Fatal("expected error for negative ID")
	}
}

// ---------------------------------------------------------------------------
// parseVectorIDs
// ---------------------------------------------------------------------------

func TestParseVectorIDs_Single(t *testing.T) {
	ids, err := parseVectorIDs("5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 || ids[0] != 5 {
		t.Errorf("expected [5], got %v", ids)
	}
}

func TestParseVectorIDs_Multiple(t *testing.T) {
	ids, err := parseVectorIDs("1,2,3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 IDs, got %d", len(ids))
	}
}

func TestParseVectorIDs_Invalid(t *testing.T) {
	_, err := parseVectorIDs("1,bad,3")
	if err == nil {
		t.Fatal("expected error for invalid ID in list")
	}
}

// ---------------------------------------------------------------------------
// formatVector
// ---------------------------------------------------------------------------

func TestFormatVector_Empty(t *testing.T) {
	s := formatVector(nil)
	if s != "" {
		t.Errorf("expected empty string, got %q", s)
	}
}

func TestFormatVector_SingleElement(t *testing.T) {
	s := formatVector([]float32{1.5})
	if s != "1.5" {
		t.Errorf("expected %q, got %q", "1.5", s)
	}
}

func TestFormatVector_MultipleElements(t *testing.T) {
	s := formatVector([]float32{1.0, 2.0, 3.0})
	parts := strings.Split(s, ",")
	if len(parts) != 3 {
		t.Errorf("expected 3 comma-separated parts, got %d in %q", len(parts), s)
	}
}

// ---------------------------------------------------------------------------
// distanceMetricName
// ---------------------------------------------------------------------------

func TestDistanceMetricName_Known(t *testing.T) {
	cases := []struct {
		dm   transport.DistanceMetric
		want string
	}{
		{transport.DistanceCosine, "cosine"},
		{transport.DistanceEuclidean, "euclidean"},
		{transport.DistanceDotProduct, "dot-product"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			got := distanceMetricName(tc.dm)
			if got != tc.want {
				t.Errorf("distanceMetricName(%q) = %q, want %q", tc.dm, got, tc.want)
			}
		})
	}
}

func TestDistanceMetricName_Unknown(t *testing.T) {
	got := distanceMetricName("hamming")
	// Unknown values are passed through as-is.
	if got != "hamming" {
		t.Errorf("expected passthrough %q, got %q", "hamming", got)
	}
}

// ---------------------------------------------------------------------------
// indexTypeName
// ---------------------------------------------------------------------------

func TestIndexTypeName_Known(t *testing.T) {
	cases := []struct {
		it   transport.VectorIndexType
		want string
	}{
		{transport.IndexHNSW, "hnsw"},
		{transport.IndexFlat, "flat"},
		{transport.IndexIVFFlat, "ivf-flat"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			got := indexTypeName(tc.it)
			if got != tc.want {
				t.Errorf("indexTypeName(%q) = %q, want %q", tc.it, got, tc.want)
			}
		})
	}
}

func TestIndexTypeName_Unknown(t *testing.T) {
	got := indexTypeName("annoy")
	if got != "annoy" {
		t.Errorf("expected passthrough %q, got %q", "annoy", got)
	}
}

// ---------------------------------------------------------------------------
// printNamespaceInfo
// ---------------------------------------------------------------------------

func TestPrintNamespaceInfo_TextFormat(t *testing.T) {
	old := outputWriter
	buf := &bytes.Buffer{}
	outputWriter = buf
	defer func() { outputWriter = old }()

	oldFmt := outputFormat
	outputFormat = "text"
	defer func() { outputFormat = oldFmt }()

	info := &transport.GetNamespaceInfoResponse{
		Namespace:      "test-ns",
		Dimension:      128,
		DistanceMetric: transport.DistanceCosine,
		IndexType:      transport.IndexHNSW,
		VectorCount:    42,
	}
	if err := printNamespaceInfo(info); err != nil {
		t.Fatalf("printNamespaceInfo: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "test-ns") {
		t.Errorf("expected namespace in output %q", out)
	}
	if !strings.Contains(out, "128") {
		t.Errorf("expected dimension in output %q", out)
	}
}

func TestPrintNamespaceInfo_JSONFormat(t *testing.T) {
	old := outputWriter
	buf := &bytes.Buffer{}
	outputWriter = buf
	defer func() { outputWriter = old }()

	oldFmt := outputFormat
	outputFormat = "json"
	defer func() { outputFormat = oldFmt }()

	info := &transport.GetNamespaceInfoResponse{
		Namespace: "ns1",
		Dimension: 64,
	}
	if err := printNamespaceInfo(info); err != nil {
		t.Fatalf("printNamespaceInfo JSON: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "ns1") {
		t.Errorf("expected namespace in JSON output %q", out)
	}
}

// ---------------------------------------------------------------------------
// printGetVectorResponse
// ---------------------------------------------------------------------------

func TestPrintGetVectorResponse_TextFormat(t *testing.T) {
	old := outputWriter
	buf := &bytes.Buffer{}
	outputWriter = buf
	defer func() { outputWriter = old }()

	oldFmt := outputFormat
	outputFormat = "text"
	defer func() { outputFormat = oldFmt }()

	resp := &transport.GetVectorResponse{
		ID:      99,
		Found:   true,
		Vector:  []float32{1.0, 2.0},
		Version: 3,
	}
	if err := printGetVectorResponse(resp); err != nil {
		t.Fatalf("printGetVectorResponse: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "99") {
		t.Errorf("expected ID in output %q", out)
	}
}

func TestPrintGetVectorResponse_JSONFormat(t *testing.T) {
	old := outputWriter
	buf := &bytes.Buffer{}
	outputWriter = buf
	defer func() { outputWriter = old }()

	oldFmt := outputFormat
	outputFormat = "json"
	defer func() { outputFormat = oldFmt }()

	resp := &transport.GetVectorResponse{ID: 7, Found: true}
	if err := printGetVectorResponse(resp); err != nil {
		t.Fatalf("printGetVectorResponse JSON: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "7") {
		t.Errorf("expected ID in JSON output %q", out)
	}
}

// ---------------------------------------------------------------------------
// printSearchResults
// ---------------------------------------------------------------------------

func TestPrintSearchResults_TextFormat_Empty(t *testing.T) {
	old := outputWriter
	buf := &bytes.Buffer{}
	outputWriter = buf
	defer func() { outputWriter = old }()

	oldFmt := outputFormat
	outputFormat = "text"
	defer func() { outputFormat = oldFmt }()

	if err := printSearchResults(nil, 1234); err != nil {
		t.Fatalf("printSearchResults: %v", err)
	}
}

func TestPrintSearchResults_TextFormat_WithResults(t *testing.T) {
	old := outputWriter
	buf := &bytes.Buffer{}
	outputWriter = buf
	defer func() { outputWriter = old }()

	oldFmt := outputFormat
	outputFormat = "text"
	defer func() { outputFormat = oldFmt }()

	results := []*transport.VectorSearchResult{
		{ID: 1, Score: 0.95, Distance: 0.05, Rank: 1},
		{ID: 2, Score: 0.80, Distance: 0.20, Rank: 2},
	}
	if err := printSearchResults(results, 999); err != nil {
		t.Fatalf("printSearchResults: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "999") {
		t.Errorf("expected query duration in output %q", out)
	}
}

func TestPrintSearchResults_JSONFormat(t *testing.T) {
	old := outputWriter
	buf := &bytes.Buffer{}
	outputWriter = buf
	defer func() { outputWriter = old }()

	oldFmt := outputFormat
	outputFormat = "json"
	defer func() { outputFormat = oldFmt }()

	if err := printSearchResults([]*transport.VectorSearchResult{{ID: 1, Score: 1.0, Rank: 1}}, 100); err != nil {
		t.Fatalf("printSearchResults JSON: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "results") {
		t.Errorf("expected results key in JSON output %q", out)
	}
}

// ---------------------------------------------------------------------------
// printRebuildProgress
// ---------------------------------------------------------------------------

func TestPrintRebuildProgress_TextFormat(t *testing.T) {
	old := outputWriter
	buf := &bytes.Buffer{}
	outputWriter = buf
	defer func() { outputWriter = old }()

	oldFmt := outputFormat
	outputFormat = "text"
	defer func() { outputFormat = oldFmt }()

	progress := &transport.GetRebuildProgressResponse{
		JobID:            "job-1",
		Namespace:        "ns",
		Status:           "running",
		PercentComplete:  50.0,
		VectorsProcessed: 500,
		TotalVectors:     1000,
		StartedAt:        1234567890,
	}
	if err := printRebuildProgress(progress); err != nil {
		t.Fatalf("printRebuildProgress: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "job-1") {
		t.Errorf("expected JobID in output %q", out)
	}
}

func TestPrintRebuildProgress_JSONFormat(t *testing.T) {
	old := outputWriter
	buf := &bytes.Buffer{}
	outputWriter = buf
	defer func() { outputWriter = old }()

	oldFmt := outputFormat
	outputFormat = "json"
	defer func() { outputFormat = oldFmt }()

	progress := &transport.GetRebuildProgressResponse{
		JobID:     "job-2",
		Namespace: "ns",
		Status:    "done",
	}
	if err := printRebuildProgress(progress); err != nil {
		t.Fatalf("printRebuildProgress JSON: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "job-2") {
		t.Errorf("expected JobID in JSON output %q", out)
	}
}

func TestPrintRebuildProgress_WithCompletedAt(t *testing.T) {
	old := outputWriter
	buf := &bytes.Buffer{}
	outputWriter = buf
	defer func() { outputWriter = old }()

	oldFmt := outputFormat
	outputFormat = "text"
	defer func() { outputFormat = oldFmt }()

	progress := &transport.GetRebuildProgressResponse{
		JobID:       "job-3",
		CompletedAt: 9999,
		Error:       "some error",
	}
	if err := printRebuildProgress(progress); err != nil {
		t.Fatalf("printRebuildProgress: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "some error") {
		t.Errorf("expected error in output %q", out)
	}
}

// ---------------------------------------------------------------------------
// printCompactionProgress
// ---------------------------------------------------------------------------

func TestPrintCompactionProgress_TextFormat(t *testing.T) {
	old := outputWriter
	buf := &bytes.Buffer{}
	outputWriter = buf
	defer func() { outputWriter = old }()

	oldFmt := outputFormat
	outputFormat = "text"
	defer func() { outputFormat = oldFmt }()

	progress := &transport.GetCompactionProgressResponse{
		OperationID:     "op-1",
		Namespace:       "ns",
		InProgress:      true,
		PercentComplete: 25.0,
		BytesCompacted:  1024,
		TotalBytes:      4096,
		StartedAt:       100,
	}
	if err := printCompactionProgress(progress); err != nil {
		t.Fatalf("printCompactionProgress: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "op-1") {
		t.Errorf("expected OperationID in output %q", out)
	}
}

func TestPrintCompactionProgress_JSONFormat(t *testing.T) {
	old := outputWriter
	buf := &bytes.Buffer{}
	outputWriter = buf
	defer func() { outputWriter = old }()

	oldFmt := outputFormat
	outputFormat = "json"
	defer func() { outputFormat = oldFmt }()

	progress := &transport.GetCompactionProgressResponse{
		OperationID: "op-2",
	}
	if err := printCompactionProgress(progress); err != nil {
		t.Fatalf("printCompactionProgress JSON: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "op-2") {
		t.Errorf("expected OperationID in JSON output %q", out)
	}
}

func TestPrintCompactionProgress_WithCompletedAt(t *testing.T) {
	old := outputWriter
	buf := &bytes.Buffer{}
	outputWriter = buf
	defer func() { outputWriter = old }()

	oldFmt := outputFormat
	outputFormat = "text"
	defer func() { outputFormat = oldFmt }()

	completedAt := int64(5555)
	progress := &transport.GetCompactionProgressResponse{
		OperationID: "op-3",
		CompletedAt: &completedAt,
		Error:       "compaction error",
	}
	if err := printCompactionProgress(progress); err != nil {
		t.Fatalf("printCompactionProgress: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "compaction error") {
		t.Errorf("expected error in output %q", out)
	}
}

// ---------------------------------------------------------------------------
// yaml format coverage for print functions
// ---------------------------------------------------------------------------

func TestPrintNamespaceInfo_YAMLFormat(t *testing.T) {
	old := outputWriter
	buf := &bytes.Buffer{}
	outputWriter = buf
	defer func() { outputWriter = old }()

	oldFmt := outputFormat
	outputFormat = "yaml"
	defer func() { outputFormat = oldFmt }()

	info := &transport.GetNamespaceInfoResponse{
		Namespace: "yaml-ns",
		Dimension: 32,
	}
	if err := printNamespaceInfo(info); err != nil {
		t.Fatalf("printNamespaceInfo YAML: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "yaml-ns") {
		t.Errorf("expected namespace in YAML output %q", out)
	}
}

func TestPrintGetVectorResponse_TextFormat_WithMetadata(t *testing.T) {
	old := outputWriter
	buf := &bytes.Buffer{}
	outputWriter = buf
	defer func() { outputWriter = old }()

	oldFmt := outputFormat
	outputFormat = "text"
	defer func() { outputFormat = oldFmt }()

	resp := &transport.GetVectorResponse{
		ID:       55,
		Found:    true,
		Vector:   []float32{0.1, 0.2},
		Metadata: map[string]interface{}{"tag": "test"},
	}
	if err := printGetVectorResponse(resp); err != nil {
		t.Fatalf("printGetVectorResponse with metadata: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "tag") {
		t.Errorf("expected metadata key in output %q", out)
	}
}

func TestPrintGetVectorResponse_YAMLFormat(t *testing.T) {
	old := outputWriter
	buf := &bytes.Buffer{}
	outputWriter = buf
	defer func() { outputWriter = old }()

	oldFmt := outputFormat
	outputFormat = "yaml"
	defer func() { outputFormat = oldFmt }()

	resp := &transport.GetVectorResponse{ID: 12, Found: false}
	if err := printGetVectorResponse(resp); err != nil {
		t.Fatalf("printGetVectorResponse YAML: %v", err)
	}
	out := buf.String()
	if len(out) == 0 {
		t.Error("expected non-empty YAML output")
	}
}

func TestPrintSearchResults_YAMLFormat(t *testing.T) {
	old := outputWriter
	buf := &bytes.Buffer{}
	outputWriter = buf
	defer func() { outputWriter = old }()

	oldFmt := outputFormat
	outputFormat = "yaml"
	defer func() { outputFormat = oldFmt }()

	results := []*transport.VectorSearchResult{{ID: 1, Score: 0.9, Rank: 1}}
	if err := printSearchResults(results, 42); err != nil {
		t.Fatalf("printSearchResults YAML: %v", err)
	}
	out := buf.String()
	if len(out) == 0 {
		t.Error("expected non-empty YAML output")
	}
}

func TestPrintRebuildProgress_YAMLFormat(t *testing.T) {
	old := outputWriter
	buf := &bytes.Buffer{}
	outputWriter = buf
	defer func() { outputWriter = old }()

	oldFmt := outputFormat
	outputFormat = "yaml"
	defer func() { outputFormat = oldFmt }()

	progress := &transport.GetRebuildProgressResponse{
		JobID:  "yaml-job",
		Status: "running",
	}
	if err := printRebuildProgress(progress); err != nil {
		t.Fatalf("printRebuildProgress YAML: %v", err)
	}
	out := buf.String()
	if len(out) == 0 {
		t.Error("expected non-empty YAML output")
	}
}

func TestPrintCompactionProgress_YAMLFormat(t *testing.T) {
	old := outputWriter
	buf := &bytes.Buffer{}
	outputWriter = buf
	defer func() { outputWriter = old }()

	oldFmt := outputFormat
	outputFormat = "yaml"
	defer func() { outputFormat = oldFmt }()

	progress := &transport.GetCompactionProgressResponse{
		OperationID: "yaml-op",
	}
	if err := printCompactionProgress(progress); err != nil {
		t.Fatalf("printCompactionProgress YAML: %v", err)
	}
	out := buf.String()
	if len(out) == 0 {
		t.Error("expected non-empty YAML output")
	}
}
