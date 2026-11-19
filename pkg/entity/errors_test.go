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
