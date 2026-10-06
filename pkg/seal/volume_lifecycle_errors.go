// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

// InvalidVolumeLifecycleError is returned by VolumeLifecycle.Track when the
// supplied pool name or Lockable is unusable.
type InvalidVolumeLifecycleError struct {
	Reason string
}

// Error implements the error interface.
func (e *InvalidVolumeLifecycleError) Error() string {
	return "qrdb/seal: invalid volume lifecycle registration: " + e.Reason
}

// VolumeAlreadyTrackedError is returned by VolumeLifecycle.Track when pool
// is already tracked. The caller must Untrack it first to replace it.
type VolumeAlreadyTrackedError struct {
	Pool string
}

// Error implements the error interface.
func (e *VolumeAlreadyTrackedError) Error() string {
	return "qrdb/seal: volume pool already tracked: " + e.Pool
}

// VolumeUnlockError reports that a tracked pool's Lockable volume could not
// be unlocked after a system-barrier unseal: key derivation failed, or the
// provider's Unlock itself failed. The pool is left locked; the node still
// boots and serves every tenant whose pool is unaffected.
type VolumeUnlockError struct {
	Pool string
	Err  error
}

// Error implements the error interface.
func (e *VolumeUnlockError) Error() string {
	return "qrdb/seal: volume pool " + e.Pool + " could not be unlocked: " + e.Err.Error()
}

// Unwrap supports errors.Is / errors.As against the wrapped cause.
func (e *VolumeUnlockError) Unwrap() error { return e.Err }

// VolumeLockError reports that a tracked pool's Lockable volume could not be
// locked while the system barrier was sealing. The system barrier's root
// key is wiped regardless; this error is reported to the caller and audited
// so the failure is visible, not swallowed.
type VolumeLockError struct {
	Pool string
	Err  error
}

// Error implements the error interface.
func (e *VolumeLockError) Error() string {
	return "qrdb/seal: volume pool " + e.Pool + " could not be locked: " + e.Err.Error()
}

// Unwrap supports errors.Is / errors.As against the wrapped cause.
func (e *VolumeLockError) Unwrap() error { return e.Err }

// VolumeLockTimeoutError reports that a tracked pool's Lock call did not
// return within VolumeLifecycle's observer timeout (WithObserverTimeout).
// The call's goroutine is abandoned, not waited for; lockAll continues to
// the remaining pools and the system barrier's root key is wiped
// regardless, so a single hung Lock can never block a node seal forever.
type VolumeLockTimeoutError struct {
	Pool string
}

// Error implements the error interface.
func (e *VolumeLockTimeoutError) Error() string {
	return "qrdb/seal: volume pool " + e.Pool + " lock timed out"
}
