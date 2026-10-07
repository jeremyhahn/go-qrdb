// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import "strconv"

// ProviderAlreadyRegisteredError is returned by Registry.Register when a
// provider is already registered under the same name. A provider cannot be
// replaced silently; the caller wanting that must build a new Registry.
type ProviderAlreadyRegisteredError struct {
	Provider string
}

// Error implements the error interface.
func (e *ProviderAlreadyRegisteredError) Error() string {
	return "volume: provider already registered: " + strconv.Quote(e.Provider)
}

// ProviderNotRegisteredError is returned by Registry.Lookup when the named
// provider has no registration on this node. Available carries every
// registered name, sorted, so an operator sees what is configured.
type ProviderNotRegisteredError struct {
	Provider  string
	Available []string
}

// Error implements the error interface.
func (e *ProviderNotRegisteredError) Error() string {
	msg := "volume: provider not registered: " + strconv.Quote(e.Provider) + " (available: "
	for i, a := range e.Available {
		if i > 0 {
			msg += ", "
		}
		msg += a
	}
	return msg + ")"
}

// InvalidProviderError is returned by Registry.Register when the supplied
// Provider is unusable: nil, or reporting an empty name.
type InvalidProviderError struct {
	Reason string
}

// Error implements the error interface.
func (e *InvalidProviderError) Error() string {
	return "volume: invalid provider: " + e.Reason
}

// InvalidSpecError is returned by a Provider's Open when spec fails
// validation common to every provider (an empty or non-canonical Path).
// Provider-specific validation (e.g. a missing device option) returns its
// own typed error instead.
type InvalidSpecError struct {
	Pool   string
	Reason string
}

// Error implements the error interface.
func (e *InvalidSpecError) Error() string {
	return "volume: invalid spec for pool " + strconv.Quote(e.Pool) + ": " + e.Reason
}

// OpenError is returned by a Provider's Open when the volume cannot be made
// available on this node: a missing directory, a permissions failure, or an
// equivalent provider-specific condition. It never carries a path in its
// message; the path is a field for the caller's own logging, never for the
// wire.
type OpenError struct {
	Provider string
	Pool     string
	Err      error
}

// Error implements the error interface.
func (e *OpenError) Error() string {
	return "volume: " + e.Provider + " provider could not open pool " + strconv.Quote(e.Pool) + ": " + e.Err.Error()
}

// Unwrap supports errors.Is / errors.As against the wrapped cause.
func (e *OpenError) Unwrap() error { return e.Err }

// UsageError is returned by a Volume's Usage when the underlying capacity
// query fails. It never carries a path in its message.
type UsageError struct {
	Pool string
	Err  error
}

// Error implements the error interface.
func (e *UsageError) Error() string {
	return "volume: could not measure usage for pool " + strconv.Quote(e.Pool) + ": " + e.Err.Error()
}

// Unwrap supports errors.Is / errors.As against the wrapped cause.
func (e *UsageError) Unwrap() error { return e.Err }

// ClosedVolumeError is returned by a Volume's Usage or Health-dependent
// calls made after Close, when the provider chooses to refuse them rather
// than report stale numbers.
type ClosedVolumeError struct {
	Pool string
}

// Error implements the error interface.
func (e *ClosedVolumeError) Error() string {
	return "volume: pool " + strconv.Quote(e.Pool) + " volume is closed"
}

// InvalidMasterKeyError is returned by DeriveVolumeKey when masterKey is
// empty: the caller asked for a key derived from nothing, which is always a
// defect in the caller rather than a transient condition.
type InvalidMasterKeyError struct{}

// Error implements the error interface.
func (e *InvalidMasterKeyError) Error() string {
	return "volume: cannot derive a volume key from an empty master key"
}

// InvalidVolumeIdentityError is returned by DeriveVolumeKey when nodeID or
// pool is empty.
type InvalidVolumeIdentityError struct {
	NodeID string
	Pool   string
}

// Error implements the error interface.
func (e *InvalidVolumeIdentityError) Error() string {
	return "volume: cannot derive a volume key with an empty node id or pool name (node=" +
		strconv.Quote(e.NodeID) + " pool=" + strconv.Quote(e.Pool) + ")"
}

// VolumeKeyDerivationError is returned by DeriveVolumeKey when the
// underlying HKDF expansion fails, which happens only for a requested key
// length HKDF-SHA256 cannot produce. It never happens for this package's
// fixed 32-byte key length under a non-empty master key; the type exists so
// the failure is still a typed, auditable error rather than a panic.
type VolumeKeyDerivationError struct {
	NodeID string
	Pool   string
	Err    error
}

// Error implements the error interface.
func (e *VolumeKeyDerivationError) Error() string {
	return "volume: could not derive key for pool " + strconv.Quote(e.Pool) + " on node " +
		strconv.Quote(e.NodeID) + ": " + e.Err.Error()
}

// Unwrap supports errors.Is / errors.As against the wrapped cause.
func (e *VolumeKeyDerivationError) Unwrap() error { return e.Err }

// OpenFailure names one configured volume pool that failed to open at
// boot and why. It is the unit OpenAtBootError lists. Unlike a replicated
// NodeReport's Health.Detail, Err may freely carry this node's own
// filesystem path: OpenAtBootError's message is local, printed to this
// node's own console/log at startup, never replicated or sent on the
// wire.
type OpenFailure struct {
	Pool     string
	Provider string
	Err      error
}

// OpenAtBootError is returned when one or more configured volume pools
// could not be opened at boot and the operator did not opt into a
// degraded boot. It names every inaccessible volume and the reason the
// node refuses to start partially rather than serve with some tenants'
// data unopened and silently unreachable.
type OpenAtBootError struct {
	Failures []OpenFailure
}

// Error implements the error interface.
func (e *OpenAtBootError) Error() string {
	msg := "volume: " + strconv.Itoa(len(e.Failures)) + " volume pool(s) failed to open at boot" +
		" (pass --allow-degraded-volume-boot to start anyway, serving every volume and database this node can):"
	for _, f := range e.Failures {
		msg += " " + strconv.Quote(f.Pool) + " (provider " + strconv.Quote(f.Provider) + "): " + f.Err.Error() + ";"
	}
	return msg
}

// Unwrap supports errors.Is / errors.As against every wrapped failure.
func (e *OpenAtBootError) Unwrap() []error {
	errs := make([]error, len(e.Failures))
	for i, f := range e.Failures {
		errs[i] = f.Err
	}
	return errs
}
