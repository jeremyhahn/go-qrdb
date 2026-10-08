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

package dek

import "context"

// Provider is the tenant DEK seam: the engine behind a tenant's keyring that
// seals and opens the caller's data.
//
// Every method acts on the tenant it is given. Tenant scoping -- a request may
// reach only the caller's own keyring -- is enforced above this interface, by
// service.DEKService, so an implementation sees only the tenant already
// resolved.
//
// The interface carries operations, not keys. A caller hands over plaintext
// and receives ciphertext, or the reverse; no method returns a key, and none
// takes one. That is what lets a hardware-backed implementation satisfy it: it
// holds each tenant's versioned keys in a device that never exports them and
// runs the AEAD, the rotation and the retirement there. A Provider that
// returned or required a raw key would be a gap in this seam.
//
// Keyring is the software default. It keeps every version's key in the
// tenant's own replicated store, wrapped under a KEK from the keywrap seam, so
// keywrap.Provider stays the key-wrap seam and is distinct from this one:
// keywrap wraps a key a Keyring holds, a Provider replaces the Keyring.
// A composer injects its own through server.WithDEKProvider.
//
// Semantics every implementation preserves, because the transports and the
// documentation promise them:
//
//   - Encrypt seals under the current version and binds the tenant and the
//     caller's associated data; a ciphertext moved to another tenant, or
//     opened with other associated data, fails to decrypt.
//   - Rotate adds a version and makes it current; earlier versions keep
//     decrypting.
//   - Rewrap moves a ciphertext to the current version without returning its
//     plaintext.
//   - Retire destroys an old version's key, irreversibly; the current version
//     is refused.
//   - Status reports versions and their wrapping metadata, never key bytes.
type Provider interface {
	// Status reports the tenant's keyring versions.
	Status(ctx context.Context, tenantID string) (*Status, error)

	// Rotate adds a key version and makes it current.
	Rotate(ctx context.Context, tenantID string) (*Status, error)

	// Retire destroys an old key version. The current version is refused.
	Retire(ctx context.Context, tenantID string, version uint64) (*Status, error)

	// Encrypt seals plaintext under the current version, binding aad.
	Encrypt(ctx context.Context, tenantID string, plaintext, aad []byte) (*Sealed, error)

	// Decrypt opens a ciphertext sealed for the same tenant and aad.
	Decrypt(ctx context.Context, tenantID string, ciphertext, aad []byte) (*Opened, error)

	// Rewrap re-seals a ciphertext under the current version without
	// returning its plaintext.
	Rewrap(ctx context.Context, tenantID string, ciphertext, aad []byte) (*Rewrapped, error)
}

// Compile-time check that the software default satisfies the seam.
var _ Provider = (*Keyring)(nil)
