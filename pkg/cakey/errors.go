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

package cakey

// BarrierSealedError is returned when a SoftwareProvider operation that
// needs the barrier-decrypted key is attempted while the barrier is sealed.
type BarrierSealedError struct{}

// Error returns a human-readable description.
func (e *BarrierSealedError) Error() string {
	return "cakey: barrier is sealed"
}

// StoreError wraps a failure reading or writing the provider's key-value
// store.
type StoreError struct {
	Op     string
	Detail string
	Err    error
}

// Error returns a human-readable description.
func (e *StoreError) Error() string {
	msg := "cakey: store " + e.Op + " failed"
	if e.Detail != "" {
		msg += " (" + e.Detail + ")"
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *StoreError) Unwrap() error {
	return e.Err
}

// GenerateError wraps a failure generating or encoding key material.
type GenerateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *GenerateError) Error() string {
	msg := "cakey: key generation failed"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *GenerateError) Unwrap() error {
	return e.Err
}

// MaterialCorruptError wraps a failure decoding a STORED record -- it is
// not valid JSON, or its key/certificate fields do not parse -- as
// distinct from GenerateError, which is this node failing to CREATE
// material. An earlier revision reported both as GenerateError, which told
// an operator "key generation failed" about a record this process never
// tried to generate: the AEAD-wrapped key, replicated to a node whose
// barrier differs from the writer's, decrypted to garbage there and the
// resulting parse failure was reported as a generation failure. Now that
// no AEAD wrap of cakey's own exists (see SoftwareProvider's doc comment),
// MaterialCorruptError means exactly what it says: the bytes read back
// from the store do not decode, independent of any barrier.
type MaterialCorruptError struct {
	Err error
}

// Error returns a human-readable description.
func (e *MaterialCorruptError) Error() string {
	msg := "cakey: stored material is corrupt"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *MaterialCorruptError) Unwrap() error {
	return e.Err
}

// MaterialMismatchError is returned by AdoptIfAbsent when the certificate
// it was asked to adopt does not match the certificate already won the
// store's PutIfAbsent race (or was already there from an earlier boot).
// Two different CAs both claiming to be the cluster's one Key A is a
// split-brain condition the caller must stop and report -- never silently
// resolved by preferring whichever happened to be stored first, and never
// by deleting either side's cached material, which this package has no
// filesystem access to do and does not attempt.
type MaterialMismatchError struct{}

// Error returns a human-readable description.
func (e *MaterialMismatchError) Error() string {
	return "cakey: the certificate offered for adoption does not match the cluster CA already persisted; " +
		"refusing to silently resolve a split-brain"
}

// NotInitializedError is returned when Signer or Certificate is called on a
// SoftwareProvider that has never been Initialize'd and has no material in
// its store.
type NotInitializedError struct{}

// Error returns a human-readable description.
func (e *NotInitializedError) Error() string {
	return "cakey: provider has not been initialized"
}

// NilProviderError is returned when a nil Provider is supplied to a
// constructor that requires one (e.g. ceremony.NewProviderCA, an issuance
// adapter). A nil seam is refused rather than silently falling back to a
// built-in key, which would make a configuration mistake look like a
// successful injection.
type NilProviderError struct{}

// Error returns a human-readable description.
func (e *NilProviderError) Error() string {
	return "cakey: nil Provider"
}
