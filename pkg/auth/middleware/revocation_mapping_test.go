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

// This file carries NO build constraint and uses only stdlib plus pkg/auth,
// so it compiles under every tag set (go-qrdb#178).

package middleware

import (
	"errors"
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// A revoked certificate is an authentication failure: the credential is no
// longer accepted. 401 / Unauthenticated, matching invalid_cert.
func TestMapAuthError_CertRevoked(t *testing.T) {
	if got := mapHTTPAuthError(auth.ErrCertificateRevoked); got != http.StatusUnauthorized {
		t.Errorf("mapHTTPAuthError(revoked) = %d, want %d", got, http.StatusUnauthorized)
	}
	if got := status.Code(mapGRPCAuthError(auth.ErrCertificateRevoked)); got != codes.Unauthenticated {
		t.Errorf("mapGRPCAuthError(revoked) = %v, want %v", got, codes.Unauthenticated)
	}
}

// A revocation check that could not run is NOT a denial: the certificate may
// be perfectly good and this node could not read replicated state. 503 /
// UNAVAILABLE tells a legitimate caller to retry instead of sending it off to
// re-enrol a credential that is fine.
func TestMapAuthError_RevocationCheckFailure(t *testing.T) {
	err := &auth.RevocationCheckError{Err: errors.New("kv read failed")}

	if got := mapHTTPAuthError(err); got != http.StatusServiceUnavailable {
		t.Errorf("mapHTTPAuthError(check failure) = %d, want %d", got, http.StatusServiceUnavailable)
	}
	if got := status.Code(mapGRPCAuthError(err)); got != codes.Unavailable {
		t.Errorf("mapGRPCAuthError(check failure) = %v, want %v", got, codes.Unavailable)
	}
}

// The authenticator chain wraps a non-AuthError in AuthChainError when every
// authenticator fails, so the mapping must survive that wrapping — otherwise
// the 503 silently degrades to the chain's 401.
func TestMapAuthError_RevocationCheckFailureThroughChain(t *testing.T) {
	err := &AuthChainError{Err: &auth.RevocationCheckError{Err: errors.New("kv read failed")}}

	if got := mapHTTPAuthError(err); got != http.StatusServiceUnavailable {
		t.Errorf("mapHTTPAuthError(wrapped) = %d, want %d", got, http.StatusServiceUnavailable)
	}
	if got := status.Code(mapGRPCAuthError(err)); got != codes.Unavailable {
		t.Errorf("mapGRPCAuthError(wrapped) = %v, want %v", got, codes.Unavailable)
	}
}
