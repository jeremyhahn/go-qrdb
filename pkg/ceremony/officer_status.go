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

package ceremony

import "time"

// RegisterOfficerRequest carries the parameters for registering a new
// Security Officer against a barrier (system or tenant).
type RegisterOfficerRequest struct {
	// Username is the unique identifier for the officer.
	Username string

	// CSRPEM is the PEM-encoded certificate signing request from the officer.
	CSRPEM []byte

	// AuthenticatorType identifies which hardware or software backs the
	// officer's signing credential.
	AuthenticatorType AuthenticatorType

	// AuthenticatorRef is an opaque reference to the credential within the
	// backing authenticator.
	AuthenticatorRef string
}

// RegisterOfficerResult is the outcome of a successful officer registration.
// It carries the signed certificate (to be transmitted to the officer out of
// band) and the officer's share index assignment.
type RegisterOfficerResult struct {
	// CertPEM is the PEM-encoded signed certificate for the officer.
	CertPEM []byte

	// ShareIndex is the 1-based Shamir share index assigned to this officer.
	ShareIndex int
}

// OfficerStatus is a non-secret view of an enrolled officer. All fields are
// public metadata; no share material is present.
type OfficerStatus struct {
	Username       string    `json:"username"`
	CertThumbprint string    `json:"cert_thumbprint,omitempty"`
	ShareIndex     int       `json:"share_index"`
	AuthFactor     string    `json:"auth_factor"`
	EnrolledAt     time.Time `json:"enrolled_at"`
	ClaimedAt      time.Time `json:"claimed_at,omitempty"` // zero = unclaimed
	SharePending   bool      `json:"share_pending"`        // true = sealed, awaiting claim
}

// ShareClaimResult is returned to the SO exactly once upon a successful
// CompleteClaimShare. The share bytes must be wiped by the caller after
// recording or presenting them to the user.
type ShareClaimResult struct {
	// ShareIndex is the 1-based Shamir share index for this officer.
	ShareIndex int `json:"share_index"`

	// ShareBytes are the raw share bytes returned to the SO. This field is
	// intentionally excluded from JSON serialization; callers encode it only
	// when transmitting to the SO's own authenticated session.
	ShareBytes []byte `json:"-"`
}
