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

package quic

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// A database-catalog refusal is the caller's condition, not a server fault:
// every transport answers a missing database as not-found (which is also how
// another tenant's database is answered), a taken name as already-exists and
// an unusable name as an invalid argument -- never as an internal error.
func TestDatabaseCatalogErrors_Status(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want uint16
	}{
		{"missing database", &database.NotFoundError{Name: "db"}, wire.StatusNotFound},
		{"name taken", &database.AlreadyExistsError{Name: "db"}, wire.StatusAlreadyExists},
		{"invalid name", &database.InvalidNameError{Name: "a b"}, wire.StatusInvalidArgument},
		{"reserved name", &database.ReservedNameError{Name: "wal"}, wire.StatusInvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := statusOf(c.err); got != c.want {
				t.Errorf("status = %v, want %v", got, c.want)
			}
		})
	}
}
