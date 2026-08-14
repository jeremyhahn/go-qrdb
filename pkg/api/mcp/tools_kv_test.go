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
)

// ---------------------------------------------------------------------------
// Binary round-trip: qrdb_put / qrdb_get / qrdb_scan carry values as base64
// (RFC 4648), matching the REST /kv/{key} MVCC-metadata and /kv/scan wire
// representation (see pkg/api/rest/handlers.go's kvResponse and
// kvScanResponse). This preserves non-UTF-8 bytes byte-for-byte instead of
// corrupting them to U+FFFD when the MCP transport's JSON envelope is
// serialized.
// ---------------------------------------------------------------------------

// nonUTF8Value is a byte string that is not valid UTF-8: 0xFF and 0xFE are
// invalid lead bytes in every UTF-8 sequence.
var nonUTF8Value = []byte{0x00, 0xff, 0xfe, 0x01}

func TestHandlePutHandleGet_BinaryRoundTrip(t *testing.T) {
	kvSvc := newMockKVService()
	putHandler := handlePut(kvSvc)
	getHandler := handleGet(kvSvc)

	putReq := buildCallToolRequest(t, "qrdb_put", map[string]any{
		"key":   "binkey",
		"value": base64.StdEncoding.EncodeToString(nonUTF8Value),
	})
	putResult, err := putHandler(context.Background(), putReq)
	if err != nil {
		t.Fatalf("handlePut: %v", err)
	}
	if putResult.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, putResult))
	}

	getReq := buildCallToolRequest(t, "qrdb_get", map[string]any{
		"key": "binkey",
	})
	getResult, err := getHandler(context.Background(), getReq)
	if err != nil {
		t.Fatalf("handleGet: %v", err)
	}
	if getResult.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, getResult))
	}

	text := resultText(t, getResult)
	got, decodeErr := base64.StdEncoding.DecodeString(text)
	if decodeErr != nil {
		t.Fatalf("result is not valid base64: %v (text=%q)", decodeErr, text)
	}
	if string(got) != string(nonUTF8Value) {
		t.Errorf("round-tripped value = %#v, want %#v", got, nonUTF8Value)
	}
}

func TestHandlePut_InvalidBase64ValueReturnsError(t *testing.T) {
	kvSvc := newMockKVService()
	handler := handlePut(kvSvc)

	// "not valid base64!" contains characters outside the base64 alphabet.
	req := buildCallToolRequest(t, "qrdb_put", map[string]any{
		"key":   "k",
		"value": "not valid base64!",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected IsError=true for invalid base64 value")
	}
}

func TestHandleScan_BinaryRoundTrip(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.store["bin/a"] = nonUTF8Value
	kvSvc.store["bin/b"] = []byte("plain text")
	handler := handleScan(kvSvc)

	req := buildCallToolRequest(t, "qrdb_scan", map[string]any{
		"prefix": "bin/",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleScan: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected tool error: %v", resultText(t, result))
	}

	text := resultText(t, result)
	var pairs map[string][]byte
	if jsonErr := json.Unmarshal([]byte(text), &pairs); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}

	if got := pairs["bin/a"]; string(got) != string(nonUTF8Value) {
		t.Errorf("bin/a = %#v, want %#v", got, nonUTF8Value)
	}
	if got := pairs["bin/b"]; string(got) != "plain text" {
		t.Errorf("bin/b = %q, want %q", got, "plain text")
	}
}

// TestHandleGet_TextValueRoundTrip confirms a UTF-8 text value survives the
// same base64 encoding unchanged, so the schema change is not a regression
// for the common case.
func TestHandleGet_TextValueRoundTrip(t *testing.T) {
	kvSvc := newMockKVService()
	putHandler := handlePut(kvSvc)
	getHandler := handleGet(kvSvc)

	putReq := buildCallToolRequest(t, "qrdb_put", map[string]any{
		"key":   "textkey",
		"value": base64.StdEncoding.EncodeToString([]byte("hello world")),
	})
	if _, err := putHandler(context.Background(), putReq); err != nil {
		t.Fatalf("handlePut: %v", err)
	}

	getReq := buildCallToolRequest(t, "qrdb_get", map[string]any{"key": "textkey"})
	result, err := getHandler(context.Background(), getReq)
	if err != nil {
		t.Fatalf("handleGet: %v", err)
	}

	got, decodeErr := base64.StdEncoding.DecodeString(resultText(t, result))
	if decodeErr != nil {
		t.Fatalf("decode: %v", decodeErr)
	}
	if string(got) != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

// Compile-time sanity check that the mcp package alias used throughout this
// file resolves to the mark3labs SDK type, not this package's own name.
var _ mcp.Tool = toolPut()
