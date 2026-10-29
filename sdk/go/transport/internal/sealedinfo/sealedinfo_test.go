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

package sealedinfo

import (
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
)

func withInfo(t *testing.T, code codes.Code, domain, reason string) *status.Status {
	t.Helper()
	st, err := status.New(code, sealedwindow.SealedMessage).WithDetails(&errdetails.ErrorInfo{Domain: domain, Reason: reason})
	if err != nil {
		t.Fatalf("attach error info: %v", err)
	}
	return st
}

func TestSealed_UnavailableWithTheSealedReason(t *testing.T) {
	if !Sealed(withInfo(t, codes.Unavailable, sealedwindow.ErrorDomain, sealedwindow.ReasonSealed)) {
		t.Fatal("the sealed refusal is not recognised")
	}
}

func TestSealed_RefusesEverythingElse(t *testing.T) {
	for name, st := range map[string]*status.Status{
		"plain unavailable": status.New(codes.Unavailable, "draining"),
		"other reason":      withInfo(t, codes.Unavailable, sealedwindow.ErrorDomain, "DRAINING"),
		"other domain":      withInfo(t, codes.Unavailable, "example.com", sealedwindow.ReasonSealed),
		"other code":        withInfo(t, codes.FailedPrecondition, sealedwindow.ErrorDomain, sealedwindow.ReasonSealed),
	} {
		if Sealed(st) {
			t.Errorf("%s: recognised as sealed", name)
		}
	}
}

func TestSealed_LooksPastDetailsOfOtherTypes(t *testing.T) {
	retry := &errdetails.RetryInfo{}
	sealed := &errdetails.ErrorInfo{Domain: sealedwindow.ErrorDomain, Reason: sealedwindow.ReasonSealed}
	st, err := status.New(codes.Unavailable, sealedwindow.SealedMessage).WithDetails(retry, sealed)
	if err != nil {
		t.Fatalf("attach details: %v", err)
	}
	if !Sealed(st) {
		t.Error("a sealed ErrorInfo after a RetryInfo was not recognised")
	}
	onlyRetry, err := status.New(codes.Unavailable, sealedwindow.SealedMessage).WithDetails(retry)
	if err != nil {
		t.Fatalf("attach details: %v", err)
	}
	if Sealed(onlyRetry) {
		t.Error("a RetryInfo alone was recognised as sealed")
	}
}
