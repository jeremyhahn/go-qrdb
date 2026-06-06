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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// DEK tools are not registered on the MCP server in this SDK. These tests
// exercise the SDK's request path and verify the expected "tool not found"
// error response when the SDK calls a non-existent MCP tool. This still
// covers the SDK marshaling and error-handling code paths.

// ---------------------------------------------------------------------------
// Closed-state paths
// ---------------------------------------------------------------------------

func TestDEK_AfterClose(t *testing.T) {
	tr := newClosedTransport()
	var ce *transport.ClosedError
	ctx := context.Background()

	if err := tr.DEKInitialize(ctx, &transport.DEKInitializeRequest{}); !errors.As(err, &ce) {
		t.Errorf("DEKInitialize: %v", err)
	}
	if _, err := tr.DEKGetStatus(ctx); !errors.As(err, &ce) {
		t.Errorf("DEKGetStatus: %v", err)
	}
	if err := tr.DEKRotate(ctx); !errors.As(err, &ce) {
		t.Errorf("DEKRotate: %v", err)
	}
	if _, err := tr.DEKGetCurrent(ctx); !errors.As(err, &ce) {
		t.Errorf("DEKGetCurrent: %v", err)
	}
	if err := tr.DEKSyncToNode(ctx, &transport.DEKSyncRequest{}); !errors.As(err, &ce) {
		t.Errorf("DEKSyncToNode: %v", err)
	}
	if _, err := tr.TenantDEKGetOrCreate(ctx, &transport.TenantDEKRequest{}); !errors.As(err, &ce) {
		t.Errorf("TenantDEKGetOrCreate: %v", err)
	}
	if err := tr.TenantDEKRotate(ctx, &transport.TenantDEKRotateRequest{}); !errors.As(err, &ce) {
		t.Errorf("TenantDEKRotate: %v", err)
	}
	if err := tr.TenantDEKDelete(ctx, &transport.TenantDEKDeleteRequest{}); !errors.As(err, &ce) {
		t.Errorf("TenantDEKDelete: %v", err)
	}
	if _, err := tr.TenantDEKList(ctx); !errors.As(err, &ce) {
		t.Errorf("TenantDEKList: %v", err)
	}
	if _, err := tr.TenantDEKGetStatus(ctx, &transport.TenantDEKRequest{}); !errors.As(err, &ce) {
		t.Errorf("TenantDEKGetStatus: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Live-call paths: DEK tools are not registered, so calls return a tool-
// not-found error. We still exercise the SDK's marshaling and error wrap.
// ---------------------------------------------------------------------------

func TestDEKInitialize_ToolNotFound(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	err := tr.DEKInitialize(context.Background(), &transport.DEKInitializeRequest{
		Algorithm: "AES",
		KeyBytes:  32,
	})
	if err == nil {
		t.Fatal("expected tool-not-found error")
	}
}

func TestDEKGetStatus_ToolNotFound(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	if _, err := tr.DEKGetStatus(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestDEKRotate_ToolNotFound(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	if err := tr.DEKRotate(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestDEKGetCurrent_ToolNotFound(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	if _, err := tr.DEKGetCurrent(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestDEKSyncToNode_ToolNotFound(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	if err := tr.DEKSyncToNode(context.Background(), &transport.DEKSyncRequest{NodeID: 1}); err == nil {
		t.Fatal("expected error")
	}
}

func TestTenantDEKGetOrCreate_ToolNotFound(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	if _, err := tr.TenantDEKGetOrCreate(context.Background(), &transport.TenantDEKRequest{TenantID: "t1"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestTenantDEKRotate_ToolNotFound(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	if err := tr.TenantDEKRotate(context.Background(), &transport.TenantDEKRotateRequest{TenantID: "t1"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestTenantDEKDelete_ToolNotFound(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	if err := tr.TenantDEKDelete(context.Background(), &transport.TenantDEKDeleteRequest{TenantID: "t1"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestTenantDEKList_ToolNotFound(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	if _, err := tr.TenantDEKList(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestTenantDEKGetStatus_ToolNotFound(t *testing.T) {
	tr, cleanup := connectInProcess(t, newMockKVService(), newMockAdminService())
	defer cleanup()

	if _, err := tr.TenantDEKGetStatus(context.Background(), &transport.TenantDEKRequest{TenantID: "t1"}); err == nil {
		t.Fatal("expected error")
	}
}
