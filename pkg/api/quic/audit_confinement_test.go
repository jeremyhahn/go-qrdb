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

package quic

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestHandleAuditQuery_ConfinedToCallersTenant: the audit.Query frame answers
// only for the caller's own tenant. A payload naming another tenant is refused
// before the shared logger is reached, and Untenanted cannot ride in on the
// payload to add the platform's events.
func TestHandleAuditQuery_ConfinedToCallersTenant(t *testing.T) {
	var queried []audit.Filter
	srv := newTestServer(&mockKVService{}, &mockAdminService{})
	srv.auditLogger = &mockAuditLogger{queryFn: func(_ context.Context, f audit.Filter) (*audit.Page, error) {
		queried = append(queried, f)
		return &audit.Page{}, nil
	}}
	acme := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "a", TenantID: "acme"})

	_, err := srv.handleAuditQuery(acme, []byte(`{"tenant_id":"globex"}`))
	var denied *tenant.CrossTenantDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("err = %v; want *tenant.CrossTenantDeniedError", err)
	}
	if len(queried) != 0 {
		t.Fatalf("a refused query reached the logger with %+v", queried)
	}

	if _, err := srv.handleAuditQuery(acme, []byte(`{"Untenanted":true}`)); err != nil {
		t.Fatalf("unfiltered query: %v", err)
	}
	if len(queried) != 1 || queried[0].TenantID != "acme" || queried[0].Untenanted {
		t.Fatalf("logger queried with %+v; want acme alone", queried)
	}
}
