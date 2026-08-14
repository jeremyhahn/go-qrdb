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
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// restoreHandler locates the qrdb_mvcc_restore_to_revision tool handler from
// mvccTools(svc).
func restoreHandler(t *testing.T, svc service.MVCCKVServicer) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	t.Helper()
	tools := mvccTools(svc)
	for _, tool := range tools {
		if tool.Tool.Name == "qrdb_mvcc_restore_to_revision" {
			return tool.Handler
		}
	}
	t.Fatal("tool qrdb_mvcc_restore_to_revision not found")
	return nil
}

func TestHandleMVCCRestoreToRevision_Success(t *testing.T) {
	var captured *service.RestoreRevisionRequest
	svc := newMockMVCCService()
	svc.restoreFn = func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
		captured = req
		return &service.RestoreRevisionResult{
			Revision: 5,
			Changes: []service.RestoreKeyChange{
				{Key: "a", Action: service.RestoreActionRevert, FromRevision: 9, ToRevision: 5, ValueBytes: 3},
			},
			Applied: 1,
		}, nil
	}
	handler := restoreHandler(t, svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"revision": float64(5),
		"prefix":   base64.StdEncoding.EncodeToString([]byte("prefix")),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, result))
	}
	if captured == nil || captured.Prefix != "prefix" {
		t.Fatalf("expected service prefix %q, got %+v", "prefix", captured)
	}

	var resp mvccRestoreResultResponse
	if jsonErr := json.Unmarshal([]byte(resultText(t, result)), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.Revision != 5 || resp.Applied != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if len(resp.Changes) != 1 || string(resp.Changes[0].Key) != "a" {
		t.Fatalf("unexpected changes: %+v", resp.Changes)
	}
}

func TestHandleMVCCRestoreToRevision_DryRun(t *testing.T) {
	var captured *service.RestoreRevisionRequest
	svc := newMockMVCCService()
	svc.restoreFn = func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
		captured = req
		return &service.RestoreRevisionResult{Revision: 5, DryRun: true}, nil
	}
	handler := restoreHandler(t, svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"revision": float64(5),
		"prefix":   base64.StdEncoding.EncodeToString([]byte("prefix")),
		"dry_run":  true,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil || !captured.DryRun {
		t.Fatal("expected service to receive DryRun=true")
	}

	var resp mvccRestoreResultResponse
	if jsonErr := json.Unmarshal([]byte(resultText(t, result)), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if !resp.DryRun {
		t.Fatal("expected dry_run=true in response")
	}
}

// TestHandleMVCCRestoreToRevision_ForceWithConflicts also proves binary
// round-trip through the "keys" array argument: each element is
// base64-decoded before reaching the service layer, and each returned
// change's "key" is base64-encoded (via []byte JSON marshaling) on the way
// back out.
func TestHandleMVCCRestoreToRevision_ForceWithConflicts(t *testing.T) {
	binaryKey := []byte{0x00, 0xff, 0xfe, 'b'}
	var captured *service.RestoreRevisionRequest
	svc := newMockMVCCService()
	svc.restoreFn = func(_ context.Context, req *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
		captured = req
		return &service.RestoreRevisionResult{
			Revision: 5,
			Conflicts: []service.RestoreKeyChange{
				{Key: string(binaryKey), Action: service.RestoreActionRevert, FromRevision: 9, ToRevision: 5},
			},
			Applied:    1,
			Conflicted: 1,
		}, nil
	}
	handler := restoreHandler(t, svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"revision": float64(5),
		"keys": []any{
			base64.StdEncoding.EncodeToString([]byte("a")),
			base64.StdEncoding.EncodeToString(binaryKey),
		},
		"force": true,
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured == nil || !captured.Force {
		t.Fatal("expected service to receive Force=true")
	}
	if len(captured.Keys) != 2 || captured.Keys[0] != "a" || captured.Keys[1] != string(binaryKey) {
		t.Fatalf("expected decoded keys [a %v], got %v", binaryKey, captured.Keys)
	}

	var resp mvccRestoreResultResponse
	if jsonErr := json.Unmarshal([]byte(resultText(t, result)), &resp); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if resp.Applied != 1 || resp.Conflicted != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if len(resp.Conflicts) != 1 || string(resp.Conflicts[0].Key) != string(binaryKey) {
		t.Fatalf("expected round-tripped binary conflict key, got %+v", resp.Conflicts)
	}
}

func TestHandleMVCCRestoreToRevision_MissingRevision(t *testing.T) {
	svc := newMockMVCCService()
	handler := restoreHandler(t, svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"prefix": base64.StdEncoding.EncodeToString([]byte("prefix")),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected tool error for missing revision")
	}
}

func TestHandleMVCCRestoreToRevision_InvalidBase64Prefix(t *testing.T) {
	svc := newMockMVCCService()
	handler := restoreHandler(t, svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"revision": float64(5),
		"prefix":   "not-valid-base64!!",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected tool error for invalid base64 prefix")
	}
}

func TestHandleMVCCRestoreToRevision_InvalidBase64Key(t *testing.T) {
	svc := newMockMVCCService()
	handler := restoreHandler(t, svc)

	result, err := handler(context.Background(), makeReq(map[string]any{
		"revision": float64(5),
		"keys":     []any{"not-valid-base64!!"},
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected tool error for invalid base64 key")
	}
}

// TestHandleMVCCRestoreToRevision_ServiceError pins that a guard error from
// the service layer (whose Error() text already names the fix, for
// MVCCBackendRequiredError) surfaces to the caller as a tool error.
func TestHandleMVCCRestoreToRevision_ServiceError(t *testing.T) {
	svc := newMockMVCCService()
	svc.restoreFn = func(_ context.Context, _ *service.RestoreRevisionRequest) (*service.RestoreRevisionResult, error) {
		return nil, &service.MVCCBackendRequiredError{}
	}
	handler := restoreHandler(t, svc)

	_, err := handler(context.Background(), makeReq(map[string]any{
		"revision": float64(5),
		"prefix":   base64.StdEncoding.EncodeToString([]byte("prefix")),
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected *ToolError, got %T: %v", err, err)
	}
	if got := toolErr.Error(); got == "" {
		t.Fatal("expected a non-empty error message")
	}
}
