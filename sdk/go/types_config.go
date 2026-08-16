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

package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/config"
)

// ---------------------------------------------------------------------------
// Daemon configuration
//
// These aliases re-export the canonical daemon (qrdbd) configuration owned by
// github.com/jeremyhahn/go-qrdb/pkg/config. node.NewStandaloneNode and
// node.NewClusteredNode accept *config.Config, so downstream composers
// (go-xkms, go-quicbaas) need to construct it — but the architectural rule is
// that they consume only sdk/go, never go-qrdb/pkg/*. Re-exporting here closes
// that boundary leak.
//
// The flat sdk package already binds DatabaseConfig, DiscoveryConfig,
// EmbeddingConfig and EmbeddingProviderConfig to OTHER (database/discovery/
// embedding) types. To avoid clashing — and to make it unambiguous that these
// describe the on-disk daemon config rather than a runtime backend — the whole
// surface carries a Daemon prefix.
// ---------------------------------------------------------------------------

// DaemonConfig is the root qrdbd configuration document.
type DaemonConfig = config.Config

// DaemonDatabaseConfig configures a single named database within the daemon.
type DaemonDatabaseConfig = config.DatabaseConfig

// DaemonServerConfig configures the multi-protocol server.
type DaemonServerConfig = config.ServerConfig

// DaemonProtocolsConfig toggles and configures individual wire protocols.
type DaemonProtocolsConfig = config.ProtocolsConfig

// DaemonEmbeddingConfig configures the embedding subsystem.
type DaemonEmbeddingConfig = config.EmbeddingConfig

// DaemonRAGConfig configures the retrieval-augmented-generation subsystem.
type DaemonRAGConfig = config.RAGConfig

// DaemonEmbeddingProviderConfig configures a single embedding provider.
type DaemonEmbeddingProviderConfig = config.EmbeddingProviderConfig

// DaemonRaftConfig configures the Raft consensus layer.
type DaemonRaftConfig = config.RaftConfig

// DaemonStorageConfig selects and configures the storage backend.
type DaemonStorageConfig = config.StorageConfig

// DaemonObjstoreConfig configures the object-store backend.
type DaemonObjstoreConfig = config.ObjstoreConfig

// DaemonVolumePoolConfig configures a storage volume pool.
type DaemonVolumePoolConfig = config.VolumePoolConfig

// DaemonTLSConfig configures server TLS.
type DaemonTLSConfig = config.TLSConfig

// DaemonMTLSConfig configures mutual-TLS client authentication.
type DaemonMTLSConfig = config.MTLSConfig

// DaemonUnixConfig configures the Unix-domain-socket (gRPC-over-UDS) transport.
type DaemonUnixConfig = config.UnixConfig

// DaemonMCPConfig configures the Model Context Protocol transport.
type DaemonMCPConfig = config.MCPConfig

// DaemonLoggingConfig configures structured logging.
type DaemonLoggingConfig = config.LoggingConfig

// DaemonDiscoveryConfig configures peer discovery for clustering.
type DaemonDiscoveryConfig = config.DiscoveryConfig

// DaemonMVCCConfig configures the MVCC engine.
type DaemonMVCCConfig = config.MVCCConfig

// DaemonWatchConfig configures the watch/notification subsystem.
type DaemonWatchConfig = config.WatchConfig

// DaemonLeaseConfig configures leases.
type DaemonLeaseConfig = config.LeaseConfig

// DaemonIndexConfig configures secondary indexes.
type DaemonIndexConfig = config.IndexConfig

// DaemonTimeSeriesConfig configures the time-series subsystem.
type DaemonTimeSeriesConfig = config.TimeSeriesConfig

// DaemonWriteModeConfig configures the default write mode.
type DaemonWriteModeConfig = config.WriteModeConfig

// DaemonBatchConfig configures the write-batch aggregator.
type DaemonBatchConfig = config.BatchConfig

// DaemonAuthConfig configures authentication and RBAC.
type DaemonAuthConfig = config.AuthConfig

// DaemonAuthABACConfig configures the ABAC authorization decorator layered
// over RBAC (auth.abac in the daemon YAML).
type DaemonAuthABACConfig = config.AuthABACConfig

// DaemonAuditConfig configures the audit log.
type DaemonAuditConfig = config.AuditConfig

// DaemonCORSConfig configures cross-origin resource sharing for the REST API.
type DaemonCORSConfig = config.CORSConfig

// DaemonSQLConfig configures the SQL compatibility layer.
type DaemonSQLConfig = config.SQLConfig

// DaemonBootstrapConfig configures first-boot bootstrap.
type DaemonBootstrapConfig = config.BootstrapConfig

// DaemonSealConfig configures the storage seal/barrier.
type DaemonSealConfig = config.SealConfig

// DaemonTenantConfig configures multi-tenancy.
type DaemonTenantConfig = config.TenantConfig

// DaemonCodecConfig configures the wire codec.
type DaemonCodecConfig = config.CodecConfig

// DaemonConfigLoadError is returned when loading the config file fails.
type DaemonConfigLoadError = config.LoadError

// DaemonConfigValidationError is returned when config validation fails.
type DaemonConfigValidationError = config.ValidationError

// DaemonDatabaseConfigError is returned for invalid per-database configuration.
type DaemonDatabaseConfigError = config.DatabaseConfigError

// DaemonDefaultShareTTLString is the default Shamir unseal-share time-to-live.
const DaemonDefaultShareTTLString = config.DefaultShareTTLString

// Daemon configuration constructors.
var (
	// DefaultDaemonConfig returns a *DaemonConfig populated with defaults.
	DefaultDaemonConfig = config.DefaultConfig

	// LoadDaemonConfig loads, validates, and returns the daemon config from a
	// YAML file (honoring QRDB_ environment overrides and defaults).
	LoadDaemonConfig = config.Load
)

// BoolPtr returns a pointer to b, for DaemonConfig fields whose zero value
// must be distinguishable from "not configured" (DaemonAuthConfig.Enabled).
var BoolPtr = config.BoolPtr
