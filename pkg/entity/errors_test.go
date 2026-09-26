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

package entity_test

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/entity"
)

func TestAlreadyRegisteredError_Error(t *testing.T) {
	e := &entity.AlreadyRegisteredError{EntityType: "users"}
	want := "entity: already registered with different type: users"
	if got := e.Error(); got != want {
		t.Errorf("AlreadyRegisteredError.Error() = %q, want %q", got, want)
	}
}

func TestAlreadyRegisteredError_ErrorsAs(t *testing.T) {
	e := &entity.AlreadyRegisteredError{EntityType: "products"}
	var target *entity.AlreadyRegisteredError
	if !errors.As(e, &target) {
		t.Fatal("errors.As should match *AlreadyRegisteredError")
	}
	if target.EntityType != "products" {
		t.Errorf("EntityType = %q, want %q", target.EntityType, "products")
	}
}

func TestInvalidRegistrationError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  *entity.InvalidRegistrationError
		want string
	}{
		{
			name: "missing EntityType",
			err:  &entity.InvalidRegistrationError{Field: "EntityType", Reason: "must not be empty"},
			want: "entity: invalid registration: EntityType: must not be empty",
		},
		{
			name: "missing StructType",
			err:  &entity.InvalidRegistrationError{Field: "StructType", Reason: "must not be nil"},
			want: "entity: invalid registration: StructType: must not be nil",
		},
		{
			name: "missing Fields",
			err:  &entity.InvalidRegistrationError{Field: "Fields", Reason: "must not be empty"},
			want: "entity: invalid registration: Fields: must not be empty",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("InvalidRegistrationError.Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInvalidRegistrationError_ErrorsAs(t *testing.T) {
	e := &entity.InvalidRegistrationError{Field: "NewFunc", Reason: "must not be nil"}
	var target *entity.InvalidRegistrationError
	if !errors.As(e, &target) {
		t.Fatal("errors.As should match *InvalidRegistrationError")
	}
	if target.Field != "NewFunc" {
		t.Errorf("Field = %q, want %q", target.Field, "NewFunc")
	}
	if target.Reason != "must not be nil" {
		t.Errorf("Reason = %q, want %q", target.Reason, "must not be nil")
	}
}

func TestErrorTypes_NotInterchangeable(t *testing.T) {
	already := &entity.AlreadyRegisteredError{EntityType: "x"}
	invalid := &entity.InvalidRegistrationError{Field: "f", Reason: "r"}

	var a *entity.AlreadyRegisteredError
	var iv *entity.InvalidRegistrationError

	if errors.As(already, &iv) {
		t.Error("AlreadyRegisteredError should not match InvalidRegistrationError")
	}
	if errors.As(invalid, &a) {
		t.Error("InvalidRegistrationError should not match AlreadyRegisteredError")
	}
}
