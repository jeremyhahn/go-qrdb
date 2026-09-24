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
	"strconv"

	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// argString returns the string argument name, or "" when it is absent or not
// a string.
func argString(args map[string]any, name string) string {
	s, _ := args[name].(string)
	return s
}

// argTenant targets the tenant named by the string argument name.
func argTenant(name string) authmw.MCPTargetFunc {
	return func(args map[string]any) authmw.Target {
		return authmw.TenantTarget(argString(args, name))
	}
}

// argUser targets the user whose decimal id is the string argument name.
func argUser(name string) authmw.MCPTargetFunc {
	return func(args map[string]any) authmw.Target {
		id, err := strconv.ParseUint(argString(args, name), 10, 64)
		if err != nil {
			return authmw.SelfTarget()
		}
		return authmw.UserTarget(id)
	}
}

// argRole targets the role whose id is the string argument name.
func argRole(name string) authmw.MCPTargetFunc {
	return func(args map[string]any) authmw.Target {
		return authmw.RoleTarget(argString(args, name))
	}
}

// identityTargets is how each MCP tool that acts on a named user, role or
// tenant names it in its arguments. registerTargets attaches them to the tool
// registry so the route-level permission check is made against that tenant,
// the same resolution every other transport uses (see authmw.Target).
func identityTargets() map[string]authmw.MCPTargetFunc {
	return map[string]authmw.MCPTargetFunc{
		"qrdb_user_create":              argTenant("tenant_id"),
		"qrdb_user_get":                 argUser("id"),
		"qrdb_user_get_by_username":     argTenant("tenant_id"),
		"qrdb_user_get_by_email":        argTenant("tenant_id"),
		"qrdb_user_add_cert_binding":    argUser("user_id"),
		"qrdb_user_remove_cert_binding": argUser("user_id"),
		"qrdb_user_update":              argUser("id"),
		"qrdb_user_delete":              argUser("id"),
		"qrdb_user_list":                argTenant("tenant_id"),
		"qrdb_user_enable":              argUser("id"),
		"qrdb_user_disable":             argUser("id"),

		"qrdb_role_reconcile_builtin": argTenant("tenant_id"),
		"qrdb_role_create":            argTenant("tenant_id"),
		"qrdb_role_get":               argRole("id"),
		"qrdb_role_get_by_name":       argTenant("tenant_id"),
		"qrdb_role_update":            argRole("id"),
		"qrdb_role_delete":            argRole("id"),
		"qrdb_role_list":              argTenant("tenant_id"),
		"qrdb_role_assign":            argUser("user_id"),
		"qrdb_role_unassign":          argUser("user_id"),

		"qrdb_tenant_barrier_seal":                 argTenant("tenant_id"),
		"qrdb_tenant_barrier_officer_register":     argTenant("tenant_id"),
		"qrdb_tenant_barrier_officer_list":         argTenant("tenant_id"),
		"qrdb_tenant_barrier_officer_claim_status": argTenant("tenant_id"),
		"qrdb_tenant_barrier_shamir_seal_vote":     argTenant("tenant_id"),
	}
}

// registerTargets attaches identityTargets to every tool this server
// registered; a tool whose backing service is not wired is not registered
// and needs no target.
func (s *Server) registerTargets() {
	for tool, fn := range identityTargets() {
		if s.toolPerms.Covered(tool) {
			s.toolPerms.SetTarget(tool, fn)
		}
	}
}
