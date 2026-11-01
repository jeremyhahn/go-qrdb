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

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.VolumePoolClient = (*Transport)(nil)

// WithVolumeService injects the volume pool management surface
// (docs/volume-pools/api.md) into the transport.
func WithVolumeService(svc volume.Service) Option {
	return func(t *Transport) {
		t.volumeSvc = svc
	}
}

// ListPools lists every pool in the catalog by delegating to the
// volume.Service.
func (t *Transport) ListPools(ctx context.Context) ([]transport.VolumePool, error) {
	ctx, err := t.admit(ctx, "ListPools")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "ListPools"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.volumeSvc == nil {
		return nil, &NilServiceError{Service: "volume.Service"}
	}
	pools, err := t.volumeSvc.ListPools(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]transport.VolumePool, len(pools))
	for i := range pools {
		out[i] = volumePoolFromCatalog(&pools[i])
	}
	return out, nil
}

// GetPool inspects one pool by delegating to the volume.Service.
func (t *Transport) GetPool(ctx context.Context, name string) (*transport.VolumePoolDetail, error) {
	ctx, err := t.admit(ctx, "GetPool")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "GetPool"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.volumeSvc == nil {
		return nil, &NilServiceError{Service: "volume.Service"}
	}
	detail, err := t.volumeSvc.GetPool(ctx, name)
	if err != nil {
		return nil, err
	}
	return volumePoolDetailFromCatalog(detail), nil
}

// CreatePool adds a pool to the catalog by delegating to the volume.Service.
func (t *Transport) CreatePool(ctx context.Context, req *transport.CreateVolumePoolRequest) (*transport.VolumePool, error) {
	ctx, err := t.admit(ctx, "CreatePool")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "CreatePool"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.volumeSvc == nil {
		return nil, &NilServiceError{Service: "volume.Service"}
	}
	pool, err := t.volumeSvc.CreatePool(ctx, volume.CreatePoolRequest{
		Name:          req.Name,
		Provider:      req.Provider,
		Description:   req.Description,
		CapacityBytes: req.CapacityBytes,
		Overcommit:    req.Overcommit,
	})
	if err != nil {
		return nil, err
	}
	out := volumePoolFromCatalog(&volume.PoolView{Pool: *pool})
	return &out, nil
}

// UpdatePool changes a pool's description, capacity or overcommit by
// delegating to the volume.Service.
func (t *Transport) UpdatePool(ctx context.Context, req *transport.UpdateVolumePoolRequest) (*transport.VolumePool, error) {
	ctx, err := t.admit(ctx, "UpdatePool")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "UpdatePool"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if req == nil {
		return nil, &transport.RequestError{Method: "UpdatePool", Err: &transport.NilRequestError{}}
	}
	if t.volumeSvc == nil {
		return nil, &NilServiceError{Service: "volume.Service"}
	}
	pool, err := t.volumeSvc.UpdatePool(ctx, req.Name, volume.UpdatePoolRequest{
		Description:   req.Description,
		CapacityBytes: req.CapacityBytes,
		Overcommit:    req.Overcommit,
	})
	if err != nil {
		return nil, err
	}
	out := volumePoolFromCatalog(&volume.PoolView{Pool: *pool})
	return &out, nil
}

// DeletePool removes a pool from the catalog by delegating to the
// volume.Service.
func (t *Transport) DeletePool(ctx context.Context, name string) error {
	ctx, err := t.admit(ctx, "DeletePool")
	if err != nil {
		return err
	}
	if err := t.authorize(ctx, "DeletePool"); err != nil {
		return err
	}
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	if t.volumeSvc == nil {
		return &NilServiceError{Service: "volume.Service"}
	}
	return t.volumeSvc.DeletePool(ctx, name)
}

// ListPoolTenants lists the tenants assigned to a pool by delegating to
// the volume.Service.
func (t *Transport) ListPoolTenants(ctx context.Context, req *transport.ListVolumePoolTenantsRequest) (*transport.ListVolumePoolTenantsResponse, error) {
	ctx, err := t.admit(ctx, "ListPoolTenants")
	if err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "ListPoolTenants"); err != nil {
		return nil, err
	}
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	if t.volumeSvc == nil {
		return nil, &NilServiceError{Service: "volume.Service"}
	}
	if req == nil {
		req = &transport.ListVolumePoolTenantsRequest{}
	}
	page, err := t.volumeSvc.ListAssignments(ctx, req.Pool, req.PageToken, req.PageSize)
	if err != nil {
		return nil, err
	}
	out := &transport.ListVolumePoolTenantsResponse{
		Tenants:       make([]transport.VolumePoolTenant, len(page.Assignments)),
		NextPageToken: page.NextPageToken,
	}
	for i, a := range page.Assignments {
		out.Tenants[i] = transport.VolumePoolTenant{TenantID: a.TenantID, ReservedBytes: a.ReservedBytes}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Catalog conversion helpers
// ---------------------------------------------------------------------------

func volumePoolFromCatalog(p *volume.PoolView) transport.VolumePool {
	return transport.VolumePool{
		Name:          p.Name,
		Provider:      p.Provider,
		Description:   p.Description,
		CapacityBytes: p.CapacityBytes,
		ReservedBytes: p.ReservedBytes,
		UsedBytes:     p.UsedBytes,
		TenantCount:   p.TenantCount,
		Nodes: transport.VolumePoolNodeCounts{
			Ready:       p.NodeCounts.Ready,
			Locked:      p.NodeCounts.Locked,
			Unavailable: p.NodeCounts.Unavailable,
			Unmapped:    p.NodeCounts.Unmapped,
		},
		Overcommit: p.Overcommit,
		CreatedAt:  p.CreatedAt,
		UpdatedAt:  p.UpdatedAt,
	}
}

func volumePoolDetailFromCatalog(d *volume.PoolDetail) *transport.VolumePoolDetail {
	out := &transport.VolumePoolDetail{
		VolumePool:  volumePoolFromCatalog(&d.PoolView),
		NodeReports: make([]transport.VolumePoolNodeReport, len(d.NodeReports)),
	}
	for i, nr := range d.NodeReports {
		out.NodeReports[i] = transport.VolumePoolNodeReport{
			NodeID:     nr.NodeID,
			State:      nr.State,
			SizeBytes:  nr.SizeBytes,
			FreeBytes:  nr.FreeBytes,
			Detail:     nr.Detail,
			ReportedAt: nr.ReportedAt,
		}
	}
	return out
}
