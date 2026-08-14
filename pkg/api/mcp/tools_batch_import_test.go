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
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// handleBatchImport
// ---------------------------------------------------------------------------

func TestHandleBatchImport_Success(t *testing.T) {
	svc := &mockBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return &service.ImportResult{Total: 2, Succeeded: 2}, nil
		},
	}
	handler := handleBatchImport(svc)

	encoded := base64.StdEncoding.EncodeToString([]byte("id,value\nk1,v1\n"))
	result, err := handler(context.Background(), makeReq(map[string]any{
		"file":       encoded,
		"format":     "csv",
		"key_field":  "id",
		"value_mode": "row",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %v", resultText(t, result))
	}

	text := resultText(t, result)
	var ir service.ImportResult
	if jsonErr := json.Unmarshal([]byte(text), &ir); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if ir.Succeeded != 2 {
		t.Errorf("expected Succeeded=2, got %d", ir.Succeeded)
	}
}

func TestHandleBatchImport_MissingFile(t *testing.T) {
	svc := &mockBatchService{}
	handler := handleBatchImport(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"format":     "csv",
		"key_field":  "id",
		"value_mode": "row",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing file")
	}
}

// TestHandleBatchImport_EncodedTooLarge verifies that an oversized base64
// "file" argument is rejected BEFORE it is decoded (M4): the mock service's
// Import is never invoked, and no decode is attempted.
func TestHandleBatchImport_EncodedTooLarge(t *testing.T) {
	importCalled := false
	svc := &mockBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			importCalled = true
			return &service.ImportResult{}, nil
		},
	}
	handler := handleBatchImport(svc)

	oversizedEncoded := strings.Repeat("A", maxEncodedImportFileSize+4)
	result, err := handler(context.Background(), makeReq(map[string]any{
		"file":       oversizedEncoded,
		"format":     "csv",
		"key_field":  "id",
		"value_mode": "row",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for oversized encoded file")
	}
	if importCalled {
		t.Error("expected Import to never be called for an oversized encoded file")
	}
	text := resultText(t, result)
	if !strings.Contains(text, "too large") {
		t.Errorf("expected 'too large' in error message, got %q", text)
	}
}

// TestHandleBatchImport_DecodedTooLarge verifies the decode-side bound: a
// base64 payload that passes the pre-decode length gate (or is crafted
// right at the boundary) but decodes to more than maxImportFileSize bytes
// is still rejected, without ever calling the batch service.
func TestHandleBatchImport_DecodedTooLarge(t *testing.T) {
	importCalled := false
	svc := &mockBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			importCalled = true
			return &service.ImportResult{}, nil
		},
	}
	handler := handleBatchImport(svc)

	oversized := bytes.Repeat([]byte("a"), maxImportFileSize+1)
	encoded := base64.StdEncoding.EncodeToString(oversized)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"file":       encoded,
		"format":     "csv",
		"key_field":  "id",
		"value_mode": "row",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected error result for a file that decodes over the max size")
	}
	if importCalled {
		t.Error("expected Import to never be called for an oversized decoded file")
	}
}

func TestHandleBatchImport_InvalidBase64(t *testing.T) {
	svc := &mockBatchService{}
	handler := handleBatchImport(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"file":       "not-valid-base64!!!",
		"format":     "csv",
		"key_field":  "id",
		"value_mode": "row",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for invalid base64")
	}
}

func TestHandleBatchImport_MissingFormat(t *testing.T) {
	svc := &mockBatchService{}
	handler := handleBatchImport(svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"file":       base64.StdEncoding.EncodeToString([]byte("x")),
		"key_field":  "id",
		"value_mode": "row",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result for missing format")
	}
}

func TestHandleBatchImport_ServiceError(t *testing.T) {
	svc := &mockBatchService{
		importFn: func(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
			return nil, &service.ImportTooManyRowsError{Max: 1000}
		},
	}
	handler := handleBatchImport(svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"file":       base64.StdEncoding.EncodeToString([]byte("id,value\nk1,v1\n")),
		"format":     "csv",
		"key_field":  "id",
		"value_mode": "row",
	}))
	if err == nil {
		t.Fatal("expected error from service")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
}
