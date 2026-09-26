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
	"errors"
	"log/slog"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// retiredQUICTenants returns the REAL seal-disabled tenant service with the
// named tenants created and deleted through it.
func retiredQUICTenants(t *testing.T, ids ...string) tenant.TenantServicer {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, id := range ids {
		if _, err := svc.CreateTenant(ctx, id, "Tenant "+id, tenant.IsolationPrefix); err != nil {
			t.Fatal(err)
		}
		if err := svc.DeleteTenant(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	return svc
}

type failingQUICDeletedSvc struct {
	tenant.TenantServicer
	report *tenant.ReconcileReport
	err    error
}

func (f failingQUICDeletedSvc) ListDeletedTenants(context.Context, string, int) (*tenant.DeletedTenantsPage, error) {
	return nil, f.err
}

func (f failingQUICDeletedSvc) ReconcileDeletedTenants(context.Context) (*tenant.ReconcileReport, error) {
	return f.report, f.err
}

func TestHandleTenantListDeleted_PagesThroughRetiredIds(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(retiredQUICTenants(t, "bravo", "alpha", "charlie")))
	ctx := context.Background()

	payload, _ := proto.Marshal(&pb.TenantListDeletedRequest{PageSize: 2})
	raw, err := srv.handleTenantListDeleted(ctx, payload)
	if err != nil {
		t.Fatalf("handleTenantListDeleted: %v", err)
	}
	var first pb.TenantListDeletedResponse
	if err := proto.Unmarshal(raw, &first); err != nil {
		t.Fatal(err)
	}
	if len(first.GetTenants()) != 2 || first.GetTenants()[0].GetId() != "alpha" || first.GetNextPageToken() == "" {
		t.Fatalf("first page = %v", &first)
	}

	payload, _ = proto.Marshal(&pb.TenantListDeletedRequest{PageSize: 2, PageToken: first.GetNextPageToken()})
	raw, err = srv.handleTenantListDeleted(ctx, payload)
	if err != nil {
		t.Fatal(err)
	}
	var second pb.TenantListDeletedResponse
	if err := proto.Unmarshal(raw, &second); err != nil {
		t.Fatal(err)
	}
	if len(second.GetTenants()) != 1 || second.GetTenants()[0].GetId() != "charlie" {
		t.Fatalf("second page = %v", &second)
	}
}

func TestHandleTenantListDeleted_RefusesBadInput(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(retiredQUICTenants(t)))
	var ue *UnmarshalError
	if _, err := srv.handleTenantListDeleted(context.Background(), []byte{0xff, 0xff}); !errors.As(err, &ue) {
		t.Fatalf("garbage payload = %v; want *UnmarshalError", err)
	}
	payload, _ := proto.Marshal(&pb.TenantListDeletedRequest{PageSize: -1})
	_, err := srv.handleTenantListDeleted(context.Background(), payload)
	if code, _ := mapServiceError(err); code != uint16(wire.StatusInvalidArgument) {
		t.Fatalf("negative page size maps to %d; want StatusInvalidArgument", code)
	}
}

func TestHandleTenantReconcileDeleted_ReportsWhatItInspected(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(retiredQUICTenants(t, "alpha")))
	raw, err := srv.handleTenantReconcileDeleted(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var resp pb.TenantReconcileDeletedResponse
	if err := proto.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.GetInspected() != 1 {
		t.Fatalf("inspected = %d; want 1", resp.GetInspected())
	}
}

func TestHandleTenantDeletedVerbs_NilServiceAndErrors(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	var snc *ServiceNotConfiguredError
	if _, err := srv.handleTenantListDeleted(context.Background(), nil); !errors.As(err, &snc) {
		t.Fatalf("list with no tenant service = %v", err)
	}
	if _, err := srv.handleTenantReconcileDeleted(context.Background(), nil); !errors.As(err, &snc) {
		t.Fatalf("reconcile with no tenant service = %v", err)
	}

	boom := errors.New("boom")
	srv = newTestServer(&mockKVService{}, &mockAdminService{}, WithTenantService(failingQUICDeletedSvc{err: boom}))
	if _, err := srv.handleTenantListDeleted(context.Background(), nil); !errors.Is(err, boom) {
		t.Fatalf("list error = %v; want the service error", err)
	}
	if _, err := srv.handleTenantReconcileDeleted(context.Background(), nil); !errors.Is(err, boom) {
		t.Fatalf("reconcile error = %v; want the service error", err)
	}
}
