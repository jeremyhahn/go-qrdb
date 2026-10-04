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
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
	secure "github.com/jeremyhahn/go-qrdb/sdk/go/secure"
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

// StandaloneRaftBackendError is returned when mode "standalone" is
// combined with a database whose backend is "raft" — the replicated data
// plane structurally requires the consensus layer.
type StandaloneRaftBackendError struct{}

// Error returns a human-readable description.
func (e *StandaloneRaftBackendError) Error() string {
	return "cli: database backend \"raft\" requires mode: raft (standalone serves node-local backends only)"
}

// StandaloneMigrationError is returned when an in-process system
// encryption migration is requested under mode "standalone". The
// migration recycle path is raft-mode-only today.
type StandaloneMigrationError struct{}

// Error returns a human-readable description.
func (e *StandaloneMigrationError) Error() string {
	return "cli: system encryption migration requires mode: raft"
}

// MissingBindHostError is returned when the resolved start flags carry no bind
// host. It is a fail-closed guard on the one path that builds the
// *qrdbconfig.Config every listener binds from.
//
// An empty host is NOT a harmless "unset". net.JoinHostPort("", port) is
// ":port", which net.Listen binds to [::] -- every interface. So an absent
// value would silently mean the widest possible bind, decided by a stdlib
// convention rather than declared in the defaults layer, and an operator
// reading the configuration could not see what the bind address would be.
// That is the failure go-qrdb#165 is about, arrived at from the other side.
//
// The daemon's own flag default is qrdbconfig.DefaultServerHost, applied by
// cobra at registration, so this is unreachable through the command. It
// guards direct constructions of startFlags.
type MissingBindHostError struct{}

// Error returns a human-readable description.
func (e *MissingBindHostError) Error() string {
	return "qrdbd: no bind host resolved for the API listeners; " +
		"set server.host, QRDB_SERVER_HOST or --host"
}

// UnknownSMStorageError is returned when --sm-storage names an
// unsupported state machine storage backend. Objstore is deliberately
// unsupported: it is a snapshot/backup tier, never the hot apply path.
type UnknownSMStorageError struct {
	Name string
}

// Error returns a human-readable description.
func (e *UnknownSMStorageError) Error() string {
	return "cli: unknown state machine storage " + strconv.Quote(e.Name) + " (objstore is a snapshot/backup tier; use pebble, file, or memory)"
}

// SMStorageTypeMismatchError is returned when --sm-storage selects a
// non-pebble backend for a pebble-native state machine type.
type SMStorageTypeMismatchError struct {
	Storage string
	SMType  string
}

// Error returns a human-readable description.
func (e *SMStorageTypeMismatchError) Error() string {
	return "cli: sm-storage " + strconv.Quote(e.Storage) + " applies to the storage-generic state machines only; use --sm disk or --sm fast (got " + strconv.Quote(e.SMType) + ")"
}

// SMLeasesUnsupportedError is returned when --sm selects a state machine that
// cannot keep the node's leases. Cluster membership and the feature gate are
// lease-backed, so the node is refused at start rather than run without them.
type SMLeasesUnsupportedError struct {
	SMType string
	Err    error
}

// Error returns a human-readable description.
func (e *SMLeasesUnsupportedError) Error() string {
	return "cli: sm " + strconv.Quote(e.SMType) + " cannot keep leases, which cluster membership requires; use --sm standard or another KV state machine: " + e.Err.Error()
}

// Unwrap returns the registry's refusal.
func (e *SMLeasesUnsupportedError) Unwrap() error { return e.Err }

// UnknownSMError is returned when the --sm flag specifies an unknown
// state machine type.
type UnknownSMError struct {
	Name string
}

// Error returns a human-readable description.
func (e *UnknownSMError) Error() string {
	return "cli: unknown state machine type " + strconv.Quote(e.Name)
}

// TLSGenerateError is returned when TLS certificate generation fails.
// Aliased from pkg/clustertls so that type assertions across qrdbd and its
// tests share the canonical error type from the clustertls library.
type TLSGenerateError = clustertls.GenerateError

// TLSLoadError is returned when loading TLS certificate material from a file
// fails. Aliased from pkg/clustertls.
type TLSLoadError = clustertls.LoadError

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

// DatabaseProvisionError reports that boot provisioning of the configured
// databases could not start: Step names what failed, Err is its cause.
type DatabaseProvisionError struct {
	Step string
	Err  error
}

// Error returns a human-readable description of the provisioning failure.
func (e *DatabaseProvisionError) Error() string {
	return "qrdbd: database provisioning: " + e.Step + ": " + e.Err.Error()
}

// Unwrap returns the underlying cause.
func (e *DatabaseProvisionError) Unwrap() error { return e.Err }

// SystemShardServingError reports that the system shard could not serve a
// linearizable read before the node started serving: no leader was elected,
// or its leader never committed in its term. Err is the readiness wait's
// typed error (*server.ShardLeaderWaitError or *server.ShardReadWaitError).
type SystemShardServingError struct {
	Err error
}

// Error returns a human-readable description of the readiness failure.
func (e *SystemShardServingError) Error() string {
	return "qrdbd: system shard not ready to serve: " + e.Err.Error()
}

// Unwrap returns the readiness wait's error.
func (e *SystemShardServingError) Unwrap() error { return e.Err }

// LeaderElectionTimeoutError is the cause recorded when no Raft leader is
// elected within the bounded number of attempts.
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

// EnrollmentUnavailableError is returned when this node is started with
// bootstrap flags: joining an existing cluster is unavailable because the
// cluster CA private key is no longer distributed. Aliased from
// pkg/clusterenroll.
type EnrollmentUnavailableError = clusterenroll.EnrollmentUnavailableError

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

// NoProbeNetworkError is returned when a port reservation is asked for with
// no networks to prove the port free on.
//
// It is not a degenerate "reserve nothing" request that could be answered with
// any free number. A caller that names no network has not said what the port
// is for, and the whole point of the reservation is that the probe matches the
// listener (go-qrdb#257) — so there is nothing to match against and the only
// honest answer is a refusal.
type NoProbeNetworkError struct {
	Host string
}

// Error returns a human-readable description.
func (e *NoProbeNetworkError) Error() string {
	return "qrdbd: port reservation at " + strconv.Quote(e.Host) +
		" named no network; a port must be proven free on the protocols its listener will bind"
}

// UnknownProbeNetworkError is returned when a port reservation names a
// network the probe table does not know how to hold a port on.
type UnknownProbeNetworkError struct {
	Network string
}

// Error returns a human-readable description.
func (e *UnknownProbeNetworkError) Error() string {
	return "qrdbd: unknown port-probe network " + strconv.Quote(e.Network) + " (want \"tcp\" or \"udp\")"
}

// ProbeAddrError is returned when a bound probe listener reports an address
// whose port cannot be parsed.
type ProbeAddrError struct {
	Addr string
	Err  error
}

// Error returns a human-readable description.
func (e *ProbeAddrError) Error() string {
	return "qrdbd: cannot read the port from probe listener address " + strconv.Quote(e.Addr) + ": " + e.Err.Error()
}

// Unwrap returns the underlying parse error.
func (e *ProbeAddrError) Unwrap() error { return e.Err }

// NoFreePortError is returned when no candidate port was free on every
// requested network at the requested host within the attempt budget.
//
// The host and the network list are both named because they are the two
// things a reader needs in order to act: a failure at "0.0.0.0" on
// ["tcp","udp"] means some other holder has each candidate on one stack or
// one interface, and the remedy is to pin the port explicitly rather than to
// re-run and hope.
type NoFreePortError struct {
	Host     string
	Networks []string
	Attempts int
	Err      error
}

// Error returns a human-readable description.
func (e *NoFreePortError) Error() string {
	msg := "qrdbd: no port free on " + strings.Join(e.Networks, "+") +
		" at " + strconv.Quote(e.Host) + " after " + strconv.Itoa(e.Attempts) + " attempts"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the last bind error observed while searching.
func (e *NoFreePortError) Unwrap() error { return e.Err }

// SystemMigrationSealDisabledError is returned when a system-scope
// enable-encryption request reaches the boot loop while the node is running
// with seal.enabled: false. There is no seal subsystem for the boot loop to
// migrate. daemonSystemMigrator.EnableEncryption rejects this earlier, before
// ever enqueueing the request; handleSystemMigration returns this error only
// as a defensive guard for a future caller that reaches it another way,
// instead of dereferencing the nil seal subsystem (go-qrdb#550 review).
type SystemMigrationSealDisabledError struct{}

// Error returns a human-readable description.
func (e *SystemMigrationSealDisabledError) Error() string {
	return "qrdbd: cannot enable encryption: seal.enabled is false for this node"
}

// errSecureMemoryBudgetNegative is wrapped by SecureMemoryBudgetError when
// the configured budget is negative. Only exactly zero means "unconfigured,
// use the default" (runStart's own convention for --discovery-cluster-size,
// --cluster-min-peers, --max-in-mem-log-size); a negative value is a
// configuration mistake and must fail closed rather than silently fall back
// to the default budget the operator did not ask for (go-qrdb#114 review,
// round 2).
var errSecureMemoryBudgetNegative = errors.New("secure memory budget must not be negative")

// errSecureMemoryBudgetOverflow is wrapped by SecureMemoryBudgetError when
// converting the configured MiB value to bytes (a left shift by 20) would
// overflow int64. Left unchecked, an operator-supplied value near
// math.MaxInt64 wraps around to a small or even negative byte count, silently
// installing a MUCH smaller effective budget than configured rather than
// failing (go-qrdb#114 review, round 2).
var errSecureMemoryBudgetOverflow = errors.New("secure memory budget in MiB overflows converting to bytes")

// secureMemoryLimitRemedy is appended to a SecureMemoryBudgetError whose cause
// is the host's locked-memory ceiling, so the operator reads what to change in
// the same line that says what failed.
const secureMemoryLimitRemedy = "raise the host's locked-memory limit " +
	"(`ulimit -l`, LimitMEMLOCK= in the systemd unit, or --ulimit memlock=-1:-1 " +
	"and --cap-add IPC_LOCK for a container) or set --secure-memory-budget-mib " +
	"to a value that fits within it"

// SecureMemoryBudgetError is returned when the process-wide locked-memory
// budget (--secure-memory-budget-mib / QRDB_SERVER_SECURE_MEMORY_BUDGET_MIB /
// server.secure_memory_budget_mib) cannot be resolved or installed —
// typically because it exceeds the host's discovered RLIMIT_MEMLOCK ceiling.
// BudgetMiB is the configured figure, named explicitly so the operator does
// not have to cross-reference the flag default against the error to find out
// what value was rejected; it is 0 when nothing was configured and the budget
// could not be derived from the host.
type SecureMemoryBudgetError struct {
	BudgetMiB int64
	Err       error
}

// Error returns a human-readable description naming the rejected budget, and
// how to raise the host limit when that limit is the cause.
func (e *SecureMemoryBudgetError) Error() string {
	var msg string
	if e.BudgetMiB == 0 {
		msg = "qrdbd: cannot derive a secure memory budget from the host's locked-memory limit: " + e.Err.Error()
	} else {
		msg = "qrdbd: secure memory budget " + strconv.FormatInt(e.BudgetMiB, 10) +
			" MiB rejected (see --secure-memory-budget-mib): " + e.Err.Error()
	}
	var tooSmall *SecureMemoryLimitTooSmallError
	if errors.Is(e.Err, secure.ErrBudgetExceedsRLimit) || errors.As(e.Err, &tooSmall) {
		msg += "; " + secureMemoryLimitRemedy
	}
	return msg
}

// Unwrap returns the underlying go-enclave error.
func (e *SecureMemoryBudgetError) Unwrap() error { return e.Err }

// SecureMemoryLimitTooSmallError is wrapped by SecureMemoryBudgetError when
// no budget was configured and the host's binding RLIMIT_MEMLOCK, less the
// headroom reserved for locked allocations outside the budget, leaves less
// than the minimum budget qrdbd will run with. All three figures are in bytes.
type SecureMemoryLimitTooSmallError struct {
	// LimitBytes is the discovered RLIMIT_MEMLOCK soft limit.
	LimitBytes int64

	// HeadroomBytes is the part of the limit reserved for unbudgeted locked
	// allocations.
	HeadroomBytes int64

	// MinimumBytes is the smallest budget qrdbd will derive.
	MinimumBytes int64
}

// Error names the limit, the headroom and the minimum.
func (e *SecureMemoryLimitTooSmallError) Error() string {
	return "RLIMIT_MEMLOCK is " + strconv.FormatInt(e.LimitBytes, 10) + " bytes; after the " +
		strconv.FormatInt(e.HeadroomBytes, 10) + "-byte headroom for locked memory outside the budget, " +
		"less than the " + strconv.FormatInt(e.MinimumBytes, 10) + "-byte minimum secure memory budget remains"
}

// SecureMemoryBudgetMismatchError is wrapped by SecureMemoryBudgetError when
// setSecureAllocator reports enclave.ErrAllocatorInstalled and the ALREADY
// installed allocator's own limit does not equal the byte value
// --secure-memory-budget-mib configured.
//
// ErrAllocatorInstalled alone is not fatal (see runStart's own documentation
// on this point) because a process allocator already existing, installed
// earlier in this same process or by a library's lazy default, is not by
// itself a reason to refuse to start. But treating it as UNCONDITIONALLY
// benign was itself the defect (go-qrdb#114 review, round 3): a library that
// allocated protected memory before runStart reached this line installs the
// lazy default sized to the discovered RLIMIT_MEMLOCK ceiling, not the
// operator's configured figure, and every protected allocation for the rest
// of the process then silently runs against a budget nobody chose. Only an
// installed allocator whose limit MATCHES what was configured is truly
// benign; anything else must fail closed and name both figures so the
// operator is not left debugging a budget that "worked" at a size they never
// asked for.
type SecureMemoryBudgetMismatchError struct {
	// ConfiguredBytes is --secure-memory-budget-mib converted to bytes.
	ConfiguredBytes int64

	// InstalledBytes is the already-installed allocator's own Limit().
	InstalledBytes int64
}

// Error names both the configured and the already-installed figures, in
// bytes, so the mismatch is legible without cross-referencing MiB by hand.
func (e *SecureMemoryBudgetMismatchError) Error() string {
	return "qrdbd: a locked-memory allocator already installed in this process (" +
		strconv.FormatInt(e.InstalledBytes, 10) + " bytes) does not match the configured budget (" +
		strconv.FormatInt(e.ConfiguredBytes, 10) + " bytes); see --secure-memory-budget-mib"
}

// SecureMemoryWipeError is returned when shutdown's sweep of mlocked memory
// (secure.WipeAll) did not fully complete: either enclaves were still held
// when the sweep finished (Remaining > 0, a shutdown-ordering condition — see
// enclave.WipeAll's documentation) or the sweep detected corrupted memory
// (Err set). Either one means qrdbd cannot promise every locked-memory
// enclave — every outstanding Buffer and every transiently-opened Secret
// plaintext — was wiped before the process exits, which is why runStart
// turns it into a nonzero exit rather than a log line an operator could miss.
// It says nothing about a Secret's own ciphertext, which lives on the
// ordinary heap by design and is unaffected by this sweep either way; see
// enclave.WipeAll's documentation for that distinction.
type SecureMemoryWipeError struct {
	Remaining int
	Err       error
}

// Error returns a human-readable description of what the sweep could not
// account for.
func (e *SecureMemoryWipeError) Error() string {
	msg := "qrdbd: secure memory wipe incomplete at shutdown"
	if e.Remaining > 0 {
		msg += ": " + strconv.Itoa(e.Remaining) + " enclave(s) still held locked memory"
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the corruption error WipeAll detected, if any.
func (e *SecureMemoryWipeError) Unwrap() error { return e.Err }

// SealedSMStoragePlaintextError is returned at start when the seal barrier is
// enabled and --sm names a storage-generic state machine (disk, fast) over an
// --sm-storage that cannot be encrypted at rest (file). Pebble storage is
// encrypted through the barrier and memory storage persists nothing, so both
// are allowed.
type SealedSMStoragePlaintextError struct {
	SMType  string
	Storage string
	Err     error
}

// Error returns a human-readable description with the remedy.
func (e *SealedSMStoragePlaintextError) Error() string {
	storage := e.Storage
	if storage == "" {
		storage = "pebble"
	}
	return "cli: --sm " + strconv.Quote(e.SMType) + " over --sm-storage " + strconv.Quote(storage) +
		" would store data in plaintext while the seal barrier is enabled; use --sm-storage pebble (encrypted at rest) or memory"
}

// Unwrap returns the registry's refusal.
func (e *SealedSMStoragePlaintextError) Unwrap() error { return e.Err }

// InspectionOptOutAuditError is returned by start when the process-inspection
// opt-out was honoured but the compliance audit trail refused the record of
// it. The node does not run inspectable off the record.
type InspectionOptOutAuditError struct {
	Err error
}

// Error implements the error interface.
func (e *InspectionOptOutAuditError) Error() string {
	return "qrdbd: process inspection hardening is disabled by environment, and the audit trail refused the record of it: " +
		e.Err.Error()
}

// Unwrap returns the audit sink's error.
func (e *InspectionOptOutAuditError) Unwrap() error { return e.Err }
