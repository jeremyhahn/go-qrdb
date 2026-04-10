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

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.DEKClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// DEKClient implementation
// ---------------------------------------------------------------------------

// DEKInitialize initializes the cluster DEK with the given algorithm and
// key size by delegating to the injected DEKServicer.
func (t *Transport) DEKInitialize(ctx context.Context, req *transport.DEKInitializeRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.dekSvc == nil {
		return &NilServiceError{Service: "DEKServicer"}
	}
	return t.dekSvc.Initialize(ctx, req.Algorithm, req.KeyBytes)
}

// DEKGetStatus returns the current cluster DEK status by delegating to the
// injected DEKServicer.
func (t *Transport) DEKGetStatus(ctx context.Context) (*transport.DEKStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.dekSvc == nil {
		return nil, &NilServiceError{Service: "DEKServicer"}
	}
	status, err := t.dekSvc.GetStatus(ctx)
	if err != nil {
		return nil, err
	}

	resp := &transport.DEKStatusResponse{
		Initialized: status.Initialized,
		Version:     status.Version,
		Algorithm:   status.Algorithm,
		KeyBytes:    status.KeyBytes,
		CreatedAt:   status.CreatedAt,
		SyncedNodes: status.SyncedNodes,
	}

	if status.Reencryption != nil {
		resp.Reencryption = &transport.DEKReencryptionInfo{
			Active:        status.Reencryption.Active,
			StartedAt:     status.Reencryption.StartedAt,
			TotalKeys:     status.Reencryption.TotalKeys,
			ProcessedKeys: status.Reencryption.ProcessedKeys,
			OldVersion:    status.Reencryption.OldVersion,
			NewVersion:    status.Reencryption.NewVersion,
			Progress:      status.Reencryption.Progress,
		}
	}

	return resp, nil
}

// DEKRotate rotates the cluster DEK to a new version by delegating to the
// injected DEKServicer.
func (t *Transport) DEKRotate(ctx context.Context) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.dekSvc == nil {
		return &NilServiceError{Service: "DEKServicer"}
	}
	return t.dekSvc.Rotate(ctx)
}

// DEKGetCurrent returns the current DEK metadata by delegating to the
// injected DEKServicer. The actual DEK bytes are not exposed through the
// transport layer for security.
func (t *Transport) DEKGetCurrent(ctx context.Context) (*transport.DEKCurrentResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.dekSvc == nil {
		return nil, &NilServiceError{Service: "DEKServicer"}
	}
	_, version, err := t.dekSvc.GetCurrentDEK(ctx)
	if err != nil {
		return nil, err
	}
	return &transport.DEKCurrentResponse{
		Version: version,
	}, nil
}

// DEKSyncToNode synchronizes the DEK to a specific node by delegating to
// the injected DEKServicer.
func (t *Transport) DEKSyncToNode(ctx context.Context, req *transport.DEKSyncRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.dekSvc == nil {
		return &NilServiceError{Service: "DEKServicer"}
	}
	return t.dekSvc.SyncToNode(ctx, req.NodeID)
}

// TenantDEKGetOrCreate returns the DEK for a tenant, creating if needed,
// by delegating to the injected DEKServicer.
func (t *Transport) TenantDEKGetOrCreate(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.dekSvc == nil {
		return nil, &NilServiceError{Service: "DEKServicer"}
	}
	_, err := t.dekSvc.TenantGetOrCreate(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}
	status, err := t.dekSvc.TenantGetStatus(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}
	return &transport.TenantDEKResponse{
		TenantID:  status.TenantID,
		Version:   status.Version,
		Algorithm: status.Algorithm,
		KeyBytes:  status.KeyBytes,
	}, nil
}

// TenantDEKRotate rotates the DEK for a specific tenant by delegating to
// the injected DEKServicer.
func (t *Transport) TenantDEKRotate(ctx context.Context, req *transport.TenantDEKRotateRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.dekSvc == nil {
		return &NilServiceError{Service: "DEKServicer"}
	}
	return t.dekSvc.TenantRotate(ctx, req.TenantID)
}

// TenantDEKDelete deletes a tenant's DEK by delegating to the injected
// DEKServicer.
func (t *Transport) TenantDEKDelete(ctx context.Context, req *transport.TenantDEKDeleteRequest) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.dekSvc == nil {
		return &NilServiceError{Service: "DEKServicer"}
	}
	return t.dekSvc.TenantDelete(ctx, req.TenantID)
}

// TenantDEKList returns all tenant IDs that have DEKs by delegating to
// the injected DEKServicer.
func (t *Transport) TenantDEKList(ctx context.Context) (*transport.TenantDEKListResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.dekSvc == nil {
		return nil, &NilServiceError{Service: "DEKServicer"}
	}
	ids, err := t.dekSvc.TenantList(ctx)
	if err != nil {
		return nil, err
	}
	return &transport.TenantDEKListResponse{
		TenantIDs: ids,
	}, nil
}

// TenantDEKGetStatus returns the status of a tenant's DEK by delegating
// to the injected DEKServicer.
func (t *Transport) TenantDEKGetStatus(ctx context.Context, req *transport.TenantDEKRequest) (*transport.TenantDEKStatusResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.dekSvc == nil {
		return nil, &NilServiceError{Service: "DEKServicer"}
	}
	status, err := t.dekSvc.TenantGetStatus(ctx, req.TenantID)
	if err != nil {
		return nil, err
	}
	return &transport.TenantDEKStatusResponse{
		TenantID:   status.TenantID,
		Version:    status.Version,
		Algorithm:  status.Algorithm,
		KeyBytes:   status.KeyBytes,
		CreatedAt:  status.CreatedAt,
		RotatedAt:  status.RotatedAt,
		AccessedAt: status.AccessedAt,
	}, nil
}

// ---------------------------------------------------------------------------
// Request dispatch handlers
// ---------------------------------------------------------------------------

func handleDEKInitialize(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.DEKInitializeRequest)
	if !ok {
		return &InvalidRequestError{Method: "DEKInitialize"}
	}
	return t.DEKInitialize(ctx, r)
}

func handleDEKGetStatus(ctx context.Context, t *Transport, _, resp any) error {
	result, err := t.DEKGetStatus(ctx)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.DEKStatusResponse); ok {
		*out = result
	}
	return nil
}

func handleDEKRotate(ctx context.Context, t *Transport, _, _ any) error {
	return t.DEKRotate(ctx)
}

func handleDEKGetCurrent(ctx context.Context, t *Transport, _, resp any) error {
	result, err := t.DEKGetCurrent(ctx)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.DEKCurrentResponse); ok {
		*out = result
	}
	return nil
}

func handleDEKSyncToNode(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.DEKSyncRequest)
	if !ok {
		return &InvalidRequestError{Method: "DEKSyncToNode"}
	}
	return t.DEKSyncToNode(ctx, r)
}

func handleTenantDEKGetOrCreate(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.TenantDEKRequest)
	if !ok {
		return &InvalidRequestError{Method: "TenantDEKGetOrCreate"}
	}
	result, err := t.TenantDEKGetOrCreate(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.TenantDEKResponse); ok {
		*out = result
	}
	return nil
}

func handleTenantDEKRotate(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.TenantDEKRotateRequest)
	if !ok {
		return &InvalidRequestError{Method: "TenantDEKRotate"}
	}
	return t.TenantDEKRotate(ctx, r)
}

func handleTenantDEKDelete(ctx context.Context, t *Transport, req, _ any) error {
	r, ok := req.(*transport.TenantDEKDeleteRequest)
	if !ok {
		return &InvalidRequestError{Method: "TenantDEKDelete"}
	}
	return t.TenantDEKDelete(ctx, r)
}

func handleTenantDEKList(ctx context.Context, t *Transport, _, resp any) error {
	result, err := t.TenantDEKList(ctx)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.TenantDEKListResponse); ok {
		*out = result
	}
	return nil
}

func handleTenantDEKGetStatus(ctx context.Context, t *Transport, req, resp any) error {
	r, ok := req.(*transport.TenantDEKRequest)
	if !ok {
		return &InvalidRequestError{Method: "TenantDEKGetStatus"}
	}
	result, err := t.TenantDEKGetStatus(ctx, r)
	if err != nil {
		return err
	}
	if out, ok := resp.(**transport.TenantDEKStatusResponse); ok {
		*out = result
	}
	return nil
}

// WithDEKService injects a DEKServicer into the transport.
func WithDEKService(svc service.DEKServicer) Option {
	return func(t *Transport) {
		t.dekSvc = svc
	}
}
