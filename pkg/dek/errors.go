// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package dek

import (
	"errors"
	"strconv"
)

// Sentinel classes. Every error this package returns matches exactly one of
// them under errors.Is, so a transport maps a class to its status code
// without enumerating the concrete types.
var (
	// ErrInvalidInput: a required argument was missing or malformed.
	ErrInvalidInput = errors.New("dek: invalid input")
	// ErrCrossTenant: the caller asked for a tenant other than its own.
	ErrCrossTenant = errors.New("dek: cross-tenant access refused")
	// ErrMalformedCiphertext: the input is not a tenant DEK ciphertext.
	ErrMalformedCiphertext = errors.New("dek: malformed ciphertext")
	// ErrAuthenticationFailed: the ciphertext did not authenticate — it was
	// tampered with, the associated data differs, or it belongs to another
	// tenant.
	ErrAuthenticationFailed = errors.New("dek: ciphertext authentication failed")
	// ErrUnknownVersion: the keyring has no such version.
	ErrUnknownVersion = errors.New("dek: unknown key version")
	// ErrVersionRetired: the version existed but its key was destroyed.
	ErrVersionRetired = errors.New("dek: key version retired")
	// ErrRetireCurrentVersion: the current version cannot be retired.
	ErrRetireCurrentVersion = errors.New("dek: cannot retire the current key version")
	// ErrKeyringUnavailable: the tenant's keyring store cannot be reached
	// (typically a sealed tenant barrier).
	ErrKeyringUnavailable = errors.New("dek: tenant keyring unavailable")
	// ErrCorruptKeyring: the stored keyring record does not decode.
	ErrCorruptKeyring = errors.New("dek: corrupt keyring record")
	// ErrConcurrentUpdate: the keyring changed under every retry of an update.
	ErrConcurrentUpdate = errors.New("dek: keyring update lost to concurrent writers")
	// ErrCrypto: a cryptographic primitive failed (entropy, cipher setup).
	ErrCrypto = errors.New("dek: cryptographic failure")
	// ErrLegacyKeyringFormat: the stored record predates key-wrap
	// integration (a plaintext key per version, no provider ID or key
	// reference) and is refused outright, never migrated.
	ErrLegacyKeyringFormat = errors.New("dek: legacy keyring record format refused")
	// ErrKeyDirNotConfigured: the built-in "software" key-wrap provider
	// was resolved but no key directory was ever configured for it.
	ErrKeyDirNotConfigured = errors.New("dek: no key directory configured for the \"software\" key-wrap provider")
	// ErrKeyWrapFailed: the keyring's KEK provider could not wrap or
	// unwrap a version's key -- an unregistered provider ID, a bad key
	// reference, or a KEK authentication failure. The cause is
	// deliberately not distinguished further; see KeyWrapFailedError.
	ErrKeyWrapFailed = errors.New("dek: key-wrap provider failed")
)

// InvalidInputError reports a missing or malformed argument.
type InvalidInputError struct {
	Op    string
	Field string
}

func (e *InvalidInputError) Error() string {
	return "dek " + e.Op + ": " + e.Field + " is required"
}

// Is matches ErrInvalidInput.
func (e *InvalidInputError) Is(target error) bool { return target == ErrInvalidInput }

// CrossTenantError reports a request naming a tenant other than the caller's.
type CrossTenantError struct {
	CallerTenant    string
	RequestedTenant string
}

func (e *CrossTenantError) Error() string {
	return "dek: tenant " + strconv.Quote(e.CallerTenant) + " may not use the keyring of tenant " +
		strconv.Quote(e.RequestedTenant)
}

// Is matches ErrCrossTenant.
func (e *CrossTenantError) Is(target error) bool { return target == ErrCrossTenant }

// MalformedCiphertextError reports input that is not a tenant DEK ciphertext.
type MalformedCiphertextError struct {
	Reason string
}

func (e *MalformedCiphertextError) Error() string {
	return "dek: malformed ciphertext: " + e.Reason
}

// Is matches ErrMalformedCiphertext.
func (e *MalformedCiphertextError) Is(target error) bool { return target == ErrMalformedCiphertext }

// AuthenticationFailedError reports a ciphertext that did not authenticate
// under the named key version. The cause is deliberately not distinguished:
// tampering, a different associated data and another tenant's ciphertext are
// indistinguishable to an AEAD, and saying which would be an oracle.
type AuthenticationFailedError struct {
	TenantID string
	Version  uint64
	Err      error
}

func (e *AuthenticationFailedError) Error() string {
	return "dek: ciphertext for tenant " + strconv.Quote(e.TenantID) + " version " +
		strconv.FormatUint(e.Version, 10) + " failed authentication"
}

// Is matches ErrAuthenticationFailed.
func (e *AuthenticationFailedError) Is(target error) bool { return target == ErrAuthenticationFailed }

// Unwrap returns the AEAD error.
func (e *AuthenticationFailedError) Unwrap() error { return e.Err }

// UnknownVersionError reports a key version the tenant's keyring never had.
type UnknownVersionError struct {
	TenantID string
	Version  uint64
}

func (e *UnknownVersionError) Error() string {
	return "dek: tenant " + strconv.Quote(e.TenantID) + " has no key version " +
		strconv.FormatUint(e.Version, 10)
}

// Is matches ErrUnknownVersion.
func (e *UnknownVersionError) Is(target error) bool { return target == ErrUnknownVersion }

// VersionRetiredError reports a key version whose key has been destroyed.
type VersionRetiredError struct {
	TenantID string
	Version  uint64
}

func (e *VersionRetiredError) Error() string {
	return "dek: tenant " + strconv.Quote(e.TenantID) + " key version " +
		strconv.FormatUint(e.Version, 10) + " is retired"
}

// Is matches ErrVersionRetired.
func (e *VersionRetiredError) Is(target error) bool { return target == ErrVersionRetired }

// RetireCurrentVersionError reports an attempt to retire the current version.
type RetireCurrentVersionError struct {
	TenantID string
	Version  uint64
}

func (e *RetireCurrentVersionError) Error() string {
	return "dek: tenant " + strconv.Quote(e.TenantID) + " key version " +
		strconv.FormatUint(e.Version, 10) + " is current and cannot be retired; rotate first"
}

// Is matches ErrRetireCurrentVersion.
func (e *RetireCurrentVersionError) Is(target error) bool { return target == ErrRetireCurrentVersion }

// KeyringUnavailableError reports that the tenant's keyring store could not
// be used. Err carries the cause (for example tenant.ErrTenantSealed).
type KeyringUnavailableError struct {
	TenantID string
	Err      error
}

func (e *KeyringUnavailableError) Error() string {
	return "dek: keyring for tenant " + strconv.Quote(e.TenantID) + " unavailable: " + e.Err.Error()
}

// Is matches ErrKeyringUnavailable.
func (e *KeyringUnavailableError) Is(target error) bool { return target == ErrKeyringUnavailable }

// Unwrap returns the cause.
func (e *KeyringUnavailableError) Unwrap() error { return e.Err }

// AtomicStoreRequiredError is the cause carried by KeyringUnavailableError
// when the resolved store cannot perform the conditional writes the keyring
// needs to be safe under concurrent rotation.
type AtomicStoreRequiredError struct{}

func (e *AtomicStoreRequiredError) Error() string {
	return "store does not support atomic conditional writes"
}

// CorruptKeyringError reports a stored keyring record that does not decode.
type CorruptKeyringError struct {
	TenantID string
	Err      error
}

func (e *CorruptKeyringError) Error() string {
	msg := "dek: keyring record for tenant " + strconv.Quote(e.TenantID) + " is corrupt"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Is matches ErrCorruptKeyring.
func (e *CorruptKeyringError) Is(target error) bool { return target == ErrCorruptKeyring }

// Unwrap returns the decode error.
func (e *CorruptKeyringError) Unwrap() error { return e.Err }

// ConcurrentUpdateError reports that every attempt of a keyring update lost
// its compare-and-swap to another writer.
type ConcurrentUpdateError struct {
	TenantID string
	Attempts int
}

func (e *ConcurrentUpdateError) Error() string {
	return "dek: keyring update for tenant " + strconv.Quote(e.TenantID) + " lost to concurrent writers after " +
		strconv.Itoa(e.Attempts) + " attempts"
}

// Is matches ErrConcurrentUpdate.
func (e *ConcurrentUpdateError) Is(target error) bool { return target == ErrConcurrentUpdate }

// StoreError reports a failed read or write of the keyring record.
type StoreError struct {
	TenantID string
	Op       string
	Err      error
}

func (e *StoreError) Error() string {
	return "dek: keyring " + e.Op + " for tenant " + strconv.Quote(e.TenantID) + ": " + e.Err.Error()
}

// Unwrap returns the store error.
func (e *StoreError) Unwrap() error { return e.Err }

// LegacyKeyringFormatError reports a stored record in the pre-key-wrap
// format: a plaintext key per version, not a wrapped one with a provider ID
// and key reference. Refused outright -- see this package's own doc
// comment, "Old-format records are refused, not migrated".
type LegacyKeyringFormatError struct {
	TenantID string
	Format   int
}

func (e *LegacyKeyringFormatError) Error() string {
	return "dek: keyring record for tenant " + strconv.Quote(e.TenantID) +
		" is in the pre-key-wrap format (format " + strconv.Itoa(e.Format) + ") and is refused, not migrated"
}

// Is matches ErrLegacyKeyringFormat.
func (e *LegacyKeyringFormatError) Is(target error) bool { return target == ErrLegacyKeyringFormat }

// KeyDirNotConfiguredError reports that the built-in "software" key-wrap
// provider was resolved but no key directory was ever configured for it
// (KeyringOption WithKeyDir, or lifecycle.Infra.DEKKeyDir).
type KeyDirNotConfiguredError struct{}

func (e *KeyDirNotConfiguredError) Error() string {
	return "dek: no key directory configured for the \"software\" key-wrap provider " +
		"(set KeyringOption WithKeyDir, or lifecycle.Infra.DEKKeyDir)"
}

// Is matches ErrKeyDirNotConfigured.
func (e *KeyDirNotConfiguredError) Is(target error) bool { return target == ErrKeyDirNotConfigured }

// KeyWrapFailedError reports that the keyring's KEK provider could not wrap
// or unwrap a version's key for tenantID. Covers an unregistered provider
// ID, a bad key reference, and a KEK authentication failure alike --
// deliberately one answer, the same discipline AuthenticationFailedError
// holds at the ciphertext layer one level up.
type KeyWrapFailedError struct {
	TenantID string
	Version  uint64
	Err      error
}

func (e *KeyWrapFailedError) Error() string {
	msg := "dek: key-wrap provider failed for tenant " + strconv.Quote(e.TenantID)
	if e.Version != 0 {
		msg += " version " + strconv.FormatUint(e.Version, 10)
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Is matches ErrKeyWrapFailed.
func (e *KeyWrapFailedError) Is(target error) bool { return target == ErrKeyWrapFailed }

// Unwrap returns the provider's error.
func (e *KeyWrapFailedError) Unwrap() error { return e.Err }

// CryptoError reports a failed cryptographic primitive.
type CryptoError struct {
	Op  string
	Err error
}

func (e *CryptoError) Error() string {
	return "dek " + e.Op + ": " + e.Err.Error()
}

// Is matches ErrCrypto.
func (e *CryptoError) Is(target error) bool { return target == ErrCrypto }

// Unwrap returns the primitive's error.
func (e *CryptoError) Unwrap() error { return e.Err }
