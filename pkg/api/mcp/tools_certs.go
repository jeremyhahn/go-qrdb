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
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// certTools returns the MCP tool registrations for certificate management
// operations.
//
// The Perm on each registration is authorized against the CALLER's tenant by
// AuthorizeMCP. It cannot authorize the tenant the TOOL CALL names, and three
// of these four name theirs — tenant_id on generate, tenant_id on list, and,
// indirectly, the serial revoke resolves. So mw is threaded into the handlers
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
			Tool:    toolCertGenerate(),
			Perm:    permission.CertsGenerate,
			Handler: handleCertGenerate(svc, mw),
		},
		{
			Tool: toolCertExport(),
			Perm: permission.CertsExport,
			// No tenancy gate: export re-encodes the certificate and key the
			// CALLER supplied and reads no server-held material, so it names
			// no tenant and there is nothing for a gate to protect.
			Handler: handleCertExport(svc),
		},
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

// toolCertGenerate defines the qrdb_cert_generate tool schema.
func toolCertGenerate() mcp.Tool {
	return mcp.NewTool("qrdb_cert_generate",
		mcp.WithDescription("Generate a client X.509 certificate signed by the internal CA for mTLS authentication."),
		mcp.WithString("username",
			mcp.Required(),
			mcp.Description("The username to set as the certificate Common Name (CN)."),
		),
		mcp.WithString("tenant_id",
			mcp.Description("The tenant ID to set as the certificate Organizational Unit (OU). Defaults to the system default tenant when empty."),
		),
		mcp.WithString("ttl",
			mcp.Description(`Certificate validity period as a Go duration string (e.g., "8760h" for one year). Defaults to the provider's default when empty.`),
		),
	)
}

// handleCertGenerate returns a tool handler that delegates to
// svc.GenerateClientCert.
func handleCertGenerate(svc cert.CertProvider, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}

		genReq := &cert.GenerateRequest{
			Username: username,
			TenantID: mcp.ParseString(req, "tenant_id", ""),
		}

		ttlStr := mcp.ParseString(req, "ttl", "")
		if ttlStr != "" {
			ttl, err := time.ParseDuration(ttlStr)
			if err != nil {
				return mcp.NewToolResultError("invalid ttl duration: " + err.Error()), nil
			}
			genReq.TTL = ttl
		}

		// The call's tenant_id becomes the certificate's Subject OU, and the
		// mTLS authenticator derives an identity's tenant from that OU.
		if err := authorizeCertTenantTool(ctx, mw, genReq.TenantID, permission.CertsGenerate); err != nil {
			return nil, &ToolError{Tool: "qrdb_cert_generate", Err: err}
		}

		bundle, err := svc.GenerateClientCert(ctx, genReq)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_cert_generate", Err: err}
		}

		data, jsonErr := json.Marshal(bundle)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_cert_generate", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCertExport defines the qrdb_cert_export tool schema.
func toolCertExport() mcp.Tool {
	return mcp.NewTool("qrdb_cert_export",
		mcp.WithDescription("Export a certificate and private key as a PKCS#12 (.p12) bundle for browser import. Returns the bundle as base64-encoded data."),
		mcp.WithString("cert_pem",
			mcp.Required(),
			mcp.Description("The PEM-encoded X.509 client certificate."),
		),
		mcp.WithString("key_pem",
			mcp.Required(),
			mcp.Description("The PEM-encoded private key."),
		),
		mcp.WithString("passphrase",
			mcp.Required(),
			mcp.Description("The passphrase to protect the PKCS#12 bundle."),
		),
	)
}

// certExportResponse is the JSON envelope for a PKCS#12 export result.
type certExportResponse struct {
	// P12Base64 is the PKCS#12 bundle encoded as base64.
	P12Base64 string `json:"p12_base64"`
}

// handleCertExport returns a tool handler that delegates to
// svc.ExportPKCS12.
func handleCertExport(svc cert.CertProvider) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		certPEM := mcp.ParseString(req, "cert_pem", "")
		if certPEM == "" {
			return mcp.NewToolResultError("cert_pem is required"), nil
		}
		keyPEM := mcp.ParseString(req, "key_pem", "")
		if keyPEM == "" {
			return mcp.NewToolResultError("key_pem is required"), nil
		}
		passphrase := mcp.ParseString(req, "passphrase", "")
		if passphrase == "" {
			return mcp.NewToolResultError("passphrase is required"), nil
		}

		p12, err := svc.ExportPKCS12(ctx, []byte(certPEM), []byte(keyPEM), passphrase)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_cert_export", Err: err}
		}

		resp := certExportResponse{
			P12Base64: base64.StdEncoding.EncodeToString(p12),
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_cert_export", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
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
		tenantID := mcp.ParseString(req, "tenant_id", tenant.SystemTenantID)

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
