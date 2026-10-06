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

package trustanchor

import "strconv"

// EmptyBundleError is returned when a PEM bundle holds no certificate.
type EmptyBundleError struct{}

// Error describes the empty bundle.
func (e *EmptyBundleError) Error() string {
	return "trustanchor: the PEM bundle holds no certificate"
}

// InvalidAnchorError is returned when a certificate in a bundle cannot be a
// trust anchor. Index is its zero-based position in the bundle.
type InvalidAnchorError struct {
	Index  int
	Reason string
}

// Error names the certificate and why it was refused.
func (e *InvalidAnchorError) Error() string {
	return "trustanchor: certificate " + strconv.Itoa(e.Index) + " in the bundle is not a usable trust anchor: " + e.Reason
}

// TooManyAnchorsError is returned when an Add would take a tenant past
// MaxAnchorsPerTenant.
type TooManyAnchorsError struct {
	TenantID string
	Max      int
}

// Error names the limit.
func (e *TooManyAnchorsError) Error() string {
	return "trustanchor: tenant " + strconv.Quote(e.TenantID) + " may register at most " + strconv.Itoa(e.Max) + " trust anchors"
}

// AnchorNotFoundError is returned when a tenant has no anchor with the given
// fingerprint.
type AnchorNotFoundError struct {
	TenantID    string
	Fingerprint string
}

// Error names the missing anchor.
func (e *AnchorNotFoundError) Error() string {
	return "trustanchor: tenant " + strconv.Quote(e.TenantID) + " has no trust anchor " + e.Fingerprint
}

// ExternallyManagedError is returned by a Registry whose anchors are managed
// outside qrdb (an injected trust store) when asked to change them.
type ExternallyManagedError struct{}

// Error describes the refusal.
func (e *ExternallyManagedError) Error() string {
	return "trustanchor: trust anchors are managed by the injected trust store, not through qrdb"
}

// InvalidTenantError is returned when a tenant id cannot name a trust-anchor
// keyspace.
type InvalidTenantError struct {
	TenantID string
	Err      error
}

// Error names the tenant.
func (e *InvalidTenantError) Error() string {
	msg := "trustanchor: invalid tenant id " + strconv.Quote(e.TenantID)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the validation cause.
func (e *InvalidTenantError) Unwrap() error { return e.Err }

// StoreError wraps a failure of the backing store.
type StoreError struct {
	Op  string
	Err error
}

// Error names the operation.
func (e *StoreError) Error() string {
	return "trustanchor: store " + e.Op + ": " + e.Err.Error()
}

// Unwrap returns the store failure.
func (e *StoreError) Unwrap() error { return e.Err }

// CertificateNotTrustedError is returned by VerifyClientCertificate when a
// certificate does not chain to any of the tenant's anchors as a client
// authentication certificate.
type CertificateNotTrustedError struct {
	TenantID string
	Err      error
}

// Error names the tenant.
func (e *CertificateNotTrustedError) Error() string {
	msg := "trustanchor: the certificate does not chain to a trust anchor of tenant " + strconv.Quote(e.TenantID)
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the verification cause.
func (e *CertificateNotTrustedError) Unwrap() error { return e.Err }

// NoAnchorsError is returned by VerifyClientCertificate when the tenant has
// registered no anchors at all, so no bring-your-own certificate can be
// verified for it.
type NoAnchorsError struct {
	TenantID string
}

// Error names the tenant.
func (e *NoAnchorsError) Error() string {
	return "trustanchor: tenant " + strconv.Quote(e.TenantID) + " has registered no trust anchors"
}

// BundleTooLargeError is returned when a PEM bundle offered to ParseAnchors
// or KVRegistry.Add exceeds MaxBundlePEMSize, before anything in it is
// parsed.
type BundleTooLargeError struct {
	Size int
	Max  int
}

// Error names the limit.
func (e *BundleTooLargeError) Error() string {
	return "trustanchor: bundle of " + strconv.Itoa(e.Size) + " bytes exceeds the " + strconv.Itoa(e.Max) + "-byte limit"
}

// AtomicStoreRequiredError is returned by Add and Remove when the configured
// store offers no AtomicKVStore capability, so the anchor-set change and its
// cap check could not be applied as one atomic transaction. There is no
// non-atomic fallback: a registry over a store that cannot make this
// guarantee refuses the write rather than risk the race ParseAnchors and
// this guard exist to close.
type AtomicStoreRequiredError struct {
	Op string
}

// Error names the operation.
func (e *AtomicStoreRequiredError) Error() string {
	return "trustanchor: " + e.Op + " requires an atomic store and the configured one does not offer it"
}

// ConcurrentUpdateError is returned when Add or Remove lost the
// optimistic-concurrency race against other writers for the same tenant's
// anchor set too many times in a row to make progress.
type ConcurrentUpdateError struct {
	TenantID string
	Op       string
}

// Error names the tenant and operation.
func (e *ConcurrentUpdateError) Error() string {
	return "trustanchor: " + e.Op + " for tenant " + strconv.Quote(e.TenantID) + " could not win the race against concurrent writers"
}
