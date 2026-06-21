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

// Package quota defines per-tenant resource limits and provides a
// Checker interface for enforcing them. A limit value of -1 means
// unlimited and 0 means use the system default.
package quota

import "strconv"

// Quotas holds per-tenant resource limits and current usage counters.
// A Max* value of -1 means unlimited; 0 means use the system default.
// Current* fields track actual usage and are persisted alongside the
// tenant metadata so quota enforcement reads a single record.
type Quotas struct {
	// Configured limits.
	MaxDatabases  int64 `json:"max_databases"`
	MaxRaftGroups int64 `json:"max_raft_groups"`
	// MaxReplicasPerGroup caps the replication factor (number of voting
	// replicas) any single dedicated-raft database may request at create time.
	// -1 means unlimited; 0 means use the system default. Evaluated per group;
	// it carries no cumulative usage counter.
	MaxReplicasPerGroup int64 `json:"max_replicas_per_group"`
	// MaxTotalReplicas caps the aggregate number of replicas a tenant may hold
	// across all of its dedicated-raft groups (the capacity/billing lever).
	// -1 means unlimited; 0 means use the system default.
	MaxTotalReplicas int64 `json:"max_total_replicas"`
	MaxCollections   int64 `json:"max_collections"`
	MaxKeys          int64 `json:"max_keys"`
	MaxStorageBytes  int64 `json:"max_storage_bytes"`
	// MaxDatabaseSizeBytes caps the on-disk size of any single database a
	// tenant may create. -1 means unlimited; 0 means use the system default.
	MaxDatabaseSizeBytes int64 `json:"max_database_size_bytes"`
	MaxUsers             int64 `json:"max_users"`
	MaxRoles             int64 `json:"max_roles"`
	MaxCertificates      int64 `json:"max_certificates"`
	MaxBackups           int64 `json:"max_backups"`
	MaxVectorDims        int64 `json:"max_vector_dims"`
	MaxTSMetrics         int64 `json:"max_ts_metrics"`
	// MaxDedicatedWALGroups caps the number of dedicated-raft groups a tenant
	// may run with a per-tenant-KEY WAL (the tenant's Raft log barrier-encrypted
	// under its own barrier instead of the shared system-barrier WAL). It is the
	// capacity lever for the 4d Option C per-tenant WAL feature. -1 means
	// unlimited; 0 means use the system default (which is 0 — opt-in only).
	MaxDedicatedWALGroups int64 `json:"max_dedicated_wal_groups"`

	// SystemBarrierQuota governs whether an ISOLATED tenant may opt to use the
	// shared SYSTEM barrier for its data instead of a dedicated barrier, and how
	// much system-tenant storage it may consume doing so (in bytes). It is the
	// admin's lever for controlling system-tenant disk consumption:
	//   0  -> the "use the system barrier" option is NOT available to the tenant
	//         (the secure/default-deny value);
	//   -1 -> unlimited;
	//   >0 -> permitted up to this many bytes (checked against
	//         CurrentSystemBarrierBytes).
	SystemBarrierQuota int64 `json:"system_barrier_quota"`

	// Current usage counters (persisted with the tenant record).
	CurrentDatabases          int64 `json:"current_databases,omitempty"`
	CurrentRaftGroups         int64 `json:"current_raft_groups,omitempty"`
	CurrentTotalReplicas      int64 `json:"current_total_replicas,omitempty"`
	CurrentCollections        int64 `json:"current_collections,omitempty"`
	CurrentKeys               int64 `json:"current_keys,omitempty"`
	CurrentStorageBytes       int64 `json:"current_storage_bytes,omitempty"`
	CurrentDatabaseSizeBytes  int64 `json:"current_database_size_bytes,omitempty"`
	CurrentUsers              int64 `json:"current_users,omitempty"`
	CurrentRoles              int64 `json:"current_roles,omitempty"`
	CurrentCertificates       int64 `json:"current_certificates,omitempty"`
	CurrentBackups            int64 `json:"current_backups,omitempty"`
	CurrentVectorDims         int64 `json:"current_vector_dims,omitempty"`
	CurrentTSMetrics          int64 `json:"current_ts_metrics,omitempty"`
	CurrentDedicatedWALGroups int64 `json:"current_dedicated_wal_groups,omitempty"`
	// CurrentSystemBarrierBytes tracks how much system-tenant storage this tenant
	// is consuming via the "use the system barrier" option, checked against
	// SystemBarrierQuota.
	CurrentSystemBarrierBytes int64 `json:"current_system_barrier_bytes,omitempty"`
}

// AllowsSystemBarrier reports whether an isolated tenant may opt to use the
// shared system barrier. The option is available iff SystemBarrierQuota is
// non-zero (>0 = capped at that many bytes, -1 = unlimited); a quota of 0 (the
// default) makes the option unavailable.
func (q *Quotas) AllowsSystemBarrier() bool {
	return q.SystemBarrierQuota != 0
}

// SystemBarrierBytesRemaining returns how many more bytes the tenant may consume
// via the system-barrier option: a large sentinel for unlimited (-1), else the
// configured quota minus current usage (never negative). Zero means either the
// option is unavailable or the quota is exhausted.
func (q *Quotas) SystemBarrierBytesRemaining() int64 {
	if q.SystemBarrierQuota < 0 {
		return 1<<62 - 1
	}
	rem := q.SystemBarrierQuota - q.CurrentSystemBarrierBytes
	if rem < 0 {
		return 0
	}
	return rem
}

// Isolation mode identifiers used by the entitlement layer. These mirror the
// string values of tenant.IsolationType but are owned here so the quota
// package (the lower layer) carries no import dependency on pkg/tenant. Any
// tenant.IsolationType value MUST equal one of these strings.
const (
	// IsolationModePrefix is shared-store logical key-prefix isolation (default).
	IsolationModePrefix = "prefix"
	// IsolationModeBarrier is a per-tenant cryptographic barrier for IAM data.
	IsolationModeBarrier = "barrier"
	// IsolationModeVolume is a dedicated storage volume per database (Phase 2).
	IsolationModeVolume = "volume"
	// IsolationModeRaft is a dedicated Raft group per database (Phase 4).
	IsolationModeRaft = "raft"
)

// Feature identifiers checked by EntitlementChecker.CheckFeature.
const (
	// FeatureDedicatedRaft gates a tenant's ability to provision dedicated
	// Raft-backed databases.
	FeatureDedicatedRaft = "dedicated_raft"
	// FeatureVolumeStorage gates a tenant's ability to provision databases on
	// a dedicated storage volume.
	FeatureVolumeStorage = "volume_storage"
	// FeatureSystemManagedBarrier gates a tenant's ability to opt into the
	// system-managed auto-unseal key-management mode for its dedicated barrier
	// (IsolationBarrier / IsolationVolume). When denied, the tenant must
	// self-manage its barrier keys (the secure default).
	FeatureSystemManagedBarrier = "system_managed_barrier"
	// FeatureDedicatedWAL gates a tenant's ability to provision dedicated-raft
	// databases with a per-tenant-KEY WAL (4d Option C). It is orthogonal to
	// FeatureDedicatedRaft: a tenant may run dedicated-raft groups on the shared
	// system-barrier WAL (the default) without this feature; the feature only
	// permits opting an individual group into its own barrier-encrypted WAL.
	FeatureDedicatedWAL = "dedicated_wal"
)

// Entitlements describes which isolation tiers and dedicated-storage features a
// tenant's plan permits. It is the feature-governance plane, orthogonal to both
// numeric Quotas (how many) and RBAC permissions (who may call). A nil
// Entitlements value means the tenant inherits the configured system defaults.
type Entitlements struct {
	// AllowedIsolationModes lists the isolation modes the tenant may use, e.g.
	// "prefix", "barrier", "volume", "raft". It is a []string (rather than a
	// tenant.IsolationType slice) to avoid an import cycle with pkg/tenant.
	AllowedIsolationModes []string `json:"allowed_isolation_modes,omitempty"`
	// AllowDedicatedRaft permits provisioning dedicated Raft-backed databases.
	AllowDedicatedRaft bool `json:"allow_dedicated_raft,omitempty"`
	// AllowVolumeStorage permits provisioning databases on a dedicated volume.
	AllowVolumeStorage bool `json:"allow_volume_storage,omitempty"`
	// AllowSystemManagedBarrier permits a tenant to opt into the system-managed
	// auto-unseal key-management mode for its dedicated barrier. When false
	// (the secure default), the tenant must self-manage its barrier keys: a
	// request for system-managed auto-unseal is denied with an EntitlementError.
	AllowSystemManagedBarrier bool `json:"allow_system_managed_barrier,omitempty"`
	// AllowDedicatedWAL permits a tenant to opt a dedicated-raft database into a
	// per-tenant-KEY WAL (4d Option C). When false (the secure default), a
	// DedicatedWAL create request is denied with an EntitlementError and the
	// group falls back to the shared system-barrier WAL.
	AllowDedicatedWAL bool `json:"allow_dedicated_wal,omitempty"`

	// AllowedBarrierModes lists the dedicated-barrier sealing strategies an
	// isolated tenant may select for its own barrier (e.g. "none", "passphrase",
	// "shamir", or an xkms-registered hardware strategy id like "tpm2"/"pkcs11").
	// An EMPTY list means the built-in software defaults (none/passphrase/shamir)
	// are permitted and any hardware strategy must be explicitly granted — so
	// hardware modes are fail-closed. A non-empty list is the exact allowed set.
	// It does NOT govern the "use the shared system barrier" option, which is
	// gated by the SystemBarrierQuota (see Quotas).
	AllowedBarrierModes []string `json:"allowed_barrier_modes,omitempty"`
}

// defaultBarrierModes are the built-in software sealing strategies an isolated
// tenant may select when AllowedBarrierModes is unset. Hardware strategies are
// intentionally excluded (they must be explicitly granted).
var defaultBarrierModes = map[string]struct{}{
	"none":       {},
	"passphrase": {},
	"shamir":     {},
}

// AllowsIsolation reports whether mode is present in AllowedIsolationModes.
func (e *Entitlements) AllowsIsolation(mode string) bool {
	for _, m := range e.AllowedIsolationModes {
		if m == mode {
			return true
		}
	}
	return false
}

// AllowsBarrierMode reports whether an isolated tenant may select the given
// dedicated-barrier sealing strategy. With an empty AllowedBarrierModes the
// built-in software defaults (none/passphrase/shamir) are permitted and all
// other modes (hardware) are denied; with a non-empty list only listed modes are
// permitted. The "software" alias is normalized to "passphrase".
func (e *Entitlements) AllowsBarrierMode(mode string) bool {
	if mode == "software" {
		mode = "passphrase"
	}
	if len(e.AllowedBarrierModes) == 0 {
		_, ok := defaultBarrierModes[mode]
		return ok
	}
	for _, m := range e.AllowedBarrierModes {
		if m == mode || (m == "software" && mode == "passphrase") {
			return true
		}
	}
	return false
}

// AllowsFeature reports whether the named feature is enabled. Unknown feature
// names return false (fail closed).
func (e *Entitlements) AllowsFeature(feature string) bool {
	switch feature {
	case FeatureDedicatedRaft:
		return e.AllowDedicatedRaft
	case FeatureVolumeStorage:
		return e.AllowVolumeStorage
	case FeatureSystemManagedBarrier:
		return e.AllowSystemManagedBarrier
	case FeatureDedicatedWAL:
		return e.AllowDedicatedWAL
	default:
		return false
	}
}

// DefaultEntitlements returns the conservative built-in entitlement defaults
// used when neither the tenant nor the operator configures entitlements: only
// shared prefix isolation is permitted, and dedicated raft/volume tiers are
// denied. Each call returns a fresh value so callers may mutate it safely.
func DefaultEntitlements() *Entitlements {
	return &Entitlements{
		AllowedIsolationModes: []string{IsolationModePrefix},
	}
}

// Resource identifies a quota-limited resource type.
type Resource int

const (
	// ResourceDatabases limits the number of databases a tenant may create.
	ResourceDatabases Resource = iota
	// ResourceRaftGroups limits the number of Raft consensus groups a tenant may use.
	ResourceRaftGroups
	// ResourceReplicasPerGroup caps the replication factor of any single
	// dedicated-raft database. It is evaluated per group against a zero current
	// usage (groups do not accumulate against it; the aggregate budget is
	// ResourceTotalReplicas).
	ResourceReplicasPerGroup
	// ResourceTotalReplicas limits the aggregate number of replicas a tenant may
	// hold across all of its dedicated-raft groups.
	ResourceTotalReplicas
	// ResourceCollections limits the number of collections a tenant may create.
	ResourceCollections
	// ResourceKeys limits the total number of keys a tenant may store.
	ResourceKeys
	// ResourceStorageBytes limits the total storage in bytes a tenant may consume.
	ResourceStorageBytes
	// ResourceDatabaseSizeBytes limits the on-disk size in bytes of any single
	// database a tenant may create.
	ResourceDatabaseSizeBytes
	// ResourceUsers limits the number of users a tenant may create.
	ResourceUsers
	// ResourceRoles limits the number of roles a tenant may create.
	ResourceRoles
	// ResourceCertificates limits the number of certificates a tenant may issue.
	ResourceCertificates
	// ResourceBackups limits the number of backups a tenant may retain.
	ResourceBackups
	// ResourceVectorDims limits the maximum vector dimensionality a tenant may use.
	ResourceVectorDims
	// ResourceTSMetrics limits the number of time-series metrics a tenant may track.
	ResourceTSMetrics
	// ResourceDedicatedWALGroups limits the number of dedicated-raft groups a
	// tenant may run with a per-tenant-KEY WAL (4d Option C).
	ResourceDedicatedWALGroups
)

// resourceNames maps each Resource to its human-readable string name.
var resourceNames = map[Resource]string{
	ResourceDatabases:          "databases",
	ResourceRaftGroups:         "raft_groups",
	ResourceReplicasPerGroup:   "replicas_per_group",
	ResourceTotalReplicas:      "total_replicas",
	ResourceCollections:        "collections",
	ResourceKeys:               "keys",
	ResourceStorageBytes:       "storage_bytes",
	ResourceDatabaseSizeBytes:  "database_size_bytes",
	ResourceUsers:              "users",
	ResourceRoles:              "roles",
	ResourceCertificates:       "certificates",
	ResourceBackups:            "backups",
	ResourceVectorDims:         "vector_dims",
	ResourceTSMetrics:          "ts_metrics",
	ResourceDedicatedWALGroups: "dedicated_wal_groups",
}

// String returns the string representation of the resource type.
func (r Resource) String() string {
	if name, ok := resourceNames[r]; ok {
		return name
	}
	return "unknown(" + strconv.Itoa(int(r)) + ")"
}

// resourceLimits maps each Resource to the corresponding limit accessor.
var resourceLimits = map[Resource]func(*Quotas) int64{
	ResourceDatabases:          func(q *Quotas) int64 { return q.MaxDatabases },
	ResourceRaftGroups:         func(q *Quotas) int64 { return q.MaxRaftGroups },
	ResourceReplicasPerGroup:   func(q *Quotas) int64 { return q.MaxReplicasPerGroup },
	ResourceTotalReplicas:      func(q *Quotas) int64 { return q.MaxTotalReplicas },
	ResourceCollections:        func(q *Quotas) int64 { return q.MaxCollections },
	ResourceKeys:               func(q *Quotas) int64 { return q.MaxKeys },
	ResourceStorageBytes:       func(q *Quotas) int64 { return q.MaxStorageBytes },
	ResourceDatabaseSizeBytes:  func(q *Quotas) int64 { return q.MaxDatabaseSizeBytes },
	ResourceUsers:              func(q *Quotas) int64 { return q.MaxUsers },
	ResourceRoles:              func(q *Quotas) int64 { return q.MaxRoles },
	ResourceCertificates:       func(q *Quotas) int64 { return q.MaxCertificates },
	ResourceBackups:            func(q *Quotas) int64 { return q.MaxBackups },
	ResourceVectorDims:         func(q *Quotas) int64 { return q.MaxVectorDims },
	ResourceTSMetrics:          func(q *Quotas) int64 { return q.MaxTSMetrics },
	ResourceDedicatedWALGroups: func(q *Quotas) int64 { return q.MaxDedicatedWALGroups },
}

// resourceCurrentUsage maps each Resource to its current-usage accessor.
var resourceCurrentUsage = map[Resource]func(*Quotas) int64{
	ResourceDatabases:  func(q *Quotas) int64 { return q.CurrentDatabases },
	ResourceRaftGroups: func(q *Quotas) int64 { return q.CurrentRaftGroups },
	// ResourceReplicasPerGroup is a per-group cap with no cumulative usage; its
	// current usage is always zero so a CheckN reduces to "requested <= cap".
	ResourceReplicasPerGroup:   func(q *Quotas) int64 { return 0 },
	ResourceTotalReplicas:      func(q *Quotas) int64 { return q.CurrentTotalReplicas },
	ResourceCollections:        func(q *Quotas) int64 { return q.CurrentCollections },
	ResourceKeys:               func(q *Quotas) int64 { return q.CurrentKeys },
	ResourceStorageBytes:       func(q *Quotas) int64 { return q.CurrentStorageBytes },
	ResourceDatabaseSizeBytes:  func(q *Quotas) int64 { return q.CurrentDatabaseSizeBytes },
	ResourceUsers:              func(q *Quotas) int64 { return q.CurrentUsers },
	ResourceRoles:              func(q *Quotas) int64 { return q.CurrentRoles },
	ResourceCertificates:       func(q *Quotas) int64 { return q.CurrentCertificates },
	ResourceBackups:            func(q *Quotas) int64 { return q.CurrentBackups },
	ResourceVectorDims:         func(q *Quotas) int64 { return q.CurrentVectorDims },
	ResourceTSMetrics:          func(q *Quotas) int64 { return q.CurrentTSMetrics },
	ResourceDedicatedWALGroups: func(q *Quotas) int64 { return q.CurrentDedicatedWALGroups },
}

// resourceSetCurrent maps each Resource to a setter that mutates the
// corresponding current-usage field in-place.
var resourceSetCurrent = map[Resource]func(*Quotas, int64){
	ResourceDatabases:  func(q *Quotas, v int64) { q.CurrentDatabases = v },
	ResourceRaftGroups: func(q *Quotas, v int64) { q.CurrentRaftGroups = v },
	// ResourceReplicasPerGroup carries no cumulative usage; the setter is a no-op.
	ResourceReplicasPerGroup:   func(q *Quotas, v int64) {},
	ResourceTotalReplicas:      func(q *Quotas, v int64) { q.CurrentTotalReplicas = v },
	ResourceCollections:        func(q *Quotas, v int64) { q.CurrentCollections = v },
	ResourceKeys:               func(q *Quotas, v int64) { q.CurrentKeys = v },
	ResourceStorageBytes:       func(q *Quotas, v int64) { q.CurrentStorageBytes = v },
	ResourceDatabaseSizeBytes:  func(q *Quotas, v int64) { q.CurrentDatabaseSizeBytes = v },
	ResourceUsers:              func(q *Quotas, v int64) { q.CurrentUsers = v },
	ResourceRoles:              func(q *Quotas, v int64) { q.CurrentRoles = v },
	ResourceCertificates:       func(q *Quotas, v int64) { q.CurrentCertificates = v },
	ResourceBackups:            func(q *Quotas, v int64) { q.CurrentBackups = v },
	ResourceVectorDims:         func(q *Quotas, v int64) { q.CurrentVectorDims = v },
	ResourceTSMetrics:          func(q *Quotas, v int64) { q.CurrentTSMetrics = v },
	ResourceDedicatedWALGroups: func(q *Quotas, v int64) { q.CurrentDedicatedWALGroups = v },
}

// Limit returns the configured limit for the given resource. Returns
// an error if the resource type is not recognized.
func (q *Quotas) Limit(r Resource) (int64, error) {
	fn, ok := resourceLimits[r]
	if !ok {
		return 0, &InvalidResourceError{Resource: r}
	}
	return fn(q), nil
}

// CurrentUsage returns the tracked current usage for the given resource.
// Returns an error if the resource type is not recognized.
func (q *Quotas) CurrentUsage(r Resource) (int64, error) {
	fn, ok := resourceCurrentUsage[r]
	if !ok {
		return 0, &InvalidResourceError{Resource: r}
	}
	return fn(q), nil
}

// SetCurrentUsage updates the tracked current usage for the given resource
// to the supplied value. Returns an error if the resource type is not
// recognized. Negative values are clamped to zero.
func (q *Quotas) SetCurrentUsage(r Resource, v int64) error {
	fn, ok := resourceSetCurrent[r]
	if !ok {
		return &InvalidResourceError{Resource: r}
	}
	if v < 0 {
		v = 0
	}
	fn(q, v)
	return nil
}
