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

// TenantDEKRequest names the tenant a keyring call acts on. Empty means the
// caller's own tenant, which is also the only tenant the server accepts: a
// request naming another tenant is refused, from the system tenant too.
type TenantDEKRequest struct {
	TenantID string `json:"tenant_id,omitempty"`
}

// TenantDEKRetireRequest names the key version to retire.
type TenantDEKRetireRequest struct {
	TenantID string `json:"tenant_id,omitempty"`
	Version  uint64 `json:"version"`
}

// TenantDEKVersion describes one keyring version.
type TenantDEKVersion struct {
	Version   uint64    `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	// RetiredAt is set once the version's key has been destroyed.
	RetiredAt *time.Time `json:"retired_at,omitempty"`
}

// TenantDEKStatusResponse describes a tenant's keyring. It never carries key
// material. CurrentVersion 0 means the tenant has no keyring yet; the first
// encrypt or rotate creates version 1.
type TenantDEKStatusResponse struct {
	TenantID       string             `json:"tenant_id"`
	Algorithm      string             `json:"algorithm"`
	CurrentVersion uint64             `json:"current_version"`
	Versions       []TenantDEKVersion `json:"versions"`
}

// TenantDEKEncryptRequest carries data to seal under the tenant's current
// key version. AAD is bound into the ciphertext and must be presented
// unchanged to decrypt and rewrap. Byte fields are base64 in JSON.
type TenantDEKEncryptRequest struct {
	TenantID  string `json:"tenant_id,omitempty"`
	Plaintext []byte `json:"plaintext"`
	AAD       []byte `json:"aad,omitempty"`
}

// TenantDEKEncryptResponse carries the opaque ciphertext and the key version
// it was sealed under. The version is informational: the ciphertext carries
// it internally.
type TenantDEKEncryptResponse struct {
	Ciphertext []byte `json:"ciphertext"`
	Version    uint64 `json:"version"`
}

// TenantDEKDecryptRequest carries a ciphertext and its associated data.
type TenantDEKDecryptRequest struct {
	TenantID   string `json:"tenant_id,omitempty"`
	Ciphertext []byte `json:"ciphertext"`
	AAD        []byte `json:"aad,omitempty"`
}

// TenantDEKDecryptResponse carries the plaintext and the key version it was
// sealed under.
type TenantDEKDecryptResponse struct {
	Plaintext []byte `json:"plaintext"`
	Version   uint64 `json:"version"`
}

// TenantDEKRewrapRequest carries a ciphertext to move to the current key
// version.
type TenantDEKRewrapRequest struct {
	TenantID   string `json:"tenant_id,omitempty"`
	Ciphertext []byte `json:"ciphertext"`
	AAD        []byte `json:"aad,omitempty"`
}

// TenantDEKRewrapResponse carries the re-sealed ciphertext. FromVersion and
// ToVersion are equal when the input was already current, in which case the
// ciphertext is returned unchanged.
type TenantDEKRewrapResponse struct {
	Ciphertext  []byte `json:"ciphertext"`
	FromVersion uint64 `json:"from_version"`
	ToVersion   uint64 `json:"to_version"`
}
