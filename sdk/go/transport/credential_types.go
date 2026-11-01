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

// CredentialSubmitRequest holds a named credential value for submission.
// Value is []byte so arbitrary binary content (private keys, certificates,
// random tokens) round-trips byte-for-byte across every transport. Over the
// JSON transports (REST, MCP) encoding/json renders it as base64 (RFC 4648);
// over gRPC/QUIC/unix it maps to a protobuf bytes field.
type CredentialSubmitRequest struct {
	Name  string `json:"name"`
	Value []byte `json:"value"`
}

// CredentialStrategyResponse describes the active credential management
// strategy.
type CredentialStrategyResponse struct {
	Strategy string `json:"strategy"`
}

// CredentialDeleteRequest identifies a credential to delete by name.
type CredentialDeleteRequest struct {
	Name string `json:"name"`
}

// =============================================================================
// Versioned rotation surface
// =============================================================================

// CredentialListRequest is the (empty) request shape for enumerating
// credential names. Reserved as a struct so future filters (tenant,
// prefix, label) can be added without breaking the wire format.
type CredentialListRequest struct{}

// CredentialListResponse carries the sorted list of credential names.
// Values are never returned by enumeration.
type CredentialListResponse struct {
	Names []string `json:"names"`
}

// CredentialRotateRequest captures all inputs to a single
// CredentialRotate call. GracePeriodSeconds == 0 means "no grace":
// the previous value is wiped immediately.
type CredentialRotateRequest struct {
	Name string `json:"name"`
	// NewValue is []byte so binary credential material round-trips
	// byte-for-byte. JSON transports render it as base64 (RFC 4648);
	// gRPC/QUIC/unix map it to a protobuf bytes field.
	NewValue           []byte `json:"new_value"`
	GracePeriodSeconds int64  `json:"grace_period_seconds"`
}

// CredentialRotateResponse describes the outcome of a rotation:
// previous + new version numbers and (for grace>0) the wall-clock
// time at which the previous version stops being retrievable.
type CredentialRotateResponse struct {
	Name            string `json:"name"`
	PreviousVersion uint64 `json:"previous_version"`
	NewVersion      uint64 `json:"new_version"`
	GraceUntilUnix  int64  `json:"grace_until_unix,omitempty"`
}

// CredentialSetRotationPolicyRequest sets a named credential's scheduled
// rotation policy. Interval is a Go duration string ("720h"); it may be
// empty when Enabled is false. A disabled policy stops scheduled rotation.
type CredentialSetRotationPolicyRequest struct {
	Name           string `json:"name"`
	Enabled        bool   `json:"enabled"`
	Interval       string `json:"interval"`
	RetainVersions int    `json:"retain_versions"`
}

// CredentialRotationPolicyRequest names the credential whose rotation
// policy is read.
type CredentialRotationPolicyRequest struct {
	Name string `json:"name"`
}

// CredentialRotationPolicyResponse is a named credential's scheduled
// rotation policy and its schedule. Interval is a Go duration string.
// NextRotation is the RFC 3339 time of the next scheduled rotation, empty
// when the policy is unset or disabled.
type CredentialRotationPolicyResponse struct {
	Name           string `json:"name"`
	Enabled        bool   `json:"enabled"`
	Interval       string `json:"interval"`
	RetainVersions int    `json:"retain_versions"`
	NextRotation   string `json:"next_rotation,omitempty"`
}

// CredentialMigrationConflict is one credential whose node-local copy in
// the old credentials/ layout differs from the replicated record another
// node migrated first. The flags name the parts that differ; no value is
// ever carried.
type CredentialMigrationConflict struct {
	Name     string `json:"name"`
	Value    bool   `json:"value"`
	Versions bool   `json:"versions"`
	Policy   bool   `json:"policy"`
}

// CredentialMigrationConflictsResponse lists the node's unresolved
// migration conflicts, sorted by name.
type CredentialMigrationConflictsResponse struct {
	Conflicts []CredentialMigrationConflict `json:"conflicts"`
}

// CredentialResolveMigrationConflictRequest settles one migration
// conflict. Resolution is "import", which rotates the node-local current
// value in as a new version, or "discard", which keeps the replicated
// record as it is.
type CredentialResolveMigrationConflictRequest struct {
	Name       string `json:"name"`
	Resolution string `json:"resolution"`
}

// CredentialRetrieveVersionRequest selects a specific version of a
// credential to retrieve. Version == 0 means "current version" and
// is equivalent to a non-versioned retrieve.
type CredentialRetrieveVersionRequest struct {
	Name    string `json:"name"`
	Version uint64 `json:"version"`
}

// CredentialRetrieveVersionResponse carries the credential value for
// the requested version. Value is []byte so binary credential material
// round-trips byte-for-byte; JSON transports render it as base64
// (RFC 4648). Value is empty on error responses.
type CredentialRetrieveVersionResponse struct {
	Value []byte `json:"value"`
}

// CredentialListVersionsRequest identifies the credential whose
// version history is being requested.
type CredentialListVersionsRequest struct {
	Name string `json:"name"`
}

// CredentialVersionInfo is the wire-format version metadata entry.
// Value is intentionally absent — use CredentialRetrieveVersion to
// fetch the bytes for a specific version.
type CredentialVersionInfo struct {
	Version        uint64 `json:"version"`
	IsCurrent      bool   `json:"is_current"`
	CreatedAtUnix  int64  `json:"created_at_unix,omitempty"`
	GraceUntilUnix int64  `json:"grace_until_unix,omitempty"`
}

// CredentialListVersionsResponse returns one entry per non-expired
// version (current + grace history).
type CredentialListVersionsResponse struct {
	Name     string                  `json:"name"`
	Versions []CredentialVersionInfo `json:"versions"`
}

// CredentialGCRequest names the credential whose expired grace
// versions should be reaped.
type CredentialGCRequest struct {
	Name string `json:"name"`
}

// CredentialGCResponse reports how many historical versions were
// reaped during the call.
type CredentialGCResponse struct {
	Reaped int `json:"reaped"`
}
