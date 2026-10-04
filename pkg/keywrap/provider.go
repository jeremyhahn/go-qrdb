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

// Package keywrap defines the single seam through which every
// data-encrypting key (DEK) go-qrdb mints is wrapped under a
// key-encrypting key (KEK): one interface, Provider, shaped like
// pkg/cakey.Provider -- it holds or references the KEK and never exports
// it. Backup ([backup-restore](../../docs/backup-restore/architecture.md))
// is the first consumer; the per-tenant DEK keyring is the planned second
// consumer and is not touched by this package.
//
// go-qrdb ships exactly one implementation, SoftwareProvider, which wraps
// and unwraps with an operator-supplied 32-byte key read from a key file.
// go-xkms and other embedders register hardware or cloud-KMS providers
// (TPM2, PKCS#11, AWS/GCP/Azure KMS, Vault) into a Registry at composition
// time -- go-qrdb never imports go-xkms, so the fit is proven in this
// package's own tests against a same-shaped local double, never against a
// real xkms type.
//
// See docs/key-wrapping/architecture.md for the full design.
package keywrap

import "context"

// Provider wraps and unwraps a data-encrypting key (DEK) under a
// key-encrypting key (KEK) this provider holds or references. It never
// exports the KEK itself -- callers get a wrapped DEK back, never KEK
// bytes, the same discipline cakey.Provider holds for a CA signing key.
//
// Thread-safe: implementations must support concurrent Wrap/Unwrap calls
// from every caller that holds this provider (backup today; the tenant DEK
// keyring is the planned second caller).
type Provider interface {
	// ProviderID identifies this provider's kind -- "software", "tpm2",
	// "pkcs11", "aws-kms", and so on. Stored verbatim in every envelope
	// this provider wraps, so a later Unwrap resolves the SAME kind of
	// provider through the Registry without guessing.
	ProviderID() string

	// Wrap encrypts dek under the KEK keyRef names, returning the bytes to
	// persist. keyRef is opaque to this package and entirely
	// provider-specific: a file path for the software provider, an HSM
	// slot/handle, a KMS key ARN. This package never inspects it.
	Wrap(ctx context.Context, keyRef string, dek []byte) (wrapped []byte, err error)

	// Unwrap decrypts wrapped, previously returned by Wrap under the SAME
	// keyRef, back into the original dek. Returns *UnwrapFailedError when
	// the KEK does not match or wrapped was tampered with -- the provider
	// must not distinguish those two causes in what it returns, so a
	// caller cannot use this as a key-guessing oracle.
	Unwrap(ctx context.Context, keyRef string, wrapped []byte) (dek []byte, err error)
}
