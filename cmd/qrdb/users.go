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

// newUsersCmd creates the "users" command group with all user management
// subcommands: list, create, get, update, delete, enable, and disable.
func newUsersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "users",
		Short: "User management",
		Long:  "Create, update, delete, enable, disable, and list passwordless user accounts.",
	}

	cmd.AddCommand(
		newUsersListCmd(),
		newUsersCreateCmd(),
		newUsersGetCmd(),
		newUsersUpdateCmd(),
		newUsersDeleteCmd(),
		newUsersEnableCmd(),
		newUsersDisableCmd(),
	)

	return cmd
}

// newUsersListCmd creates the "users list" subcommand.
func newUsersListCmd() *cobra.Command {
	var (
		tenantID string
		page     int
		pageSize int
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List users",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.ListUsersRequest{
				TenantID: tenantID,
				Page:     page,
				PageSize: pageSize,
			}

			resp, err := client.Users().ListUsers(cmd.Context(), req)
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				headers := []string{"ID", "USERNAME", "EMAIL", "TENANT_ID", "ROLES", "ENABLED", "SYSTEM", "CREATED_AT"}
				var rows [][]string
				for _, u := range resp.Users {
					rows = append(rows, userRow(u))
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

// newUsersCreateCmd creates the "users create" subcommand.
func newUsersCreateCmd() *cobra.Command {
	var (
		username             string
		email                string
		name                 string
		tenantID             string
		roles                string
		withoutEmailLoginKey bool
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new user",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.CreateUserRequest{
				Username:             username,
				Email:                email,
				Name:                 name,
				WithoutEmailLoginKey: withoutEmailLoginKey,
				TenantID:             tenantID,
			}

			if roles != "" {
				req.RoleIDs = splitAndTrim(roles)
			}

			user, err := client.Users().CreateUser(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printUser(user)
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "Username for the new user")
	_ = cmd.MarkFlagRequired("username")
	cmd.Flags().StringVar(&email, "email", "", "Canonical login key, unique within the tenant (required unless --without-email-login-key)")
	cmd.Flags().StringVar(&name, "name", "", "Display name (presentation only)")
	cmd.Flags().BoolVar(&withoutEmailLoginKey, "without-email-login-key", false,
		"Create a user with no email login key — a certificate-only or bootstrap identity that cannot be resolved by email")
	cmd.Flags().StringVar(&tenantID, "tenant-id", "", "Tenant ID")
	cmd.Flags().StringVar(&roles, "roles", "", "Comma-separated role IDs to assign")

	return cmd
}

// newUsersGetCmd creates the "users get" subcommand.
func newUsersGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Retrieve a user by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseUserID(args[0])
			if err != nil {
				return err
			}

			user, err := client.Users().GetUser(cmd.Context(), id)
			if err != nil {
				return err
			}

			return printUser(user)
		},
	}
}

// newUsersUpdateCmd creates the "users update" subcommand.
func newUsersUpdateCmd() *cobra.Command {
	var (
		username string
		roles    string
		email    string
		name     string
	)

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update an existing user",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseUserID(args[0])
			if err != nil {
				return err
			}
			req := &transport.UpdateUserRequest{
				ID:       id,
				Username: username,
				Email:    email,
				Name:     name,
			}

			if roles != "" {
				req.RoleIDs = splitAndTrim(roles)
			}

			user, updateErr := client.Users().UpdateUser(cmd.Context(), req)
			if updateErr != nil {
				return updateErr
			}

			return printUser(user)
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "New username")
	cmd.Flags().StringVar(&email, "email", "", "New canonical login key; uniqueness is re-validated within the tenant. Left unchanged when empty")
	cmd.Flags().StringVar(&name, "name", "", "New display name. Left unchanged when empty")
	cmd.Flags().StringVar(&roles, "roles", "", "Comma-separated role IDs to assign")

	return cmd
}

// newUsersDeleteCmd creates the "users delete" subcommand.
func newUsersDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a user by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseUserID(args[0])
			if err != nil {
				return err
			}

			if err := client.Users().DeleteUser(cmd.Context(), id); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}
}

// newUsersEnableCmd creates the "users enable" subcommand.
func newUsersEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <id>",
		Short: "Enable a user account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseUserID(args[0])
			if err != nil {
				return err
			}

			if err := client.Users().EnableUser(cmd.Context(), id); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}
}

// newUsersDisableCmd creates the "users disable" subcommand.
func newUsersDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable <id>",
		Short: "Disable a user account",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseUserID(args[0])
			if err != nil {
				return err
			}

			if err := client.Users().DisableUser(cmd.Context(), id); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}
}

// printUser prints a single User in the current output format.
func printUser(u *transport.User) error {
	switch outputFormat {
	case "json":
		return printJSON(u)
	case "yaml":
		return printYAML(u)
	default:
		_, _ = fmt.Fprintf(outputWriter, "ID:        %d\n", u.ID)
		_, _ = fmt.Fprintf(outputWriter, "Username:  %s\n", u.Username)
		// The canonical login key, and the display name. Omitting them meant a
		// caller could set --email and never see it back.
		_, _ = fmt.Fprintf(outputWriter, "Email:     %s\n", u.Email)
		_, _ = fmt.Fprintf(outputWriter, "Name:      %s\n", u.Name)
		_, _ = fmt.Fprintf(outputWriter, "TenantID:  %s\n", u.TenantID)
		_, _ = fmt.Fprintf(outputWriter, "RoleIDs:   %s\n", strings.Join(u.RoleIDs, ", "))
		_, _ = fmt.Fprintf(outputWriter, "Enabled:   %s\n", strconv.FormatBool(u.Enabled))
		_, _ = fmt.Fprintf(outputWriter, "System:    %s\n", strconv.FormatBool(u.System))
		_, _ = fmt.Fprintf(outputWriter, "CreatedAt: %s\n", u.CreatedAt)
		_, _ = fmt.Fprintf(outputWriter, "UpdatedAt: %s\n", u.UpdatedAt)
		return nil
	}
}

// userRow converts a User into a table row for tabular output.
func userRow(u *transport.User) []string {
	return []string{
		strconv.FormatUint(u.ID, 10),
		u.Username,
		u.Email,
		u.TenantID,
		strings.Join(u.RoleIDs, ","),
		strconv.FormatBool(u.Enabled),
		strconv.FormatBool(u.System),
		u.CreatedAt,
	}
}

// splitAndTrim splits a comma-separated string and trims whitespace
// from each element.
func splitAndTrim(s string) []string {
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}
