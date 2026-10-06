// Copyright (c) 2026 Jeremy Hahn
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
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// certTools returns the MCP tool registrations for certificate management
// operations.
//
// The Perm on each registration is authorized against the CALLER's tenant by
// AuthorizeMCP. It cannot authorize the tenant the TOOL CALL names, and three
// of these name theirs — tenant_id on list and, indirectly, the serial revoke
// resolves. (Issuing and enrolling are enrollmentTools.) So mw is threaded into the handlers
// for the second question, answered by the one cross-transport home of the
// rule (pkg/auth/middleware/cert_authz.go).
//
// qrdb_cert_list is the sharpest case here: its tenant_id DEFAULTS to the
// system tenant when omitted, so without the gate a tenant-local agent that
// simply left the argument off read the platform's own inventory.
//
// mw may be nil (no-auth deployments), in which case the gates are no-ops.
func certTools(svc cert.CertProvider, mw *authmw.AuthMiddleware) []serverTool {
	return []serverTool{
		{
			Tool:    toolCertRevoke(),
			Perm:    permission.CertsRevoke,
			Handler: handleCertRevoke(svc, mw),
		},
		{
			Tool:    toolCertList(),
			Perm:    permission.CertsList,
			Handler: handleCertList(svc, mw),
		},
	}
}

// authorizeCertTenantTool gates a certificate tool on the tenant the CALL
// names rather than on the tenant the caller happens to be in. A nil middleware
// is the no-auth posture and admits.
func authorizeCertTenantTool(ctx context.Context, mw *authmw.AuthMiddleware, tenantID string, perm permission.Permission) error {
	if mw == nil {
		return nil
	}
	return mw.AuthorizeCertTenant(ctx, tenantID, perm)
}

// authorizeCertRevokeTool gates a revocation on the tenant that owns the
// serial, falling back to the CA-operator authority for deny-by-serial.
func authorizeCertRevokeTool(ctx context.Context, mw *authmw.AuthMiddleware, svc cert.CertProvider, serial string) error {
	if mw == nil {
		return nil
	}
	return mw.AuthorizeCertRevoke(ctx, serial, svc)
}

// toolCertRevoke defines the qrdb_cert_revoke tool schema.
func toolCertRevoke() mcp.Tool {
	return mcp.NewTool("qrdb_cert_revoke",
		mcp.WithDescription("Revoke a certificate by its hexadecimal serial number."),
		mcp.WithString("serial",
			mcp.Required(),
			mcp.Description("The hexadecimal serial number of the certificate to revoke."),
		),
	)
}

// certRevokeResponse is the JSON envelope for a revocation outcome.
// RecordFound distinguishes a certificate this node holds a record for
// from a deny-by-serial revocation of one it does not -- both are
// successes.
type certRevokeResponse struct {
	Status      string `json:"status"`
	RecordFound bool   `json:"recordFound"`
}

// handleCertRevoke returns a tool handler that delegates to
// svc.RevokeCert.
func handleCertRevoke(svc cert.CertProvider, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		serial := mcp.ParseString(req, "serial", "")
		if serial == "" {
			return mcp.NewToolResultError("serial is required"), nil
		}

		if err := authorizeCertRevokeTool(ctx, mw, svc, serial); err != nil {
			return nil, &ToolError{Tool: "qrdb_cert_revoke", Err: err}
		}

		result, err := svc.RevokeCert(ctx, serial)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_cert_revoke", Err: err}
		}

		data, jsonErr := json.Marshal(certRevokeResponse{Status: "ok", RecordFound: result.RecordFound()})
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_cert_revoke", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCertList defines the qrdb_cert_list tool schema.
func toolCertList() mcp.Tool {
	return mcp.NewTool("qrdb_cert_list",
		mcp.WithDescription("List all issued client certificates for a tenant."),
		mcp.WithString("tenant_id",
			mcp.Description(`The tenant ID to list certificates for. Defaults to the system tenant id when empty.`),
		),
	)
}

// handleCertList returns a tool handler that delegates to svc.ListCerts.
func handleCertList(svc cert.CertProvider, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenantID := mcp.ParseString(req, "tenant_id", tenantid.System)

		if err := authorizeCertTenantTool(ctx, mw, tenantID, permission.CertsList); err != nil {
			return nil, &ToolError{Tool: "qrdb_cert_list", Err: err}
		}

		records, err := svc.ListCerts(ctx, tenantID)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_cert_list", Err: err}
		}

		data, jsonErr := json.Marshal(records)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_cert_list", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}
