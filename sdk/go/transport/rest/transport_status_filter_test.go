// Copyright 2026 Jeremy Hahn
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

package rest

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestRESTListTenants_OlderServerIgnoresStatus is the rolling-upgrade case.
// The double is a base-version server: it predates the ?status= filter,
// drops it and answers every tenant. A filtered list must fail with
// *CapabilityUnsupportedError and no tenants; an unfiltered list still
// succeeds.
func TestRESTListTenants_OlderServerIgnoresStatus(t *testing.T) {
	rt, rs, _ := newExtTransport(t)
	rs.handle(exactPath(http.MethodGet, "/v1/tenants/list"), func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"tenants": []map[string]any{
				{"id": "t-active", "name": "a", "status": "active"},
				{"id": "t-suspended", "name": "s", "status": "suspended"},
			},
			"total": 2,
		})
	})

	resp, err := rt.ListTenants(context.Background(), &transport.ListTenantsRequest{Status: transport.TenantStatusSuspended})
	var unsupported *transport.CapabilityUnsupportedError
	if !errors.As(err, &unsupported) || resp != nil {
		t.Fatalf("ListTenants(status=suspended) on an older server = %+v, %v; want nil, CapabilityUnsupportedError", resp, err)
	}

	resp, err = rt.ListTenants(context.Background(), &transport.ListTenantsRequest{})
	if err != nil || len(resp.Tenants) != 2 {
		t.Fatalf("unfiltered ListTenants on an older server = %+v, %v; want 2 tenants", resp, err)
	}
}
