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

package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newGroupsCmd creates the "groups" command group: IAM group management
// (go-qrdb#1651). A group is a named set of roles of one tenant; every
// member holds the group's roles.
func newGroupsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "groups",
		Short: "IAM group management",
		Long: "Create, update, delete and list IAM groups, and add or remove their members.\n" +
			"A group is a named set of roles of one tenant; every member of the group\n" +
			"holds the group's roles in addition to its own.",
	}

	cmd.AddCommand(
		newGroupsListCmd(),
		newGroupsCreateCmd(),
		newGroupsGetCmd(),
		newGroupsGetByNameCmd(),
		newGroupsUpdateCmd(),
		newGroupsDeleteCmd(),
		newGroupsAddMemberCmd(),
		newGroupsRemoveMemberCmd(),
		newGroupsMembersCmd(),
	)

	return cmd
}

// newGroupsListCmd lists a tenant's groups.
func newGroupsListCmd() *cobra.Command {
	var (
		tenantID string
		page     int
		pageSize int
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List groups",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Groups().ListGroups(cmd.Context(), &transport.ListGroupsRequest{
				TenantID: tenantID,
				Page:     page,
				PageSize: pageSize,
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
				headers := []string{"ID", "NAME", "TENANT_ID", "ROLES", "DESCRIPTION", "CREATED_AT"}
				var rows [][]string
				for _, g := range resp.Groups {
					rows = append(rows, groupRow(g))
				}
				printTable(headers, rows)
				if resp.HasMore {
					_, _ = fmt.Fprintf(outputWriter, "\nTotal: %d (more results available)\n", resp.Total)
				}
				return nil
			}
		},
	}

	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Tenant ID (defaults to the caller's own tenant)")
	cmd.Flags().IntVar(&page, "page", 0, "Page number for pagination")
	cmd.Flags().IntVar(&pageSize, "page-size", 0, "Number of results per page")

	return cmd
}

// newGroupsCreateCmd creates a group.
func newGroupsCreateCmd() *cobra.Command {
	var (
		name        string
		tenantID    string
		description string
		roles       string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new group",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.CreateGroupRequest{
				Name:        name,
				TenantID:    tenantID,
				Description: description,
			}
			if roles != "" {
				req.RoleIDs = splitAndTrim(roles)
			}

			g, err := client.Groups().CreateGroup(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printGroup(g)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Group name")
	_ = cmd.MarkFlagRequired("name")
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Tenant ID (defaults to the caller's own tenant)")
	cmd.Flags().StringVar(&description, "description", "", "Group description")
	cmd.Flags().StringVar(&roles, "roles", "", "Comma-separated role IDs the group grants")

	return cmd
}

// newGroupsGetCmd retrieves a group by ID.
func newGroupsGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Retrieve a group by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := client.Groups().GetGroup(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			return printGroup(g)
		},
	}
}

// newGroupsGetByNameCmd retrieves a group by its tenant-scoped name.
func newGroupsGetByNameCmd() *cobra.Command {
	var tenantID string

	cmd := &cobra.Command{
		Use:   "get-by-name <name>",
		Short: "Retrieve a group by its tenant-scoped name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			g, err := client.Groups().GetGroupByName(cmd.Context(), &transport.GetGroupByNameRequest{
				TenantID: tenantID,
				Name:     args[0],
			})
			if err != nil {
				return err
			}

			return printGroup(g)
		},
	}

	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Tenant ID (defaults to the caller's own tenant)")

	return cmd
}

// newGroupsUpdateCmd changes a group's name, description or role set. A
// flag left unset leaves that field unchanged; --roles "" clears the roles.
func newGroupsUpdateCmd() *cobra.Command {
	var (
		name        string
		description string
		roles       string
	)

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update an existing group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &transport.UpdateGroupRequest{ID: args[0], Name: name}
			if cmd.Flags().Changed("description") {
				req.Description = &description
			}
			if cmd.Flags().Changed("roles") {
				ids := []string{}
				if roles != "" {
					ids = splitAndTrim(roles)
				}
				req.RoleIDs = &ids
			}

			g, err := client.Groups().UpdateGroup(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printGroup(g)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "New group name")
	cmd.Flags().StringVar(&description, "description", "", "New group description")
	cmd.Flags().StringVar(&roles, "roles", "", `Comma-separated role IDs replacing the group's roles ("" clears them)`)

	return cmd
}

// newGroupsDeleteCmd removes a group from every member, then deletes it.
func newGroupsDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a group by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client.Groups().DeleteGroup(cmd.Context(), args[0]); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}
}

// newGroupsAddMemberCmd adds a user to a group.
func newGroupsAddMemberCmd() *cobra.Command {
	var (
		userID  string
		groupID string
	)

	cmd := &cobra.Command{
		Use:   "add-member",
		Short: "Add a user to a group",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			uid, err := parseUserID(userID)
			if err != nil {
				return err
			}
			if err := client.Groups().AddGroupMember(cmd.Context(), uid, groupID); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}

	cmd.Flags().StringVar(&userID, "user", "", "User ID")
	_ = cmd.MarkFlagRequired("user")
	cmd.Flags().StringVar(&groupID, "group", "", "Group ID")
	_ = cmd.MarkFlagRequired("group")

	return cmd
}

// newGroupsRemoveMemberCmd removes a user from a group.
func newGroupsRemoveMemberCmd() *cobra.Command {
	var (
		userID  string
		groupID string
	)

	cmd := &cobra.Command{
		Use:   "remove-member",
		Short: "Remove a user from a group",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			uid, err := parseUserID(userID)
			if err != nil {
				return err
			}
			if err := client.Groups().RemoveGroupMember(cmd.Context(), uid, groupID); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}

	cmd.Flags().StringVar(&userID, "user", "", "User ID")
	_ = cmd.MarkFlagRequired("user")
	cmd.Flags().StringVar(&groupID, "group", "", "Group ID")
	_ = cmd.MarkFlagRequired("group")

	return cmd
}

// newGroupsMembersCmd lists a group's members.
func newGroupsMembersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "members <group-id>",
		Short: "List the members of a group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := client.Groups().ListGroupMembers(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				rows := make([][]string, len(resp.Members))
				for i, m := range resp.Members {
					rows[i] = []string{strconv.FormatUint(m.UserID, 10), m.Username, m.TenantID}
				}
				printTable([]string{"USER_ID", "USERNAME", "TENANT_ID"}, rows)
				return nil
			}
		},
	}
}

// printGroup prints one group in the selected output format.
func printGroup(g *transport.Group) error {
	switch outputFormat {
	case "json":
		return printJSON(g)
	case "yaml":
		return printYAML(g)
	default:
		_, _ = fmt.Fprintf(outputWriter, "ID:          %s\n", g.ID)
		_, _ = fmt.Fprintf(outputWriter, "Name:        %s\n", g.Name)
		_, _ = fmt.Fprintf(outputWriter, "TenantID:    %s\n", g.TenantID)
		_, _ = fmt.Fprintf(outputWriter, "Description: %s\n", g.Description)
		_, _ = fmt.Fprintf(outputWriter, "Roles:       %s\n", strings.Join(g.RoleIDs, ", "))
		_, _ = fmt.Fprintf(outputWriter, "CreatedAt:   %s\n", g.CreatedAt)
		_, _ = fmt.Fprintf(outputWriter, "UpdatedAt:   %s\n", g.UpdatedAt)
		return nil
	}
}

// groupRow is one group's row in the list table.
func groupRow(g *transport.Group) []string {
	return []string{g.ID, g.Name, g.TenantID, strings.Join(g.RoleIDs, ","), g.Description, g.CreatedAt}
}
