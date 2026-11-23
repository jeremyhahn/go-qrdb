// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestTransport_ListTenants_OlderServerIgnoresStatus is the rolling-upgrade
// case. The double is a base-version server: its qrdb_tenant_list tool
// predates the status argument, drops it and answers every tenant. A
// filtered list must fail with *CapabilityUnsupportedError and no tenants;
// an unfiltered list still succeeds.
func TestTransport_ListTenants_OlderServerIgnoresStatus(t *testing.T) {
	srv := mcpserver.NewMCPServer("older-tenant-list", "0")
	srv.AddTool(mcp.NewTool("qrdb_tenant_list"), func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(`[{"id":"t-active","name":"a","status":"active"},` +
			`{"id":"t-suspended","name":"s","status":"suspended"}]`), nil
	})
	tr, err := NewInProcess(srv)
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer func() { _ = tr.Close() }()

	resp, err := tr.ListTenants(context.Background(), &transport.ListTenantsRequest{Status: transport.TenantStatusSuspended})
	var unsupported *transport.CapabilityUnsupportedError
	if !errors.As(err, &unsupported) || resp != nil {
		t.Fatalf("ListTenants(status=suspended) on an older server = %+v, %v; want nil, CapabilityUnsupportedError", resp, err)
	}

	resp, err = tr.ListTenants(context.Background(), nil)
	if err != nil || len(resp.Tenants) != 2 {
		t.Fatalf("unfiltered ListTenants on an older server = %+v, %v; want 2 tenants", resp, err)
	}
}
