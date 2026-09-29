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

package mcp

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// A per-database barrier the store cannot honour is a precondition the
// caller's request does not meet, not a server fault: every transport must
// answer it as such rather than as an internal error.
func TestDatabaseBarrierRefusal_IsFailedPrecondition(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"shared store", &database.SharedStoreBarrierUnsupportedError{Name: "db", Backend: database.BackendPebble}},
		{"off data path", &database.BarrierOffDataPathError{Name: "db", ServedBy: database.ServedByRaftGroup}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ServiceErrorCode(c.err); got != wire.CodeFailedPrecondition {
				t.Errorf("status = %v, want CodeFailedPrecondition", got)
			}
		})
	}
}
