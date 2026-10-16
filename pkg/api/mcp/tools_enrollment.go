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
	"encoding/json"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/trustanchor"
)

// enrollmentTools returns the certificate issue/enrol and trust-anchor tools.
// Every one is tenant-self: issue and enrol act on the caller's own tenant,
// and the trust-anchor tools name a tenant_id the handler authorizes against
// the caller (ScopeSelfStrict). mw may be nil (no-auth deployments).
func enrollmentTools(svc service.CertEnrollmentServicer, mw *authmw.AuthMiddleware) []serverTool {
	return []serverTool{
		{Tool: toolCertIssue(), Perm: permission.CertsIssue, Handler: handleCertIssue(svc, mw)},
		{Tool: toolCertEnroll(), Perm: permission.CertsEnroll, Handler: handleCertEnroll(svc, mw)},
		{Tool: toolCertDelegate(), Perm: permission.CertsDelegate, Handler: handleCertDelegate(svc, mw)},
		{Tool: toolTrustAnchorAdd(), Perm: permission.TrustAnchorsAdd, Handler: handleTrustAnchorAdd(svc, mw)},
		{Tool: toolTrustAnchorList(), Perm: permission.TrustAnchorsList, Handler: handleTrustAnchorList(svc, mw)},
		{Tool: toolTrustAnchorRemove(), Perm: permission.TrustAnchorsRemove, Handler: handleTrustAnchorRemove(svc, mw)},
	}
}

// enrolledCertResult is a certificate now bound to an account. There is no
// key: the requester keeps its own.
type enrolledCertResult struct {
	UserID     uint64    `json:"userID,string"`
	Username   string    `json:"username"`
	TenantID   string    `json:"tenantID"`
	CertPEM    string    `json:"certPEM"`
	Serial     string    `json:"serial,omitempty"`
	Thumbprint string    `json:"thumbprint"`
	NotAfter   time.Time `json:"notAfter"`
}

func enrolledResult(tool string, e *service.EnrolledCertificate) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(enrolledCertResult{
		UserID:     e.UserID,
		Username:   e.Username,
		TenantID:   e.TenantID,
		CertPEM:    string(e.CertPEM),
		Serial:     e.Serial,
		Thumbprint: e.Thumbprint,
		NotAfter:   e.NotAfter,
	})
	if err != nil {
		return nil, &ToolError{Tool: tool, Err: err}
	}
	return mcp.NewToolResultText(string(data)), nil
}

// callerTenant is the tenant issue and enrol act on: the caller's own.
func callerTenant(ctx context.Context) string {
	return tenantid.Normalize(tenant.FromContext(ctx))
}

// toolCertIssue defines the qrdb_cert_issue tool schema.
func toolCertIssue() mcp.Tool {
	return mcp.NewTool("qrdb_cert_issue",
		mcp.WithDescription("Issue a client certificate from a certificate signing request for an existing account of your own tenant, signed by your tenant's own CA, and bind it to the account. You keep your private key; only the certificate is returned."),
		mcp.WithString("username", mcp.Required(), mcp.Description("The existing account the certificate is for.")),
		mcp.WithString("csr_pem", mcp.Required(), mcp.Description("The PEM certificate signing request. Only its key is used: the certificate names the account.")),
		mcp.WithString("ttl", mcp.Description(`Validity as a Go duration (e.g. "8760h"). Empty uses the issuer's default.`)),
	)
}

func handleCertIssue(svc service.CertEnrollmentServicer, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}
		var ttl time.Duration
		if s := mcp.ParseString(req, "ttl", ""); s != "" {
			d, err := time.ParseDuration(s)
			if err != nil {
				return mcp.NewToolResultError("invalid ttl duration: " + err.Error()), nil
			}
			ttl = d
		}
		tenantID := callerTenant(ctx)
		if err := authorizeCertTenantTool(ctx, mw, tenantID, permission.CertsIssue); err != nil {
			return nil, &ToolError{Tool: "qrdb_cert_issue", Err: err}
		}
		enrolled, err := svc.SignCertificate(ctx, &service.SignCertificateRequest{
			TenantID: tenantID,
			Username: username,
			CSRPEM:   []byte(mcp.ParseString(req, "csr_pem", "")),
			TTL:      ttl,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_cert_issue", Err: err}
		}
		return enrolledResult("qrdb_cert_issue", enrolled)
	}
}

// toolCertEnroll defines the qrdb_cert_enroll tool schema.
func toolCertEnroll() mcp.Tool {
	return mcp.NewTool("qrdb_cert_enroll",
		mcp.WithDescription("Enrol a bring-your-own client certificate on an existing account of your own tenant. It must chain to one of your tenant's registered trust anchors and authenticate as exactly that account."),
		mcp.WithString("username", mcp.Required(), mcp.Description("The existing account the certificate is enrolled on.")),
		mcp.WithString("cert_pem", mcp.Required(), mcp.Description("The PEM certificate, leaf first; further certificates are offered as intermediates.")),
		mcp.WithString("chain_pem", mcp.Description("Optional PEM intermediates for the certificate's chain.")),
	)
}

func handleCertEnroll(svc service.CertEnrollmentServicer, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		username := mcp.ParseString(req, "username", "")
		if username == "" {
			return mcp.NewToolResultError("username is required"), nil
		}
		tenantID := callerTenant(ctx)
		if err := authorizeCertTenantTool(ctx, mw, tenantID, permission.CertsEnroll); err != nil {
			return nil, &ToolError{Tool: "qrdb_cert_enroll", Err: err}
		}
		enrolled, err := svc.EnrollCertificate(ctx, &service.EnrollCertificateRequest{
			TenantID: tenantID,
			Username: username,
			CertPEM:  []byte(mcp.ParseString(req, "cert_pem", "")),
			ChainPEM: []byte(mcp.ParseString(req, "chain_pem", "")),
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_cert_enroll", Err: err}
		}
		return enrolledResult("qrdb_cert_enroll", enrolled)
	}
}

// issuedDelegationResult is a delegation certificate the tenant's CA issued.
type issuedDelegationResult struct {
	TenantID  string    `json:"tenantID"`
	Principal string    `json:"principal"`
	Scope     []string  `json:"scope"`
	CertPEM   string    `json:"certPEM"`
	Serial    string    `json:"serial"`
	NotAfter  time.Time `json:"notAfter"`
}

// toolCertDelegate defines the qrdb_cert_delegate tool schema.
func toolCertDelegate() mcp.Tool {
	return mcp.NewTool("qrdb_cert_delegate",
		mcp.WithDescription("Have your tenant's own CA issue a short-lived delegation certificate to a service principal: the principal may act inside your tenant with at most the delegated scope, intersected with its own grants. Revoke it by serial like any certificate."),
		mcp.WithString("principal", mcp.Required(), mcp.Description("The service principal the delegation names.")),
		mcp.WithArray("scope", mcp.Required(), mcp.WithStringItems(), mcp.Description("The permissions delegated, e.g. kv:get.")),
		mcp.WithString("csr_pem", mcp.Required(), mcp.Description("The principal's PEM certificate signing request. Only its key is used.")),
		mcp.WithString("ttl", mcp.Description(`Lifetime as a Go duration (e.g. "30m"). Empty is one hour; more than 24h is refused.`)),
	)
}

func handleCertDelegate(svc service.CertEnrollmentServicer, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var ttl time.Duration
		if s := mcp.ParseString(req, "ttl", ""); s != "" {
			d, err := time.ParseDuration(s)
			if err != nil {
				return mcp.NewToolResultError("invalid ttl duration: " + err.Error()), nil
			}
			ttl = d
		}
		tenantID := callerTenant(ctx)
		if err := authorizeCertTenantTool(ctx, mw, tenantID, permission.CertsDelegate); err != nil {
			return nil, &ToolError{Tool: "qrdb_cert_delegate", Err: err}
		}
		issued, err := svc.IssueDelegation(ctx, &service.IssueDelegationRequest{
			TenantID:  tenantID,
			Principal: mcp.ParseString(req, "principal", ""),
			Scope:     req.GetStringSlice("scope", nil),
			CSRPEM:    []byte(mcp.ParseString(req, "csr_pem", "")),
			TTL:       ttl,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_cert_delegate", Err: err}
		}
		data, err := json.Marshal(issuedDelegationResult{
			TenantID:  issued.TenantID,
			Principal: issued.Principal,
			Scope:     issued.Scope,
			CertPEM:   string(issued.CertPEM),
			Serial:    issued.Serial,
			NotAfter:  issued.NotAfter,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_cert_delegate", Err: err}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// trustAnchorResult is one registered trust anchor.
type trustAnchorResult struct {
	Fingerprint string    `json:"fingerprint"`
	Subject     string    `json:"subject"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	CertPEM     string    `json:"cert_pem"`
	AddedAt     time.Time `json:"added_at,omitzero"`
}

func anchorsResult(tool string, anchors []trustanchor.Anchor) (*mcp.CallToolResult, error) {
	out := struct {
		Anchors []trustAnchorResult `json:"anchors"`
	}{Anchors: make([]trustAnchorResult, 0, len(anchors))}
	for _, a := range anchors {
		out.Anchors = append(out.Anchors, trustAnchorResult{
			Fingerprint: a.Fingerprint,
			Subject:     a.Subject,
			NotBefore:   a.NotBefore,
			NotAfter:    a.NotAfter,
			CertPEM:     string(a.CertPEM),
			AddedAt:     a.AddedAt,
		})
	}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, &ToolError{Tool: tool, Err: err}
	}
	return mcp.NewToolResultText(string(data)), nil
}

func toolTrustAnchorAdd() mcp.Tool {
	return mcp.NewTool("qrdb_trust_anchor_add",
		mcp.WithDescription("Register CA certificates as trust anchors of your own tenant: certificates chaining to one may then be enrolled on, and log in as, your tenant's accounts -- no other tenant's."),
		mcp.WithString("tenant_id", mcp.Required(), mcp.Description("Your own tenant.")),
		mcp.WithString("certs_pem", mcp.Required(), mcp.Description("A PEM bundle of CA certificates.")),
	)
}

func handleTrustAnchorAdd(svc service.CertEnrollmentServicer, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenantID := mcp.ParseString(req, "tenant_id", "")
		if err := authorizeCertTenantTool(ctx, mw, tenantID, permission.TrustAnchorsAdd); err != nil {
			return nil, &ToolError{Tool: "qrdb_trust_anchor_add", Err: err}
		}
		anchors, err := svc.AddTrustAnchors(ctx, tenantID, []byte(mcp.ParseString(req, "certs_pem", "")))
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_trust_anchor_add", Err: err}
		}
		return anchorsResult("qrdb_trust_anchor_add", anchors)
	}
}

func toolTrustAnchorList() mcp.Tool {
	return mcp.NewTool("qrdb_trust_anchor_list",
		mcp.WithDescription("List your own tenant's trust anchors."),
		mcp.WithString("tenant_id", mcp.Required(), mcp.Description("Your own tenant.")),
	)
}

func handleTrustAnchorList(svc service.CertEnrollmentServicer, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenantID := mcp.ParseString(req, "tenant_id", "")
		if err := authorizeCertTenantTool(ctx, mw, tenantID, permission.TrustAnchorsList); err != nil {
			return nil, &ToolError{Tool: "qrdb_trust_anchor_list", Err: err}
		}
		anchors, err := svc.ListTrustAnchors(ctx, tenantID)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_trust_anchor_list", Err: err}
		}
		return anchorsResult("qrdb_trust_anchor_list", anchors)
	}
}

func toolTrustAnchorRemove() mcp.Tool {
	return mcp.NewTool("qrdb_trust_anchor_remove",
		mcp.WithDescription("Withdraw one of your own tenant's trust anchors; it stops vouching for logins at once."),
		mcp.WithString("tenant_id", mcp.Required(), mcp.Description("Your own tenant.")),
		mcp.WithString("fingerprint", mcp.Required(), mcp.Description("The anchor's fingerprint: lowercase hex SHA-256 of its DER.")),
	)
}

func handleTrustAnchorRemove(svc service.CertEnrollmentServicer, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenantID := mcp.ParseString(req, "tenant_id", "")
		if err := authorizeCertTenantTool(ctx, mw, tenantID, permission.TrustAnchorsRemove); err != nil {
			return nil, &ToolError{Tool: "qrdb_trust_anchor_remove", Err: err}
		}
		if err := svc.RemoveTrustAnchor(ctx, tenantID, mcp.ParseString(req, "fingerprint", "")); err != nil {
			return nil, &ToolError{Tool: "qrdb_trust_anchor_remove", Err: err}
		}
		return mcp.NewToolResultText(`{"status":"ok"}`), nil
	}
}
