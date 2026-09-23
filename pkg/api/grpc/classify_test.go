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

	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// classifierFixtures is one representative error per family, and it is the
// input to both tests below.
//
// It is deliberately not exhaustive — the family mappers have their own tests
// for that — but it must contain at least one error from EVERY classifier in
// serviceErrorClassifiers, because the disagreement test is only as good as
// the errors it feeds in.
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

// TestServiceErrorCode_ClassifiesEveryFamily asserts the chained classifier
// reaches errors no single family mapper knows about.
//
// This is the property MCP depends on: its tool-handler middleware is
// family-agnostic by design, so it asks ServiceErrorCode and has to get the
// answer a per-family gRPC service would have given.
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

// TestServiceErrorCode_AgreesWithTheFamilyMapper pins that consulting every
// classifier does not change the answer a gRPC service gets from its own.
//
// A divergence here is the thing that would make MCP and gRPC report one
// condition as two, which is precisely what go-qrdb#438 chose this design to
// avoid.
func TestServiceErrorCode_AgreesWithTheFamilyMapper(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		mapper func(error) error
	}{
		{"data plane", &service.KeyNotFoundError{Key: "k"}, mapServiceError},
		{"data plane", &service.CapabilityUnsupportedError{Message: "x"}, mapServiceError},
		{"identity", &user.UserNotFoundError{ID: 1}, mapAuthServiceError},
		{"identity", &user.UsernameExistsError{}, mapAuthServiceError},
		{"identity", &user.UserIDExistsError{}, mapAuthServiceError},
		{"identity", &role.RoleNotFoundError{ID: "r"}, mapAuthServiceError},
	} {
		t.Run(tc.name+": "+tc.err.Error(), func(t *testing.T) {
			want := status.Code(tc.mapper(tc.err))
			if got := ServiceErrorCode(tc.err); got != want {
				t.Errorf("chained classifier said %s, the family mapper said %s", got, want)
			}
		})
	}
}

// TestServiceErrorCode_NoFamilyDisagrees is the test ServiceErrorCode's doc
// comment names as the guard on its ordering.
//
// The chain takes the FIRST classifier that answers something other than
// Internal, so the order only matters for an error two families both classify
// — and then only if they classify it DIFFERENTLY. No fixture does today. If
// one ever does, the ordering stops being a formality and this is where that
// surfaces, rather than in a transport reporting a condition its sibling
// reports another way.
func TestServiceErrorCode_NoFamilyDisagrees(t *testing.T) {
	for _, tc := range classifierFixtures {
		t.Run(tc.name, func(t *testing.T) {
			var claimants []codes.Code
			for _, classify := range serviceErrorClassifiers {
				if code := status.Code(classify(tc.err)); code != codes.Internal {
					claimants = append(claimants, code)
				}
			}
			for _, c := range claimants {
				if c != claimants[0] {
					t.Fatalf("two family classifiers disagree about %v: %s and %s. The "+
						"chain's order has become load-bearing; decide which family owns "+
						"this error rather than letting position decide", tc.err, claimants[0], c)
				}
			}
		})
	}
}
