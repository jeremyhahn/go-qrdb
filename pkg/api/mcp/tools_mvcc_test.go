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
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
)

// TestHandleMVCCPutWithLease_BinaryRoundTrip confirms qrdb_mvcc_put_with_lease
// accepts a base64-encoded (RFC 4648) "value" argument, decodes it before
// calling the service layer, and the returned KeyValue's "value" field
// (encoding/json base64-encodes []byte struct fields automatically) decodes
// back to the exact original bytes -- including non-UTF-8 content that would
// otherwise be corrupted to U+FFFD if carried as a raw JSON string.
func TestHandleMVCCPutWithLease_BinaryRoundTrip(t *testing.T) {
	svc := newMockMVCCService()
	tools := mvccTools(svc)
	var handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
	for _, tool := range tools {
		if tool.Tool.Name == "qrdb_mvcc_put_with_lease" {
			handler = tool.Handler
			break
		}
	}
	if handler == nil {
		t.Fatal("tool qrdb_mvcc_put_with_lease not found")
	}

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":      "binkey",
		"value":    base64.StdEncoding.EncodeToString(nonUTF8Value),
		"lease_id": float64(7),
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, result))
	}

	text := resultText(t, result)
	var kvResult kv.KeyValue
	if jsonErr := json.Unmarshal([]byte(text), &kvResult); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if string(kvResult.Value) != string(nonUTF8Value) {
		t.Errorf("round-tripped value = %#v, want %#v", kvResult.Value, nonUTF8Value)
	}
	if string(kvResult.Key) != "binkey" {
		t.Errorf("key = %q, want %q", kvResult.Key, "binkey")
	}
}

// TestHandleMVCCPutWithLease_InvalidBase64ValueReturnsError mirrors the KV
// qrdb_put behavior: a "value" argument that is not valid base64 is
// rejected with a tool error rather than silently truncated or corrupted.
func TestHandleMVCCPutWithLease_InvalidBase64ValueReturnsError(t *testing.T) {
	svc := newMockMVCCService()
	tools := mvccTools(svc)
	var handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
	for _, tool := range tools {
		if tool.Tool.Name == "qrdb_mvcc_put_with_lease" {
			handler = tool.Handler
			break
		}
	}
	if handler == nil {
		t.Fatal("tool qrdb_mvcc_put_with_lease not found")
	}

	result, err := handler(context.Background(), makeReq(map[string]any{
		"key":      "k",
		"value":    "not valid base64!",
		"lease_id": float64(1),
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for invalid base64 value")
	}
}
