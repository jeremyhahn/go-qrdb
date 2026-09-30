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

package rest

import (
	"errors"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestHTTPStatusByClass_IsTotal fails for a class with no row. A missing row
// would render as status 0, which net/http refuses to write.
func TestHTTPStatusByClass_IsTotal(t *testing.T) {
	for _, c := range errclass.All() {
		if code, ok := httpStatusByClass[c]; !ok || http.StatusText(code) == "" {
			t.Errorf("class %s has no HTTP status (got %d)", c, code)
		}
	}
	if len(httpStatusByClass) != len(errclass.All()) {
		t.Errorf("httpStatusByClass has %d rows for %d classes", len(httpStatusByClass), len(errclass.All()))
	}
}

func TestMapHTTPError_NilIsOK(t *testing.T) {
	if got := mapHTTPError(nil); got != http.StatusOK {
		t.Errorf("mapHTTPError(nil) = %d, want 200", got)
	}
	if got := ServiceErrorStatus(errors.New("nobody typed this")); got != http.StatusInternalServerError {
		t.Errorf("unclassified = %d, want 500", got)
	}
}

// TestMapHTTPError_ResolvedDisagreements pins the rows where REST and gRPC
// used to answer one condition two different ways (go-qrdb#557). Each now
// has one class, and REST renders it.
func TestMapHTTPError_ResolvedDisagreements(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		// gRPC said FailedPrecondition; the identity contract says 403.
		{"system role immutable", &role.SystemRoleImmutableError{}, http.StatusForbidden},
		// REST said 410; the credentials contract says 404, as gRPC did.
		{"credential version not found", credentials.ErrVersionNotFound, http.StatusNotFound},
		{"credential version expired", credentials.ErrVersionExpired, http.StatusNotFound},
		// REST fell to 500; gRPC said AlreadyExists.
		{"credential already submitted", credentials.ErrCredentialAlreadySubmitted, http.StatusConflict},
		// One catch-all type used to carry every state-machine refusal: REST
		// said 501, gRPC and QUIC Internal. Each kind now has its own type.
		{"state machine: invalid TTL", &service.CommandRejectedError{Message: "invalid ttl"}, http.StatusBadRequest},
		{"state machine: unique violation", &service.CommandConflictError{Message: "unique"}, http.StatusConflict},
		{"state machine: closed lessor", &service.SubsystemClosedError{Message: "lessor closed"}, http.StatusServiceUnavailable},
		{"state machine: undecodable command", &service.CommandCorruptError{Message: "decode"}, http.StatusInternalServerError},
		// gRPC said Internal for the drain REST did not list; both now 503.
		{"draining node", &service.DrainError{Err: errors.New("leaving")}, http.StatusServiceUnavailable},
	} {
		if got := mapHTTPError(tc.err); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestLocalErrorsDeclareTheirClass(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
	}{
		{&ServiceNotImplementedError{Service: "mvcc"}, http.StatusNotImplemented},
		{&RequestTooLargeError{Limit: 1}, http.StatusRequestEntityTooLarge},
		{&PayloadTooLargeError{Size: 2, Limit: 1}, http.StatusRequestEntityTooLarge},
		{&KeyTooLargeError{Size: 2, Limit: 1}, http.StatusRequestEntityTooLarge},
		{&service.ClientCertRequiredError{}, http.StatusUnauthorized},
	} {
		if got := ServiceErrorStatus(tc.err); got != tc.want {
			t.Errorf("ServiceErrorStatus(%T) = %d, want %d", tc.err, got, tc.want)
		}
	}
}
