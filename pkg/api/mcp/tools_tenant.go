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
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// tenantTools returns the MCP tool registrations for tenant lifecycle
// management: create, get, list, update, delete, and quota management.
func tenantTools(svc tenant.TenantServicer, onboarding service.TenantOnboardingServicer, mw *authmw.AuthMiddleware) []serverTool {
	return []serverTool{
		{Tool: toolTenantCreate(), Perm: permission.TenantsCreate, Handler: handleTenantCreate(onboarding)},
		{Tool: toolTenantGet(), Perm: permission.TenantsRead, Handler: handleTenantGet(svc, mw)},
		{Tool: toolTenantList(), Perm: permission.TenantsList, Handler: handleTenantList(svc, mw)},
		{Tool: toolTenantUpdate(), Perm: permission.TenantsUpdate, Handler: handleTenantUpdate(svc, mw)},
		{Tool: toolClusterIsolationPolicyGet(), Perm: permission.TenantsRead, Handler: handleClusterIsolationPolicyGet(svc)},
		{Tool: toolClusterIsolationPolicySet(), Perm: permission.TenantsUpdateCross, Handler: handleClusterIsolationPolicySet(svc)},
		{Tool: toolTenantDelete(), Perm: permission.TenantsDelete, Handler: handleTenantDelete(svc, mw)},
		{Tool: toolTenantUpdateQuotas(), Perm: permission.TenantsSetQuotas, Handler: handleTenantUpdateQuotas(svc, mw)},
		{Tool: toolTenantUpdateEntitlements(), Perm: permission.TenantsSetEntitlements, Handler: handleTenantUpdateEntitlements(svc, mw)},
		{Tool: toolTenantListDeleted(), Perm: permission.TenantsListDeleted, Handler: handleTenantListDeleted(svc)},
		{Tool: toolTenantReconcileDeleted(), Perm: permission.TenantsReconcileDeleted, Handler: handleTenantReconcileDeleted(svc)},
	}
}

// toolTenantListDeleted defines the qrdb_tenant_list_deleted tool schema.
func toolTenantListDeleted() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_list_deleted",
		mcp.WithDescription("Return one page of retired tenant ids with their deletion records, in ascending id order. A deleted tenant id can never be used again."),
		mcp.WithNumber("page_size",
			mcp.Description("Records per page. 0 or absent uses the server default; larger than the server maximum is served at the maximum."),
		),
		mcp.WithString("page_token",
			mcp.Description("The next_page_token of the previous page. Absent for the first page."),
		),
	)
}

// handleTenantListDeleted returns a tool handler that pages through retired
// tenant ids.
func handleTenantListDeleted(svc tenant.TenantServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		page, err := svc.ListDeletedTenants(ctx, mcp.ParseString(req, "page_token", ""), mcp.ParseInt(req, "page_size", 0))
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_list_deleted", Err: err}
		}
		data, jsonErr := json.Marshal(page)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_list_deleted", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolTenantReconcileDeleted defines the qrdb_tenant_reconcile_deleted tool
// schema.
func toolTenantReconcileDeleted() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_reconcile_deleted",
		mcp.WithDescription("Remove, from the node serving the request, the on-disk residue of every retired tenant id. Never removes a directory a live tenant claims; reports what it reclaimed and what it left in place."),
	)
}

// handleTenantReconcileDeleted returns a tool handler that runs the
// deleted-tenant reconcile on this node.
func handleTenantReconcileDeleted(svc tenant.TenantServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		report, err := svc.ReconcileDeletedTenants(ctx)
		// A pass that failed only for some ids answers with its report, whose
		// failed list names them; only a guard failure is a tool error.
		if rerr := tenant.ReconcileResponseError(report, err); rerr != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_reconcile_deleted", Err: rerr}
		}
		data, jsonErr := json.Marshal(report)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_reconcile_deleted", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolTenantCreate defines the qrdb_tenant_create tool schema.
func toolTenantCreate() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_create",
		mcp.WithDescription("Create a new tenant with the given ID, name, and optional isolation type."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The unique tenant identifier."),
		),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The human-readable tenant name."),
		),
		mcp.WithString("isolation_type",
			mcp.Description("Isolation strategy: 'barrier' (default) or 'prefix'."),
		),
		mcp.WithBoolean("barrier_auto_unseal",
			mcp.Description("Opt a dedicated-isolation tenant (barrier/volume) into the system-managed auto-unseal key-management mode (entitlement-gated). Default false = self-managed."),
		),
		mcp.WithString("volume_pool",
			mcp.Description("The named volume pool an IsolationVolume/IsolationRaft tenant is placed on. Empty (default) uses the tenant's default pool, the first entry of its AllowedVolumePools."),
		),
		mcp.WithString("admin_username",
			mcp.Required(),
			mcp.Description("The tenant's first admin. A tenant is created with its first admin's login credential, or not at all."),
		),
		mcp.WithString("admin_csr_pem",
			mcp.Description("The admin's PEM certificate signing request, signed by the tenant's own CA; the admin keeps its key. Exactly one of this and admin_cert_pem."),
		),
		mcp.WithString("admin_cert_pem",
			mcp.Description("The admin's own PEM certificate, leaf first; it must chain to one of trust_anchors_pem."),
		),
		mcp.WithString("admin_chain_pem",
			mcp.Description("Optional PEM intermediates for admin_cert_pem."),
		),
		mcp.WithString("trust_anchors_pem",
			mcp.Description("A PEM bundle of CA certificates to register as the tenant's trust anchors."),
		),
		mcp.WithString("so_username",
			mcp.Description("The tenant's optional security officer, enrolled with the so role alone. Given with so_pin and exactly one of so_csr_pem and so_cert_pem, or not at all; it must differ from the admin by name and by key."),
		),
		mcp.WithString("so_csr_pem",
			mcp.Description("The SO's PEM certificate signing request, signed like the admin's. Exactly one of this and so_cert_pem."),
		),
		mcp.WithString("so_cert_pem",
			mcp.Description("The SO's own PEM certificate, leaf first; it must chain to one of trust_anchors_pem."),
		),
		mcp.WithString("so_chain_pem",
			mcp.Description("Optional PEM intermediates for so_cert_pem."),
		),
		mcp.WithString("so_pin",
			mcp.Description("The SO's PIN, the second factor the SO gate asks for."),
		),
	)
}

// tenantCreateResult is qrdb_tenant_create's result: the tenant, its first
// admin's credential and, when one was given, its SO's. There is never a
// private key.
type tenantCreateResult struct {
	Tenant *tenant.TenantInfo         `json:"tenant"`
	Admin  tenantAdminCredentialJSON  `json:"admin"`
	SO     *tenantAdminCredentialJSON `json:"so,omitempty"`
}

// tenantAdminCredentialJSON is a tenant's first admin as onboarding left it.
type tenantAdminCredentialJSON struct {
	UserID            uint64 `json:"user_id,omitempty,string"`
	Username          string `json:"username"`
	CertPEM           string `json:"cert_pem"`
	Serial            string `json:"serial,omitempty"`
	Thumbprint        string `json:"thumbprint"`
	AwaitsBarrierInit bool   `json:"awaits_barrier_init,omitempty"`
}

// credentialJSON renders an account onboarding enrolled.
func credentialJSON(c service.TenantAdminCredential) tenantAdminCredentialJSON {
	return tenantAdminCredentialJSON{
		UserID:            c.UserID,
		Username:          c.Username,
		CertPEM:           string(c.CertPEM),
		Serial:            c.Serial,
		Thumbprint:        c.Thumbprint,
		AwaitsBarrierInit: c.AwaitsBarrierInit,
	}
}

// handleTenantCreate returns a tool handler that onboards a new tenant with
// its first admin.
func handleTenantCreate(svc service.TenantOnboardingServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if svc == nil {
			return nil, &ToolError{Tool: "qrdb_tenant_create", Err: &service.TenantOnboardingNotConfiguredError{}}
		}
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}

		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		result, err := svc.OnboardTenant(ctx, &service.OnboardTenantRequest{
			ID:                id,
			Name:              name,
			IsolationType:     tenant.IsolationType(mcp.ParseString(req, "isolation_type", "")),
			BarrierAutoUnseal: mcp.ParseBoolean(req, "barrier_auto_unseal", false),
			VolumePool:        mcp.ParseString(req, "volume_pool", ""),
			AdminUsername:     mcp.ParseString(req, "admin_username", ""),
			AdminCSRPEM:       []byte(mcp.ParseString(req, "admin_csr_pem", "")),
			AdminCertPEM:      []byte(mcp.ParseString(req, "admin_cert_pem", "")),
			AdminChainPEM:     []byte(mcp.ParseString(req, "admin_chain_pem", "")),
			TrustAnchorsPEM:   []byte(mcp.ParseString(req, "trust_anchors_pem", "")),
			SOUsername:        mcp.ParseString(req, "so_username", ""),
			SOCSRPEM:          []byte(mcp.ParseString(req, "so_csr_pem", "")),
			SOCertPEM:         []byte(mcp.ParseString(req, "so_cert_pem", "")),
			SOChainPEM:        []byte(mcp.ParseString(req, "so_chain_pem", "")),
			SOPIN:             []byte(mcp.ParseString(req, "so_pin", "")),
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_create", Err: err}
		}

		out := tenantCreateResult{
			Tenant: result.Tenant,
			Admin:  credentialJSON(result.Admin),
		}
		if result.SO != nil {
			so := credentialJSON(*result.SO)
			out.SO = &so
		}
		data, jsonErr := json.Marshal(out)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_create", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolTenantGet defines the qrdb_tenant_get tool schema.
func toolTenantGet() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_get",
		mcp.WithDescription("Retrieve tenant metadata by ID."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The tenant ID to retrieve."),
		),
	)
}

// handleTenantGet returns a tool handler that retrieves a tenant by ID.
// tenantRefusalJSON mirrors pkg/api/rest/handlers_tenants.go's tenantRefusal:
// the wire shape of a tenant.Refusal.
type tenantRefusalJSON struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// tenantInfoJSON is the wire shape for a tenant returned over MCP.
// tenant.TenantInfo.Refusal is typed error and tagged json:"-" (an error
// has no fixed shape to marshal), so json.Marshal(info) alone never
// carries it -- the same reason pkg/api/rest/handlers_tenants.go builds
// its own tenantResponse rather than marshaling *tenant.TenantInfo
// directly. This rebuilds the same "refusal" field from tenant.RefusalOf
// for MCP's wire, which otherwise silently dropped it.
type tenantInfoJSON struct {
	*tenant.TenantInfo
	Refusal *tenantRefusalJSON `json:"refusal,omitempty"`
}

// tenantInfoToJSON converts info to its MCP wire shape, carrying its
// refusal (if any) as "refusal" rather than losing it to TenantInfo's own
// json:"-" tag.
func tenantInfoToJSON(info *tenant.TenantInfo) *tenantInfoJSON {
	if info == nil {
		return nil
	}
	out := &tenantInfoJSON{TenantInfo: info}
	if r := tenant.RefusalOf(info.Refusal); r != nil {
		out.Refusal = &tenantRefusalJSON{Code: r.Code, Message: r.Message}
	}
	return out
}

// tenantInfosToJSON converts a slice the same way, for qrdb_tenant_list.
func tenantInfosToJSON(infos []tenant.TenantInfo) []*tenantInfoJSON {
	out := make([]*tenantInfoJSON, len(infos))
	for i := range infos {
		out[i] = tenantInfoToJSON(&infos[i])
	}
	return out
}

func handleTenantGet(svc tenant.TenantServicer, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		if refusal := tenantTargetRefusal(ctx, "qrdb_tenant_get", mw, id, permission.TenantsRead); refusal != nil {
			return refusal, nil
		}

		info, err := svc.GetTenant(ctx, id)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_get", Err: err}
		}

		data, jsonErr := json.Marshal(tenantInfoToJSON(info))
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_get", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolTenantList defines the qrdb_tenant_list tool schema.
func toolTenantList() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_list",
		mcp.WithDescription("Return metadata for all registered tenants."),
	)
}

// handleTenantList returns a tool handler that lists every tenant to a caller
// holding tenants:list:cross, and the caller's own tenant only to anyone else.
func handleTenantList(svc tenant.TenantServicer, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenants, err := svc.ListTenants(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_list", Err: err}
		}
		if mw != nil {
			if tenants, err = mw.FilterTenantList(ctx, tenants); err != nil {
				return authzToolError(err), nil
			}
		}

		data, jsonErr := json.Marshal(tenantInfosToJSON(tenants))
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_list", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolTenantUpdate defines the qrdb_tenant_update tool schema.
func toolTenantUpdate() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_update",
		mcp.WithDescription("Apply a partial update to an existing tenant. Only non-empty fields are changed."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The tenant ID to update."),
		),
		mcp.WithString("name",
			mcp.Description("New display name for the tenant."),
		),
		mcp.WithString("isolation_type",
			mcp.Description("New isolation strategy: 'barrier' or 'prefix'."),
		),
		mcp.WithString("description",
			mcp.Description("New free-text description. Left unchanged when absent; an empty string clears it."),
		),
		mcp.WithString("status",
			mcp.Description("New operational status: 'active', 'suspended' or 'disabled'. Needs tenants:set_status; left unchanged when absent."),
		),
		mcp.WithString("metadata",
			mcp.Description(`JSON object of string key/value pairs replacing the whole metadata map, e.g. {"env":"prod"}. Left unchanged when absent; use clear_metadata to remove every entry.`),
		),
		mcp.WithBoolean("clear_metadata",
			mcp.Description("Remove every metadata entry. Ignored when metadata is given."),
		),
		mcp.WithString("isolation_policy",
			mcp.Description("The tenant's isolation policy: 'strict', or an empty string to inherit the cluster floor. Left unchanged when absent; strict back to inherit is refused."),
		),
	)
}

// parseTenantPatchArgs reads the description, status, metadata and
// clear_metadata arguments onto patch. An absent argument leaves its field
// nil (unchanged).
func parseTenantPatchArgs(req mcp.CallToolRequest, patch *tenant.TenantPatch) error {
	if raw, present := req.GetArguments()["description"]; present && raw != nil {
		desc, ok := raw.(string)
		if !ok {
			return &ToolArgumentError{Argument: "description", Reason: "must be a string"}
		}
		patch.Description = &desc
	}
	if raw, present := req.GetArguments()["status"]; present && raw != nil {
		s, ok := raw.(string)
		if !ok {
			return &ToolArgumentError{Argument: "status", Reason: "must be a string"}
		}
		status := tenant.Status(s)
		patch.Status = &status
	}
	metadata, err := parseAttributesArg(req, "metadata")
	if err != nil {
		return err
	}
	switch {
	case metadata != nil:
		patch.Metadata = metadata
	case mcp.ParseBoolean(req, "clear_metadata", false):
		patch.Metadata = map[string]string{}
	}
	return nil
}

// parseIsolationPolicyArg copies a present isolation_policy argument into
// the patch. It reports true when the argument asks for inherit (""), which
// the patch cannot carry: an empty patch field means unchanged.
func parseIsolationPolicyArg(req mcp.CallToolRequest, patch *tenant.TenantPatch) (bool, error) {
	raw, present := req.GetArguments()["isolation_policy"]
	if !present || raw == nil {
		return false, nil
	}
	p, ok := raw.(string)
	if !ok {
		return false, &ToolArgumentError{Argument: "isolation_policy", Reason: "must be a string"}
	}
	patch.IsolationPolicy = tenant.TenantIsolationPolicy(p)
	return patch.IsolationPolicy == tenant.TenantIsolationPolicyInherit, nil
}

// refuseIsolationPolicyLoosening refuses an explicit inherit request on a
// strict tenant: the tenant policy is tighten-only.
func refuseIsolationPolicyLoosening(ctx context.Context, svc tenant.TenantServicer, id string) error {
	info, err := svc.GetTenant(ctx, id)
	if err != nil {
		return err
	}
	if info.IsolationPolicy == tenant.TenantIsolationPolicyStrict {
		return &tenant.IsolationPolicyChangeError{From: string(tenant.TenantIsolationPolicyStrict), To: "inherit"}
	}
	return nil
}

// handleTenantUpdate returns a tool handler that applies a partial update to a tenant.
func handleTenantUpdate(svc tenant.TenantServicer, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		patch := tenant.TenantPatch{
			Name:          mcp.ParseString(req, "name", ""),
			IsolationType: tenant.IsolationType(mcp.ParseString(req, "isolation_type", "")),
		}
		if err := parseTenantPatchArgs(req, &patch); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		inherit, err := parseIsolationPolicyArg(req, &patch)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		if refusal := tenantRequestRefusal("qrdb_tenant_update", mw, id, func(mw *authmw.AuthMiddleware) error {
			return mw.AuthorizeTenantUpdate(ctx, id, patch)
		}); refusal != nil {
			return refusal, nil
		}
		if inherit || patch.IsolationPolicy != "" {
			if err := featuregate.RequireStrictIsolation(svc); err != nil {
				return nil, &ToolError{Tool: "qrdb_tenant_update", Err: err}
			}
		}
		if inherit {
			if err := refuseIsolationPolicyLoosening(ctx, svc, id); err != nil {
				return nil, &ToolError{Tool: "qrdb_tenant_update", Err: err}
			}
		}

		info, err := svc.UpdateTenant(ctx, id, patch)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_update", Err: err}
		}

		data, jsonErr := json.Marshal(tenantInfoToJSON(info))
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_update", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolTenantDelete defines the qrdb_tenant_delete tool schema.
func toolTenantDelete() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_delete",
		mcp.WithDescription("Remove a tenant and its metadata. This operation is irreversible."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The tenant ID to delete."),
		),
	)
}

// handleTenantDelete returns a tool handler that removes a tenant.
func handleTenantDelete(svc tenant.TenantServicer, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		if refusal := tenantTargetRefusal(ctx, "qrdb_tenant_delete", mw, id, permission.TenantsDelete); refusal != nil {
			return refusal, nil
		}

		if err := svc.DeleteTenant(ctx, id); err != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_delete", Err: err}
		}

		return mcp.NewToolResultText(`{"deleted":true}`), nil
	}
}

// toolTenantUpdateQuotas defines the qrdb_tenant_update_quotas tool schema.
func toolTenantUpdateQuotas() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_update_quotas",
		mcp.WithDescription("Set resource quotas for an existing tenant. Use -1 for unlimited, 0 for system default."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The tenant ID to update quotas for."),
		),
		mcp.WithNumber("max_databases",
			mcp.Description("Maximum number of databases. -1 = unlimited."),
		),
		mcp.WithNumber("max_raft_groups",
			mcp.Description("Maximum number of Raft groups. -1 = unlimited."),
		),
		mcp.WithNumber("max_replicas_per_group",
			mcp.Description("Maximum replication factor per dedicated-raft database. -1 = unlimited."),
		),
		mcp.WithNumber("max_total_replicas",
			mcp.Description("Maximum aggregate replicas across all the tenant's raft groups. -1 = unlimited."),
		),
		mcp.WithNumber("max_collections",
			mcp.Description("Maximum number of collections. -1 = unlimited."),
		),
		mcp.WithNumber("max_keys",
			mcp.Description("Maximum number of keys. -1 = unlimited."),
		),
		mcp.WithNumber("max_storage_bytes",
			mcp.Description("Maximum storage in bytes. -1 = unlimited."),
		),
		mcp.WithNumber("max_database_size_bytes",
			mcp.Description("Maximum size in bytes of any single database. -1 = unlimited."),
		),
		mcp.WithNumber("max_users",
			mcp.Description("Maximum number of users. -1 = unlimited."),
		),
		mcp.WithNumber("max_roles",
			mcp.Description("Maximum number of roles. -1 = unlimited."),
		),
		mcp.WithNumber("max_certificates",
			mcp.Description("Maximum number of certificates. -1 = unlimited."),
		),
		mcp.WithNumber("max_backups",
			mcp.Description("Maximum number of backups. -1 = unlimited."),
		),
		mcp.WithNumber("max_vector_dims",
			mcp.Description("Maximum vector dimensionality. -1 = unlimited."),
		),
		mcp.WithNumber("max_ts_metrics",
			mcp.Description("Maximum number of time-series metrics. -1 = unlimited."),
		),
		mcp.WithNumber("system_barrier_quota",
			mcp.Description("Bytes an isolated tenant may use of the shared system barrier. 0 = unavailable, -1 = unlimited."),
		),
		mcp.WithNumber("max_dedicated_wal_groups",
			mcp.Description("Maximum dedicated-raft groups the tenant may opt into a per-tenant-key WAL. -1 = unlimited."),
		),
	)
}

// handleTenantUpdateQuotas returns a tool handler that sets quotas for a tenant.
func handleTenantUpdateQuotas(svc tenant.TenantServicer, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		if refusal := tenantTargetRefusal(ctx, "qrdb_tenant_update_quotas", mw, id, permission.TenantsSetQuotas); refusal != nil {
			return refusal, nil
		}

		q := &quota.Quotas{
			MaxDatabases:          int64(mcp.ParseInt(req, "max_databases", 0)),
			MaxRaftGroups:         int64(mcp.ParseInt(req, "max_raft_groups", 0)),
			MaxReplicasPerGroup:   int64(mcp.ParseInt(req, "max_replicas_per_group", 0)),
			MaxTotalReplicas:      int64(mcp.ParseInt(req, "max_total_replicas", 0)),
			MaxCollections:        int64(mcp.ParseInt(req, "max_collections", 0)),
			MaxKeys:               int64(mcp.ParseInt(req, "max_keys", 0)),
			MaxStorageBytes:       int64(mcp.ParseInt(req, "max_storage_bytes", 0)),
			MaxDatabaseSizeBytes:  int64(mcp.ParseInt(req, "max_database_size_bytes", 0)),
			MaxUsers:              int64(mcp.ParseInt(req, "max_users", 0)),
			MaxRoles:              int64(mcp.ParseInt(req, "max_roles", 0)),
			MaxCertificates:       int64(mcp.ParseInt(req, "max_certificates", 0)),
			MaxBackups:            int64(mcp.ParseInt(req, "max_backups", 0)),
			MaxVectorDims:         int64(mcp.ParseInt(req, "max_vector_dims", 0)),
			MaxTSMetrics:          int64(mcp.ParseInt(req, "max_ts_metrics", 0)),
			SystemBarrierQuota:    int64(mcp.ParseInt(req, "system_barrier_quota", 0)),
			MaxDedicatedWALGroups: int64(mcp.ParseInt(req, "max_dedicated_wal_groups", 0)),
		}

		info, err := svc.UpdateQuotas(ctx, id, q)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_update_quotas", Err: err}
		}

		data, jsonErr := json.Marshal(tenantInfoToJSON(info))
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_update_quotas", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolTenantUpdateEntitlements defines the qrdb_tenant_update_entitlements tool schema.
func toolTenantUpdateEntitlements() mcp.Tool {
	return mcp.NewTool("qrdb_tenant_update_entitlements",
		mcp.WithDescription("Set feature entitlements for an existing tenant: which isolation modes and dedicated-storage features the tenant's plan permits."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The tenant ID to update entitlements for."),
		),
		mcp.WithString("allowed_isolation_modes",
			mcp.Description("Comma-separated list of permitted isolation modes (e.g. \"prefix,volume,raft\")."),
		),
		mcp.WithBoolean("allow_dedicated_raft",
			mcp.Description("Whether the tenant may provision dedicated Raft-backed databases."),
		),
		mcp.WithBoolean("allow_volume_storage",
			mcp.Description("Whether the tenant may provision databases on a dedicated storage volume."),
		),
		mcp.WithBoolean("allow_system_managed_barrier",
			mcp.Description("Whether the tenant may opt into the system-managed auto-unseal key-management mode for its dedicated barrier."),
		),
		mcp.WithBoolean("allow_dedicated_wal",
			mcp.Description("Whether the tenant may opt a dedicated-raft database into a per-tenant-key WAL."),
		),
		mcp.WithString("allowed_barrier_modes",
			mcp.Description("Comma-separated dedicated-barrier sealing strategies the tenant may select (e.g. \"none,passphrase,shamir\")."),
		),
	)
}

// handleTenantUpdateEntitlements returns a tool handler that sets entitlements
// for a tenant.
func handleTenantUpdateEntitlements(svc tenant.TenantServicer, mw *authmw.AuthMiddleware) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		if refusal := tenantTargetRefusal(ctx, "qrdb_tenant_update_entitlements", mw, id, permission.TenantsSetEntitlements); refusal != nil {
			return refusal, nil
		}

		var modes []string
		if raw := mcp.ParseString(req, "allowed_isolation_modes", ""); raw != "" {
			for _, m := range strings.Split(raw, ",") {
				if trimmed := strings.TrimSpace(m); trimmed != "" {
					modes = append(modes, trimmed)
				}
			}
		}

		var barrierModes []string
		if raw := mcp.ParseString(req, "allowed_barrier_modes", ""); raw != "" {
			for _, m := range strings.Split(raw, ",") {
				if trimmed := strings.TrimSpace(m); trimmed != "" {
					barrierModes = append(barrierModes, trimmed)
				}
			}
		}

		e := &quota.Entitlements{
			AllowedIsolationModes:     modes,
			AllowDedicatedRaft:        mcp.ParseBoolean(req, "allow_dedicated_raft", false),
			AllowVolumeStorage:        mcp.ParseBoolean(req, "allow_volume_storage", false),
			AllowSystemManagedBarrier: mcp.ParseBoolean(req, "allow_system_managed_barrier", false),
			AllowDedicatedWAL:         mcp.ParseBoolean(req, "allow_dedicated_wal", false),
			AllowedBarrierModes:       barrierModes,
		}

		info, err := svc.UpdateEntitlements(ctx, id, e)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_update_entitlements", Err: err}
		}

		data, jsonErr := json.Marshal(tenantInfoToJSON(info))
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_update_entitlements", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// tenantTargetRefusal re-checks perm against the tenant the CALL names
// (authmw.AuthorizeTenantTarget). The tool check authorized it against the
// caller's own tenant only, which would let a tenant-local Admin read,
// change or delete any tenant. It returns the refusal to answer with, or nil
// when the call may proceed.
func tenantTargetRefusal(ctx context.Context, tool string, mw *authmw.AuthMiddleware, tenantID string, perm permission.Permission) *mcp.CallToolResult {
	return tenantRequestRefusal(tool, mw, tenantID, func(mw *authmw.AuthMiddleware) error {
		return mw.AuthorizeTenantTarget(ctx, tenantID, perm)
	})
}

// tenantRequestRefusal refuses an id that cannot name a tenant
// (tenant.ValidateTenantIDShape, classified InvalidArgument), then runs
// decide against mw and renders its refusal exactly as the tool-authorization
// middleware renders one (authzToolError). A nil middleware is the no-auth
// posture and the authorization step admits.
func tenantRequestRefusal(tool string, mw *authmw.AuthMiddleware, tenantID string, decide func(*authmw.AuthMiddleware) error) *mcp.CallToolResult {
	if err := tenant.ValidateTenantIDShape(tenantID); err != nil {
		return classifiedToolError(tool, err)
	}
	if mw == nil {
		return nil
	}
	if err := decide(mw); err != nil {
		return authzToolError(err)
	}
	return nil
}

// clusterIsolationPolicyJSON is the result of both floor tools.
type clusterIsolationPolicyJSON struct {
	Policy tenant.ClusterIsolationPolicy `json:"policy"`
}

func toolClusterIsolationPolicyGet() mcp.Tool {
	return mcp.NewTool("qrdb_cluster_isolation_policy_get",
		mcp.WithDescription("Read the cluster isolation floor: 'operator-access' or 'strict'. An unset floor reads as 'operator-access'."),
	)
}

func handleClusterIsolationPolicyGet(svc tenant.TenantServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		const tool = "qrdb_cluster_isolation_policy_get"
		if err := featuregate.RequireStrictIsolation(svc); err != nil {
			return nil, &ToolError{Tool: tool, Err: err}
		}
		return clusterIsolationPolicyResult(ctx, svc, tool)
	}
}

func toolClusterIsolationPolicySet() mcp.Tool {
	return mcp.NewTool("qrdb_cluster_isolation_policy_set",
		mcp.WithDescription("Set the cluster isolation floor. Tighten-only: strict cannot go back to operator-access."),
		mcp.WithString("policy",
			mcp.Required(),
			mcp.Description("The new floor: 'strict' or 'operator-access'."),
		),
	)
}

func handleClusterIsolationPolicySet(svc tenant.TenantServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		const tool = "qrdb_cluster_isolation_policy_set"
		if err := featuregate.RequireStrictIsolation(svc); err != nil {
			return nil, &ToolError{Tool: tool, Err: err}
		}
		policy := mcp.ParseString(req, "policy", "")
		if policy == "" {
			return mcp.NewToolResultError("policy is required"), nil
		}
		if err := svc.SetClusterIsolationPolicy(ctx, tenant.ClusterIsolationPolicy(policy)); err != nil {
			return nil, &ToolError{Tool: tool, Err: err}
		}
		return clusterIsolationPolicyResult(ctx, svc, tool)
	}
}

func clusterIsolationPolicyResult(ctx context.Context, svc tenant.TenantServicer, tool string) (*mcp.CallToolResult, error) {
	policy, err := svc.ClusterIsolationPolicy(ctx)
	if err != nil {
		return nil, &ToolError{Tool: tool, Err: err}
	}
	data, err := json.Marshal(clusterIsolationPolicyJSON{Policy: policy})
	if err != nil {
		return nil, &ToolError{Tool: tool, Err: err}
	}
	return mcp.NewToolResultText(string(data)), nil
}
