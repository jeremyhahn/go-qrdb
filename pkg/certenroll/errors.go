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

package certenroll

import "strconv"

// InvalidCertificateError is returned when a bring-your-own certificate (or
// its chain) does not decode.
type InvalidCertificateError struct {
	Err error
}

// Error describes the refusal.
func (e *InvalidCertificateError) Error() string {
	return "certenroll: the certificate does not decode: " + e.Err.Error()
}

// Unwrap returns the decode cause.
func (e *InvalidCertificateError) Unwrap() error { return e.Err }

// IdentityMismatchError is returned when a certificate would authenticate as
// someone other than the account it is being enrolled on.
type IdentityMismatchError struct {
	TenantID     string
	Username     string
	CertTenantID string
	CertUsername string
	Err          error
}

// Error names both identities.
func (e *IdentityMismatchError) Error() string {
	msg := "certenroll: the certificate does not authenticate as " + strconv.Quote(e.Username) +
		" in tenant " + strconv.Quote(e.TenantID)
	if e.Err != nil {
		return msg + ": " + e.Err.Error()
	}
	return msg + "; it names " + strconv.Quote(e.CertUsername) + " in tenant " + strconv.Quote(e.CertTenantID)
}

// Unwrap returns the tenant-derivation cause, when there was one.
func (e *IdentityMismatchError) Unwrap() error { return e.Err }

// IssuedCertificateUnreadableError is returned when the certificate an issuer
// returned does not decode -- a defective CertProvider.
type IssuedCertificateUnreadableError struct {
	Err error
}

// Error describes the defect.
func (e *IssuedCertificateUnreadableError) Error() string {
	return "certenroll: the issued certificate does not decode: " + e.Err.Error()
}

// Unwrap returns the decode cause.
func (e *IssuedCertificateUnreadableError) Unwrap() error { return e.Err }
