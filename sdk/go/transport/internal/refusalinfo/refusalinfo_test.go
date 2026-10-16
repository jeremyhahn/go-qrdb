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

package refusalinfo

import (
	"errors"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
)

func withInfo(t *testing.T, info *errdetails.ErrorInfo) *status.Status {
	t.Helper()
	st, err := status.New(codes.AlreadyExists, "refused").WithDetails(info)
	if err != nil {
		t.Fatalf("WithDetails: %v", err)
	}
	return st
}

func TestErrorDomain_matchesServer(t *testing.T) {
	if ErrorDomain != replicaforward.ErrorDomain {
		t.Fatalf("ErrorDomain = %q, server sends %q", ErrorDomain, replicaforward.ErrorDomain)
	}
}

func TestError_factorConditionFailed(t *testing.T) {
	want := &auth.FactorConditionFailedError{UserID: 3, Type: auth.AuthFactorPIV, Condition: auth.FactorConditionNoActiveOfType, ExistingFactorID: "f-2"}
	st := withInfo(t, &errdetails.ErrorInfo{Domain: ErrorDomain, Reason: auth.ReasonFactorConditionFailed, Metadata: want.Metadata()})
	got, ok := errors.AsType[*auth.FactorConditionFailedError](Error(st))
	if !ok || *got != *want {
		t.Fatalf("Error() = %+v, want %+v", got, want)
	}
}

func TestError_noneDecoded(t *testing.T) {
	cases := map[string]*status.Status{
		"no details":     status.New(codes.Internal, "boom"),
		"foreign domain": withInfo(t, &errdetails.ErrorInfo{Domain: "other.v1", Reason: auth.ReasonFactorConditionFailed}),
		"replica reason": withInfo(t, &errdetails.ErrorInfo{Domain: ErrorDomain, Reason: replicaforward.ReasonNotLeader}),
	}
	for name, st := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Error(st); err != nil {
				t.Fatalf("Error() = %v, want nil", err)
			}
		})
	}
}
