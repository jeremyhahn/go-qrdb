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

package transport

import "time"

// DEKInitializeRequest holds the parameters for initializing the cluster DEK.
type DEKInitializeRequest struct {
	Algorithm string `json:"algorithm"`
	KeyBytes  int    `json:"key_bytes"`
}

// DEKStatusResponse returns the current cluster DEK status.
type DEKStatusResponse struct {
	Initialized  bool                 `json:"initialized"`
	Version      uint64               `json:"version"`
	Algorithm    string               `json:"algorithm"`
	KeyBytes     int                  `json:"key_bytes"`
	CreatedAt    time.Time            `json:"created_at"`
	SyncedNodes  map[uint64]string    `json:"synced_nodes"`
	Reencryption *DEKReencryptionInfo `json:"reencryption,omitempty"`
}

// DEKReencryptionInfo provides information about ongoing re-encryption.
type DEKReencryptionInfo struct {
	Active        bool      `json:"active"`
	StartedAt     time.Time `json:"started_at"`
	TotalKeys     uint64    `json:"total_keys"`
	ProcessedKeys uint64    `json:"processed_keys"`
	OldVersion    uint64    `json:"old_version"`
	NewVersion    uint64    `json:"new_version"`
	Progress      float64   `json:"progress"`
}

// DEKCurrentResponse returns the current DEK metadata.
type DEKCurrentResponse struct {
	Version   uint64 `json:"version"`
	Algorithm string `json:"algorithm"`
	KeyBytes  int    `json:"key_bytes"`
}

// DEKSyncRequest holds the node ID to sync the DEK to.
type DEKSyncRequest struct {
	NodeID uint64 `json:"node_id"`
}

// TenantDEKRequest holds the tenant ID for DEK operations.
type TenantDEKRequest struct {
	TenantID string `json:"tenant_id"`
}

// TenantDEKResponse returns a tenant DEK.
type TenantDEKResponse struct {
	TenantID  string `json:"tenant_id"`
	Version   uint64 `json:"version"`
	Algorithm string `json:"algorithm"`
	KeyBytes  int    `json:"key_bytes"`
}

// TenantDEKRotateRequest holds parameters for rotating a tenant DEK.
type TenantDEKRotateRequest struct {
	TenantID string `json:"tenant_id"`
}

// TenantDEKDeleteRequest holds parameters for deleting a tenant DEK.
type TenantDEKDeleteRequest struct {
	TenantID string `json:"tenant_id"`
}

// TenantDEKStatusResponse returns the status of a tenant DEK.
type TenantDEKStatusResponse struct {
	TenantID   string    `json:"tenant_id"`
	Version    uint64    `json:"version"`
	Algorithm  string    `json:"algorithm"`
	KeyBytes   int       `json:"key_bytes"`
	CreatedAt  time.Time `json:"created_at"`
	RotatedAt  time.Time `json:"rotated_at,omitempty"`
	AccessedAt time.Time `json:"accessed_at,omitempty"`
}

// TenantDEKListResponse returns a list of tenant IDs with DEKs.
type TenantDEKListResponse struct {
	TenantIDs []string `json:"tenant_ids"`
}
