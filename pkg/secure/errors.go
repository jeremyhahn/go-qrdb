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
// entered Op while it already held a borrow of protected memory, which risks
// the self-deadlock the guard exists to catch (see borrowguard.go).
//
// It is a diagnostic, not a control-flow error — the operation it names never
// completes, because a handler that returns is panicked past. BorrowStack is
// the stack at the OUTER borrow, which is the half a reader cannot recover
// from the panic's own trace. Neither field can carry key material: Op is a
// compile-time constant and BorrowStack is a stack trace.
type NestedBorrowError struct {
	Op          string
	BorrowStack string
}

// Error implements the error interface.
func (e *NestedBorrowError) Error() string {
	return "secure: " + e.Op + " attempted while this goroutine holds a borrow" +
		" of protected memory; nested calls into this package from inside a" +
		" borrow are forbidden and risk deadlock. The borrow was taken at:\n" + e.BorrowStack
}

// EmptyBorrowError is returned by CopyOut when the borrow yields no bytes.
//
// It cannot be produced by a live, non-empty Buffer: both constructors reject
// a zero length and WithBytes refuses a destroyed buffer without calling the
// callback. The reachable cause is a process-wide purge that has already taken
// the pages — and the caller must hear about that rather than receive a
// correctly sized run of zeros, which is the fail-open corruption found in
// review of go-qrdb#74.
type EmptyBorrowError struct{}

// Error implements the error interface.
func (e *EmptyBorrowError) Error() string {
	return "secure: borrow yielded no bytes; the protected pages are gone"
}
