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

package grpc

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// classifierFixtures is one representative error per family. It is
// deliberately not exhaustive: every row of the classification table is fed
// through every transport by the parity test in pkg/api/errclass.
var classifierFixtures = []struct {
	name string
	err  error
	want codes.Code
}{
	{"data plane: missing key", &service.KeyNotFoundError{Key: "k"}, codes.NotFound},
	{"data plane: closed store", &service.ProposeError{Err: storage.ErrStorageClosed}, codes.Unavailable},
	{"data plane: capability refusal", &service.CapabilityUnsupportedError{Message: "mvcc not configured"}, codes.Unimplemented},
	{"mode gate: raft-only capability under standalone", &service.CapabilityUnsupportedError{Message: "mvcc: not implemented on this server: requires mode: raft"}, codes.Unimplemented},
	{"data plane: compacted revision", &service.RevisionCompactedError{}, codes.OutOfRange},
	{"identity: missing user", &user.UserNotFoundError{ID: 3}, codes.NotFound},
	{"identity: username taken", &user.UsernameExistsError{}, codes.AlreadyExists},
	{"identity: generated user id collided", &user.UserIDExistsError{}, codes.AlreadyExists},
	{"identity: thumbprint bound to another user", &user.CertBindingConflictError{Thumbprint: "AAAA", BoundUserID: 7, TargetUserID: 9}, codes.AlreadyExists},
	{"identity: lost concurrent update", &user.ConcurrentUpdateError{}, codes.Aborted},
	{"identity: missing role", &role.RoleNotFoundError{ID: "admin"}, codes.NotFound},
	{"nothing typed it", errors.New("a bare error"), codes.Internal},
}

// TestServiceErrorCode_ClassifiesEveryFamily asserts one mapping answers for
// every family. MCP's tool-handler middleware is family-agnostic by design, so
// it depends on exactly this.
func TestServiceErrorCode_ClassifiesEveryFamily(t *testing.T) {
	for _, tc := range classifierFixtures {
		t.Run(tc.name, func(t *testing.T) {
			if got := ServiceErrorCode(tc.err); got != tc.want {
				t.Errorf("ServiceErrorCode = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestServiceErrorCode_NilIsOK(t *testing.T) {
	if got := ServiceErrorCode(nil); got != codes.OK {
		t.Errorf("ServiceErrorCode(nil) = %s, want OK", got)
	}
}

// TestMapServiceError_CarriesServiceErrorCode pins that the status error every
// gRPC service returns carries exactly the code ServiceErrorCode reports — the
// code MCP renders — and the error's own message.
func TestMapServiceError_CarriesServiceErrorCode(t *testing.T) {
	for _, tc := range classifierFixtures {
		t.Run(tc.name, func(t *testing.T) {
			st, ok := status.FromError(mapServiceError(tc.err))
			if !ok {
				t.Fatalf("mapServiceError(%v) is not a gRPC status error", tc.err)
			}
			if st.Code() != ServiceErrorCode(tc.err) {
				t.Errorf("status code %s, ServiceErrorCode %s", st.Code(), ServiceErrorCode(tc.err))
			}
			if st.Message() != tc.err.Error() {
				t.Errorf("status message %q, want %q", st.Message(), tc.err.Error())
			}
		})
	}
}

func TestMapServiceError_NilIsNil(t *testing.T) {
	if err := mapServiceError(nil); err != nil {
		t.Errorf("mapServiceError(nil) = %v, want nil", err)
	}
}

// TestGRPCCodeByClass_IsTotal fails for a class with no row. A missing row
// reads as codes.OK, the zero value — a failure reported as a success.
func TestGRPCCodeByClass_IsTotal(t *testing.T) {
	for _, c := range errclass.All() {
		if _, ok := grpcCodeByClass[c]; !ok {
			t.Errorf("class %s has no gRPC code", c)
		}
	}
	if len(grpcCodeByClass) != len(errclass.All()) {
		t.Errorf("grpcCodeByClass has %d rows for %d classes", len(grpcCodeByClass), len(errclass.All()))
	}
}

// TestLocalErrorsDeclareTheirClass drives this package's own refusals through
// the real mapping: they live here, so the table in pkg/api/errclass cannot
// list them, and each one's class is declared beside grpcCodeByClass instead.
func TestLocalErrorsDeclareTheirClass(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want codes.Code
	}{
		{&ServiceNotConfiguredError{Service: "backup"}, codes.Unimplemented},
		{&CeremonyCANotConfiguredError{}, codes.Unimplemented},
	} {
		if got := ServiceErrorCode(tc.err); got != tc.want {
			t.Errorf("ServiceErrorCode(%T) = %s, want %s", tc.err, got, tc.want)
		}
	}
}
