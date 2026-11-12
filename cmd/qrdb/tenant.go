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
	"context"
	"crypto"
	"encoding/base64"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jeremyhahn/go-qrdb/sdk/go/secure"
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
		newTenantBarrierInitBeginCmd(),
		newTenantBarrierInitCompleteCmd(),
		newTenantBarrierUnsealCmd(),
		newTenantBarrierSealCmd(),
		newTenantBarrierStatusCmd(),
		newTenantTrustAnchorsCmd(),
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
				headers := []string{"ID", "NAME", "STATUS", "DESCRIPTION", "METADATA", "CREATED_AT", "UPDATED_AT"}
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
		volumePool        string
		adminUsername     string
		adminCSRFile      string
		adminCertFile     string
		adminChainFile    string
		trustAnchorsFile  string
		soUsername        string
		soCSRFile         string
		soCertFile        string
		soChainFile       string
		soPin             string
		outDir            string
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new tenant with its first admin",
		Long: `Create a tenant together with its first admin's login certificate. A tenant
is created with its first admin, or not at all.

The first admin keeps its own key: pass either the admin's certificate signing
request (--admin-csr; the admin makes one with "qrdb ca csr"), which the
tenant's own CA signs, or the admin's own certificate (--admin-cert), which
must chain to one of the tenant's trust anchors (--trust-anchors). The admin's
certificate is printed, or written to --out as admin-cert.pem.

A tenant may be created with its security officer as well: --so-username,
--so-pin and exactly one of --so-csr and --so-cert, given together or not at
all. The SO is enrolled with the tenant's so role, and its certificate is
printed beside the admin's, or written to --out as so-cert.pem. Under strict
separation of duties (auth.separation_of_duties, forced on by FIPS 140-3) the
SO must differ from the admin by username and by key; otherwise the admin may
be named the SO, by its username and key together.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := &transport.CreateTenantRequest{
				ID:                id,
				Name:              name,
				IsolationType:     transport.TenantIsolationType(isolationType),
				BarrierAutoUnseal: barrierAutoUnseal,
				VolumePool:        volumePool,
				AdminUsername:     adminUsername,
				SOUsername:        soUsername,
				SOPin:             soPin,
			}
			// A list, not a map keyed by path: one file may serve two
			// flags, as when the admin is named the SO with its own CSR.
			files := []struct {
				path string
				dest *string
			}{
				{adminCSRFile, &req.AdminCSRPEM},
				{adminCertFile, &req.AdminCertPEM},
				{adminChainFile, &req.AdminChainPEM},
				{trustAnchorsFile, &req.TrustAnchorsPEM},
				{soCSRFile, &req.SOCSRPEM},
				{soCertFile, &req.SOCertPEM},
				{soChainFile, &req.SOChainPEM},
			}
			for _, f := range files {
				path, dest := f.path, f.dest
				if path == "" {
					continue
				}
				data, err := os.ReadFile(path)
				if err != nil {
					return &FileReadError{Path: path, Err: err}
				}
				*dest = string(data)
			}

			resp, err := client.Tenants().CreateTenant(cmd.Context(), req)
			if err != nil {
				return err
			}
			if outDir != "" {
				certs := map[string][]byte{"admin-cert.pem": []byte(resp.Admin.CertPEM)}
				if resp.SO != nil {
					certs["so-cert.pem"] = []byte(resp.SO.CertPEM)
				}
				if err := writePEMFiles(outDir, certs); err != nil {
					return err
				}
			}
			return printCreatedTenant(resp)
		},
	}

	cmd.Flags().StringVar(&id, "id", "", "Tenant ID (must be unique)")
	_ = cmd.MarkFlagRequired("id")
	cmd.Flags().StringVar(&name, "name", "", "Human-readable tenant name")
	_ = cmd.MarkFlagRequired("name")
	cmd.Flags().StringVar(&isolationType, "isolation-type", "", "Isolation type (prefix|barrier|volume|raft); empty = prefix")
	cmd.Flags().BoolVar(&barrierAutoUnseal, "barrier-auto-unseal", false,
		"For a dedicated-isolation tenant (barrier/volume/raft), use system-managed auto-unseal instead of the secure self-managed default (entitlement-gated)")
	cmd.Flags().StringVar(&volumePool, "pool", "",
		"For a volume/raft isolation tenant, place it on this volume pool instead of its default; the tenant must be entitled to the pool")
	cmd.Flags().StringVar(&adminUsername, "admin-username", "", "The tenant's first admin")
	_ = cmd.MarkFlagRequired("admin-username")
	cmd.Flags().StringVar(&adminCSRFile, "admin-csr", "", "The first admin's PEM certificate signing request")
	cmd.Flags().StringVar(&adminCertFile, "admin-cert", "", "The first admin's own PEM certificate (must chain to --trust-anchors)")
	cmd.Flags().StringVar(&adminChainFile, "admin-chain", "", "Optional PEM intermediates for --admin-cert")
	cmd.Flags().StringVar(&trustAnchorsFile, "trust-anchors", "", "A PEM bundle of CA certificates to register as the tenant's trust anchors")
	cmd.Flags().StringVar(&soUsername, "so-username", "", "The tenant's security officer (optional; enrolled with the so role alone)")
	cmd.Flags().StringVar(&soCSRFile, "so-csr", "", "The SO's PEM certificate signing request")
	cmd.Flags().StringVar(&soCertFile, "so-cert", "", "The SO's own PEM certificate (must chain to --trust-anchors)")
	cmd.Flags().StringVar(&soChainFile, "so-chain", "", "Optional PEM intermediates for --so-cert")
	cmd.Flags().StringVar(&soPin, "so-pin", "", "The SO's PIN, the second factor the SO gate asks for")
	cmd.Flags().StringVar(&outDir, "out", "", "Also write the admin's certificate to this directory as admin-cert.pem, and the SO's as so-cert.pem")
	cmd.MarkFlagsMutuallyExclusive("admin-csr", "admin-cert")
	cmd.MarkFlagsOneRequired("admin-csr", "admin-cert")
	cmd.MarkFlagsMutuallyExclusive("so-csr", "so-cert")

	return cmd
}

// printCreatedTenant prints a created tenant, its first admin and its SO.
func printCreatedTenant(resp *transport.CreateTenantResponse) error {
	switch outputFormat {
	case "json":
		return printJSON(resp)
	case "yaml":
		return printYAML(resp)
	default:
		if err := printTenant(resp.Tenant); err != nil {
			return err
		}
		printTenantAccount("Admin", "admin", resp.Admin)
		if resp.SO != nil {
			_, _ = fmt.Fprintln(outputWriter)
			printTenantAccount("SO", "SO", *resp.SO)
		}
		return nil
	}
}

// printTenantAccount prints an account tenant create enrolled under label.
func printTenantAccount(label, noun string, a transport.TenantAdminCredential) {
	_, _ = fmt.Fprintf(outputWriter, "%-11s %s\n", label+":", a.Username)
	_, _ = fmt.Fprintf(outputWriter, "Thumbprint: %s\n", a.Thumbprint)
	if a.Serial != "" {
		_, _ = fmt.Fprintf(outputWriter, "Serial:     %s\n", a.Serial)
	}
	if a.AwaitsBarrierInit {
		_, _ = fmt.Fprintf(outputWriter, "The %s's account is created when the admin runs \"tenant barrier-init\" with the admin's certificate.\n", noun)
	}
	_, _ = fmt.Fprintf(outputWriter, "\n--- %s certificate ---\n%s\n", label, a.CertPEM)
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
		description   string
		metadata      []string
		clearMetadata bool
		status        string
	)

	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a tenant's mutable fields",
		Long: `Update a tenant's mutable fields. A flag that is not given leaves its
field unchanged. --description "" clears the description; --metadata
replaces the whole metadata map with the given key=value entries, and
--clear-metadata removes every entry. --status moves the tenant to active,
suspended or disabled (a disabled tenant cannot move to suspended); it needs
tenants:set_status, which only a system-tenant Admin holds, and while a
tenant is not active every principal of it is refused.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &transport.UpdateTenantRequest{
				TenantID:      args[0],
				Name:          name,
				IsolationType: transport.TenantIsolationType(isolationType),
			}
			if cmd.Flags().Changed("description") {
				req.Description = &description
			}
			md, err := tenantMetadataFlag(metadata, clearMetadata)
			if err != nil {
				return err
			}
			req.Metadata = md
			if cmd.Flags().Changed("status") {
				s := transport.TenantStatus(status)
				req.Status = &s
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
	cmd.Flags().StringVar(&description, "description", "", "New tenant description (\"\" clears it)")
	cmd.Flags().StringArrayVar(&metadata, "metadata", nil, "Metadata entry key=value; repeatable, replaces the whole map")
	cmd.Flags().BoolVar(&clearMetadata, "clear-metadata", false, "Remove every metadata entry")
	cmd.MarkFlagsMutuallyExclusive("metadata", "clear-metadata")
	cmd.Flags().StringVar(&status, "status", "", "New tenant status (active|suspended|disabled)")

	return cmd
}

// tenantMetadataFlag turns the --metadata and --clear-metadata flags into the
// UpdateTenantRequest.Metadata patch: nil leaves the map unchanged, an empty
// map clears it, and otherwise the entries replace it.
func tenantMetadataFlag(entries []string, clear bool) (map[string]string, error) {
	if clear {
		return map[string]string{}, nil
	}
	if len(entries) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		k, v, ok := strings.Cut(e, "=")
		if !ok || k == "" {
			return nil, &InvalidFlagValueError{Flag: "metadata", Value: e, Want: "key=value"}
		}
		out[k] = v
	}
	return out, nil
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
		allowedVolumePools        []string
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
					AllowedVolumePools:        allowedVolumePools,
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
	cmd.Flags().StringSliceVar(&allowedVolumePools, "allowed-volume-pools", nil, "Comma-separated named volume pools the tenant may be placed on; the first is its default. Empty = no pool, fail-closed.")

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
		officers   []string
		custodians []string
		policy     string
	)

	cmd := &cobra.Command{
		Use:   "barrier-init",
		Short: "Initialize the per-tenant cryptographic barrier",
		Long: "Initialize the per-tenant cryptographic barrier with the given sealing strategy.\n" +
			"--passphrase is required for the passphrase strategy (the default when --strategy\n" +
			"is omitted); --threshold/--shares configure a Shamir M-of-N quorum when\n" +
			"--strategy=shamir. Share material is never returned here -- Security Officers\n" +
			"retrieve their individual shares via the officer claim flow.\n\n" +
			"A dedicated tenant's own IAM is encrypted behind the very barrier its share\n" +
			"holders exist to help initialize, so standalone `tenant barrier-officer\n" +
			"register`/`list` can never reach it beforehand. --officers/--custodians\n" +
			"(--strategy=shamir only, repeatable, username=path/to/csr.pem) name the\n" +
			"roster inline: every officer is validated as one batch and registered\n" +
			"atomically with initialization, rolled back together on any failure. Their\n" +
			"combined count must equal --shares. Once initialized, later holder changes\n" +
			"go through ordinary RBAC via `tenant barrier-officer register`/`list`.\n\n" +
			"On a network transport initialization is two calls on one connection: the\n" +
			"node issues a single-use nonce, and the key in --key-file signs it together\n" +
			"with the connection's TLS exporter, proving the caller holds the key and not\n" +
			"merely a certificate. The key never leaves this process.\n\n" +
			"A barrier served by several nodes holds its own key on each. A passphrase init\n" +
			"reads the nodes from --server's barrier status and is sent to every one of them,\n" +
			"over --server's transport (rest, grpc, quic or mcp) at the endpoint each node\n" +
			"publishes, with one result printed per node. Under any other strategy the init\n" +
			"goes to --server's node and the others initialize their own keys from it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			officerRoster, err := tenantBarrierRoster(officers, custodians, shares)
			if err != nil {
				return err
			}

			keyFile, _ := cmd.Root().PersistentFlags().GetString("key-file")
			signer, err := initProofSigner(keyFile)
			if err != nil {
				return err
			}

			req := &transport.TenantBarrierInitRequest{
				TenantID:   tenantID,
				Passphrase: secretBytes(passphrase),
				Threshold:  threshold,
				Shares:     shares,
				Strategy:   strategy,
				Officers:   officerRoster,
				Policy:     policy,
				Signer:     signer,
			}
			defer secure.Wipe(req.Passphrase)

			if initFansOut(strategy) {
				status, err := client.Tenants().TenantBarrierStatus(cmd.Context(), &transport.TenantBarrierStatusRequest{TenantID: tenantID})
				if err != nil {
					return err
				}
				// A barrier served by one node -- a single-member Raft
				// deployment's -- is that node's, reached on --server's
				// client over any transport, unix and embedded included.
				// An unreadable serving set is not a standalone barrier.
				if status.NodesUnreadable {
					return &TenantServingNodesUnknownError{Command: "barrier-init", TenantID: tenantID}
				}
				if len(status.Nodes) > 1 {
					return runOnServingNodes(cmd, status.Nodes, "the barrier init", func(ctx context.Context, tenants transport.TenantClient) error {
						_, err := tenants.TenantBarrierInit(ctx, req)
						return err
					})
				}
			}

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
	cmd.Flags().StringArrayVar(&officers, "officers", nil,
		"Security Officer share holder: username=path/to/csr.pem (repeatable; shamir strategy only). Combined with --custodians, must equal --shares.")
	cmd.Flags().StringArrayVar(&custodians, "custodians", nil,
		"Key custodian share holder: username=path/to/csr.pem (repeatable; shamir strategy only). Combined with --officers, must equal --shares.")
	cmd.Flags().StringVar(&policy, "policy", "",
		"Factor policy for --officers/--custodians: designated (default) or any-enrolled")

	return cmd
}

// initFansOut reports whether an init under strategy is sent to every node
// serving the barrier. Each node holds its own key, and a passphrase is the
// operator's alone to give each of them; under any other strategy one node's
// claimed init is enough, the others initializing their own keys from it (a
// Shamir key travels to them sealed).
func initFansOut(strategy string) bool {
	return strategy == "" || strategy == "passphrase"
}

// initProofSigner loads keyFile, the key of the client certificate the
// connection authenticates with, as the signer of the tenant barrier init
// proof of possession. An empty keyFile leaves the signer nil: a node that
// asks no proof (the embedded transport, an unauthenticated deployment)
// needs none, and one that does refuses with a typed
// transport.InitSignerRequiredError.
func initProofSigner(keyFile string) (crypto.Signer, error) {
	if keyFile == "" {
		return nil, nil
	}
	return readSigner(keyFile)
}

// tenantBarrierRoster is a Shamir init's inline roster from the --officers and
// --custodians flags, whose combined count must equal shares.
func tenantBarrierRoster(officers, custodians []string, shares int) ([]transport.OfficerRegisterRequest, error) {
	roster, err := parseTenantOfficerFlags("--officers", transport.RoleSecurityOfficer, officers)
	if err != nil {
		return nil, err
	}
	custodianRoster, err := parseTenantOfficerFlags("--custodians", transport.RoleCustodian, custodians)
	if err != nil {
		return nil, err
	}
	roster = append(roster, custodianRoster...)
	if len(roster) > 0 && len(roster) != shares {
		return nil, &InvalidArgError{
			Arg:    "--shares",
			Reason: "must equal the number of --officers plus --custodians (" + strconv.Itoa(len(roster)) + ")",
		}
	}
	return roster, nil
}

// parseTenantOfficerFlags parses repeatable holder flag values
// ("username=path/to/csr.pem") of one role into the inline roster's wire
// shape, reading each named CSR file from disk. flag names the flag for
// error messages.
func parseTenantOfficerFlags(flag, role string, raw []string) ([]transport.OfficerRegisterRequest, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	officers := make([]transport.OfficerRegisterRequest, 0, len(raw))
	for _, entry := range raw {
		username, csrPath, ok := strings.Cut(entry, "=")
		if !ok || username == "" || csrPath == "" {
			return nil, &InvalidArgError{
				Arg:    flag,
				Reason: "must be in the form username=path/to/csr.pem, got " + entry,
			}
		}
		csrPEM, err := os.ReadFile(csrPath)
		if err != nil {
			return nil, &FileReadError{Path: csrPath, Err: err}
		}
		officers = append(officers, transport.OfficerRegisterRequest{
			Username: username,
			Role:     role,
			CSRPEM:   string(csrPEM),
		})
	}
	return officers, nil
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
		for _, h := range resp.Holders {
			_, _ = fmt.Fprintf(outputWriter, "Holder:      %s (%s)\n", h.Username, h.Role)
		}
		if resp.AdminPending {
			_, _ = fmt.Fprintln(outputWriter, "AdminPending: the first admin completes once holders unseal enough nodes for the tenant's home group to elect a leader")
		}
		return nil
	}
}

// newTenantBarrierUnsealCmd creates the "tenant barrier-unseal" subcommand:
// the credential unseal, with --key the base64 credential the barrier was
// initialized with. A Shamir tenant barrier is unsealed by its share holders
// through "tenant barrier-quorum submit".
func newTenantBarrierUnsealCmd() *cobra.Command {
	var (
		tenantID string
		keyFlag  string
	)

	cmd := &cobra.Command{
		Use:   "barrier-unseal",
		Short: "Unseal the per-tenant cryptographic barrier",
		Long: "Unseal the per-tenant cryptographic barrier with the credential in --key.\n\n" +
			"A barrier served by several nodes holds its own key on each. The unseal reads\n" +
			"the nodes from --server's barrier status and is sent to every one of them,\n" +
			"over --server's transport (rest, grpc, quic or mcp) at the endpoint each node\n" +
			"publishes, with one result printed per node. A barrier served by one node is\n" +
			"unsealed on --server's node.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if keyFlag == "" {
				return &TenantUnsealInputError{}
			}
			decoded, err := base64.StdEncoding.DecodeString(keyFlag)
			if err != nil {
				return &Base64DecodeError{Field: "key", Err: err}
			}
			req := &transport.TenantBarrierUnsealRequest{TenantID: tenantID, Key: decoded}
			defer secure.Wipe(req.Key)

			status, err := client.Tenants().TenantBarrierStatus(cmd.Context(), &transport.TenantBarrierStatusRequest{TenantID: tenantID})
			if err != nil {
				return err
			}
			// Every node serving the barrier holds its own key sealed
			// under the same credential, so each one is unsealed. A
			// barrier served by one node is unsealed on --server's client
			// over any transport, unix and embedded included.
			// An unreadable serving set is not a standalone barrier:
			// unsealing --server's node alone could leave the others
			// sealed while reporting success.
			if status.NodesUnreadable {
				return &TenantServingNodesUnknownError{Command: "barrier-unseal", TenantID: tenantID}
			}
			if len(status.Nodes) > 1 {
				return runOnServingNodes(cmd, status.Nodes, "the barrier unseal", barrierUnseal(req))
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
	cmd.Flags().StringVar(&keyFlag, "key", "", "Base64-encoded barrier credential")

	return cmd
}

// barrierUnseal returns the unseal sent to each node serving the barrier.
func barrierUnseal(req *transport.TenantBarrierUnsealRequest) func(context.Context, transport.TenantClient) error {
	return func(ctx context.Context, tenants transport.TenantClient) error {
		return tenants.TenantBarrierUnseal(ctx, req)
	}
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
		if status.KeyFingerprint != "" {
			_, _ = fmt.Fprintf(outputWriter, "Fingerprint: %s\n", status.KeyFingerprint)
		}
		for _, n := range status.Nodes {
			_, _ = fmt.Fprintf(outputWriter, "Node:        %s\n", n.Node)
		}
		printClusterSeal(status.ClusterSeal)
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
		if t.Status != "" {
			_, _ = fmt.Fprintf(outputWriter, "Status:     %s\n", t.Status)
		}
		if t.VolumePool != "" {
			_, _ = fmt.Fprintf(outputWriter, "Pool:       %s\n", t.VolumePool)
		}
		if t.Description != "" {
			_, _ = fmt.Fprintf(outputWriter, "Description: %s\n", t.Description)
		}
		if len(t.Metadata) > 0 {
			_, _ = fmt.Fprintf(outputWriter, "Metadata:   %s\n", formatTenantMetadata(t.Metadata))
		}
		_, _ = fmt.Fprintf(outputWriter, "CreatedAt:  %s\n", t.CreatedAt.String())
		_, _ = fmt.Fprintf(outputWriter, "UpdatedAt:  %s\n", t.UpdatedAt.String())
		if t.Refusal != nil {
			_, _ = fmt.Fprintf(outputWriter, "Refused:    %s: %s\n", t.Refusal.Code, t.Refusal.Message)
		}
		return nil
	}
}

// tenantRow converts a TenantInfo into a table row for tabular output.
func tenantRow(t transport.TenantInfo) []string {
	return []string{
		t.ID,
		t.Name,
		string(t.Status),
		t.Description,
		formatTenantMetadata(t.Metadata),
		t.CreatedAt.String(),
		t.UpdatedAt.String(),
	}
}

// formatTenantMetadata renders tenant metadata as comma-separated key=value
// entries in key order, so table output is stable.
func formatTenantMetadata(md map[string]string) string {
	entries := make([]string, 0, len(md))
	for _, k := range slices.Sorted(maps.Keys(md)) {
		entries = append(entries, k+"="+md[k])
	}
	return strings.Join(entries, ",")
}
