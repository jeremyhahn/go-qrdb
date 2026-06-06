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

// OpenError is returned when the underlying memguard enclave fails to
// unseal. It wraps the original error for diagnostic inspection.
type OpenError struct {
	Err error
}

// Error implements the error interface.
func (e *OpenError) Error() string {
	return "secure: failed to open secret: " + e.Err.Error()
}

// Unwrap returns the underlying memguard error.
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

// NotInitializedError is returned by Purge when it is called before
// CatchInterrupt has been invoked by the owning binary.
type NotInitializedError struct{}

// Error implements the error interface.
func (e *NotInitializedError) Error() string {
	return "secure: process lifecycle not initialized; call CatchInterrupt first"
}
