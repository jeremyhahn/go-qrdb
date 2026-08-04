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

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// tenantTools returns the MCP tool registrations for tenant lifecycle
// management: create, get, list, update, delete, and quota management.
func tenantTools(svc tenant.TenantServicer) []mcpserver.ServerTool {
	return []mcpserver.ServerTool{
		{Tool: toolTenantCreate(), Handler: handleTenantCreate(svc)},
		{Tool: toolTenantGet(), Handler: handleTenantGet(svc)},
		{Tool: toolTenantList(), Handler: handleTenantList(svc)},
		{Tool: toolTenantUpdate(), Handler: handleTenantUpdate(svc)},
		{Tool: toolTenantDelete(), Handler: handleTenantDelete(svc)},
		{Tool: toolTenantUpdateQuotas(), Handler: handleTenantUpdateQuotas(svc)},
		{Tool: toolTenantUpdateEntitlements(), Handler: handleTenantUpdateEntitlements(svc)},
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
	)
}

// handleTenantCreate returns a tool handler that creates a new tenant.
func handleTenantCreate(svc tenant.TenantServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}

		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		isolationType := tenant.IsolationType(mcp.ParseString(req, "isolation_type", ""))
		barrierAutoUnseal := mcp.ParseBoolean(req, "barrier_auto_unseal", false)

		info, err := svc.CreateTenant(ctx, id, name, isolationType,
			tenant.WithBarrierAutoUnseal(barrierAutoUnseal))
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_create", Err: err}
		}

		data, jsonErr := json.Marshal(info)
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
func handleTenantGet(svc tenant.TenantServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}

		info, err := svc.GetTenant(ctx, id)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_get", Err: err}
		}

		data, jsonErr := json.Marshal(info)
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

// handleTenantList returns a tool handler that lists all tenants.
func handleTenantList(svc tenant.TenantServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		tenants, err := svc.ListTenants(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_list", Err: err}
		}

		data, jsonErr := json.Marshal(tenants)
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
	)
}

// handleTenantUpdate returns a tool handler that applies a partial update to a tenant.
func handleTenantUpdate(svc tenant.TenantServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}

		patch := tenant.TenantPatch{
			Name:          mcp.ParseString(req, "name", ""),
			IsolationType: tenant.IsolationType(mcp.ParseString(req, "isolation_type", "")),
		}

		info, err := svc.UpdateTenant(ctx, id, patch)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_update", Err: err}
		}

		data, jsonErr := json.Marshal(info)
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
func handleTenantDelete(svc tenant.TenantServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
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
func handleTenantUpdateQuotas(svc tenant.TenantServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
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

		data, jsonErr := json.Marshal(info)
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
func handleTenantUpdateEntitlements(svc tenant.TenantServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
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

		data, jsonErr := json.Marshal(info)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_tenant_update_entitlements", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}
