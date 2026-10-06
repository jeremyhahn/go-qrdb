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

package auth

import (
	"errors"
	"strings"
	"testing"
)

// TestIssuerTenantMismatchError_Error pins the message for both the clean
// "no" case (Err nil) and the failed-lookup case (Err set), and that the
// empty tenant ID -- the system tenant -- is named explicitly rather than
// rendered as an unhelpful empty string.
func TestIssuerTenantMismatchError_Error(t *testing.T) {
	bare := &IssuerTenantMismatchError{TenantID: "acme"}
	if !strings.Contains(bare.Error(), `"acme"`) {
		t.Errorf("Error() = %q, want it to name tenant %q", bare.Error(), "acme")
	}

	wrapped := &IssuerTenantMismatchError{TenantID: "acme", Err: errors.New("barrier sealed")}
	if !strings.Contains(wrapped.Error(), "barrier sealed") {
		t.Errorf("Error() = %q, want it to include the cause", wrapped.Error())
	}

	system := &IssuerTenantMismatchError{}
	if !strings.Contains(system.Error(), "(system)") {
		t.Errorf("Error() = %q, want the empty tenant ID named as (system)", system.Error())
	}
}

// TestIssuerTenantMismatchError_Unwrap pins that the error matches
// ErrInvalidCertificate under errors.Is in both the bare and wrapped forms,
// and surfaces its own cause too when it has one.
func TestIssuerTenantMismatchError_Unwrap(t *testing.T) {
	bare := &IssuerTenantMismatchError{TenantID: "acme"}
	if !errors.Is(bare, ErrInvalidCertificate) {
		t.Error("bare IssuerTenantMismatchError does not match ErrInvalidCertificate")
	}

	cause := errors.New("barrier sealed")
	wrapped := &IssuerTenantMismatchError{TenantID: "acme", Err: cause}
	if !errors.Is(wrapped, ErrInvalidCertificate) {
		t.Error("wrapped IssuerTenantMismatchError does not match ErrInvalidCertificate")
	}
	if !errors.Is(wrapped, cause) {
		t.Error("wrapped IssuerTenantMismatchError does not match its own cause")
	}
}
