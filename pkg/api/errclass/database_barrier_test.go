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

package errclass

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// TestClassify_DatabaseBarrierRequestRefusals pins the per-database barrier
// refusals a create or update can meet. Each is decided by the request
// against the database's own configuration, never a server fault, so none may
// answer Internal (they answered 500 on REST).
func TestClassify_DatabaseBarrierRequestRefusals(t *testing.T) {
	cases := []struct {
		err  error
		want Class
	}{
		// A barrier asked for on the shared default store, which has no key
		// of its own: the configuration refusing the feature.
		{&database.SharedStoreBarrierUnsupportedError{Name: "orders", Backend: database.BackendPebble}, Unprocessable},
		// A barrier strategy per-database barriers do not implement.
		{&database.UnsupportedBarrierStrategyError{Mode: "shamir"}, InvalidArgument},
		// Turning an existing database's barrier on or off would need a data
		// migration this release does not perform.
		{&database.BarrierMigrationNotSupportedError{Name: "orders", Direction: "enable"}, FailedPrecondition},
		// Shamir custody of a database barrier.
		{&database.ShamirSecretNotAllowedError{Name: "vault"}, InvalidArgument},
		{&database.ShamirCustodyNotConfiguredError{Name: "vault"}, FailedPrecondition},
		{&database.NotShamirBarrierError{Name: "plain"}, FailedPrecondition},
	}
	for _, tc := range cases {
		if got := Classify(tc.err); got != tc.want {
			t.Errorf("Classify(%T) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
