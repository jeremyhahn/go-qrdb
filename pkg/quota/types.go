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
	MaxDatabases    int64 `json:"max_databases"`
	MaxRaftGroups   int64 `json:"max_raft_groups"`
	MaxCollections  int64 `json:"max_collections"`
	MaxKeys         int64 `json:"max_keys"`
	MaxStorageBytes int64 `json:"max_storage_bytes"`
	MaxUsers        int64 `json:"max_users"`
	MaxRoles        int64 `json:"max_roles"`
	MaxCertificates int64 `json:"max_certificates"`
	MaxBackups      int64 `json:"max_backups"`
	MaxVectorDims   int64 `json:"max_vector_dims"`
	MaxTSMetrics    int64 `json:"max_ts_metrics"`

	// Current usage counters (persisted with the tenant record).
	CurrentDatabases    int64 `json:"current_databases,omitempty"`
	CurrentRaftGroups   int64 `json:"current_raft_groups,omitempty"`
	CurrentCollections  int64 `json:"current_collections,omitempty"`
	CurrentKeys         int64 `json:"current_keys,omitempty"`
	CurrentStorageBytes int64 `json:"current_storage_bytes,omitempty"`
	CurrentUsers        int64 `json:"current_users,omitempty"`
	CurrentRoles        int64 `json:"current_roles,omitempty"`
	CurrentCertificates int64 `json:"current_certificates,omitempty"`
	CurrentBackups      int64 `json:"current_backups,omitempty"`
	CurrentVectorDims   int64 `json:"current_vector_dims,omitempty"`
	CurrentTSMetrics    int64 `json:"current_ts_metrics,omitempty"`
}

// Resource identifies a quota-limited resource type.
type Resource int

const (
	// ResourceDatabases limits the number of databases a tenant may create.
	ResourceDatabases Resource = iota
	// ResourceRaftGroups limits the number of Raft consensus groups a tenant may use.
	ResourceRaftGroups
	// ResourceCollections limits the number of collections a tenant may create.
	ResourceCollections
	// ResourceKeys limits the total number of keys a tenant may store.
	ResourceKeys
	// ResourceStorageBytes limits the total storage in bytes a tenant may consume.
	ResourceStorageBytes
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
)

// resourceNames maps each Resource to its human-readable string name.
var resourceNames = map[Resource]string{
	ResourceDatabases:    "databases",
	ResourceRaftGroups:   "raft_groups",
	ResourceCollections:  "collections",
	ResourceKeys:         "keys",
	ResourceStorageBytes: "storage_bytes",
	ResourceUsers:        "users",
	ResourceRoles:        "roles",
	ResourceCertificates: "certificates",
	ResourceBackups:      "backups",
	ResourceVectorDims:   "vector_dims",
	ResourceTSMetrics:    "ts_metrics",
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
	ResourceDatabases:    func(q *Quotas) int64 { return q.MaxDatabases },
	ResourceRaftGroups:   func(q *Quotas) int64 { return q.MaxRaftGroups },
	ResourceCollections:  func(q *Quotas) int64 { return q.MaxCollections },
	ResourceKeys:         func(q *Quotas) int64 { return q.MaxKeys },
	ResourceStorageBytes: func(q *Quotas) int64 { return q.MaxStorageBytes },
	ResourceUsers:        func(q *Quotas) int64 { return q.MaxUsers },
	ResourceRoles:        func(q *Quotas) int64 { return q.MaxRoles },
	ResourceCertificates: func(q *Quotas) int64 { return q.MaxCertificates },
	ResourceBackups:      func(q *Quotas) int64 { return q.MaxBackups },
	ResourceVectorDims:   func(q *Quotas) int64 { return q.MaxVectorDims },
	ResourceTSMetrics:    func(q *Quotas) int64 { return q.MaxTSMetrics },
}

// resourceCurrentUsage maps each Resource to its current-usage accessor.
var resourceCurrentUsage = map[Resource]func(*Quotas) int64{
	ResourceDatabases:    func(q *Quotas) int64 { return q.CurrentDatabases },
	ResourceRaftGroups:   func(q *Quotas) int64 { return q.CurrentRaftGroups },
	ResourceCollections:  func(q *Quotas) int64 { return q.CurrentCollections },
	ResourceKeys:         func(q *Quotas) int64 { return q.CurrentKeys },
	ResourceStorageBytes: func(q *Quotas) int64 { return q.CurrentStorageBytes },
	ResourceUsers:        func(q *Quotas) int64 { return q.CurrentUsers },
	ResourceRoles:        func(q *Quotas) int64 { return q.CurrentRoles },
	ResourceCertificates: func(q *Quotas) int64 { return q.CurrentCertificates },
	ResourceBackups:      func(q *Quotas) int64 { return q.CurrentBackups },
	ResourceVectorDims:   func(q *Quotas) int64 { return q.CurrentVectorDims },
	ResourceTSMetrics:    func(q *Quotas) int64 { return q.CurrentTSMetrics },
}

// resourceSetCurrent maps each Resource to a setter that mutates the
// corresponding current-usage field in-place.
var resourceSetCurrent = map[Resource]func(*Quotas, int64){
	ResourceDatabases:    func(q *Quotas, v int64) { q.CurrentDatabases = v },
	ResourceRaftGroups:   func(q *Quotas, v int64) { q.CurrentRaftGroups = v },
	ResourceCollections:  func(q *Quotas, v int64) { q.CurrentCollections = v },
	ResourceKeys:         func(q *Quotas, v int64) { q.CurrentKeys = v },
	ResourceStorageBytes: func(q *Quotas, v int64) { q.CurrentStorageBytes = v },
	ResourceUsers:        func(q *Quotas, v int64) { q.CurrentUsers = v },
	ResourceRoles:        func(q *Quotas, v int64) { q.CurrentRoles = v },
	ResourceCertificates: func(q *Quotas, v int64) { q.CurrentCertificates = v },
	ResourceBackups:      func(q *Quotas, v int64) { q.CurrentBackups = v },
	ResourceVectorDims:   func(q *Quotas, v int64) { q.CurrentVectorDims = v },
	ResourceTSMetrics:    func(q *Quotas, v int64) { q.CurrentTSMetrics = v },
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
