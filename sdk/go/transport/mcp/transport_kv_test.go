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
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// Scan - the MCP server marshals values as map[string][]byte, which
// encoding/json base64-encodes (RFC 4648); the SDK's Scan decodes the same
// way, so arbitrary binary content round-trips byte-for-byte. See
// TestScan_BinaryRoundTrip below for an end-to-end value assertion.
// ---------------------------------------------------------------------------

// nonUTF8Value is a byte string that is not valid UTF-8: 0xFF and 0xFE are
// invalid lead bytes in every UTF-8 sequence. Round-tripping it through the
// MCP transport (which carries values as base64 JSON strings, see
// pkg/api/mcp/tools_kv.go) must reproduce it byte-for-byte.
var nonUTF8Value = []byte{0x00, 0xff, 0xfe, 0x01}

func TestPutGet_BinaryRoundTrip(t *testing.T) {
	kvSvc := newMockKVService()
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
	defer cleanup()

	ctx := context.Background()
	if err := tr.Put(ctx, "binkey", nonUTF8Value); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := tr.Get(ctx, "binkey")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(nonUTF8Value) {
		t.Errorf("Get = %#v, want %#v", got, nonUTF8Value)
	}
}

func TestScan_BinaryRoundTrip(t *testing.T) {
	kvSvc := newMockKVService()
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
	defer cleanup()

	ctx := context.Background()
	if err := tr.Put(ctx, "bin/a", nonUTF8Value); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := tr.Put(ctx, "bin/b", []byte("plain text")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	pairs, err := tr.Scan(ctx, "bin/")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(pairs) != 2 {
		t.Fatalf("expected 2 pairs, got %d: %v", len(pairs), pairs)
	}
	if got := pairs["bin/a"]; string(got) != string(nonUTF8Value) {
		t.Errorf("bin/a = %#v, want %#v", got, nonUTF8Value)
	}
	if got := pairs["bin/b"]; string(got) != "plain text" {
		t.Errorf("bin/b = %q, want %q", got, "plain text")
	}
}

func TestScan_EmptyResult(t *testing.T) {
	kvSvc := newMockKVService()
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
	defer cleanup()

	// Empty store -> empty map; no decode issues.
	resp, err := tr.Scan(context.Background(), "anything")
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(resp) != 0 {
		t.Errorf("expected empty map, got %d", len(resp))
	}
}

func TestScan_ServiceError(t *testing.T) {
	kvSvc := newMockKVService()
	kvSvc.scanErr = errors.New("scan failed")
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
	defer cleanup()

	if _, err := tr.Scan(context.Background(), ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestExists_True(t *testing.T) {
	kvSvc := newMockKVService()
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
	defer cleanup()

	ctx := context.Background()
	if err := tr.Put(ctx, "key", []byte("value")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	exists, err := tr.Exists(ctx, "key")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Error("expected exists=true")
	}
}

func TestExists_False(t *testing.T) {
	kvSvc := newMockKVService()
	tr, cleanup := connectInProcess(t, kvSvc, newMockAdminService())
	defer cleanup()

	exists, err := tr.Exists(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Error("expected exists=false")
	}
}

// ---------------------------------------------------------------------------
// Capabilities
// ---------------------------------------------------------------------------

func TestCapabilities_Success(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	resp, err := tr.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if !resp.KV {
		t.Error("expected KV=true")
	}
}
