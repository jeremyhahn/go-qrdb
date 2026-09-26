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
	"encoding/base64"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newTenantCmd creates the "tenant" command group with all tenant management
// subcommands: list, create, get, update, update-quotas, delete,
// barrier-init, barrier-unseal, and barrier-seal.
func newTenantCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tenant",
		Short: "Tenant management",
		Long:  "Create, delete, and list multi-tenant namespaces, and manage per-tenant cryptographic barriers.",
	}

	cmd.AddCommand(
		newTenantListCmd(),
		newTenantCreateCmd(),
		newTenantGetCmd(),
		newTenantUpdateCmd(),
		newTenantUpdateQuotasCmd(),
		newTenantSetEntitlementCmd(),
		newTenantDeleteCmd(),
		newTenantListDeletedCmd(),
		newTenantReconcileDeletedCmd(),
		newTenantBarrierInitCmd(),
		newTenantBarrierUnsealCmd(),
		newTenantBarrierSealCmd(),
		newTenantBarrierStatusCmd(),
	)

	return cmd
}

// newTenantListCmd creates the "tenant list" subcommand.
func newTenantListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List tenants",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			resp, err := client.Tenants().ListTenants(cmd.Context())
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(resp)
			case "yaml":
				return printYAML(resp)
			default:
				headers := []string{"ID", "NAME", "CREATED_AT", "UPDATED_AT"}
				rows := make([][]string, 0, len(resp.Tenants))
				for _, t := range resp.Tenants {
					rows = append(rows, tenantRow(t))
				}
				printTable(headers, rows)
				return nil
			}
		},
	}
}

// newTenantCreateCmd creates the "tenant create" subcommand.
func newTenantCreateCmd() *cobra.Command {
	var (
		id                string
		name              string
		isolationType     string
		barrierAutoUnseal bool
		dataRoot          string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new tenant",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.CreateTenantRequest{
				ID:                id,
				Name:              name,
				IsolationType:     transport.TenantIsolationType(isolationType),
				BarrierAutoUnseal: barrierAutoUnseal,
				DataRoot:          dataRoot,
			}

			resp, err := client.Tenants().CreateTenant(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printTenant(resp.Tenant)
		},
	}

	cmd.Flags().StringVar(&id, "id", "", "Tenant ID (must be unique)")
	_ = cmd.MarkFlagRequired("id")
	cmd.Flags().StringVar(&name, "name", "", "Human-readable tenant name")
	_ = cmd.MarkFlagRequired("name")
	cmd.Flags().StringVar(&isolationType, "isolation-type", "", "Isolation type (prefix|barrier|volume|raft); empty = prefix")
	cmd.Flags().BoolVar(&barrierAutoUnseal, "barrier-auto-unseal", false,
		"For a dedicated-isolation tenant (barrier/volume/raft), use system-managed auto-unseal instead of the secure self-managed default (entitlement-gated)")
	cmd.Flags().StringVar(&dataRoot, "data-root", "",
		"For a volume/raft isolation tenant, pin the dedicated filesystem DataRoot to this absolute path instead of pool allocation (e.g. a dedicated disk mount); must name an empty or not-yet-existing directory")

	return cmd
}

// newTenantGetCmd creates the "tenant get" subcommand.
func newTenantGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <id>",
		Short: "Retrieve a tenant by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := client.Tenants().GetTenant(cmd.Context(), args[0])
			if err != nil {
				return err
			}

			return printTenant(resp.Tenant)
		},
	}
}

// newTenantDeleteCmd creates the "tenant delete" subcommand.
func newTenantDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a tenant by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client.Tenants().DeleteTenant(cmd.Context(), args[0]); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}
}

// newTenantUpdateCmd creates the "tenant update" subcommand.
func newTenantUpdateCmd() *cobra.Command {
	var (
		name          string
		isolationType string
	)

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a tenant's mutable fields",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &transport.UpdateTenantRequest{
				TenantID:      args[0],
				Name:          name,
				IsolationType: transport.TenantIsolationType(isolationType),
			}

			resp, err := client.Tenants().UpdateTenant(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printTenant(resp.Tenant)
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "New human-readable tenant name")
	cmd.Flags().StringVar(&isolationType, "isolation-type", "", "Isolation type (barrier|prefix)")

	return cmd
}

// newTenantUpdateQuotasCmd creates the "tenant update-quotas" subcommand.
func newTenantUpdateQuotasCmd() *cobra.Command {
	var (
		maxDatabases         int64
		maxRaftGroups        int64
		maxReplicasPerGroup  int64
		maxTotalReplicas     int64
		maxCollections       int64
		maxKeys              int64
		maxStorageBytes      int64
		maxDatabaseSizeBytes int64
		maxUsers             int64
		maxRoles             int64
		maxCertificates      int64
		maxBackups           int64
		maxVectorDims        int64
		maxTSMetrics         int64
		systemBarrierQuota   int64
	)

	cmd := &cobra.Command{
		Use:   "update-quotas <id>",
		Short: "Set resource quotas for a tenant",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &transport.UpdateTenantQuotasRequest{
				TenantID: args[0],
				Quotas: transport.TenantQuotas{
					MaxDatabases:         maxDatabases,
					MaxRaftGroups:        maxRaftGroups,
					MaxReplicasPerGroup:  maxReplicasPerGroup,
					MaxTotalReplicas:     maxTotalReplicas,
					MaxCollections:       maxCollections,
					MaxKeys:              maxKeys,
					MaxStorageBytes:      maxStorageBytes,
					MaxDatabaseSizeBytes: maxDatabaseSizeBytes,
					MaxUsers:             maxUsers,
					MaxRoles:             maxRoles,
					MaxCertificates:      maxCertificates,
					MaxBackups:           maxBackups,
					MaxVectorDims:        maxVectorDims,
					MaxTSMetrics:         maxTSMetrics,
					SystemBarrierQuota:   systemBarrierQuota,
				},
			}

			resp, err := client.Tenants().UpdateTenantQuotas(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printTenant(resp.Tenant)
		},
	}

	cmd.Flags().Int64Var(&maxDatabases, "max-databases", 0, "Maximum number of databases (-1 = unlimited)")
	cmd.Flags().Int64Var(&maxRaftGroups, "max-raft-groups", 0, "Maximum number of Raft groups")
	cmd.Flags().Int64Var(&maxReplicasPerGroup, "max-replicas-per-group", 0, "Maximum replication factor per dedicated-raft database (-1 = unlimited)")
	cmd.Flags().Int64Var(&maxTotalReplicas, "max-total-replicas", 0, "Maximum aggregate replicas across all the tenant's raft groups (-1 = unlimited)")
	cmd.Flags().Int64Var(&maxCollections, "max-collections", 0, "Maximum number of collections")
	cmd.Flags().Int64Var(&maxKeys, "max-keys", 0, "Maximum number of keys")
	cmd.Flags().Int64Var(&maxStorageBytes, "max-storage-bytes", 0, "Maximum storage in bytes")
	cmd.Flags().Int64Var(&maxDatabaseSizeBytes, "max-database-size-bytes", 0, "Maximum size in bytes of any single database")
	cmd.Flags().Int64Var(&maxUsers, "max-users", 0, "Maximum number of users")
	cmd.Flags().Int64Var(&maxRoles, "max-roles", 0, "Maximum number of roles")
	cmd.Flags().Int64Var(&maxCertificates, "max-certificates", 0, "Maximum number of certificates")
	cmd.Flags().Int64Var(&maxBackups, "max-backups", 0, "Maximum number of backups")
	cmd.Flags().Int64Var(&maxVectorDims, "max-vector-dims", 0, "Maximum vector dimensions")
	cmd.Flags().Int64Var(&maxTSMetrics, "max-ts-metrics", 0, "Maximum number of time-series metrics")
	cmd.Flags().Int64Var(&systemBarrierQuota, "system-barrier-quota", 0, "Bytes an isolated tenant may consume via the shared system barrier (0 = option unavailable, -1 = unlimited)")

	return cmd
}

// newTenantSetEntitlementCmd creates the "tenant set-entitlement" subcommand,
// which sets a tenant's feature entitlements (allowed isolation modes and
// dedicated-storage features).
func newTenantSetEntitlementCmd() *cobra.Command {
	var (
		isolationModes            []string
		allowDedicatedRaft        bool
		allowVolumeStorage        bool
		allowSystemManagedBarrier bool
		allowedBarrierModes       []string
	)

	cmd := &cobra.Command{
		Use:   "set-entitlement <id>",
		Short: "Set feature entitlements for a tenant",
		Long:  "Set which isolation modes and dedicated-storage features a tenant's plan permits.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &transport.UpdateTenantEntitlementsRequest{
				TenantID: args[0],
				Entitlements: transport.TenantEntitlements{
					AllowedIsolationModes:     isolationModes,
					AllowDedicatedRaft:        allowDedicatedRaft,
					AllowVolumeStorage:        allowVolumeStorage,
					AllowSystemManagedBarrier: allowSystemManagedBarrier,
					AllowedBarrierModes:       allowedBarrierModes,
				},
			}

			resp, err := client.Tenants().UpdateTenantEntitlements(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printTenant(resp.Tenant)
		},
	}

	cmd.Flags().StringSliceVar(&isolationModes, "isolation-modes", nil, "Comma-separated list of permitted isolation modes (e.g. prefix,volume,raft)")
	cmd.Flags().BoolVar(&allowDedicatedRaft, "allow-dedicated-raft", false, "Permit provisioning dedicated Raft-backed databases")
	cmd.Flags().BoolVar(&allowVolumeStorage, "allow-volume-storage", false, "Permit provisioning databases on a dedicated storage volume")
	cmd.Flags().BoolVar(&allowSystemManagedBarrier, "allow-system-managed-barrier", false, "Permit opting a dedicated-isolation tenant into system-managed barrier auto-unseal")
	cmd.Flags().StringSliceVar(&allowedBarrierModes, "allowed-barrier-modes", nil, "Comma-separated dedicated-barrier sealing strategies the tenant may select (e.g. none,passphrase,shamir or an xkms hardware id); empty = software defaults")

	return cmd
}

// newTenantBarrierInitCmd creates the "tenant barrier-init" subcommand.
//
// --strategy selects the seal strategy (none|passphrase|shamir|a registered
// hardware id); when omitted the server defaults to the passphrase strategy
// (pkg/tenant/service.go TenantBarrierInitWithStrategy). --passphrase carries
// the caller-supplied secret required by the passphrase strategy -- there is
// no derived fallback, so omitting it against the passphrase strategy fails
// with a typed empty-passphrase error from the server. --threshold/--shares
// configure the Shamir M-of-N quorum and are ignored by every other
// strategy. This mirrors the request shape the desktop GUI sends via
// TenantBinding.BarrierInit / sdk/typescript TenantBarrierInitRequest
// (tenantId, passphrase, strategy, threshold, shares).
func newTenantBarrierInitCmd() *cobra.Command {
	var (
		tenantID   string
		passphrase string
		strategy   string
		threshold  int
		shares     int
	)

	cmd := &cobra.Command{
		Use:   "barrier-init",
		Short: "Initialize the per-tenant cryptographic barrier",
		Long: "Initialize the per-tenant cryptographic barrier with the given sealing strategy.\n" +
			"--passphrase is required for the passphrase strategy (the default when --strategy\n" +
			"is omitted); --threshold/--shares configure a Shamir M-of-N quorum when\n" +
			"--strategy=shamir. Share material is never returned here -- Security Officers\n" +
			"retrieve their individual shares via the officer claim flow.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.TenantBarrierInitRequest{
				TenantID:   tenantID,
				Passphrase: secretBytes(passphrase),
				Threshold:  threshold,
				Shares:     shares,
				Strategy:   strategy,
			}
			defer secure.Wipe(req.Passphrase)

			resp, err := client.Tenants().TenantBarrierInit(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printTenantBarrierInitResponse(resp)
		},
	}

	cmd.Flags().StringVar(&tenantID, "id", "", "Tenant ID")
	_ = cmd.MarkFlagRequired("id")
	cmd.Flags().StringVar(&passphrase, "passphrase", "",
		"Passphrase used to seal the barrier (required for the passphrase strategy; ignored by none/hardware strategies)")
	cmd.Flags().StringVar(&strategy, "strategy", "",
		"Seal strategy: none | passphrase | shamir | a registered hardware id (empty = passphrase)")
	cmd.Flags().IntVar(&threshold, "threshold", 0, "Minimum shares required to unseal (shamir strategy only; 0 = default)")
	cmd.Flags().IntVar(&shares, "shares", 0, "Total number of shares to generate (shamir strategy only; 0 = default)")

	return cmd
}

// printTenantBarrierInitResponse renders the result of `tenant barrier-init`.
// Share bytes are never returned by the server; for the Shamir strategy the
// response carries threshold/total_shares/share_count so the operator can
// confirm quorum enrollment succeeded, and this prints them in the current
// output format. Every other strategy (none, passphrase, hardware) returns
// 204 No Content on the wire -- the SDK synthesizes a response with
// ShareCount==0 in that case -- so this simply prints OK, matching the prior
// (pre-parity-fix) CLI behavior and the REST/GUI contract
// (pkg/api/rest/handlers_tenants.go handleTenantBarrierInit).
func printTenantBarrierInitResponse(resp *transport.TenantBarrierInitResponse) error {
	if resp == nil || resp.ShareCount == 0 {
		printOK()
		return nil
	}

	switch outputFormat {
	case "json":
		return printJSON(resp)
	case "yaml":
		return printYAML(resp)
	default:
		_, _ = fmt.Fprintf(outputWriter, "TenantID:    %s\n", resp.TenantID)
		_, _ = fmt.Fprintf(outputWriter, "Strategy:    %s\n", resp.Strategy)
		_, _ = fmt.Fprintf(outputWriter, "Threshold:   %d\n", resp.Threshold)
		_, _ = fmt.Fprintf(outputWriter, "TotalShares: %d\n", resp.TotalShares)
		_, _ = fmt.Fprintf(outputWriter, "ShareCount:  %d\n", resp.ShareCount)
		return nil
	}
}

// newTenantBarrierUnsealCmd creates the "tenant barrier-unseal" subcommand.
// The --share flag accepts a base64-encoded Shamir share; --key accepts a
// base64-encoded raw master key. Exactly one must be provided.
func newTenantBarrierUnsealCmd() *cobra.Command {
	var (
		tenantID  string
		shareFlag string
		keyFlag   string
	)

	cmd := &cobra.Command{
		Use:   "barrier-unseal",
		Short: "Unseal the per-tenant cryptographic barrier",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if shareFlag == "" && keyFlag == "" {
				return &TenantUnsealInputError{}
			}

			req := &transport.TenantBarrierUnsealRequest{
				TenantID: tenantID,
			}

			if shareFlag != "" {
				decoded, err := base64.StdEncoding.DecodeString(shareFlag)
				if err != nil {
					return &Base64DecodeError{Field: "share", Err: err}
				}
				req.Share = decoded
			} else {
				decoded, err := base64.StdEncoding.DecodeString(keyFlag)
				if err != nil {
					return &Base64DecodeError{Field: "key", Err: err}
				}
				req.Key = decoded
			}

			if err := client.Tenants().TenantBarrierUnseal(cmd.Context(), req); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}

	cmd.Flags().StringVar(&tenantID, "id", "", "Tenant ID")
	_ = cmd.MarkFlagRequired("id")
	cmd.Flags().StringVar(&shareFlag, "share", "", "Base64-encoded Shamir share")
	cmd.Flags().StringVar(&keyFlag, "key", "", "Base64-encoded master key")

	return cmd
}

// newTenantBarrierSealCmd creates the "tenant barrier-seal" subcommand. It
// is the inverse of "tenant barrier-unseal": sealing requires no
// credentials and returns the per-tenant barrier to the sealed state,
// refusing further reads or writes until unsealed again
// (pkg/tenant/service.go TenantService.TenantBarrierSeal). This mirrors the
// system-level "barrier seal" command and the SDK's full transport parity
// for client.Tenants().TenantBarrierSeal.
func newTenantBarrierSealCmd() *cobra.Command {
	var tenantID string

	cmd := &cobra.Command{
		Use:   "barrier-seal",
		Short: "Seal the per-tenant cryptographic barrier",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.TenantBarrierSealRequest{
				TenantID: tenantID,
			}

			if err := client.Tenants().TenantBarrierSeal(cmd.Context(), req); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}

	cmd.Flags().StringVar(&tenantID, "id", "", "Tenant ID")
	_ = cmd.MarkFlagRequired("id")

	return cmd
}

// newTenantBarrierStatusCmd creates the "tenant barrier-status" subcommand.
// It reports the per-tenant barrier's sealed/initialized state and active
// strategy, completing CLI parity with the REST, gRPC, QUIC, and MCP
// transports' TenantBarrierStatus surface. Like barrier-init and
// barrier-unseal it remains reachable by the tenant's own mTLS identity
// during the sealed window (the server-side IdentityGated bootstrap rules),
// which is what lets a self-managed tenant drive its own unseal flow.
func newTenantBarrierStatusCmd() *cobra.Command {
	var tenantID string

	cmd := &cobra.Command{
		Use:   "barrier-status",
		Short: "Report the per-tenant cryptographic barrier status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			status, err := client.Tenants().TenantBarrierStatus(cmd.Context(), &transport.TenantBarrierStatusRequest{
				TenantID: tenantID,
			})
			if err != nil {
				return err
			}
			return printTenantBarrierStatus(status)
		},
	}

	cmd.Flags().StringVar(&tenantID, "id", "", "Tenant ID")
	_ = cmd.MarkFlagRequired("id")

	return cmd
}

// printTenantBarrierStatus prints a TenantBarrierStatusResponse in the
// current output format.
func printTenantBarrierStatus(status *transport.TenantBarrierStatusResponse) error {
	switch outputFormat {
	case "json":
		return printJSON(status)
	case "yaml":
		return printYAML(status)
	default:
		_, _ = fmt.Fprintf(outputWriter, "TenantID:    %s\n", status.TenantID)
		_, _ = fmt.Fprintf(outputWriter, "Sealed:      %t\n", status.Sealed)
		_, _ = fmt.Fprintf(outputWriter, "Initialized: %t\n", status.Initialized)
		if status.Strategy != "" {
			_, _ = fmt.Fprintf(outputWriter, "Strategy:    %s\n", status.Strategy)
		}
		if status.ShamirThreshold > 0 {
			_, _ = fmt.Fprintf(outputWriter, "Threshold:   %d of %d\n", status.ShamirThreshold, status.ShamirTotal)
		}
		return nil
	}
}

// printTenant prints a single TenantInfo in the current output format.
func printTenant(t transport.TenantInfo) error {
	switch outputFormat {
	case "json":
		return printJSON(t)
	case "yaml":
		return printYAML(t)
	default:
		_, _ = fmt.Fprintf(outputWriter, "ID:         %s\n", t.ID)
		_, _ = fmt.Fprintf(outputWriter, "Name:       %s\n", t.Name)
		if t.IsolationType != "" {
			_, _ = fmt.Fprintf(outputWriter, "Isolation:  %s\n", t.IsolationType)
		}
		if t.DataRoot != "" {
			_, _ = fmt.Fprintf(outputWriter, "DataRoot:   %s\n", t.DataRoot)
		}
		_, _ = fmt.Fprintf(outputWriter, "CreatedAt:  %s\n", t.CreatedAt.String())
		_, _ = fmt.Fprintf(outputWriter, "UpdatedAt:  %s\n", t.UpdatedAt.String())
		return nil
	}
}

// tenantRow converts a TenantInfo into a table row for tabular output.
func tenantRow(t transport.TenantInfo) []string {
	return []string{
		t.ID,
		t.Name,
		t.CreatedAt.String(),
		t.UpdatedAt.String(),
	}
}
