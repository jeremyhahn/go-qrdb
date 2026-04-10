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
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.DEKClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// DEKClient implementation (stub - pending proto generation)
// ---------------------------------------------------------------------------

// DEKInitialize initializes the cluster DEK with the given algorithm and key
// size.
func (t *Transport) DEKInitialize(ctx context.Context, req *transport.DEKInitializeRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return &transport.NotImplementedError{Method: "DEKInitialize"}
}

// DEKGetStatus returns the current cluster DEK status.
func (t *Transport) DEKGetStatus(ctx context.Context) (*transport.DEKStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return nil, &transport.NotImplementedError{Method: "DEKGetStatus"}
}

// DEKRotate rotates the cluster DEK to a new version.
func (t *Transport) DEKRotate(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return &transport.NotImplementedError{Method: "DEKRotate"}
}

// DEKGetCurrent returns the current DEK metadata.
func (t *Transport) DEKGetCurrent(ctx context.Context) (*transport.DEKCurrentResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return nil, &transport.NotImplementedError{Method: "DEKGetCurrent"}
}

// DEKSyncToNode synchronizes the DEK to a specific node.
func (t *Transport) DEKSyncToNode(ctx context.Context, req *transport.DEKSyncRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return &transport.NotImplementedError{Method: "DEKSyncToNode"}
}

// TenantDEKGetOrCreate returns the DEK for a tenant, creating if needed.
func (t *Transport) TenantDEKGetOrCreate(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return nil, &transport.NotImplementedError{Method: "TenantDEKGetOrCreate"}
}

// TenantDEKRotate rotates the DEK for a specific tenant.
func (t *Transport) TenantDEKRotate(ctx context.Context, req *transport.TenantDEKRotateRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return &transport.NotImplementedError{Method: "TenantDEKRotate"}
}

// TenantDEKDelete deletes a tenant's DEK.
func (t *Transport) TenantDEKDelete(ctx context.Context, req *transport.TenantDEKDeleteRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	return &transport.NotImplementedError{Method: "TenantDEKDelete"}
}

// TenantDEKList returns all tenant IDs that have DEKs.
func (t *Transport) TenantDEKList(ctx context.Context) (*transport.TenantDEKListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return nil, &transport.NotImplementedError{Method: "TenantDEKList"}
}

// TenantDEKGetStatus returns the status of a tenant's DEK.
func (t *Transport) TenantDEKGetStatus(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	return nil, &transport.NotImplementedError{Method: "TenantDEKGetStatus"}
}
