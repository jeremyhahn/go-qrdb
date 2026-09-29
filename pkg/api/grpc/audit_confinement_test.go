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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package grpc

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// TestAuditQuery_ConfinedToCallersTenant: the AuditService (and the unix
// socket, which serves it) answers only for the caller's own tenant. Another
// tenant's id is PERMISSION_DENIED before the shared logger is reached, and an
// empty one is the caller's tenant, never "every tenant".
func TestAuditQuery_ConfinedToCallersTenant(t *testing.T) {
	var queried []audit.Filter
	s := &auditServer{auditLogger: &mockAuditLogger{queryFn: func(_ context.Context, f audit.Filter) (*audit.Page, error) {
		queried = append(queried, f)
		return &audit.Page{}, nil
	}}}
	acme := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "a", TenantID: "acme"})

	for _, foreign := range []string{"globex", tenantid.System} {
		_, err := s.Query(acme, &pb.AuditQueryRequest{TenantId: foreign})
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("tenant_id=%s: code = %v (%v), want PermissionDenied", foreign, status.Code(err), err)
		}
	}
	if len(queried) != 0 {
		t.Fatalf("a refused query reached the logger with %+v", queried)
	}

	if _, err := s.Query(acme, &pb.AuditQueryRequest{}); err != nil {
		t.Fatalf("unfiltered query: %v", err)
	}
	sys := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "op", TenantID: tenantid.System})
	if _, err := s.Query(sys, &pb.AuditQueryRequest{}); err != nil {
		t.Fatalf("system query: %v", err)
	}
	if len(queried) != 2 || queried[0].TenantID != "acme" || queried[0].Untenanted ||
		queried[1].TenantID != tenantid.System || !queried[1].Untenanted {
		t.Fatalf("logger queried with %+v; want acme alone, then the system tenant with platform events", queried)
	}
}
