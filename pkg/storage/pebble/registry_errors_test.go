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

package pebble_test

import (
	"errors"
	"testing"

	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

func TestRegistryErrors_Error(t *testing.T) {
	cause := errors.New("disk gone")
	tests := map[string]struct {
		err  error
		want string
	}{
		"empty tenant id": {
			err:  &pebblestore.EmptyTenantIDError{},
			want: "qrdb/storage/pebble: tenant id must not be empty",
		},
		"empty path": {
			err:  &pebblestore.EmptyPathError{},
			want: "qrdb/storage/pebble: per-tenant path must not be empty",
		},
		"registry closed": {
			err:  &pebblestore.RegistryClosedError{},
			want: "qrdb/storage/pebble: per-tenant registry is closed",
		},
		"path conflict": {
			err: &pebblestore.TenantPathConflictError{
				TenantID: "tenant-a", ExistingPath: "/data/a", RequestedPath: "/data/b",
			},
			want: `qrdb/storage/pebble: tenant "tenant-a" already opened at "/data/a", refused to re-open at "/data/b"`,
		},
		"not registered": {
			err:  &pebblestore.NotRegisteredError{TenantID: "ghost"},
			want: `qrdb/storage/pebble: tenant "ghost" is not registered`,
		},
		"open": {
			err:  &pebblestore.OpenError{TenantID: "t1", Path: "/p", Err: cause},
			want: `qrdb/storage/pebble: open tenant "t1" at "/p": disk gone`,
		},
		"close": {
			err:  &pebblestore.CloseError{TenantID: "t1", Path: "/p", Err: cause},
			want: `qrdb/storage/pebble: close tenant "t1" at "/p": disk gone`,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRegistryErrors_Unwrap(t *testing.T) {
	cause := errors.New("disk gone")
	tests := map[string]error{
		"open":  &pebblestore.OpenError{TenantID: "t1", Path: "/p", Err: cause},
		"close": &pebblestore.CloseError{TenantID: "t1", Path: "/p", Err: cause},
	}
	for name, err := range tests {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(err, cause) {
				t.Errorf("errors.Is(%v, cause) = false, want true", err)
			}
		})
	}
}
