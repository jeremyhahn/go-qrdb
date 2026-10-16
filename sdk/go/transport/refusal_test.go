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

package transport

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

func TestRefusalError_factorConditionFailed(t *testing.T) {
	want := &auth.FactorConditionFailedError{
		UserID: 4, Type: auth.AuthFactorFIDO2, Condition: auth.FactorConditionExpectedVersion,
		ExpectedVersion: 1, CurrentVersion: 2,
	}
	err := RefusalError(auth.ReasonFactorConditionFailed, want.Metadata())
	if !errors.Is(err, auth.ErrFactorConditionFailed) {
		t.Fatalf("RefusalError() = %v, want ErrFactorConditionFailed", err)
	}
	got, ok := errors.AsType[*auth.FactorConditionFailedError](err)
	if !ok || *got != *want {
		t.Fatalf("RefusalError() = %+v, want %+v", got, want)
	}
}

func TestRefusalError_unknownReasonIsNil(t *testing.T) {
	for _, reason := range []string{"", "NOT_A_REPLICA", "NOT_LEADER", "SOMETHING_NEWER"} {
		if err := RefusalError(reason, map[string]string{"k": "v"}); err != nil {
			t.Fatalf("RefusalError(%q) = %v, want nil", reason, err)
		}
	}
}

func TestAuthFactorAddRequest_Condition(t *testing.T) {
	v := uint64(0)
	cases := map[string]struct {
		req  AuthFactorAddRequest
		want *auth.FactorAddCondition
	}{
		"plain":            {req: AuthFactorAddRequest{}},
		"no active type":   {req: AuthFactorAddRequest{IfNoActiveType: true}, want: &auth.FactorAddCondition{NoActiveOfType: true}},
		"expected version": {req: AuthFactorAddRequest{ExpectedFactorSetVersion: &v}, want: &auth.FactorAddCondition{ExpectedVersion: &v}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.req.Condition()
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("Condition() = %+v, want %+v", got, tc.want)
			}
			if got != nil && (got.NoActiveOfType != tc.want.NoActiveOfType || got.ExpectedVersion != tc.want.ExpectedVersion) {
				t.Fatalf("Condition() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestAuthFactorRotateRequest_Validate(t *testing.T) {
	plain := AuthFactorRotateRequest{OldFactorID: "f-1", NewFactor: AuthFactorAddRequest{Type: "mtls"}}
	if err := plain.Validate(); err != nil {
		t.Fatalf("plain rotation: err = %v, want nil", err)
	}
	v := uint64(2)
	for name, nf := range map[string]AuthFactorAddRequest{
		"no active type":   {IfNoActiveType: true},
		"expected version": {ExpectedFactorSetVersion: &v},
	} {
		req := AuthFactorRotateRequest{OldFactorID: "f-1", NewFactor: nf}
		if err := req.Validate(); !errors.Is(err, auth.ErrInvalidFactor) {
			t.Fatalf("%s: err = %v, want ErrInvalidFactor", name, err)
		}
	}
}
