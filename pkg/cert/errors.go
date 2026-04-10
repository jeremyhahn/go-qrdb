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

package cert

// CertNotFoundError is returned when a certificate serial does not exist
// in the store.
type CertNotFoundError struct {
	Serial string
}

// Error returns a human-readable description of the missing certificate.
func (e *CertNotFoundError) Error() string {
	return "qrdb/cert: certificate not found: " + e.Serial
}

// CertRevokedError is returned when an operation targets a revoked
// certificate.
type CertRevokedError struct {
	Serial string
}

// Error returns a human-readable description of the revoked certificate.
func (e *CertRevokedError) Error() string {
	return "qrdb/cert: certificate revoked: " + e.Serial
}

// CertGenerateError wraps an error that occurred during certificate
// generation.
type CertGenerateError struct {
	Err error
}

// Error returns a human-readable description of the generation failure.
func (e *CertGenerateError) Error() string {
	msg := "qrdb/cert: certificate generation failed"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *CertGenerateError) Unwrap() error {
	return e.Err
}

// CertExportError wraps an error that occurred during certificate export.
type CertExportError struct {
	Err error
}

// Error returns a human-readable description of the export failure.
func (e *CertExportError) Error() string {
	msg := "qrdb/cert: certificate export failed"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *CertExportError) Unwrap() error {
	return e.Err
}

// CAKeyNotAvailableError is returned when the CA private key is not
// loaded or has been destroyed.
type CAKeyNotAvailableError struct{}

// Error returns a human-readable description indicating the CA key is
// unavailable.
func (e *CAKeyNotAvailableError) Error() string {
	return "qrdb/cert: CA private key not available"
}

// BarrierSealedError is returned when an operation requires the
// encryption barrier to be unsealed but it is currently sealed.
type BarrierSealedError struct{}

// Error returns a human-readable description indicating the barrier is
// sealed.
func (e *BarrierSealedError) Error() string {
	return "qrdb/cert: encryption barrier is sealed"
}

// StoreError wraps an error from a KVStore operation with the operation
// name and detail context.
type StoreError struct {
	Op     string
	Detail string
	Err    error
}

// Error returns a human-readable description of the store failure.
func (e *StoreError) Error() string {
	msg := "qrdb/cert: store " + e.Op
	if e.Detail != "" {
		msg += " " + e.Detail
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *StoreError) Unwrap() error {
	return e.Err
}

// ValidationError is returned when a request field fails validation.
type ValidationError struct {
	Field  string
	Reason string
}

// Error returns a human-readable description of the validation failure.
func (e *ValidationError) Error() string {
	return "qrdb/cert: invalid " + e.Field + ": " + e.Reason
}
