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

// Quotas holds per-tenant resource limits. A value of -1 means unlimited
// and 0 means use the system default.
type Quotas struct {
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

// resourceLimits maps each Resource to the corresponding field accessor
// on Quotas.
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

// Limit returns the configured limit for the given resource. Returns
// an error if the resource type is not recognized.
func (q *Quotas) Limit(r Resource) (int64, error) {
	fn, ok := resourceLimits[r]
	if !ok {
		return 0, &InvalidResourceError{Resource: r}
	}
	return fn(q), nil
}
