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
	"strconv"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
)

// errNoCertsInPEM is returned when a PEM file contains no valid certificate
// blocks. Aliased from pkg/clustertls so the same sentinel is shared across
// the clustertls library and the qrdbd daemon.
var errNoCertsInPEM = clustertls.ErrNoCertsInPEM

// PeerParseError is returned when a peer specification string cannot be
// parsed. The canonical type lives in pkg/cluster; the daemon aliases it so
// CLI callers and tests share the cluster package's error contract.
type PeerParseError = cluster.PeerParseError

// UnknownDiscoveryError is returned when the --discovery flag specifies an
// unknown discovery method. Aliased from pkg/cluster.
type UnknownDiscoveryError = cluster.UnknownDiscoveryError

// MissingFlagError is returned when a required discovery input is not set.
// Aliased from pkg/cluster.
type MissingFlagError = cluster.MissingFlagError

// NoPeersError is returned when no peers are configured for static
// discovery. Aliased from pkg/cluster.
type NoPeersError = cluster.NoPeersError

// StandaloneModeNotSupportedError is returned when a configuration with
// mode "standalone" is passed to the daemon before the standalone
// composition branch lands (run-modes plan, phase 3). Refusing is safer
// than silently booting the raft stack against a raft-free intent.
type StandaloneModeNotSupportedError struct{}

// Error returns a human-readable description.
func (e *StandaloneModeNotSupportedError) Error() string {
	return "cli: mode \"standalone\" is not yet supported by qrdbd; run mode \"raft\" (the default), or embed via sdk/go/node.NewStandaloneNode"
}

// UnknownSMError is returned when the --sm flag specifies an unknown
// state machine type.
type UnknownSMError struct {
	Name string
}

// Error returns a human-readable description.
func (e *UnknownSMError) Error() string {
	return "cli: unknown state machine type " + strconv.Quote(e.Name)
}

// AdminFetchError is returned when an HTTP request to the admin server
// fails.
type AdminFetchError struct {
	URL string
	Err error
}

// Error returns a human-readable description.
func (e *AdminFetchError) Error() string {
	return "cli: failed to fetch " + e.URL + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *AdminFetchError) Unwrap() error {
	return e.Err
}

// AdminResponseError is returned when the admin server returns a
// non-200 status code.
type AdminResponseError struct {
	URL        string
	StatusCode int
}

// Error returns a human-readable description.
func (e *AdminResponseError) Error() string {
	return "cli: admin server returned status " + strconv.Itoa(e.StatusCode) + " for " + e.URL
}

// ConfigApplyError is returned when a YAML configuration file cannot be
// applied to the start flags.
type ConfigApplyError struct {
	Field string
	Err   error
}

// Error returns a human-readable description.
func (e *ConfigApplyError) Error() string {
	return "cli: config apply failed for " + strconv.Quote(e.Field) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ConfigApplyError) Unwrap() error {
	return e.Err
}

// TLSGenerateError is returned when TLS certificate generation fails.
// Aliased from pkg/clustertls so that type assertions across qrdbd and its
// tests share the canonical error type from the clustertls library.
type TLSGenerateError = clustertls.GenerateError

// TLSLoadError is returned when loading TLS certificate material from a file
// fails. Aliased from pkg/clustertls.
type TLSLoadError = clustertls.LoadError

// CACertWriteError is returned when writing the CA certificate to disk
// fails.
type CACertWriteError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *CACertWriteError) Error() string {
	return "cli: failed to write CA certificate to " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CACertWriteError) Unwrap() error {
	return e.Err
}

// AuthBootstrapError is returned when authentication infrastructure
// initialization fails during the bootstrap sequence.
type AuthBootstrapError struct {
	Step string
	Err  error
}

// Error returns a human-readable description of the bootstrap failure.
func (e *AuthBootstrapError) Error() string {
	return "cli: auth bootstrap failed at " + strconv.Quote(e.Step) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *AuthBootstrapError) Unwrap() error {
	return e.Err
}

// LeaderElectionTimeoutError is the cause recorded in an AuthBootstrapError when
// no Raft leader is elected within the bounded number of attempts.
type LeaderElectionTimeoutError struct {
	Attempts int
}

// Error returns a human-readable description of the leader-election timeout.
func (e *LeaderElectionTimeoutError) Error() string {
	return "no leader after " + strconv.Itoa(e.Attempts) + " attempts"
}

// AdminReplicationTimeoutError is the cause recorded in an AuthBootstrapError
// when the admin user is not replicated to a follower within the timeout.
type AdminReplicationTimeoutError struct {
	Timeout time.Duration
}

// Error returns a human-readable description of the admin-replication timeout.
func (e *AdminReplicationTimeoutError) Error() string {
	return "admin user not replicated within " + e.Timeout.String()
}

// AdminCertError is returned when admin client certificate generation fails.
type AdminCertError struct {
	Err error
}

// Error returns a human-readable description.
func (e *AdminCertError) Error() string {
	return "cli: admin client certificate generation failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *AdminCertError) Unwrap() error {
	return e.Err
}

// AdminCertWriteError is returned when writing the admin client cert to
// disk fails.
type AdminCertWriteError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *AdminCertWriteError) Error() string {
	return "cli: failed to write admin certificate to " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *AdminCertWriteError) Unwrap() error {
	return e.Err
}

// BootstrapFetchError is returned when fetching the CA bundle from a
// bootstrap server fails. Aliased from pkg/clusterenroll.
type BootstrapFetchError = clusterenroll.FetchError

// BootstrapError is returned when the overall bootstrap process fails.
// Aliased from pkg/clusterenroll.
type BootstrapError = clusterenroll.JoinError

// BootstrapServerError is returned when starting a bootstrap server
// fails. Aliased from pkg/clusterenroll.
type BootstrapServerError = clusterenroll.ServerError

// BootstrapWriteError is returned when writing bootstrap material to disk
// fails. Aliased from pkg/clustertls so that type assertions across qrdbd and
// its tests share the canonical error type from the clustertls library.
type BootstrapWriteError = clustertls.WriteError

// BootstrapKeyError is returned when parsing bootstrap key material fails.
// Aliased from pkg/clustertls so that type assertions across qrdbd and its
// tests share the canonical error type from the clustertls library.
type BootstrapKeyError = clustertls.KeyError

// NoBootstrapAddrError is returned when no bootstrap address can be
// determined.
type NoBootstrapAddrError struct{}

// Error returns a human-readable description.
func (e *NoBootstrapAddrError) Error() string {
	return "cli: no bootstrap address available, use --bootstrap-addr or --bootstrap-noise-addr"
}

// CertReloadError is returned when hot-reloading a TLS certificate
// from disk fails.
type CertReloadError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *CertReloadError) Error() string {
	return "cli: TLS certificate reload failed for " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CertReloadError) Unwrap() error {
	return e.Err
}

// SealStorageError is returned when the seal storage backend
// fails to initialize.
type SealStorageError struct {
	Err error
}

// Error returns a human-readable description.
func (e *SealStorageError) Error() string {
	return "cli: seal storage initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *SealStorageError) Unwrap() error {
	return e.Err
}

// SealStrategyError is returned when a sealing strategy fails to
// initialize.
type SealStrategyError struct {
	Strategy string
	Err      error
}

// Error returns a human-readable description.
func (e *SealStrategyError) Error() string {
	return "cli: seal strategy " + strconv.Quote(e.Strategy) + " initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *SealStrategyError) Unwrap() error {
	return e.Err
}

// SealBarrierError is returned when the storage barrier fails to
// initialize.
type SealBarrierError struct {
	Err error
}

// Error returns a human-readable description.
func (e *SealBarrierError) Error() string {
	return "cli: seal barrier initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *SealBarrierError) Unwrap() error {
	return e.Err
}

// CredentialServiceError is returned when the credential service
// fails to initialize.
type CredentialServiceError struct {
	Err error
}

// Error returns a human-readable description.
func (e *CredentialServiceError) Error() string {
	return "cli: credential service initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CredentialServiceError) Unwrap() error {
	return e.Err
}

// CeremonyServiceError is returned when the ceremony service fails
// to initialize.
type CeremonyServiceError struct {
	Err error
}

// Error returns a human-readable description.
func (e *CeremonyServiceError) Error() string {
	return "cli: ceremony service initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CeremonyServiceError) Unwrap() error {
	return e.Err
}

// VectorStorageError is returned when the vector storage backend
// fails to initialize.
type VectorStorageError struct {
	Err error
}

// Error returns a human-readable description.
func (e *VectorStorageError) Error() string {
	return "cli: vector storage initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *VectorStorageError) Unwrap() error {
	return e.Err
}

// VectorServiceError is returned when the vector service fails to
// initialize.
type VectorServiceError struct {
	Err error
}

// Error returns a human-readable description.
func (e *VectorServiceError) Error() string {
	return "cli: vector service initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *VectorServiceError) Unwrap() error {
	return e.Err
}

// BarrierRegistryError is returned when the barrier registry fails to
// initialize. This prevents per-tenant encryption isolation from
// being established.
type BarrierRegistryError struct {
	Err error
}

// Error returns a human-readable description.
func (e *BarrierRegistryError) Error() string {
	return "cli: barrier registry initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *BarrierRegistryError) Unwrap() error {
	return e.Err
}

// ClusterDEKError is returned when the cluster DEK manager fails to
// initialize. This prevents the tenant DEK hierarchy from being
// established.
type ClusterDEKError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ClusterDEKError) Error() string {
	return "cli: cluster DEK manager initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ClusterDEKError) Unwrap() error {
	return e.Err
}

// DEKStorageError is returned when the DEK storage backend fails to
// initialize.
type DEKStorageError struct {
	Err error
}

// Error returns a human-readable description.
func (e *DEKStorageError) Error() string {
	return "cli: DEK storage initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *DEKStorageError) Unwrap() error {
	return e.Err
}

// TenantDEKError is returned when the tenant DEK manager fails to
// initialize.
type TenantDEKError struct {
	Err error
}

// Error returns a human-readable description.
func (e *TenantDEKError) Error() string {
	return "cli: tenant DEK manager initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *TenantDEKError) Unwrap() error {
	return e.Err
}

// TenantServiceError is returned when the tenant lifecycle service fails
// to initialize.
type TenantServiceError struct {
	Err error
}

// Error returns a human-readable description.
func (e *TenantServiceError) Error() string {
	return "cli: tenant service initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *TenantServiceError) Unwrap() error {
	return e.Err
}

// LegacyDataDirError is returned when the daemon refuses to start
// because the data dir was created by a pre-encfs build whose seal
// sub-store lacks a format-version sentinel. There is no in-place
// upgrade path; operators must export their data through the
// application API and re-import it on a fresh install.
type LegacyDataDirError struct {
	DataDir string
	Err     error
}

// Error returns a human-readable description.
func (e *LegacyDataDirError) Error() string {
	return "cli: refusing to start on legacy data dir " + e.DataDir +
		" (pre-encfs format detected): " + e.Err.Error()
}

// Unwrap returns the underlying seal package sentinel error so
// callers can match with errors.Is(err, seal.ErrLegacyDataDir).
func (e *LegacyDataDirError) Unwrap() error {
	return e.Err
}

// SealedListenerStartupError is returned when the daemon fails to
// bring up its sealed-window API listeners (REST, gRPC) before the
// barrier is unsealed.
type SealedListenerStartupError struct {
	Err error
}

// Error returns a human-readable description.
func (e *SealedListenerStartupError) Error() string {
	return "cli: sealed-window listener startup failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *SealedListenerStartupError) Unwrap() error {
	return e.Err
}

// CeremonyCAError is returned when the ceremony CA cannot be built
// from the node's TLS bundle (CA certificate or key parse failure).
type CeremonyCAError struct {
	Err error
}

// Error returns a human-readable description.
func (e *CeremonyCAError) Error() string {
	return "cli: ceremony CA construction failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CeremonyCAError) Unwrap() error {
	return e.Err
}

// DataPlaneStartupError is returned when post-unseal data-plane
// initialization fails (state machine factory, host construction,
// or lifecycle.BuildServices).
type DataPlaneStartupError struct {
	Phase string
	Err   error
}

// Error returns a human-readable description.
func (e *DataPlaneStartupError) Error() string {
	return "cli: data-plane startup failed during " + e.Phase + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *DataPlaneStartupError) Unwrap() error {
	return e.Err
}

// UnsealWaitError is returned when the daemon's unseal-wait loop
// terminates abnormally (context cancellation other than clean
// shutdown).
type UnsealWaitError struct {
	Err error
}

// Error returns a human-readable description.
func (e *UnsealWaitError) Error() string {
	return "cli: unseal wait aborted: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *UnsealWaitError) Unwrap() error {
	return e.Err
}

// VolumePoolStorageError is returned when the volume-pool state store (the
// dedicated Pebble that persists round-robin cursors) fails to open. Aliased
// from pkg/storage/pool so the canonical type is shared with the SDK node
// builder.
type VolumePoolStorageError = qrdbpool.VolumePoolStorageError

// VolumePoolRegisterError is returned when objstore's pool.Manager rejects a
// Register call (typically duplicate name or empty candidates). Aliased from
// pkg/storage/pool.
type VolumePoolRegisterError = qrdbpool.VolumePoolRegisterError

// VolumePoolStrategyError is returned when a volume-pool YAML config
// references an unknown strategy name. Aliased from pkg/storage/pool.
type VolumePoolStrategyError = qrdbpool.VolumePoolStrategyError

// VolumePoolPathError is returned when one of the configured local paths
// cannot be created or wrapped as an objstore Backend. Aliased from
// pkg/storage/pool.
type VolumePoolPathError = qrdbpool.VolumePoolPathError

// VolumeReattachError is returned when an IsolationVolume tenant cannot be
// re-bound to its on-disk state during daemon boot. Aliased from pkg/server.
type VolumeReattachError = server.VolumeReattachError

// RaftReattachError is returned when an IsolationRaft tenant cannot be re-bound
// to its on-disk state during daemon boot. Aliased from pkg/server.
type RaftReattachError = server.RaftReattachError

// RestoreBackupSourceRequiredError is returned when --restore-backup-id is
// set without --backup-dir: there is no durable repository to read the
// backup artifact from.
type RestoreBackupSourceRequiredError struct{}

// Error returns a human-readable description.
func (e *RestoreBackupSourceRequiredError) Error() string {
	return "cli: --restore-backup-id requires --backup-dir to locate the backup"
}

// RestoreBackupError is returned when a step of the offline, boot-time
// backup restore sequence (see restore.go) fails.
type RestoreBackupError struct {
	Phase    string
	BackupID string
	ShardID  uint64
	Err      error
}

// Error returns a human-readable description.
func (e *RestoreBackupError) Error() string {
	return "cli: offline restore of backup " + strconv.Quote(e.BackupID) +
		" into shard " + strconv.FormatUint(e.ShardID, 10) +
		" failed during " + strconv.Quote(e.Phase) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *RestoreBackupError) Unwrap() error {
	return e.Err
}

// RestoreBackupVerifyError is returned when the backup targeted by
// --restore-backup-id fails pre-restore checksum verification. The restore
// aborts before the shard is touched.
type RestoreBackupVerifyError struct {
	BackupID string
	Result   service.VerifyResult
}

// Error returns a human-readable description.
func (e *RestoreBackupVerifyError) Error() string {
	return "cli: backup " + strconv.Quote(e.BackupID) +
		" failed pre-restore verification (checksum_match=" + strconv.FormatBool(e.Result.ChecksumMatch) +
		"): refusing to restore"
}

// RestoreBackupShardNotInArtifactError is returned when the backup targeted
// by --restore-backup-id has no payload for the shard being restored.
type RestoreBackupShardNotInArtifactError struct {
	BackupID string
	ShardID  uint64
}

// Error returns a human-readable description.
func (e *RestoreBackupShardNotInArtifactError) Error() string {
	return "cli: backup " + strconv.Quote(e.BackupID) +
		" contains no data for shard " + strconv.FormatUint(e.ShardID, 10)
}
