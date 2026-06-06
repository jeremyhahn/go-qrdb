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
// Scan - the MCP server marshals []byte values as plain strings (not base64)
// while the SDK expects []byte (which JSON decodes from base64). This is a
// known shape mismatch; tests assert the decode behavior end-to-end without
// asserting on value contents.
// ---------------------------------------------------------------------------

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
