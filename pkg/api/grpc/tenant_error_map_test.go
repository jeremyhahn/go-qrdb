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

package grpc

import (
	"fmt"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// This file carries NO build constraint, deliberately: service_tenant.go,
// where mapServiceError lives, carries none either, and a test narrower than
// the code it covers stops covering it under every tag set it excludes. The
// package's other tenant test file is constrained on six feature tags, which
// is the right scope for the fixtures it holds and the wrong scope for a
// status-mapping table.

// TestMapTenantError_TeardownRefusals pins the codes for the three refusals
// the tenant teardown added (go-qrdb#413).
//
// ReservedTenantIDError is in the table because it had NO mapping at all: it
// fell through to Internal, reporting a client naming a reserved id as a
// server fault. That was latent on the create path and became reachable on
// the delete path when the reserved-id guard was hoisted there.
func TestMapTenantError_TeardownRefusals(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{
			name: "deleted id is not available",
			err:  &tenant.TenantDeletedError{TenantID: "acme", DeletedAt: time.Now().UTC()},
			want: codes.AlreadyExists,
		},
		{
			name: "tenant still owns databases",
			err:  &tenant.TenantHasDatabasesError{TenantID: "acme", Databases: []string{"ledger"}},
			want: codes.FailedPrecondition,
		},
		{
			name: "reserved id is a client error, not a server fault",
			err:  &tenant.ReservedTenantIDError{TenantID: "system", Reason: "reserved"},
			want: codes.InvalidArgument,
		},
		{
			name: "reserved id wrapped by a caller",
			err:  fmt.Errorf("delete: %w", &tenant.ReservedTenantIDError{TenantID: "system", Reason: "reserved"}),
			want: codes.InvalidArgument,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := status.Code(mapServiceError(tc.err)); got != tc.want {
				t.Errorf("mapServiceError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
