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

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

// TestHandleReconcileRoles_Success covers the whole report crossing the QUIC
// wire: a payload that lost its added/removed lists would still read as success
// and tell an operator nothing about what changed.
func TestHandleReconcileRoles_Success(t *testing.T) {
	var gotTenant string
	mock := &mockRoleService{
		reconcileFn: func(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
			gotTenant = tenantID
			return &role.ReconcileReport{
				TenantID: tenantID,
				Roles: []role.RoleReconcileResult{
					{
						RoleID:  "r-admin",
						Name:    role.RoleNameAdmin,
						Added:   []permission.Permission{"sdlc:bind"},
						Removed: []permission.Permission{"sdlc:register"},
					},
					{
						RoleID:  "r-operator",
						Name:    role.RoleNameOperator,
						Created: true,
						Added:   []permission.Permission{"orchestrator:task_block"},
					},
				},
			}, nil
		},
	}
	srv := newRoleServer(mock, nil)

	payload, err := json.Marshal(reconcileRolesWireRequest{TenantID: "t1"})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	data, err := srv.handleReconcileRoles(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleReconcileRoles: %v", err)
	}
	if gotTenant != "t1" {
		t.Errorf("service saw tenant %q, want t1", gotTenant)
	}

	var resp reconcileRolesWireResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.TenantID != "t1" {
		t.Errorf("tenantID = %q, want t1", resp.TenantID)
	}
	if len(resp.Roles) != 2 {
		t.Fatalf("roles length = %d, want 2: %+v", len(resp.Roles), resp.Roles)
	}
	if len(resp.Roles[0].Added) != 1 || resp.Roles[0].Added[0] != "sdlc:bind" {
		t.Errorf("admin added = %v, want [sdlc:bind]", resp.Roles[0].Added)
	}
	if len(resp.Roles[0].Removed) != 1 || resp.Roles[0].Removed[0] != "sdlc:register" {
		t.Errorf("admin removed = %v, want [sdlc:register]", resp.Roles[0].Removed)
	}
	if !resp.Roles[1].Created {
		t.Error("operator not reported as created")
	}
}

// TestHandleReconcileRoles_EmptyPayload: an absent payload means "my own
// tenant" and must not be read as malformed.
func TestHandleReconcileRoles_EmptyPayload(t *testing.T) {
	mock := &mockRoleService{
		reconcileFn: func(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
			if tenantID != "" {
				t.Errorf("tenantID = %q, want empty", tenantID)
			}
			return &role.ReconcileReport{TenantID: "own"}, nil
		},
	}
	srv := newRoleServer(mock, nil)

	data, err := srv.handleReconcileRoles(context.Background(), nil)
	if err != nil {
		t.Fatalf("handleReconcileRoles: %v", err)
	}
	var resp reconcileRolesWireResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.TenantID != "own" {
		t.Errorf("tenantID = %q, want own", resp.TenantID)
	}
	if len(resp.Roles) != 0 {
		t.Errorf("roles = %+v, want empty", resp.Roles)
	}
}

func TestHandleReconcileRoles_InvalidJSON(t *testing.T) {
	srv := newRoleServer(&mockRoleService{}, nil)

	_, err := srv.handleReconcileRoles(context.Background(), []byte("{bad"))
	var unmarshalErr *UnmarshalError
	if !errors.As(err, &unmarshalErr) {
		t.Fatalf("err = %v, want UnmarshalError", err)
	}
}

func TestHandleReconcileRoles_ServiceError(t *testing.T) {
	mock := &mockRoleService{
		reconcileFn: func(context.Context, string) (*role.ReconcileReport, error) {
			return nil, &role.StoreError{Op: "reconcile", Err: errors.New("storage failure")}
		},
	}
	srv := newRoleServer(mock, nil)

	if _, err := srv.handleReconcileRoles(context.Background(), nil); err == nil {
		t.Fatal("handleReconcileRoles succeeded against a failing service")
	}
}

func TestHandleReconcileRoles_NoRoleService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")

	_, err := srv.handleReconcileRoles(context.Background(), nil)
	var notConfigured *ServiceNotConfiguredError
	if !errors.As(err, &notConfigured) {
		t.Fatalf("err = %v, want ServiceNotConfiguredError", err)
	}
}

// TestReconcileReportToWire_Nil covers the nil guard in the converter.
func TestReconcileReportToWire_Nil(t *testing.T) {
	got := reconcileReportToWire(nil)
	if got.TenantID != "" || len(got.Roles) != 0 {
		t.Errorf("reconcileReportToWire(nil) = %+v, want the zero value", got)
	}
}
