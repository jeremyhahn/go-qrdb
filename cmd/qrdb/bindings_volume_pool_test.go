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

package main

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/pkg/volume/volumetest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// newTestVolumePoolBinding builds a VolumePoolBinding over an embedded
// transport whose volume.Service is svc.
func newTestVolumePoolBinding(t *testing.T, svc volume.Service) *VolumePoolBinding {
	t.Helper()
	conn := newTestConnectionBinding(t, &noopKVService{}, newMockAdminService(),
		embedded.WithVolumeService(svc))
	return NewVolumePoolBinding(testLogger(t), conn)
}

func TestVolumePoolBinding_RoundTrip(t *testing.T) {
	var (
		created volume.CreatePoolRequest
		updated volume.UpdatePoolRequest
		deleted string
		listed  [3]any
	)
	svc := &volumetest.Service{
		T:           t,
		ListPoolsFn: func(context.Context) ([]volume.PoolView, error) { return samplePoolViews(), nil },
		GetPoolFn: func(_ context.Context, name string) (*volume.PoolDetail, error) {
			return &volume.PoolDetail{
				PoolView:    samplePoolViews()[0],
				NodeReports: []volume.NodeReportRow{{NodeID: "node-1", State: "ready"}},
			}, nil
		},
		CreatePoolFn: func(_ context.Context, req volume.CreatePoolRequest) (*volume.Pool, error) {
			created = req
			return &volume.Pool{Name: req.Name, Provider: "dir", CapacityBytes: req.CapacityBytes, Overcommit: 1}, nil
		},
		UpdatePoolFn: func(_ context.Context, name string, req volume.UpdatePoolRequest) (*volume.Pool, error) {
			updated = req
			return &volume.Pool{Name: name, Provider: "dir", Description: *req.Description, Overcommit: 1}, nil
		},
		DeletePoolFn: func(_ context.Context, name string) error {
			deleted = name
			return nil
		},
		ListAssignmentsFn: func(_ context.Context, pool, token string, size int) (*volume.AssignmentPage, error) {
			listed = [3]any{pool, token, size}
			return &volume.AssignmentPage{
				Assignments:   []volume.Assignment{{TenantID: "acme", Pool: pool, ReservedBytes: 1024}},
				NextPageToken: "next",
			}, nil
		},
	}
	b := newTestVolumePoolBinding(t, svc)

	pools, err := b.List()
	if err != nil || len(pools) != 1 || pools[0].Name != "fast" || pools[0].TenantCount != 3 {
		t.Fatalf("List = %+v, %v; want one pool fast with 3 tenants", pools, err)
	}

	detail, err := b.Get("fast")
	if err != nil || detail.Name != "fast" || len(detail.NodeReports) != 1 || detail.NodeReports[0].NodeID != "node-1" {
		t.Fatalf("Get = %+v, %v; want fast with node-1", detail, err)
	}

	pool, err := b.Create(&transport.CreateVolumePoolRequest{Name: "bulk", CapacityBytes: 1 << 30})
	if err != nil || pool.Name != "bulk" || created.Name != "bulk" || created.CapacityBytes != 1<<30 {
		t.Fatalf("Create = %+v, %v (service saw %+v); want bulk", pool, err, created)
	}

	desc := "archive tier"
	pool, err = b.Update("bulk", &transport.UpdateVolumePoolRequest{Name: "ignored", Description: &desc})
	if err != nil || pool.Name != "bulk" || pool.Description != desc || updated.Description == nil || *updated.Description != desc {
		t.Fatalf("Update = %+v, %v; want bulk with description %q", pool, err, desc)
	}

	page, err := b.ListTenants("fast", 10, "tok")
	if err != nil || len(page.Tenants) != 1 || page.Tenants[0].TenantID != "acme" || page.NextPageToken != "next" {
		t.Fatalf("ListTenants = %+v, %v; want acme and next token", page, err)
	}
	if listed != [3]any{"fast", "tok", 10} {
		t.Fatalf("ListTenants passed %v; want [fast tok 10]", listed)
	}

	if err := b.Delete("bulk"); err != nil || deleted != "bulk" {
		t.Fatalf("Delete = %v (service saw %q); want bulk deleted", err, deleted)
	}
}

func TestVolumePoolBinding_EmptyListsAreNotNil(t *testing.T) {
	svc := &volumetest.Service{
		T:           t,
		ListPoolsFn: func(context.Context) ([]volume.PoolView, error) { return nil, nil },
		ListAssignmentsFn: func(context.Context, string, string, int) (*volume.AssignmentPage, error) {
			return &volume.AssignmentPage{}, nil
		},
	}
	b := newTestVolumePoolBinding(t, svc)
	pools, err := b.List()
	if err != nil || pools == nil || len(pools) != 0 {
		t.Fatalf("List = %#v, %v; want empty non-nil slice", pools, err)
	}
	page, err := b.ListTenants("fast", 0, "")
	if err != nil || page.Tenants == nil || len(page.Tenants) != 0 {
		t.Fatalf("ListTenants = %#v, %v; want empty non-nil tenants", page, err)
	}
}

func TestVolumePoolBinding_ServiceErrors(t *testing.T) {
	boom := errors.New("catalog down")
	svc := &volumetest.Service{
		T:           t,
		ListPoolsFn: func(context.Context) ([]volume.PoolView, error) { return nil, boom },
		GetPoolFn:   func(context.Context, string) (*volume.PoolDetail, error) { return nil, boom },
		CreatePoolFn: func(context.Context, volume.CreatePoolRequest) (*volume.Pool, error) {
			return nil, boom
		},
		UpdatePoolFn: func(context.Context, string, volume.UpdatePoolRequest) (*volume.Pool, error) {
			return nil, boom
		},
		DeletePoolFn: func(context.Context, string) error { return boom },
		ListAssignmentsFn: func(context.Context, string, string, int) (*volume.AssignmentPage, error) {
			return nil, boom
		},
	}
	b := newTestVolumePoolBinding(t, svc)
	desc := "x"
	for name, call := range volumePoolBindingCalls(b, &desc) {
		if err := call(); !errors.Is(err, boom) {
			t.Errorf("%s error = %v; want %v", name, err, boom)
		}
	}
}

func TestVolumePoolBinding_NotConnected(t *testing.T) {
	b := NewVolumePoolBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	desc := "x"
	for name, call := range volumePoolBindingCalls(b, &desc) {
		t.Run(name, func(t *testing.T) {
			assertNotConnected(t, call())
		})
	}
}

// volumePoolBindingCalls returns each binding method as an error-only call.
func volumePoolBindingCalls(b *VolumePoolBinding, desc *string) map[string]func() error {
	return map[string]func() error{
		"List": func() error { _, err := b.List(); return err },
		"Get":  func() error { _, err := b.Get("fast"); return err },
		"Create": func() error {
			_, err := b.Create(&transport.CreateVolumePoolRequest{Name: "bulk"})
			return err
		},
		"Update": func() error {
			_, err := b.Update("bulk", &transport.UpdateVolumePoolRequest{Description: desc})
			return err
		},
		"Delete":      func() error { return b.Delete("bulk") },
		"ListTenants": func() error { _, err := b.ListTenants("fast", 0, ""); return err },
	}
}
