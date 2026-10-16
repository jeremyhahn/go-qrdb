// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package secure

import "strconv"

// NilSecretError is returned when Open is called on a Secret whose
// underlying enclave has not been initialized or has already been
// destroyed.
type NilSecretError struct{}

// Error implements the error interface.
func (e *NilSecretError) Error() string {
	return "secure: secret is nil or destroyed"
}

// NilBufferError is returned when a method is called on a Buffer whose
// underlying locked buffer has not been initialized or has already been
// destroyed.
type NilBufferError struct{}

// Error implements the error interface.
func (e *NilBufferError) Error() string {
	return "secure: buffer is nil or destroyed"
}

// OpenError is returned when the underlying go-enclave sealed enclave fails
// to unseal. It wraps the original error for diagnostic inspection.
type OpenError struct {
	Err error
}

// Error implements the error interface.
func (e *OpenError) Error() string {
	return "secure: failed to open secret: " + e.Err.Error()
}

// Unwrap returns the underlying go-enclave error.
func (e *OpenError) Unwrap() error {
	return e.Err
}

// InvalidSizeError is returned when a Buffer constructor is called with
// a non-positive size.
type InvalidSizeError struct {
	Size int
}

// Error implements the error interface.
func (e *InvalidSizeError) Error() string {
	return "secure: invalid buffer size"
}

// ProviderError is returned when a PassphraseProvider fails to read
// input. The underlying source error is preserved for diagnostics.
type ProviderError struct {
	Source string
	Err    error
}

// Error implements the error interface.
func (e *ProviderError) Error() string {
	return "secure: passphrase provider " + e.Source + ": " + e.Err.Error()
}

// Unwrap returns the underlying source error.
func (e *ProviderError) Unwrap() error {
	return e.Err
}

// NestedBorrowError is what the borrow guard reports: the calling goroutine
// entered Op on a Buffer it was already borrowing, which is the self-deadlock
// the guard exists to catch (see borrowguard.go).
//
// It is a diagnostic, not a control-flow error — the operation it names never
// completes, because a handler that returns is panicked past. BorrowStack is
// the stack at the borrow being reentered, which is the half a reader cannot
// recover from the panic's own trace. Neither field can carry key material: Op
// is a compile-time constant and BorrowStack is a stack trace.
type NestedBorrowError struct {
	Op          string
	BorrowStack string
}

// Error implements the error interface.
func (e *NestedBorrowError) Error() string {
	return "secure: " + e.Op + " attempted on a buffer this goroutine is" +
		" borrowing; reentering a borrowed buffer from inside its own borrow" +
		" deadlocks. The borrow was taken at:\n" + e.BorrowStack
}

// EmptyBorrowError is returned by CopyOut when the borrow yields no bytes.
//
// It cannot be produced by a live, non-empty Buffer: both constructors reject
// a zero length and WithBytes refuses a destroyed buffer without calling the
// callback. It guards the Borrower contract, whose other implementations make
// no such promise: a caller must hear that the pages are gone rather than
// receive a correctly sized run of zeros, which is the fail-open corruption
// found in review of go-qrdb#74.
//
// The live empty Buffer NewEmptyBuffer returns is refused too: CopyOut sees
// only the borrow, and an empty borrow cannot say whether it is empty on
// purpose. A caller that accepts an empty value reads it through WithBytes.
type EmptyBorrowError struct{}

// Error implements the error interface.
func (e *EmptyBorrowError) Error() string {
	return "secure: borrow yielded no bytes; the protected pages are gone"
}

// ProcessHardeningError is returned by HardenProcess when the platform refused
// the mechanism that stops other processes reading this one's memory. The
// process is NOT hardened. Mechanism names what was attempted, for example
// "prctl(PR_SET_DUMPABLE, 0)".
type ProcessHardeningError struct {
	Mechanism string
	Err       error
}

// Error implements the error interface.
func (e *ProcessHardeningError) Error() string {
	return "secure: process inspection hardening " + e.Mechanism + " failed: " + e.Err.Error()
}

// Unwrap returns the platform error.
func (e *ProcessHardeningError) Unwrap() error {
	return e.Err
}

// ProcessInspectionOptOutError is returned by HardenProcess when
// AllowProcessInspectionEnv holds a value strconv.ParseBool rejects. The
// process is NOT hardened: a malformed security opt-out is refused rather
// than read as either answer. Value is the rejected text, which is an
// operator-supplied switch and never key material.
type ProcessInspectionOptOutError struct {
	Value string
	Err   error
}

// Error implements the error interface.
func (e *ProcessInspectionOptOutError) Error() string {
	return "secure: " + AllowProcessInspectionEnv + "=" + strconv.Quote(e.Value) +
		" is not a boolean; unset it, or set it to true to leave the process inspectable"
}

// Unwrap returns the parse error.
func (e *ProcessInspectionOptOutError) Unwrap() error {
	return e.Err
}

// ProcessInspectionOptOutRefusedError is returned by HardenProcess when
// AllowProcessInspectionEnv is set in a mode that forbids it. Mode names the
// mode, for example "FIPS 140-3". The process is NOT hardened: the opt-out is
// refused rather than silently ignored, so the operator learns that the
// debugging session they asked for cannot happen in this mode.
type ProcessInspectionOptOutRefusedError struct {
	Mode string
}

// Error implements the error interface.
func (e *ProcessInspectionOptOutRefusedError) Error() string {
	return "secure: " + AllowProcessInspectionEnv + " is refused in " + e.Mode +
		" mode; a process in this mode must not be inspectable, so unset it"
}

// ProcessTracedError is returned by HardenProcess when a tracer is already
// attached to the process. Marking the process non-dumpable stops new
// attaches but leaves an existing tracer its access, so the process refuses to
// continue. TracerPID is the tracer's process ID.
type ProcessTracedError struct {
	TracerPID int
}

// Error implements the error interface.
func (e *ProcessTracedError) Error() string {
	return "secure: process is already traced by pid " + strconv.Itoa(e.TracerPID) +
		"; detach the tracer, or set " + AllowProcessInspectionEnv + "=true to debug"
}
