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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TenantBarrierSeal seals a per-tenant barrier by delegating to the
// configured TenantServicer.
func (t *Transport) TenantBarrierSeal(ctx context.Context, req *transport.TenantBarrierSealRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return &NilServiceError{Service: "TenantServicer"}
	}
	if req == nil {
		return &NilRequestError{Method: "TenantBarrierSeal"}
	}
	return t.tenantSvc.TenantBarrierSeal(ctx, req.TenantID)
}

// TenantBarrierStatus reports the current sealed/initialized state for a
// per-tenant barrier by delegating to the configured TenantServicer.
func (t *Transport) TenantBarrierStatus(ctx context.Context, req *transport.TenantBarrierStatusRequest) (*transport.TenantBarrierStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.tenantSvc == nil {
		return nil, &NilServiceError{Service: "TenantServicer"}
	}
	if req == nil {
		return nil, &NilRequestError{Method: "TenantBarrierStatus"}
	}
	status, err := t.tenantSvc.TenantBarrierStatus(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}
	return &transport.TenantBarrierStatusResponse{
		TenantID:        status.TenantID,
		Sealed:          status.Sealed,
		Initialized:     status.Initialized,
		Strategy:        status.Strategy,
		HardwareBacked:  status.HardwareBacked,
		ShamirThreshold: status.ShamirThreshold,
		ShamirTotal:     status.ShamirTotal,
	}, nil
}
