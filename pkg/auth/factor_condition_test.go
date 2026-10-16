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

func TestFactorAddCondition_Validate(t *testing.T) {
	v := uint64(0)
	cases := map[string]struct {
		cond    FactorAddCondition
		wantErr bool
	}{
		"empty":            {cond: FactorAddCondition{}, wantErr: true},
		"no active":        {cond: FactorAddCondition{NoActiveOfType: true}},
		"expected version": {cond: FactorAddCondition{ExpectedVersion: &v}},
		"both":             {cond: FactorAddCondition{NoActiveOfType: true, ExpectedVersion: &v}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := tc.cond.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidFactor) {
				t.Fatalf("Validate() = %v, want ErrInvalidFactor", err)
			}
		})
	}
}

func TestFactorAddConditionOf(t *testing.T) {
	if c := FactorAddConditionOf(false, nil); c != nil {
		t.Fatalf("no fields = %+v, want nil (unconditional)", c)
	}
	v := uint64(3)
	c := FactorAddConditionOf(true, &v)
	if c == nil || !c.NoActiveOfType || c.ExpectedVersion == nil || *c.ExpectedVersion != 3 {
		t.Fatalf("both fields = %+v, want both conditions", c)
	}
	if c := FactorAddConditionOf(false, &v); c == nil || c.NoActiveOfType || c.Validate() != nil {
		t.Fatalf("version only = %+v, want a valid version condition", c)
	}
}

func TestRefuseRotationCondition(t *testing.T) {
	if err := RefuseRotationCondition(nil); err != nil {
		t.Fatalf("no condition: err = %v, want nil", err)
	}
	v := uint64(0)
	for name, cond := range map[string]*FactorAddCondition{
		"no active of type": {NoActiveOfType: true},
		"expected version":  {ExpectedVersion: &v},
	} {
		if err := RefuseRotationCondition(cond); !errors.Is(err, ErrInvalidFactor) {
			t.Fatalf("%s: err = %v, want ErrInvalidFactor", name, err)
		}
	}
}

func TestFactorConditionFailedError_ErrorAndUnwrap(t *testing.T) {
	typeErr := &FactorConditionFailedError{
		UserID: 7, Type: AuthFactorMTLS, Condition: FactorConditionNoActiveOfType, ExistingFactorID: "f-1",
	}
	if !errors.Is(typeErr, ErrFactorConditionFailed) {
		t.Fatal("errors.Is(no-active error, ErrFactorConditionFailed) = false")
	}
	if !strings.Contains(typeErr.Error(), "f-1") || !strings.Contains(typeErr.Error(), string(AuthFactorMTLS)) {
		t.Fatalf("Error() = %q, want factor id and type", typeErr.Error())
	}

	versionErr := &FactorConditionFailedError{
		UserID: 7, Condition: FactorConditionExpectedVersion, ExpectedVersion: 2, CurrentVersion: 5,
	}
	if !errors.Is(versionErr, ErrFactorConditionFailed) {
		t.Fatal("errors.Is(version error, ErrFactorConditionFailed) = false")
	}
	if !strings.Contains(versionErr.Error(), "is 5, expected 2") {
		t.Fatalf("Error() = %q, want both versions", versionErr.Error())
	}
}

func TestFactorConditionFailedError_MetadataRoundTrip(t *testing.T) {
	want := &FactorConditionFailedError{
		UserID:           42,
		Type:             AuthFactorFIDO2,
		Condition:        FactorConditionExpectedVersion,
		ExpectedVersion:  3,
		CurrentVersion:   4,
		ExistingFactorID: "f-9",
	}
	got := FactorConditionFailedFromMetadata(want.Metadata())
	if *got != *want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestFactorConditionFailedFromMetadata_Malformed(t *testing.T) {
	got := FactorConditionFailedFromMetadata(map[string]string{
		conditionKeyUserID:         "not-a-number",
		conditionKeyCurrentVersion: "-1",
	})
	if !errors.Is(got, ErrFactorConditionFailed) {
		t.Fatal("decoded error does not match ErrFactorConditionFailed")
	}
	if got.UserID != 0 || got.CurrentVersion != 0 {
		t.Fatalf("malformed keys decoded to %+v, want zero fields", got)
	}
	if empty := FactorConditionFailedFromMetadata(nil); !errors.Is(empty, ErrFactorConditionFailed) {
		t.Fatal("nil metadata does not decode to the sentinel")
	}
}
