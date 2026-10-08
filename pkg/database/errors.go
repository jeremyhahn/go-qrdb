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

package database

import (
	"errors"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// InvalidNameError is returned when a database name is empty or contains
// characters outside the allowed set (alphanumeric, hyphens, underscores).
type InvalidNameError struct {
	Name string
}

// Error returns a human-readable description of the invalid name.
func (e *InvalidNameError) Error() string {
	return "database: invalid database name: " + strconv.Quote(e.Name)
}

// ReservedNameError is returned when a database name collides with a
// directory the node keeps beside per-database directories (see
// IsReservedName). A database of that name would be created inside that
// state, so the name is refused before any directory is made.
type ReservedNameError struct {
	Name string
}

// Error returns a human-readable description of the reserved name.
func (e *ReservedNameError) Error() string {
	return "database: database name is reserved: " + strconv.Quote(e.Name)
}

// InvalidBackendError is returned when an unrecognized backend type is specified.
type InvalidBackendError struct {
	Backend string
}

// Error returns a human-readable description of the invalid backend type.
func (e *InvalidBackendError) Error() string {
	return "database: invalid backend type: " + strconv.Quote(e.Backend)
}

// AlreadyExistsError is returned when attempting to create a database with
// a name that is already in use.
type AlreadyExistsError struct {
	Name string

	// Elsewhere reports that the name is live in the replicated store under
	// another database's incarnation -- created on another node, whose
	// catalog this node does not share -- rather than in this node's catalog.
	Elsewhere bool
}

// Error returns a human-readable description of the duplicate database.
func (e *AlreadyExistsError) Error() string {
	if e.Elsewhere {
		return "database: database already exists on another node: " + strconv.Quote(e.Name)
	}
	return "database: database already exists: " + strconv.Quote(e.Name)
}

// NotFoundError is returned when a requested database does not exist.
type NotFoundError struct {
	Name string
}

// Error returns a human-readable description of the missing database.
func (e *NotFoundError) Error() string {
	return "database: database not found: " + strconv.Quote(e.Name)
}

// NotOpenError is returned when an operation is attempted on a database
// that is not in the open state.
type NotOpenError struct {
	Name string
}

// Error returns a human-readable description of the not-open condition.
func (e *NotOpenError) Error() string {
	return "database: database not open: " + strconv.Quote(e.Name)
}

// OpenError is returned when the underlying storage fails to open.
type OpenError struct {
	Name string
	Err  error
}

// Error returns a human-readable description of the open failure.
func (e *OpenError) Error() string {
	return "database: failed to open database " + strconv.Quote(e.Name) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *OpenError) Unwrap() error {
	return e.Err
}

// CloseError is returned when the database fails to close cleanly.
type CloseError struct {
	Name string
	Err  error
}

// Error returns a human-readable description of the close failure.
func (e *CloseError) Error() string {
	return "database: failed to close database " + strconv.Quote(e.Name) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *CloseError) Unwrap() error {
	return e.Err
}

// MetaReadError is returned when reading the on-disk metadata (meta.json) fails.
type MetaReadError struct {
	Path string
	Err  error
}

// Error returns a human-readable description of the metadata read failure.
func (e *MetaReadError) Error() string {
	return "database: failed to read database metadata " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *MetaReadError) Unwrap() error {
	return e.Err
}

// LegacyMetadataError is returned when a database's on-disk meta.json still
// carries a field this version no longer understands -- today, only the
// "engine" field removed in v0.45 (OD-13). The no-backward-compat ruling of
// 2026-10-02 supersedes the alternative of silently stripping the field and
// rewriting the file: the format is refused outright, fail-closed, and the
// database must be recreated.
type LegacyMetadataError struct {
	Path string
	Err  error
}

// Error returns a human-readable description of the unsupported format.
func (e *LegacyMetadataError) Error() string {
	return "database: unsupported metadata format " + strconv.Quote(e.Path) + ": " + e.Err.Error() + "; recreate the database"
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *LegacyMetadataError) Unwrap() error {
	return e.Err
}

// MetaWriteError is returned when writing the on-disk metadata (meta.json) fails.
type MetaWriteError struct {
	Path string
	Err  error
}

// Error returns a human-readable description of the metadata write failure.
func (e *MetaWriteError) Error() string {
	return "database: failed to write database metadata " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *MetaWriteError) Unwrap() error {
	return e.Err
}

// DropError is returned when a database cannot be dropped (deleted).
type DropError struct {
	Name string
	Err  error
}

// Error returns a human-readable description of the drop failure.
func (e *DropError) Error() string {
	return "database: failed to drop database " + strconv.Quote(e.Name) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *DropError) Unwrap() error {
	return e.Err
}

// DropInProgressError is returned when a Drop is issued for a database that
// already has one in flight on this node.
//
// It exists because Drop reclaims a database's data BEFORE it removes the
// catalog entry, so the entry no longer decides the race the way
// LoadAndDelete used to. Letting both callers run to completion would report
// success twice for one database, and DropForTenant releases the tenant's
// quota on success — so the second success would release quota the tenant
// never held.
type DropInProgressError struct {
	Name string
}

// Error returns a human-readable description of the in-flight drop.
func (e *DropInProgressError) Error() string {
	return "database: drop already in progress for database " + strconv.Quote(e.Name)
}

// DroppingError is returned by Manager.Get when the named database has a
// Drop in flight on this node. Manager.Drop sets this fence (step 0) BEFORE
// it reclaims the database's keyspace, so a caller that resolves the
// database after that point -- rather than before it, as go-qrdb#412 left
// possible -- is refused instead of being served into a keyspace that is
// already being, or about to be, reclaimed. The catalog record itself
// survives until the drop completes, so the database is still
// there for a retry; it is simply no longer resolvable for ordinary
// operations while its drop is in progress (go-qrdb#482).
//
// TenantID is populated from the fenced database's own record so a caller
// that needs it -- DatabaseService.Drop's tenant lookup, in particular --
// can recover it without resolving the database, which is exactly what this
// error refuses (go-qrdb#482 round 3): a node that restarts mid-drop reopens
// the database still fenced (Open restores Dropping from meta.json), so
// Manager.Get keeps returning this error across the restart, and a caller
// that could only get the tenant ID from a successful Get would never be
// able to resume the drop at all.
type DroppingError struct {
	Name     string
	TenantID string
}

// Error returns a human-readable description of the in-flight drop.
func (e *DroppingError) Error() string {
	return "database: database is being dropped: " + strconv.Quote(e.Name)
}

// KeyspaceFenceError is returned when a database's drop fence could not be
// written to -- or, when a database of that name is created, cleared from --
// the Raft group holding its data. A failed drop is rolled back and changes
// nothing, and re-issuing it retries; a failed create creates nothing.
type KeyspaceFenceError struct {
	Name     string
	TenantID string
	Err      error

	// RolledBack reports that the failed drop was rolled back completely:
	// the store fence was cleared, and then the local one.
	RolledBack bool
}

// Error returns a human-readable description of the fence failure.
func (e *KeyspaceFenceError) Error() string {
	return "database: failed to update the drop fence of database " +
		strconv.Quote(e.Name) + " (tenant: " + strconv.Quote(e.TenantID) + "): " + e.Err.Error()
}

// Unwrap returns the underlying store error.
func (e *KeyspaceFenceError) Unwrap() error {
	return e.Err
}

// DropRolledBack reports whether the drop that failed was rolled back
// completely. See DropRolledBack.
func (e *KeyspaceFenceError) DropRolledBack() bool {
	return e.RolledBack
}

// LocalFenceError is returned when a drop could not persist its local fence,
// the first thing it does. Nothing was fenced or reclaimed: the in-memory
// fence is rolled back with it, so the drop changed nothing.
type LocalFenceError struct {
	Name string
	Err  error
}

// Error returns a human-readable description of the failure.
func (e *LocalFenceError) Error() string {
	return "database: failed to persist the drop fence of database " + strconv.Quote(e.Name) + ": " + e.Err.Error()
}

// Unwrap returns the underlying write error.
func (e *LocalFenceError) Unwrap() error {
	return e.Err
}

// DropRolledBack is always true: the drop changed nothing.
func (e *LocalFenceError) DropRolledBack() bool {
	return true
}

// DropRolledBack reports whether err is a failed drop that was rolled back
// completely -- every fence the Manager set is cleared again -- so a caller
// that set fences of its own around the drop may roll those back too. A
// failed drop that could NOT be rolled back leaves the database fenced, and
// the caller's fences must stay with it.
func DropRolledBack(err error) bool {
	var rb interface{ DropRolledBack() bool }
	return errors.As(err, &rb) && rb.DropRolledBack()
}

// ReclaimerRequiredError is returned when a database is created under a name
// this node dropped, while no keyspace reclaimer is attached. The drop left
// a tombstone in the store; only the reclaimer can replace it with the new
// database's live incarnation, so the database would refuse every write. The
// create succeeds once the data plane is up.
type ReclaimerRequiredError struct {
	Name string
}

// Error returns a human-readable description of the refusal.
func (e *ReclaimerRequiredError) Error() string {
	return "database: database " + strconv.Quote(e.Name) +
		" was dropped on this node; creating it again requires the data plane to be attached"
}

// ShardIDClaimError reports that a database could not be created because a
// cluster-wide shard ID could not be claimed. Nothing was created; the
// condition the wrapped error names (a store outage, sustained contention) is
// what decides whether a retry can succeed.
type ShardIDClaimError struct {
	Err error
}

// Error returns a human-readable description of the refusal.
func (e *ShardIDClaimError) Error() string {
	return "database: claim cluster-wide shard ID: " + e.Err.Error()
}

// Unwrap returns the claimer's error.
func (e *ShardIDClaimError) Unwrap() error { return e.Err }

// CallerShardIDError reports a create or a Raft update that named a shard ID.
// Shard IDs are issued by the cluster (Manager.SetShardIDClaimer); a caller's
// ID is never checked against what other nodes hold, so it is refused.
type CallerShardIDError struct {
	Name    string
	ShardID uint64
}

// Error returns a human-readable description of the refusal.
func (e *CallerShardIDError) Error() string {
	return "database: database " + strconv.Quote(e.Name) + " names shard " +
		strconv.FormatUint(e.ShardID, 10) + "; shard IDs are issued by the cluster, never chosen by the caller"
}

// DiscoverySecretRebindError is returned when a Raft update changes what a
// stored discovery secret authenticates -- the static peer set (the static
// block's peers and, under static discovery, the top-level ones; Field is
// "peers" for either), the multicast group or interface, the token endpoint --
// without sending the secret. Addresses compare literally. The
// stored secret is kept only for an unchanged destination: carrying it to a
// new one would give a secret the caller never knew to a destination the
// caller chose. Send the secret with the new destination.
type DiscoverySecretRebindError struct {
	Method string
	Field  string
}

func (e *DiscoverySecretRebindError) Error() string {
	return "database: changing " + e.Method + " discovery's " + e.Field +
		" requires sending its secret again; the stored secret is kept only while " + strconv.Quote(e.Field) + " is unchanged"
}

// RaftConfigRequiredError is returned when an update would clear the Raft
// configuration of a raft-backend database. That configuration carries the
// database's issued shard ID, its identity, so it can be changed but never
// removed.
type RaftConfigRequiredError struct {
	Name string
}

func (e *RaftConfigRequiredError) Error() string {
	return "database: " + strconv.Quote(e.Name) + " is raft-backed; its Raft configuration can be updated, never removed"
}

// ProvisionRaftError reports a provisioned database with a Raft
// configuration. Every node provisions its configured databases on its own,
// and nodes cannot agree on a raft database's shard ID without a claim.
type ProvisionRaftError struct {
	Name string
}

// Error returns a human-readable description of the refusal.
func (e *ProvisionRaftError) Error() string {
	return "database: database " + strconv.Quote(e.Name) +
		" cannot be provisioned with a Raft configuration; create it through the API"
}

// ShardIssuerRequiredError reports a raft database create on a Manager with no
// shard ID claimer: a composition fault.
type ShardIssuerRequiredError struct{}

// Error returns a human-readable description of the refusal.
func (e *ShardIssuerRequiredError) Error() string {
	return "database: no shard ID claimer is configured; raft databases cannot be created"
}

// IncarnationError reports that a new database incarnation could not be
// generated because the system random source failed.
type IncarnationError struct {
	Err error
}

// Error returns a human-readable description of the failure.
func (e *IncarnationError) Error() string {
	return "database: cannot generate a database incarnation: " + e.Err.Error()
}

// Unwrap returns the random source's error.
func (e *IncarnationError) Unwrap() error {
	return e.Err
}

// DefaultDatabaseDropError is returned when a drop names the default
// database. Every tenant's unscoped requests resolve to it and the bootstrap
// records live in it, so it is not a database a caller can remove.
type DefaultDatabaseDropError struct {
	Name string
}

// Error returns a human-readable description of the refusal.
func (e *DefaultDatabaseDropError) Error() string {
	return "database: the default database " + strconv.Quote(e.Name) + " cannot be dropped"
}

// CheckDroppable reports whether name may be dropped: every database except
// the default one, tenant.DefaultDatabase. Each drop entry point calls it
// before it touches anything, so every transport refuses identically.
func CheckDroppable(name string) error {
	if name == tenant.DefaultDatabase {
		return &DefaultDatabaseDropError{Name: name}
	}
	return nil
}

// KeyspaceReclaimError is returned when a database's data could not be
// reclaimed from the store it shares with other databases, which fails the
// whole drop: the database, its catalog entry and its data are all left
// exactly as they were, and re-issuing the drop retries the reclamation.
type KeyspaceReclaimError struct {
	Name     string
	TenantID string
	Err      error

	// RolledBack reports that the failed drop was rolled back completely:
	// the store fence was cleared, and then the local one.
	RolledBack bool
}

// DropRolledBack reports whether the drop that failed was rolled back
// completely. See DropRolledBack.
func (e *KeyspaceReclaimError) DropRolledBack() bool {
	return e.RolledBack
}

// Error returns a human-readable description of the reclamation failure.
func (e *KeyspaceReclaimError) Error() string {
	return "database: failed to reclaim the shared-store keyspace of database " +
		strconv.Quote(e.Name) + " (tenant: " + strconv.Quote(e.TenantID) + "): " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *KeyspaceReclaimError) Unwrap() error {
	return e.Err
}

// StorageCreateError is returned when the storage layer cannot be initialized
// for a database with the specified backend.
type StorageCreateError struct {
	Name    string
	Backend string
	Err     error
}

// Error returns a human-readable description of the storage creation failure.
func (e *StorageCreateError) Error() string {
	return "database: failed to create " + e.Backend + " storage for database " + strconv.Quote(e.Name) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *StorageCreateError) Unwrap() error {
	return e.Err
}

// InvalidModeError is returned when an unrecognized database mode is specified.
type InvalidModeError struct {
	Mode string
}

// Error returns a human-readable description of the invalid mode.
func (e *InvalidModeError) Error() string {
	return "database: invalid database mode: " + strconv.Quote(e.Mode)
}

// ConnectionRequiredError is returned when a remote-mode database is missing
// its connection configuration.
type ConnectionRequiredError struct{}

// Error returns a human-readable description of the missing connection config.
func (e *ConnectionRequiredError) Error() string {
	return "database: connection configuration is required for remote mode"
}

// InvalidRaftClusterModeError is returned when an unrecognized Raft cluster
// mode is specified.
type InvalidRaftClusterModeError struct {
	Mode string
}

// Error returns a human-readable description of the invalid Raft cluster mode.
func (e *InvalidRaftClusterModeError) Error() string {
	return "database: invalid raft cluster mode: " + strconv.Quote(e.Mode)
}

// InvalidDiscoveryMethodError is returned when an unrecognized discovery
// method is specified.
type InvalidDiscoveryMethodError struct {
	Method string
}

// Error returns a human-readable description of the invalid discovery method.
func (e *InvalidDiscoveryMethodError) Error() string {
	return "database: invalid discovery method: " + strconv.Quote(e.Method)
}

// DiscoveryConfigRequiredError is returned when a discovery method is selected
// but its corresponding configuration is missing.
type DiscoveryConfigRequiredError struct {
	Method string
}

// Error returns a human-readable description of the missing discovery config.
func (e *DiscoveryConfigRequiredError) Error() string {
	return "database: discovery configuration required for method: " + strconv.Quote(e.Method)
}

// DiscoverySharedSecretRequiredError is returned when a discovery method
// requires a shared secret but none was provided.
type DiscoverySharedSecretRequiredError struct {
	Method string
}

// Error returns a human-readable description of the missing shared secret.
func (e *DiscoverySharedSecretRequiredError) Error() string {
	return "database: shared secret required for discovery method: " + strconv.Quote(e.Method)
}

// DiscoveryDomainRequiredError is returned when DNS discovery is selected
// but the domain field is empty.
type DiscoveryDomainRequiredError struct{}

// Error returns a human-readable description of the missing domain.
func (e *DiscoveryDomainRequiredError) Error() string {
	return "database: domain is required for dns discovery"
}

// DiscoveryEndpointRequiredError is returned when token discovery is selected
// but the endpoint field is empty.
type DiscoveryEndpointRequiredError struct{}

// Error returns a human-readable description of the missing endpoint.
func (e *DiscoveryEndpointRequiredError) Error() string {
	return "database: endpoint is required for token discovery"
}

// DiscoveryTokenRequiredError is returned when token discovery is selected
// but the token field is empty.
type DiscoveryTokenRequiredError struct{}

// Error returns a human-readable description of the missing token.
func (e *DiscoveryTokenRequiredError) Error() string {
	return "database: token is required for token discovery"
}

// DiscoveryClusterSizeError is returned when the cluster size for token
// discovery is less than 1.
type DiscoveryClusterSizeError struct {
	Size int
}

// Error returns a human-readable description of the invalid cluster size.
func (e *DiscoveryClusterSizeError) Error() string {
	return "database: invalid cluster size for token discovery: " + strconv.Itoa(e.Size)
}

// EtcdListenAddrRequiredError is returned when etcd is enabled but no listen
// address is configured.
type EtcdListenAddrRequiredError struct{}

// Error returns a human-readable description of the missing etcd listen address.
func (e *EtcdListenAddrRequiredError) Error() string {
	return "database: etcd listen_addr is required when etcd is enabled"
}

// MigrationError is returned when a step in the one-time layout migration fails.
type MigrationError struct {
	Step string
	Err  error
}

// Error returns a human-readable description of the migration failure.
func (e *MigrationError) Error() string {
	return "database: migration failed at step " + strconv.Quote(e.Step) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *MigrationError) Unwrap() error {
	return e.Err
}

// TenantIDRequiredError is returned when a tenant-scoped operation is
// called without a tenant ID in the context.
type TenantIDRequiredError struct {
	Operation string
}

// Error returns a human-readable description.
func (e *TenantIDRequiredError) Error() string {
	return "database: tenant ID required for operation: " + e.Operation
}

// SealedError is returned when operations are attempted on a sealed database.
type SealedError struct{}

// Error returns a human-readable description of the sealed condition.
func (e *SealedError) Error() string {
	return "database: operation rejected \u2014 database is sealed"
}

// BarrierDecryptError is returned when barrier decryption fails for a key.
type BarrierDecryptError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the decryption failure.
func (e *BarrierDecryptError) Error() string {
	return "database: barrier decrypt failed for key " + strconv.Quote(e.Key) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *BarrierDecryptError) Unwrap() error {
	return e.Err
}

// BarrierEncryptError is returned when barrier encryption fails for a key.
type BarrierEncryptError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the encryption failure.
func (e *BarrierEncryptError) Error() string {
	return "database: barrier encrypt failed for key " + strconv.Quote(e.Key) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *BarrierEncryptError) Unwrap() error {
	return e.Err
}

// InvalidStorageCodecError is returned when a storage codec name is specified
// that is not registered in the current build.
type InvalidStorageCodecError struct {
	Codec string
}

// Error returns a human-readable description of the invalid storage codec.
func (e *InvalidStorageCodecError) Error() string {
	return "database: invalid or unsupported storage codec: " + strconv.Quote(e.Codec)
}

// InvalidConcurrencyError is returned when a database's SQL concurrency mode
// is neither optimistic nor pessimistic.
type InvalidConcurrencyError struct {
	Concurrency string
}

// Error returns a human-readable description of the invalid mode.
func (e *InvalidConcurrencyError) Error() string {
	return "database: invalid SQL concurrency mode " + strconv.Quote(e.Concurrency) +
		" (want optimistic or pessimistic)"
}

// SharedStoreBarrierUnsupportedError is returned when a caller attempts to
// enable a per-database encryption barrier on a database that has no
// dedicated store of its own (see DatabaseConfig.HasDedicatedStore) -- i.e.
// it shares the system's default store keyspace with other prefix-isolated
// databases. A barrier over shared storage cannot provide real cryptographic
// isolation, so the request fails closed instead of creating inert barrier
// state.
type SharedStoreBarrierUnsupportedError struct {
	Name    string
	Backend BackendType
}

// Error returns a human-readable description of the unsupported request.
func (e *SharedStoreBarrierUnsupportedError) Error() string {
	return "database: encryption barrier requires a dedicated store: database " +
		strconv.Quote(e.Name) + " (backend " + strconv.Quote(string(e.Backend)) +
		") uses the shared default store"
}

// BarrierOffDataPathError is returned when a caller asks for a per-database
// encryption barrier on a database whose data would not be served from the
// store that barrier wraps. The barrier is only on the data path of a
// non-raft database with a store of its own under a tenant that serves the
// database from that store (see DatabaseConfig.BarrierOnDataPath). Anywhere
// else -- a backend: raft database, whose data lives in a Raft group, or any
// database of a volume- or raft-isolated tenant, whose data lives in the
// tenant's own store -- the barrier would wrap an unused store while the data
// stayed readable and writable, and sealing it would protect nothing. The
// request fails closed instead of creating that decorative barrier.
type BarrierOffDataPathError struct {
	Name string
	// ServedBy names the store the database's data is actually served from.
	ServedBy string
}

// Error returns a human-readable description of the refused request.
func (e *BarrierOffDataPathError) Error() string {
	return "database: a per-database barrier would not protect database " +
		strconv.Quote(e.Name) + ": its data is served from " + e.ServedBy +
		", not from a store the barrier wraps"
}

// Barrier data-path descriptions carried by BarrierOffDataPathError.ServedBy.
const (
	// ServedByRaftGroup is the data path of a backend: raft database.
	ServedByRaftGroup = "its raft group"
	// ServedByVolumeTenantStore is the data path of every database of a
	// volume-isolated tenant.
	ServedByVolumeTenantStore = "the volume-isolated tenant's own store"
	// ServedByRaftTenantGroup is the data path of every database of a
	// raft-isolated tenant.
	ServedByRaftTenantGroup = "the raft-isolated tenant's dedicated raft group"
)

// BarrierOffDataPathWarning is the startup warning logged for a
// barrier-enabled database record whose barrier does not wrap the store its
// data is served from: one written before creation refused that shape. The
// database fails closed while sealed and is unprotected once unsealed.
const BarrierOffDataPathWarning = "database barrier is not on the data path; the database fails closed while sealed and is unprotected once unsealed"

// UnsupportedBarrierStrategyError is returned when a database barrier is
// requested with a strategy mode that is neither built in (passphrase, shamir)
// nor held by the barrier manager's strategy registry.
type UnsupportedBarrierStrategyError struct {
	Mode string
}

// Error returns a human-readable description of the unsupported mode.
func (e *UnsupportedBarrierStrategyError) Error() string {
	return "database: unsupported barrier strategy: " + strconv.Quote(e.Mode)
}

// InvalidDataDirError is returned when a caller-supplied per-database
// DataDir cannot be canonicalized (e.g. filepath.Abs or symlink resolution
// fails).
type InvalidDataDirError struct {
	Name    string
	DataDir string
	Err     error
}

// Error returns a human-readable description of the invalid data directory.
func (e *InvalidDataDirError) Error() string {
	return "database: invalid data directory for database " + strconv.Quote(e.Name) +
		" (" + strconv.Quote(e.DataDir) + "): " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *InvalidDataDirError) Unwrap() error {
	return e.Err
}

// DataDirReservedPathError is returned when a caller-supplied per-database
// DataDir resolves to, or overlaps, a path the manager reserves for its own
// catalog or node-internal state (the manager's own data root, its shared
// databases catalog directory, or a database's own meta.json/barrier
// bookkeeping directory).
type DataDirReservedPathError struct {
	Name     string
	DataDir  string
	Reserved string
}

// Error returns a human-readable description of the reserved-path collision.
func (e *DataDirReservedPathError) Error() string {
	return "database: data directory for database " + strconv.Quote(e.Name) +
		" overlaps a reserved path " + strconv.Quote(e.Reserved) + ": " + strconv.Quote(e.DataDir)
}

// DataDirConflictError is returned when a caller-supplied per-database
// DataDir collides -- by equality, ancestry, or descendance -- with another
// already-registered database's own storage directory.
type DataDirConflictError struct {
	Name    string
	DataDir string
	With    string
}

// Error returns a human-readable description of the cross-database conflict.
func (e *DataDirConflictError) Error() string {
	return "database: data directory for database " + strconv.Quote(e.Name) +
		" conflicts with database " + strconv.Quote(e.With) + ": " + strconv.Quote(e.DataDir)
}

// BarrierMigrationNotSupportedError is returned when a caller attempts to
// flip a database's encryption barrier between enabled and disabled via
// UpdateBarrier. Changing barrier state on a database that may already hold
// data requires re-encrypting (enable) or decrypting (disable) every
// existing value under the new state; this API performs neither migration,
// so the barrier's Enabled flag is fixed at database creation time.
type BarrierMigrationNotSupportedError struct {
	Name      string
	Direction string // "enable" or "disable"
}

// Error returns a human-readable description of the unsupported transition.
func (e *BarrierMigrationNotSupportedError) Error() string {
	return "database: cannot " + e.Direction + " the encryption barrier for database " +
		strconv.Quote(e.Name) + " after creation: no re-encryption migration is available; " +
		"create the database with the desired barrier configuration instead"
}

// BarrierEncryptorUnavailableError is returned when a database barrier was
// just registered or initialized but the registry cannot yet produce a
// usable BarrierEncryptor for it. This indicates an internal registry
// inconsistency rather than a caller error.
type BarrierEncryptorUnavailableError struct {
	Name string
}

// Error returns a human-readable description of the unavailable encryptor.
func (e *BarrierEncryptorUnavailableError) Error() string {
	return "database: barrier encryptor unavailable for database " + strconv.Quote(e.Name) + " immediately after registration"
}

// InvalidTenantIsolationError is returned by ResolveBinding for a tenant whose
// isolation mode is not one the binding table knows. Resolution fails closed
// rather than guessing where the tenant's data lives.
type InvalidTenantIsolationError struct {
	TenantID  string
	Isolation string
}

// Error returns a human-readable description of the unknown isolation mode.
func (e *InvalidTenantIsolationError) Error() string {
	return "database: tenant " + strconv.Quote(e.TenantID) + " has unknown isolation mode " +
		strconv.Quote(e.Isolation)
}

// RaftBackendUnavailableError is returned by ResolveBinding for a backend:
// raft database on a standalone node, which has no Raft host to place the
// database's group on.
type RaftBackendUnavailableError struct {
	Name string
}

// Error returns a human-readable description of the refused binding.
func (e *RaftBackendUnavailableError) Error() string {
	return "database: backend \"raft\" for database " + strconv.Quote(e.Name) +
		" requires a node in raft mode; this node runs standalone"
}

// RaftOptionsRequireRaftBackendError is returned by ResolveBinding for a
// database that sets a raft-only option -- replicas, a dedicated WAL or any
// raft setting -- on a backend other than raft. Replication is a property of
// the raft binding only, so the option would be meaningless.
type RaftOptionsRequireRaftBackendError struct {
	Name    string
	Backend BackendType
	// Option names the first raft-only option the record sets.
	Option string
}

// Error returns a human-readable description of the refused option.
func (e *RaftOptionsRequireRaftBackendError) Error() string {
	return "database: option " + strconv.Quote(e.Option) + " of database " + strconv.Quote(e.Name) +
		" requires backend \"raft\", not " + strconv.Quote(string(e.Backend))
}

// BarrierRequiresPersistentBackendError is returned by ResolveBinding for a
// per-database barrier on backend: memory. Nothing of a memory store is at
// rest, so the barrier would be decorative.
type BarrierRequiresPersistentBackendError struct {
	Name string
}

// Error returns a human-readable description of the refused barrier.
func (e *BarrierRequiresPersistentBackendError) Error() string {
	return "database: a per-database barrier on database " + strconv.Quote(e.Name) +
		" requires a persistent backend, not \"memory\""
}

// EtcdRequiresRaftBackendError is returned by ResolveBinding for a database
// that enables the etcd protocol on a backend other than raft: etcd's
// revision counter needs a replicated log.
type EtcdRequiresRaftBackendError struct {
	Name    string
	Backend BackendType
}

// Error returns a human-readable description of the refused protocol.
func (e *EtcdRequiresRaftBackendError) Error() string {
	return "database: the etcd protocol on database " + strconv.Quote(e.Name) +
		" requires backend \"raft\", not " + strconv.Quote(string(e.Backend))
}

// DataDirOutsideTenantRootError is returned by ResolveBinding for an explicit
// data_dir that does not lie under a volume- or raft-isolated tenant's data
// root: that tenant's contract is that its data lives under its root.
type DataDirOutsideTenantRootError struct {
	Name       string
	DataDir    string
	TenantRoot string
}

// Error returns a human-readable description of the refused directory.
func (e *DataDirOutsideTenantRootError) Error() string {
	return "database: data_dir " + strconv.Quote(e.DataDir) + " of database " + strconv.Quote(e.Name) +
		" is outside the tenant's data root " + strconv.Quote(e.TenantRoot)
}

// BackendIsolationConflictError is returned by ResolveBinding for a file or
// objstore store -- as the database's backend or as its raft group's state
// storage -- under a volume- or raft-isolated tenant (OD-7, ruled B). Those
// stores expose key names as file or object names, outside the tenant's
// encryption.
type BackendIsolationConflictError struct {
	Name      string
	Backend   BackendType
	Isolation string
}

// Error returns a human-readable description of the refused backend.
func (e *BackendIsolationConflictError) Error() string {
	return "database: backend " + strconv.Quote(string(e.Backend)) + " of database " + strconv.Quote(e.Name) +
		" exposes key names outside the encryption of a " + strconv.Quote(e.Isolation) + "-isolated tenant"
}

// BindingRootUnresolvedError is returned by ResolveBinding when the directory
// a database's store belongs in cannot be determined: a volume- or
// raft-isolated tenant with no data root on this node, or a node with no data
// directory. Resolution fails closed rather than placing the store elsewhere.
type BindingRootUnresolvedError struct {
	TenantID  string
	Name      string
	Isolation string
}

// Error returns a human-readable description of the unresolved root.
func (e *BindingRootUnresolvedError) Error() string {
	return "database: no data root for database " + strconv.Quote(e.Name) + " of " +
		strconv.Quote(e.Isolation) + "-isolated tenant " + strconv.Quote(e.TenantID)
}

// InvalidRaftStateStorageError is returned by ResolveBinding for a raft
// group's state storage that is not pebble, file or memory (OD-8, ruled A).
type InvalidRaftStateStorageError struct {
	Name         string
	StateStorage BackendType
}

// Error returns a human-readable description of the refused state storage.
func (e *InvalidRaftStateStorageError) Error() string {
	return "database: raft state storage " + strconv.Quote(string(e.StateStorage)) + " of database " +
		strconv.Quote(e.Name) + " is not one of pebble, file, memory"
}
