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

package rest

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ReconcileBuiltInRoles converges the tenant's stored built-in roles onto the
// server build's definitions. Maps to POST /roles/reconcile.
func (t *Transport) ReconcileBuiltInRoles(ctx context.Context, req *transport.ReconcileRolesRequest) (*transport.ReconcileRolesResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	body := transport.ReconcileRolesRequest{}
	if req != nil {
		body = *req
	}

	var resp transport.ReconcileRolesResponse
	if err := t.postJSONDecode(ctx, "ReconcileBuiltInRoles", "/roles/reconcile", body, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}
