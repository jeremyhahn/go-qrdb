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
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newUsersCmd creates the "users" command group with all user management
// subcommands: list, create, get, update, delete, enable, disable, suspend
// and unsuspend.
func newUsersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "users",
		Short: "User management",
		Long:  "Create, update, delete, enable, disable, suspend, unsuspend, and list passwordless user accounts.",
	}

	cmd.AddCommand(
		newUsersListCmd(),
		newUsersCreateCmd(),
		newUsersGetCmd(),
		newUsersUpdateCmd(),
		newUsersDeleteCmd(),
		newUsersEnableCmd(),
		newUsersDisableCmd(),
		newUsersSuspendCmd(),
		newUsersUnsuspendCmd(),
		newUsersInviteCmd(),
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
				headers := []string{"ID", "USERNAME", "EMAIL", "TENANT_ID", "ROLES", "LIFECYCLE", "SYSTEM", "CREATED_AT"}
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

// newUsersInviteCmd creates the "users invite" subcommand.
func newUsersInviteCmd() *cobra.Command {
	var (
		username          string
		email             string
		name              string
		roles             string
		keyPinFingerprint string
	)

	cmd := &cobra.Command{
		Use:   "invite",
		Short: "Invite a user into your own tenant",
		Long: `Create a pending account in your own tenant -- or, given only --username,
re-invite one still pending -- and return a single-use enrolment token.

The token is returned exactly once: save it now and deliver it to the
invited user out of band (chat, email, however you reach them). They
redeem it themselves with "qrdb enroll", generating a key of their own;
you never hold a credential for their account. An invitation expires
after the server's configured auth.invitation_ttl (24h by default).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !client.SupportsInvitations() {
				return &transport.CapabilityUnsupportedError{Message: "invitation self-enrolment is not carried on this transport"}
			}

			req := &transport.InviteUserRequest{
				Username:          username,
				Email:             email,
				Name:              name,
				KeyPinFingerprint: keyPinFingerprint,
			}

			if roles != "" {
				req.RoleIDs = splitAndTrim(roles)
			}

			inv, err := client.Invitations().InviteUser(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printInvitation(inv)
		},
	}

	cmd.Flags().StringVar(&username, "username", "", "The account to invite")
	_ = cmd.MarkFlagRequired("username")
	cmd.Flags().StringVar(&email, "email", "", "Canonical login key for a new account")
	cmd.Flags().StringVar(&name, "name", "", "Display name for a new account (presentation only)")
	cmd.Flags().StringVar(&roles, "roles", "", "Comma-separated role IDs for a new account")
	cmd.Flags().StringVar(&keyPinFingerprint, "key-pin-fingerprint", "",
		"Pin the invitation to one key -- the base64url SHA-256 SPKI thumbprint of a CSR the invited user sends out of band. Only that key can redeem it")

	return cmd
}

// printInvitation prints a single Invitation in the current output format.
// The token it carries is returned by the server exactly once; this is that
// one appearance.
func printInvitation(inv *transport.Invitation) error {
	switch outputFormat {
	case "json":
		return printJSON(inv)
	case "yaml":
		return printYAML(inv)
	default:
		_, _ = fmt.Fprintf(outputWriter, "ID:        %d\n", inv.ID)
		_, _ = fmt.Fprintf(outputWriter, "Token:     %s\n", inv.Token)
		_, _ = fmt.Fprintf(outputWriter, "ExpiresAt: %s\n", inv.ExpiresAt.Format(time.RFC3339))
		_, _ = fmt.Fprintf(outputWriter, "UserID:    %d\n", inv.UserID)
		_, _ = fmt.Fprintf(outputWriter, "Username:  %s\n", inv.Username)
		_, _ = fmt.Fprintf(outputWriter, "TenantID:  %s\n", inv.TenantID)
		_, _ = fmt.Fprintf(outputWriter, "Created:   %s\n", strconv.FormatBool(inv.Created))
		if inv.KeyPinFingerprint != "" {
			_, _ = fmt.Fprintf(outputWriter, "KeyPinFingerprint: %s\n", inv.KeyPinFingerprint)
		}
		return nil
	}
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
		username          string
		roles             string
		email             string
		name              string
		roleBindings      []string
		clearRoleBindings bool
		attributes        []string
		clearAttributes   bool
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

			switch {
			case len(roleBindings) > 0:
				parsed, perr := parseRoleBindings(roleBindings)
				if perr != nil {
					return perr
				}
				req.RoleBindings = parsed
			case clearRoleBindings:
				req.RoleBindings = []transport.RoleBinding{}
			}

			switch {
			case len(attributes) > 0:
				parsed, perr := parseAttributes(attributes)
				if perr != nil {
					return perr
				}
				req.Attributes = parsed
			case clearAttributes:
				req.Attributes = map[string]string{}
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
	cmd.Flags().StringArrayVar(&roleBindings, "role-binding", nil,
		"Role binding to set, as role_id:tenant_id; tenant_id must be the user's own tenant. Repeatable. Replaces the current set. Left unchanged when absent")
	cmd.Flags().BoolVar(&clearRoleBindings, "clear-role-bindings", false, "Remove every tenant-scoped role binding")
	cmd.Flags().StringArrayVar(&attributes, "attribute", nil,
		"ABAC attribute to set, as key=value. Repeatable. Replaces the current set. Left unchanged when absent")
	cmd.Flags().BoolVar(&clearAttributes, "clear-attributes", false, "Remove every ABAC attribute")

	return cmd
}

// parseRoleBindings parses a list of "role_id:tenant_id" flag values into
// transport.RoleBinding values.
func parseRoleBindings(values []string) ([]transport.RoleBinding, error) {
	out := make([]transport.RoleBinding, 0, len(values))
	for _, v := range values {
		parts := strings.SplitN(v, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, &InvalidFlagValueError{Flag: "role-binding", Value: v, Want: "role_id:tenant_id"}
		}
		out = append(out, transport.RoleBinding{RoleID: parts[0], TenantID: parts[1]})
	}
	return out, nil
}

// parseAttributes parses a list of "key=value" flag values into an
// attributes map.
func parseAttributes(values []string) (map[string]string, error) {
	out := make(map[string]string, len(values))
	for _, v := range values {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 || parts[0] == "" {
			return nil, &InvalidFlagValueError{Flag: "attribute", Value: v, Want: "key=value"}
		}
		out[parts[0]] = parts[1]
	}
	return out, nil
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

// newUsersSuspendCmd creates the "users suspend" subcommand.
func newUsersSuspendCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "suspend <id>",
		Short: "Suspend an active user account",
		Long: "Suspend an active user account. Its requests are refused, including those " +
			"carrying a token issued before the suspension, until it is unsuspended.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseUserID(args[0])
			if err != nil {
				return err
			}

			if err := client.Users().SuspendUser(cmd.Context(), id); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}
}

// newUsersUnsuspendCmd creates the "users unsuspend" subcommand.
func newUsersUnsuspendCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unsuspend <id>",
		Short: "Return a suspended user account to active",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseUserID(args[0])
			if err != nil {
				return err
			}

			if err := client.Users().UnsuspendUser(cmd.Context(), id); err != nil {
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
		_, _ = fmt.Fprintf(outputWriter, "Lifecycle: %s\n", u.Lifecycle)
		_, _ = fmt.Fprintf(outputWriter, "System:    %s\n", strconv.FormatBool(u.System))
		// The enrolled mTLS keys, expired ones included: after a key
		// compromise "which keys authenticate as this account" is the first
		// question, and the thumbprint printed here is the one the withdrawal
		// verb takes.
		for _, b := range u.CertBindings {
			line := b.Thumbprint + " (" + b.Kind
			if b.NotAfter != "" {
				line += ", expires " + b.NotAfter
			}
			_, _ = fmt.Fprintf(outputWriter, "CertBinding: %s)\n", line)
		}
		for _, rb := range u.RoleBindings {
			_, _ = fmt.Fprintf(outputWriter, "RoleBinding: %s@%s\n", rb.RoleID, rb.TenantID)
		}
		for k, v := range u.Attributes {
			_, _ = fmt.Fprintf(outputWriter, "Attribute: %s=%s\n", k, v)
		}
		// MFAEnabled/MFAMethods/MFAEnrolledAt are DERIVED from the account's
		// enrolled factors, never stored — see pkg/user.ResolveFactorView.
		_, _ = fmt.Fprintf(outputWriter, "MFAEnabled: %s\n", strconv.FormatBool(u.MFAEnabled))
		if len(u.MFAMethods) > 0 {
			_, _ = fmt.Fprintf(outputWriter, "MFAMethods: %s\n", strings.Join(u.MFAMethods, ", "))
		}
		for _, f := range u.Factors {
			// Data is never exposed on any transport (go-qrdb#192/#197); only
			// metadata is ever available here to print.
			_, _ = fmt.Fprintf(outputWriter, "Factor: %s (%s)\n", f.ID, f.FactorName)
		}
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
		u.Lifecycle,
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
