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
type CredentialSubmitRequest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
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
	Name               string `json:"name"`
	NewValue           string `json:"new_value"`
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

// CredentialRetrieveVersionRequest selects a specific version of a
// credential to retrieve. Version == 0 means "current version" and
// is equivalent to a non-versioned retrieve.
type CredentialRetrieveVersionRequest struct {
	Name    string `json:"name"`
	Version uint64 `json:"version"`
}

// CredentialRetrieveVersionResponse carries the credential value for
// the requested version. Value is empty on error responses.
type CredentialRetrieveVersionResponse struct {
	Value string `json:"value"`
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
