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

package firstboot

import (
	"errors"
	"testing"
)

// TestConflictingAdminCertInputsError_Error pins the message's wording.
func TestConflictingAdminCertInputsError_Error(t *testing.T) {
	err := &ConflictingAdminCertInputsError{}
	want := "firstboot: AdminCSRPEM and AdminCertPEM are mutually exclusive: " +
		"supply a CSR to have the CA issue a certificate, or a certificate to adopt one you already hold"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestMissingAdminCertPoolError_Error pins the message's wording.
func TestMissingAdminCertPoolError_Error(t *testing.T) {
	err := &MissingAdminCertPoolError{}
	want := "firstboot: AdminCertPEM requires AdminCertPool: a presented certificate " +
		"must chain to a trust anchor the deployment already accepts"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestAdminCertRejectedError_ErrorAndUnwrap pins the message's wording and
// that the cause is reachable via errors.Is/errors.As.
func TestAdminCertRejectedError_ErrorAndUnwrap(t *testing.T) {
	cause := errors.New("certificate expired")
	err := &AdminCertRejectedError{Cause: cause}
	want := "firstboot: presented admin certificate rejected: certificate expired"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Fatal("AdminCertRejectedError must unwrap to its Cause")
	}
}

// TestAdminCredentialAlreadyPendingError_Error pins the message's wording.
func TestAdminCredentialAlreadyPendingError_Error(t *testing.T) {
	err := &AdminCredentialAlreadyPendingError{}
	want := "firstboot: another node already recorded a pending admin credential; " +
		"this one will not be bound -- discard it and use the credential the winning node reports"
	if got := err.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// TestHasAdminCertPath covers the exported wrapper's three true branches and
// its false default, matching hasAdminCertPath's own contract exactly (the
// two must never diverge -- see HasAdminCertPath's doc comment).
func TestHasAdminCertPath(t *testing.T) {
	if HasAdminCertPath(Dependencies{}) {
		t.Fatal("no admin cert path configured must answer false")
	}
	if !HasAdminCertPath(Dependencies{AdminCertPEM: []byte("cert")}) {
		t.Fatal("a presented AdminCertPEM must answer true")
	}
	if !HasAdminCertPath(Dependencies{AdminCSRPEM: []byte("csr"), Signer: &fakeSigner{}}) {
		t.Fatal("an AdminCSRPEM with a Signer must answer true")
	}
	if HasAdminCertPath(Dependencies{AdminCSRPEM: []byte("csr")}) {
		t.Fatal("an AdminCSRPEM with no Signer must answer false")
	}
	if !HasAdminCertPath(Dependencies{CA: &fakeCA{}}) {
		t.Fatal("a configured CA must answer true")
	}
}
