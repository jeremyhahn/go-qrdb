package mcp

import (
	"context"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

// operatorIdentityTools names the identity tools that take the tenant
// qualifier, with the tenant-qualified permission an operator needs to run
// each against another tenant (pkg/permission/tenant_identity.go).
var operatorIdentityTools = map[string]permission.Permission{
	"qrdb_user_create":                 permission.UsersWriteTenant,
	"qrdb_user_get":                    permission.UsersReadTenant,
	"qrdb_user_get_by_username":        permission.UsersReadTenant,
	"qrdb_user_get_by_email":           permission.UsersReadTenant,
	"qrdb_user_get_by_cert_thumbprint": permission.UsersReadTenant,
	"qrdb_user_add_cert_binding":       permission.UsersWriteTenant,
	"qrdb_user_remove_cert_binding":    permission.UsersWriteTenant,
	"qrdb_user_update":                 permission.UsersWriteTenant,
	"qrdb_user_delete":                 permission.UsersWriteTenant,
	"qrdb_user_list":                   permission.UsersReadTenant,
	"qrdb_user_enable":                 permission.UsersWriteTenant,
	"qrdb_user_disable":                permission.UsersWriteTenant,
	"qrdb_user_suspend":                permission.UsersWriteTenant,
	"qrdb_user_unsuspend":              permission.UsersWriteTenant,
	"qrdb_role_create":                 permission.RolesWriteTenant,
	"qrdb_role_get":                    permission.RolesReadTenant,
	"qrdb_role_get_by_name":            permission.RolesReadTenant,
	"qrdb_role_update":                 permission.RolesWriteTenant,
	"qrdb_role_delete":                 permission.RolesWriteTenant,
	"qrdb_role_list":                   permission.RolesReadTenant,
	"qrdb_role_assign":                 permission.RolesWriteTenant,
	"qrdb_role_unassign":               permission.RolesWriteTenant,
}

// withOperatorTenant gives every identity tool in tools the tenant_id
// argument and runs its handler under the tenant the operator gate
// (AuthMiddleware.AuthorizeOperatorIdentityAccess) admits: the caller's own
// tenant when tenant_id is empty or names it, otherwise the named tenant if
// its isolation policy allows operator access. The gate records the access
// in the platform trail and the tenant's trail.
func withOperatorTenant(authMW *authmw.AuthMiddleware, tools []serverTool) []serverTool {
	for i := range tools {
		perm, ok := operatorIdentityTools[tools[i].Tool.Name]
		if !ok {
			continue
		}
		if _, has := tools[i].Tool.InputSchema.Properties["tenant_id"]; !has {
			mcp.WithString("tenant_id",
				mcp.Description("The tenant whose identity records to act on; empty is the caller's own. Another tenant is an operator access, allowed only under that tenant's operator-access policy and recorded in both audit trails."),
			)(&tools[i].Tool)
		}
		tools[i].Handler = operatorTenantHandler(authMW, perm, tools[i].Handler)
	}
	return tools
}

func operatorTenantHandler(authMW *authmw.AuthMiddleware, perm permission.Permission, next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ctx, err := authMW.AuthorizeOperatorIdentityAccess(ctx, mcp.ParseString(req, "tenant_id", ""), perm)
		if err != nil {
			return nil, &ToolError{Tool: req.Params.Name, Err: err}
		}
		return next(ctx, req)
	}
}
