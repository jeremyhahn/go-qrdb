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

// newDatabaseCmd creates the "database" parent command with subcommands
// for database management operations.
func newDatabaseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "database",
		Aliases: []string{"db"},
		Short:   "Database management operations",
		Long:    "Create, inspect, list, and drop databases.",
	}

	cmd.AddCommand(
		newDatabaseCreateCmd(),
		newDatabaseListCmd(),
		newDatabaseInfoCmd(),
		newDatabaseDropCmd(),
		newDatabaseUpdateProtocolCmd(),
		newDatabaseUpdateEtcdCmd(),
		newDatabaseUpdateRaftCmd(),
		newDatabaseScaleCmd(),
		newDatabaseUpdateBarrierCmd(),
		newDatabaseUpdateConnectionCmd(),
		newDatabaseUpdateSQLCmd(),
		newDatabaseUpdateCompactionCmd(),
		newDatabaseSealCmd(),
		newDatabaseUnsealCmd(),
		newDatabaseBarrierStatusCmd(),
	)
	cmd.AddCommand(newDatabaseBarrierHolderCmds()...)

	return cmd
}

// newDatabaseCreateCmd creates the "database create" subcommand.
func newDatabaseCreateCmd() *cobra.Command {
	var (
		engine            string
		backend           string
		dataDir           string
		replicas          int
		clusterMode       string
		etcd              bool
		etcdListenAddr    string
		etcdAdvertiseURLs []string

		barrierEnabled         bool
		barrierStrategy        string
		barrierPassphrase      string
		barrierShamirThreshold int
		barrierShamirTotal     int

		sqlConcurrency string
	)

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new database",
		Long: "Create a new database with the specified engine and storage backend.\n\n" +
			"A per-database encryption barrier (--barrier-enabled) can only be enabled " +
			"on a database that has a dedicated store of its own: an explicit --data-dir, " +
			"a dedicated-raft (--backend raft) database, or a database created for an " +
			"IsolationVolume/IsolationRaft tenant. Enabling a barrier on a database that " +
			"shares the default store is rejected.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			req := transport.CreateDatabaseRequest{
				Name:     name,
				Engine:   engine,
				Backend:  backend,
				DataDir:  dataDir,
				Replicas: replicas,
			}

			// Raft topology (cluster mode) is meaningful only for a
			// dedicated-raft (--backend raft) database; only send it when the
			// operator explicitly requested one so a plain create is unaffected.
			if clusterMode != "" {
				req.Raft = &transport.DatabaseRaftConfig{ClusterMode: clusterMode}
			}

			if etcd {
				req.Protocol.Etcd = &transport.EtcdProtocolConfig{
					Enabled:             true,
					ListenAddr:          etcdListenAddr,
					AdvertiseClientURLs: etcdAdvertiseURLs,
				}
			}

			if barrierEnabled {
				req.Barrier = &transport.DatabaseBarrierConfig{
					Enabled:        true,
					Mode:           barrierStrategy,
					SharesRequired: barrierShamirThreshold,
					SharesTotal:    barrierShamirTotal,
					Secret:         barrierPassphrase,
				}
			}

			if sqlConcurrency != "" {
				req.SQL = &transport.DatabaseSQLConfig{Concurrency: sqlConcurrency}
			}

			info, err := client.Database().CreateDatabase(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printDatabaseInfo(info)
		},
	}

	cmd.Flags().StringVar(&engine, "engine", "kv", "Database engine type (kv, timeseries)")
	cmd.Flags().StringVar(&backend, "backend", "pebble", "Storage backend (pebble, memory, file, raft)")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "Dedicated data directory for this database (required for a real per-database encryption barrier)")
	cmd.Flags().IntVar(&replicas, "replicas", 0, "Replication factor for a dedicated-raft database (raft backend only; defaults to 1)")
	cmd.Flags().StringVar(&clusterMode, "cluster-mode", "", "Raft cluster topology for a dedicated-raft database (local, distributed; raft backend only, defaults to local)")
	cmd.Flags().BoolVar(&etcd, "etcd", false, "Enable etcd wire protocol compatibility")
	cmd.Flags().StringVar(&etcdListenAddr, "etcd-listen-addr", "localhost:2379", "Etcd listen address")
	cmd.Flags().StringSliceVar(&etcdAdvertiseURLs, "etcd-advertise-urls", nil, "Etcd advertise client URLs")

	cmd.Flags().BoolVar(&barrierEnabled, "barrier-enabled", false, "Enable a per-database encryption barrier (requires a dedicated store)")
	cmd.Flags().StringVar(&barrierStrategy, "barrier-strategy", "passphrase", "Barrier sealing strategy (passphrase, shamir)")
	cmd.Flags().StringVar(&barrierPassphrase, "barrier-passphrase", "", "Passphrase used to initialize the barrier (barrier-strategy=passphrase)")
	cmd.Flags().IntVar(&barrierShamirThreshold, "barrier-shamir-threshold", 0, "Shamir quorum threshold (barrier-strategy=shamir)")
	cmd.Flags().IntVar(&barrierShamirTotal, "barrier-shamir-total", 0, "Shamir total share count (barrier-strategy=shamir)")

	cmd.Flags().StringVar(&sqlConcurrency, "sql-concurrency", "", "The database's SQL concurrency mode (optimistic, pessimistic; defaults to optimistic)")

	return cmd
}

// newDatabaseUpdateSQLCmd creates the "database update-sql" subcommand.
func newDatabaseUpdateSQLCmd() *cobra.Command {
	var concurrency string

	cmd := &cobra.Command{
		Use:   "update-sql <name>",
		Short: "Update a database's SQL settings",
		Long: "Update a database's SQL concurrency mode. In pessimistic mode SQL DML takes row " +
			"locks and concurrent writers of a row wait instead of failing at COMMIT; " +
			"SELECT ... FOR UPDATE and FOR SHARE lock in either mode. New transactions " +
			"pick the change up.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := client.Database().UpdateDatabaseSQL(cmd.Context(), transport.UpdateDatabaseSQLRequest{
				Name: args[0],
				SQL:  &transport.DatabaseSQLConfig{Concurrency: concurrency},
			})
			if err != nil {
				return err
			}
			return printDatabaseInfo(info)
		},
	}

	cmd.Flags().StringVar(&concurrency, "concurrency", "", "SQL concurrency mode (optimistic, pessimistic)")
	_ = cmd.MarkFlagRequired("concurrency")

	return cmd
}

// newDatabaseUpdateCompactionCmd creates the "database update-compaction"
// subcommand.
func newDatabaseUpdateCompactionCmd() *cobra.Command {
	var flags compactionPolicyFlags

	cmd := &cobra.Command{
		Use:   "update-compaction <name>",
		Short: "Set a database's scheduled-compaction policy",
		Long: "Set the schedule on which the node compacts this database's MVCC history: off, " +
			"revision (keep the last --retain-revisions revisions) or period (keep the history " +
			"newer than --retain-period). It is off unless set, requires mvcc:compact, and only " +
			"the tenant that owns the database may set it.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := client.Database().UpdateDatabaseCompaction(cmd.Context(), transport.UpdateDatabaseCompactionRequest{
				Name:       args[0],
				Compaction: flags.policy(),
			})
			if err != nil {
				return err
			}
			return printDatabaseInfo(info)
		},
	}

	flags.bind(cmd)

	return cmd
}

// compactionPolicyFlags are the flags every command that sets a
// scheduled-compaction policy takes: --mode (required), --retain-revisions
// and --retain-period.
type compactionPolicyFlags struct {
	mode            string
	retainRevisions int64
	retainPeriod    time.Duration
}

// bind registers the flags on cmd.
func (f *compactionPolicyFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.mode, "mode", "", "Compaction mode (off, revision, period)")
	cmd.Flags().Int64Var(&f.retainRevisions, "retain-revisions", 0, "Revision mode: keep the last N revisions")
	cmd.Flags().DurationVar(&f.retainPeriod, "retain-period", 0, "Period mode: keep the history newer than this (at least 1s)")
	_ = cmd.MarkFlagRequired("mode")
}

// policy returns the policy the flags describe; the server validates it.
func (f *compactionPolicyFlags) policy() *transport.DatabaseCompactionConfig {
	p := &transport.DatabaseCompactionConfig{Mode: f.mode, RetainRevisions: f.retainRevisions}
	if f.retainPeriod != 0 {
		p.RetainPeriod = f.retainPeriod.String()
	}
	return p
}

// printCompactionPolicy writes a policy as an indented Compaction block in
// the table format; nothing for no policy.
func printCompactionPolicy(c *transport.DatabaseCompactionConfig) {
	if c == nil || c.Mode == "" {
		return
	}
	_, _ = fmt.Fprintf(outputWriter, "Compaction:\n")
	_, _ = fmt.Fprintf(outputWriter, "  Mode:     %s\n", c.Mode)
	if c.RetainRevisions != 0 {
		_, _ = fmt.Fprintf(outputWriter, "  Retain:   %d revisions\n", c.RetainRevisions)
	}
	if c.RetainPeriod != "" {
		_, _ = fmt.Fprintf(outputWriter, "  Retain:   %s\n", c.RetainPeriod)
	}
}

// newDatabaseListCmd creates the "database list" subcommand.
func newDatabaseListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all databases",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			databases, err := client.Database().ListDatabases(cmd.Context())
			if err != nil {
				return err
			}

			switch outputFormat {
			case "json":
				return printJSON(databases)
			case "yaml":
				return printYAML(databases)
			default:
				headers := []string{"NAME", "ENGINE", "BACKEND", "STATUS", "PROTOCOLS"}
				rows := make([][]string, len(databases))
				for i, db := range databases {
					rows[i] = []string{db.Name, db.Engine, db.Backend, db.Status, formatProtocols(db.Protocol)}
				}
				printTable(headers, rows)
				return nil
			}
		},
	}
}

// newDatabaseInfoCmd creates the "database info" subcommand.
func newDatabaseInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info <name>",
		Short: "Show database details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			info, err := client.Database().GetDatabase(cmd.Context(), name)
			if err != nil {
				return err
			}

			return printDatabaseInfo(info)
		},
	}
}

// newDatabaseDropCmd creates the "database drop" subcommand.
func newDatabaseDropCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "drop <name>",
		Short: "Drop a database",
		Long:  "Drop a database and delete all of its data. This operation is irreversible.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if err := client.Database().DropDatabase(cmd.Context(), name); err != nil {
				return err
			}

			printOK()
			return nil
		},
	}
}

// newDatabaseUpdateProtocolCmd creates the "database update-protocol" subcommand.
func newDatabaseUpdateProtocolCmd() *cobra.Command {
	var (
		mysql      bool
		postgresql bool
	)

	cmd := &cobra.Command{
		Use:   "update-protocol <name>",
		Short: "Update wire protocol configuration",
		Long:  "Update the wire protocol configuration for a database.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			req := transport.UpdateDatabaseProtocolRequest{
				Name:       name,
				MySQL:      mysql,
				PostgreSQL: postgresql,
			}
			info, err := client.Database().UpdateDatabaseProtocol(cmd.Context(), req)
			if err != nil {
				return err
			}
			return printDatabaseInfo(info)
		},
	}

	cmd.Flags().BoolVar(&mysql, "mysql", false, "Enable MySQL wire protocol")
	cmd.Flags().BoolVar(&postgresql, "postgresql", false, "Enable PostgreSQL wire protocol")

	return cmd
}

// newDatabaseUpdateEtcdCmd creates the "database update-etcd" subcommand.
func newDatabaseUpdateEtcdCmd() *cobra.Command {
	var (
		enabled       bool
		disabled      bool
		listenAddr    string
		advertiseURLs []string
	)

	cmd := &cobra.Command{
		Use:   "update-etcd <name>",
		Short: "Update etcd protocol configuration",
		Long:  "Update the etcd wire protocol configuration for a database.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			req := transport.UpdateDatabaseEtcdRequest{
				Name:                name,
				Enabled:             enabled,
				ListenAddr:          listenAddr,
				AdvertiseClientURLs: advertiseURLs,
			}

			info, err := client.Database().UpdateDatabaseEtcd(cmd.Context(), req)
			if err != nil {
				return err
			}

			return printDatabaseInfo(info)
		},
	}

	cmd.Flags().BoolVar(&enabled, "enabled", false, "Enable etcd wire protocol")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "Disable etcd wire protocol")
	cmd.Flags().StringVar(&listenAddr, "listen-addr", "", "Etcd listen address")
	cmd.Flags().StringSliceVar(&advertiseURLs, "advertise-urls", nil, "Etcd advertise client URLs")
	cmd.MarkFlagsMutuallyExclusive("enabled", "disabled")

	return cmd
}

// newDatabaseUpdateRaftCmd creates the "database update-raft" subcommand.
func newDatabaseUpdateRaftCmd() *cobra.Command {
	var (
		clusterMode string
		replicaID   uint64
		raftAddress string
	)

	cmd := &cobra.Command{
		Use:   "update-raft <name>",
		Short: "Update Raft consensus configuration",
		Long:  "Update the Raft consensus configuration for a database.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			req := transport.UpdateDatabaseRaftRequest{
				Name: name,
				Raft: &transport.DatabaseRaftConfig{
					ClusterMode: clusterMode,
					ReplicaID:   replicaID,
					RaftAddress: raftAddress,
				},
			}
			info, err := client.Database().UpdateDatabaseRaft(cmd.Context(), req)
			if err != nil {
				return err
			}
			return printDatabaseInfo(info)
		},
	}

	cmd.Flags().StringVar(&clusterMode, "cluster-mode", "", "Raft cluster mode (none, local, distributed)")
	cmd.Flags().Uint64Var(&replicaID, "replica-id", 0, "Raft replica ID")
	cmd.Flags().StringVar(&raftAddress, "raft-address", "", "Raft address (host:port)")

	return cmd
}

// newDatabaseScaleCmd creates the "database scale" subcommand.
func newDatabaseScaleCmd() *cobra.Command {
	var targetReplicas int

	cmd := &cobra.Command{
		Use:   "scale <name>",
		Short: "Change the replication factor of a dedicated-raft database",
		Long:  "Change the replication factor of an existing dedicated-raft database, adding or removing voting replicas without recreating the database.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			info, err := client.Database().ScaleDatabase(cmd.Context(), transport.ScaleDatabaseRequest{
				Name:           name,
				TargetReplicas: targetReplicas,
			})
			if err != nil {
				return err
			}
			return printDatabaseInfo(info)
		},
	}

	cmd.Flags().IntVar(&targetReplicas, "replicas", 0, "Target replication factor (must be >= 1)")
	_ = cmd.MarkFlagRequired("replicas")

	return cmd
}

// newDatabaseUpdateBarrierCmd creates the "database update-barrier" subcommand.
func newDatabaseUpdateBarrierCmd() *cobra.Command {
	var (
		enabled  bool
		disabled bool
	)

	cmd := &cobra.Command{
		Use:   "update-barrier <name>",
		Short: "Update encryption barrier configuration",
		Long:  "Update the encryption barrier configuration for a database.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			req := transport.UpdateDatabaseBarrierRequest{
				Name: name,
				Barrier: &transport.DatabaseBarrierConfig{
					Enabled: enabled && !disabled,
				},
			}
			info, err := client.Database().UpdateDatabaseBarrier(cmd.Context(), req)
			if err != nil {
				return err
			}
			return printDatabaseInfo(info)
		},
	}

	cmd.Flags().BoolVar(&enabled, "enabled", false, "Enable encryption barrier")
	cmd.Flags().BoolVar(&disabled, "disabled", false, "Disable encryption barrier")
	cmd.MarkFlagsMutuallyExclusive("enabled", "disabled")

	return cmd
}

// newDatabaseUpdateConnectionCmd creates the "database update-connection" subcommand.
func newDatabaseUpdateConnectionCmd() *cobra.Command {
	var (
		connTransport string
		address       string
		caFile        string
		certFile      string
		keyFile       string
	)

	cmd := &cobra.Command{
		Use:   "update-connection <name>",
		Short: "Update remote connection configuration",
		Long:  "Update the remote connection configuration for a database.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			req := transport.UpdateDatabaseConnectionRequest{
				Name: name,
				Connection: &transport.DatabaseConnectionConfig{
					Transport: connTransport,
					Address:   address,
					CAFile:    caFile,
					CertFile:  certFile,
					KeyFile:   keyFile,
				},
			}
			info, err := client.Database().UpdateDatabaseConnection(cmd.Context(), req)
			if err != nil {
				return err
			}
			return printDatabaseInfo(info)
		},
	}

	cmd.Flags().StringVar(&connTransport, "conn-transport", "tcp", "Connection transport (tcp, quic)")
	cmd.Flags().StringVar(&address, "address", "", "Remote database address (host:port)")
	cmd.Flags().StringVar(&caFile, "conn-ca-file", "", "Connection TLS CA certificate file")
	cmd.Flags().StringVar(&certFile, "conn-cert-file", "", "Connection TLS client certificate file")
	cmd.Flags().StringVar(&keyFile, "conn-key-file", "", "Connection TLS client key file")

	return cmd
}

// formatProtocols returns a comma-separated string of active protocol names.
func formatProtocols(proto transport.ProtocolConfig) string {
	var protocols []string
	if proto.MySQL {
		protocols = append(protocols, "mysql")
	}
	if proto.PostgreSQL {
		protocols = append(protocols, "postgresql")
	}
	if proto.Etcd != nil && proto.Etcd.Enabled {
		protocols = append(protocols, "etcd")
	}
	if len(protocols) == 0 {
		return "-"
	}
	return strings.Join(protocols, ",")
}

// printDatabaseInfo prints a DatabaseInfo in the current output format.
func printDatabaseInfo(info *transport.DatabaseInfo) error {
	switch outputFormat {
	case "json":
		return printJSON(info)
	case "yaml":
		return printYAML(info)
	default:
		_, _ = fmt.Fprintf(outputWriter, "Name:    %s\n", info.Name)
		_, _ = fmt.Fprintf(outputWriter, "Engine:  %s\n", info.Engine)
		_, _ = fmt.Fprintf(outputWriter, "Backend: %s\n", info.Backend)
		_, _ = fmt.Fprintf(outputWriter, "Status:  %s\n", info.Status)
		if info.DataDir != "" {
			_, _ = fmt.Fprintf(outputWriter, "DataDir: %s\n", info.DataDir)
		}
		if info.ShardID != 0 {
			_, _ = fmt.Fprintf(outputWriter, "ShardID: %s\n", strconv.FormatUint(info.ShardID, 10))
		}
		if info.Replicas != 0 {
			_, _ = fmt.Fprintf(outputWriter, "Replicas: %d\n", info.Replicas)
		}
		if info.Protocol.Etcd != nil {
			_, _ = fmt.Fprintf(outputWriter, "Etcd:\n")
			_, _ = fmt.Fprintf(outputWriter, "  Enabled:  %s\n", strconv.FormatBool(info.Protocol.Etcd.Enabled))
			if info.Protocol.Etcd.ListenAddr != "" {
				_, _ = fmt.Fprintf(outputWriter, "  Listen:   %s\n", info.Protocol.Etcd.ListenAddr)
			}
			if len(info.Protocol.Etcd.AdvertiseClientURLs) > 0 {
				_, _ = fmt.Fprintf(outputWriter, "  Advertise: %s\n", strings.Join(info.Protocol.Etcd.AdvertiseClientURLs, ", "))
			}
		}
		if info.Raft != nil {
			_, _ = fmt.Fprintf(outputWriter, "Raft:\n")
			_, _ = fmt.Fprintf(outputWriter, "  Mode:     %s\n", info.Raft.ClusterMode)
			if info.Raft.RaftAddress != "" {
				_, _ = fmt.Fprintf(outputWriter, "  Address:  %s\n", info.Raft.RaftAddress)
			}
		}
		if info.Barrier != nil {
			_, _ = fmt.Fprintf(outputWriter, "Barrier:\n")
			_, _ = fmt.Fprintf(outputWriter, "  Enabled:  %s\n", strconv.FormatBool(info.Barrier.Enabled))
		}
		if info.Connection != nil {
			_, _ = fmt.Fprintf(outputWriter, "Connection:\n")
			_, _ = fmt.Fprintf(outputWriter, "  Transport: %s\n", info.Connection.Transport)
			_, _ = fmt.Fprintf(outputWriter, "  Address:   %s\n", info.Connection.Address)
		}
		if info.SQL != nil && info.SQL.Concurrency != "" {
			_, _ = fmt.Fprintf(outputWriter, "SQL:\n")
			_, _ = fmt.Fprintf(outputWriter, "  Concurrency: %s\n", info.SQL.Concurrency)
		}
		printCompactionPolicy(info.Compaction)
		return nil
	}
}

// newDatabaseSealCmd creates the "database seal" subcommand.
func newDatabaseSealCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "seal <name>",
		Short: "Seal a database's encryption barrier",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client.Database().SealDatabase(cmd.Context(), args[0]); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}
}

// newDatabaseUnsealCmd creates the "database unseal" subcommand.
func newDatabaseUnsealCmd() *cobra.Command {
	var secret string
	cmd := &cobra.Command{
		Use:   "unseal <name>",
		Short: "Unseal a database's encryption barrier",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client.Database().UnsealDatabase(cmd.Context(), args[0], secret); err != nil {
				return err
			}
			printOK()
			return nil
		},
	}
	cmd.Flags().StringVar(&secret, "secret", "", "Unseal secret for the database barrier")
	return cmd
}

// newDatabaseBarrierStatusCmd creates the "database barrier-status" subcommand.
func newDatabaseBarrierStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "barrier-status <name>",
		Short: "Show a database's encryption-barrier status",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := client.Database().DatabaseBarrierStatus(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			switch outputFormat {
			case "json":
				return printJSON(st)
			case "yaml":
				return printYAML(st)
			default:
				_, _ = fmt.Fprintf(outputWriter, "sealed:          %t\nstrategy:        %s\nhardware_backed: %t\n",
					st.Sealed, st.Strategy, st.HardwareBacked)
				if !st.InitializedAt.IsZero() {
					_, _ = fmt.Fprintf(outputWriter, "initialized_at:  %s\n", st.InitializedAt.UTC().Format("2006-01-02T15:04:05Z07:00"))
				}
				printClusterSeal(st.ClusterSeal)
				return nil
			}
		},
	}
}
