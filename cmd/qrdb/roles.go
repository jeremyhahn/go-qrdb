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

package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newRolesCmd creates the "roles" command group with all role management
// subcommands: list, create, get, update, delete, assign, unassign, and permissions.
func newRolesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "roles",
		Short: "Role management",
		Long:  "Create, update, delete, assign, unassign, and list RBAC roles and permissions.",
	}

	cmd.AddCommand(
		newRolesListCmd(),
		newRolesCreateCmd(),
		newRolesGetCmd(),
		newRolesGetByNameCmd(),
		newRolesUpdateCmd(),
		newRolesDeleteCmd(),
		newRolesAssignCmd(),
		newRolesUnassignCmd(),
		newRolesPermissionsCmd(),
		newRolesResolvePermissionsCmd(),
		newRolesOwnPermissionsCmd(),
		newRolesReconcileCmd(),
	)

	return cmd
}

// newRolesListCmd creates the "roles list" subcommand.
func newRolesListCmd() *cobra.Command {
	var (
		tenantID string
		page     int
		pageSize int
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List roles",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.ListRolesRequest{
				TenantID: tenantID,
				Page:     page,
				PageSize: pageSize,
			}

			resp, err := client.Roles().ListRoles(cmd.Context(), req)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				headers := []string{"ID", "NAME", "TENANT_ID", "PERMISSIONS", "SYSTEM", "CREATED_AT"}
				var rows [][]string
				for _, r := range resp.Roles {
					rows = append(rows, roleRow(r))
				}
				printTable(headers, rows)
				if resp.HasMore {
					_, _ = fmt.Fprintf(outputWriter, "\nTotal: %d (more results available)\n", resp.Total)
				}
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Filter by tenant ID")
	cmd.Flags().IntVar(&page, "page", 0, "Page number for pagination")
	cmd.Flags().IntVar(&pageSize, "page-size", 0, "Number of results per page")

	return cmd
}

// newRolesCreateCmd creates the "roles create" subcommand.
func newRolesCreateCmd() *cobra.Command {
	var (
		name        string
		tenantID    string
		permissions string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new role",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.CreateRoleRequest{
				Name:     name,
				TenantID: tenantID,
			}

			if permissions != "" {
				req.Permissions = splitAndTrim(permissions)
			}

			role, err := client.Roles().CreateRole(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printRole(role)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Role name")
	_ = cmd.MarkFlagRequired("name")
	cmd.Flags().StringVar(&permissions, "permissions", "", "Comma-separated permissions")
	_ = cmd.MarkFlagRequired("permissions")
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Tenant ID")

	return cmd
}

// newRolesGetCmd creates the "roles get" subcommand.
func newRolesGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Retrieve a role by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			role, err := client.Roles().GetRole(cmd.Context(), id)
			if err != nil {
				return err
			}

			return printRole(role)
		},
	}
}

// newRolesGetByNameCmd creates the "roles get-by-name" subcommand.
func newRolesGetByNameCmd() *cobra.Command {
	var tenantID string

	cmd := &cobra.Command{
		Use:   "get-by-name <name>",
		Short: "Retrieve a role by its tenant-scoped name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			role, err := client.Roles().GetRoleByName(cmd.Context(), &transport.GetRoleByNameRequest{
				TenantID: tenantID,
				Name:     args[0],
			})
			if err != nil {
				return err
			}

			return printRole(role)
		},
	}

	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Tenant ID (defaults to the system tenant)")

	return cmd
}

// newRolesUpdateCmd creates the "roles update" subcommand.
func newRolesUpdateCmd() *cobra.Command {
	var (
		name        string
		permissions string
	)

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update an existing role",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &transport.UpdateRoleRequest{
				ID:   args[0],
				Name: name,
			}

			if permissions != "" {
				req.Permissions = splitAndTrim(permissions)
			}

			role, err := client.Roles().UpdateRole(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printRole(role)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "New role name")
	cmd.Flags().StringVar(&permissions, "permissions", "", "Comma-separated permissions")

	return cmd
}

// newRolesDeleteCmd creates the "roles delete" subcommand.
func newRolesDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a role by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]

			if err := client.Roles().DeleteRole(cmd.Context(), id); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}
}

// newRolesAssignCmd creates the "roles assign" subcommand.
func newRolesAssignCmd() *cobra.Command {
	var (
		userID string
		roleID string
	)

	cmd := &cobra.Command{
		Use:   "assign",
		Short: "Assign a role to a user",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			uid, err := parseUserID(userID)
			if err != nil {
				return err
			}
			if err := client.Roles().AssignRole(cmd.Context(), uid, roleID); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}

	cmd.Flags().StringVar(&userID, "user", "", "User ID")
	_ = cmd.MarkFlagRequired("user")
	cmd.Flags().StringVar(&roleID, "role", "", "Role ID")
	_ = cmd.MarkFlagRequired("role")

	return cmd
}

// newRolesUnassignCmd creates the "roles unassign" subcommand.
func newRolesUnassignCmd() *cobra.Command {
	var (
		userID string
		roleID string
	)

	cmd := &cobra.Command{
		Use:   "unassign",
		Short: "Remove a role from a user",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			uid, err := parseUserID(userID)
			if err != nil {
				return err
			}
			if err := client.Roles().UnassignRole(cmd.Context(), uid, roleID); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}

	cmd.Flags().StringVar(&userID, "user", "", "User ID")
	_ = cmd.MarkFlagRequired("user")
	cmd.Flags().StringVar(&roleID, "role", "", "Role ID")
	_ = cmd.MarkFlagRequired("role")

	return cmd
}

// newRolesPermissionsCmd creates the "roles permissions" subcommand.
func newRolesPermissionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "permissions",
		Short: "List all available permissions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Roles().ListPermissions(cmd.Context())
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				for _, cat := range resp.Categories {
					_, _ = fmt.Fprintf(outputWriter, "%s (%s):\n", cat.Name, cat.Group)
					for _, perm := range cat.Permissions {
						_, _ = fmt.Fprintf(outputWriter, "  - %s\n", perm)
					}
				}
				return nil
			}
		},
	}
}

// newRolesResolvePermissionsCmd creates the "roles resolve-permissions"
// subcommand, which reports the union of effective permissions for the
// given role IDs.
func newRolesResolvePermissionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resolve-permissions <role-id> [role-id...]",
		Short: "Resolve the effective permissions for a set of roles",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := client.Roles().ResolvePermissions(cmd.Context(), &transport.ResolvePermissionsRequest{
				RoleIDs: args,
			})
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				for _, perm := range resp.Permissions {
					_, _ = fmt.Fprintf(outputWriter, "%s\n", perm)
				}
				return nil
			}
		},
	}
}

// ownPermissionsOutput is the json/yaml shape of roles own-permissions,
// the same document the server's own-permissions endpoint returns.
type ownPermissionsOutput struct {
	Permissions []transport.PermissionInfo `json:"permissions" yaml:"permissions"`
}

func newRolesOwnPermissionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "own-permissions",
		Short: "List the authenticated caller's own effective permissions",
		Long: "List the concrete permissions the authenticated caller holds, with wildcard grants " +
			"expanded and each permission's tenancy scope. The server answers for the caller " +
			"only, so no permission beyond authentication is needed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rc := client.RBAC()
			if rc == nil {
				return &RBACClientUnavailableError{}
			}
			perms, err := rc.ListOwnPermissions(cmd.Context())
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(ownPermissionsOutput{Permissions: perms})
			case "yaml":
				return printYAML(ownPermissionsOutput{Permissions: perms})
			default:
				rows := make([][]string, len(perms))
				for i, p := range perms {
					rows[i] = []string{p.Name, p.Resource, p.Action, string(p.Scope)}
				}
				printTable([]string{"NAME", "RESOURCE", "ACTION", "SCOPE"}, rows)
				return nil
			}
		},
	}
}

// RBACClientUnavailableError is returned when the active connection does
// not expose an RBAC client (its transport does not implement
// transport.RBACProvider).
type RBACClientUnavailableError struct{}

// Error returns a human-readable description.
func (e *RBACClientUnavailableError) Error() string {
	return "qrdb: roles: no RBAC client available on the active connection"
}

// newRolesReconcileCmd creates the "roles reconcile" subcommand, which
// converges a tenant's stored built-in roles onto the server build's
// definitions. It is the only way to repair a tenant seeded before a
// permission was renamed or added: built-in roles are otherwise immutable.
func newRolesReconcileCmd() *cobra.Command {
	var tenantID string

	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Converge a tenant's built-in roles onto the server build's definitions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Roles().ReconcileBuiltInRoles(cmd.Context(), &transport.ReconcileRolesRequest{
				TenantID: tenantID,
			})
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				if len(resp.Roles) == 0 {
					_, _ = fmt.Fprintf(outputWriter, "Tenant %s: built-in roles already current\n", resp.TenantID)
					return nil
				}
				_, _ = fmt.Fprintf(outputWriter, "Tenant %s:\n", resp.TenantID)
				for _, r := range resp.Roles {
					verb := "updated"
					if r.Created {
						verb = "created"
					}
					_, _ = fmt.Fprintf(outputWriter, "  %s (%s) %s\n", r.Name, r.RoleID, verb)
					for _, p := range r.Added {
						_, _ = fmt.Fprintf(outputWriter, "    + %s\n", p)
					}
					for _, p := range r.Removed {
						_, _ = fmt.Fprintf(outputWriter, "    - %s\n", p)
					}
				}
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&tenantID, "tenant", "", "Tenant ID (defaults to the caller's own tenant)")

	return cmd
}

// printRole prints a single Role in the current output format.
func printRole(r *transport.Role) error {
	switch outputFormat {
	case "json":
		return printJSON(r)
	case "yaml":
		return printYAML(r)
	default:
		_, _ = fmt.Fprintf(outputWriter, "ID:          %s\n", r.ID)
		_, _ = fmt.Fprintf(outputWriter, "Name:        %s\n", r.Name)
		_, _ = fmt.Fprintf(outputWriter, "TenantID:    %s\n", r.TenantID)
		_, _ = fmt.Fprintf(outputWriter, "Permissions: %s\n", strings.Join(r.Permissions, ", "))
		_, _ = fmt.Fprintf(outputWriter, "System:      %s\n", strconv.FormatBool(r.System))
		_, _ = fmt.Fprintf(outputWriter, "CreatedAt:   %s\n", r.CreatedAt)
		_, _ = fmt.Fprintf(outputWriter, "UpdatedAt:   %s\n", r.UpdatedAt)
		if len(r.RetiredPermissions) > 0 {
			// Drift, not an error: the role carries verbs this server build no
			// longer defines. `qrdb roles reconcile` is the repair.
			_, _ = fmt.Fprintf(outputWriter, "Retired:     %s\n", strings.Join(r.RetiredPermissions, ", "))
		}
		return nil
	}
}

// roleRow converts a Role into a table row for tabular output.
func roleRow(r *transport.Role) []string {
	return []string{
		r.ID,
		r.Name,
		r.TenantID,
		strings.Join(r.Permissions, ","),
		strconv.FormatBool(r.System),
		r.CreatedAt,
	}
}
