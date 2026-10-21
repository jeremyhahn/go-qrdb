// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build qrdb_pgsql

package pgsql

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// portalRows is how many rows the portal tests' table holds; each Execute
// asks for one.
const portalRows = 3

// openRowLimitedPortal hijacks a connection opened while acme is active and
// executes an unnamed portal of portalRows rows over it with a one-row limit,
// which suspends the portal after its first row.
func openRowLimitedPortal(t *testing.T) (tenant.TenantServicer, *pgproto3.Frontend) {
	t.Helper()
	tenants, conn, _ := pgTenantConn(t)
	hc, err := conn.PgConn().Hijack()
	if err != nil {
		t.Fatalf("Hijack: %v", err)
	}
	t.Cleanup(func() { _ = hc.Conn.Close() })
	if err := hc.Conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	fe := hc.Frontend
	fe.Send(&pgproto3.Parse{Query: "SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3"})
	fe.Send(&pgproto3.Bind{})
	if rows, end := executePortal(t, fe); rows != 1 || end != portalEndSuspended {
		t.Fatalf("first Execute = %d rows, %s; want 1 row, suspended", rows, end)
	}
	return tenants, fe
}

// portalEnd is how one Execute of a portal ended.
type portalEnd string

const (
	portalEndSuspended portalEnd = "suspended"
	portalEndComplete  portalEnd = "complete"
	portalEndRefused   portalEnd = "refused"
	portalEndClosed    portalEnd = "closed"
)

// executePortal sends an Execute of the unnamed portal for one row, with a
// Flush rather than a Sync so the portal outlives it, and reports the rows it
// delivered and how it ended. A refusal must be the tenant-not-active one.
func executePortal(t *testing.T, fe *pgproto3.Frontend) (int, portalEnd) {
	t.Helper()
	fe.Send(&pgproto3.Execute{MaxRows: 1})
	fe.Send(&pgproto3.Flush{})
	if err := fe.Flush(); err != nil {
		return 0, portalEndClosed
	}
	rows := 0
	for {
		msg, err := fe.Receive()
		if err != nil {
			return rows, portalEndClosed
		}
		switch m := msg.(type) {
		case *pgproto3.DataRow:
			rows++
		case *pgproto3.PortalSuspended:
			return rows, portalEndSuspended
		case *pgproto3.CommandComplete:
			return rows, portalEndComplete
		case *pgproto3.ErrorResponse:
			if !strings.Contains(m.Message, "tenant is not active: acme (status suspended)") {
				t.Fatalf("Execute refused with %q, want the tenant-not-active refusal", m.Message)
			}
			return rows, portalEndRefused
		}
	}
}

// A portal suspended at its row limit while its tenant was active is not
// resumed once the tenant is suspended: psql-wire resumes it past every
// statement admission, so the portal cache re-admits the session before the
// resume and refuses it -- or the seal guard has already closed the
// connection. Either way no further row is delivered.
func TestPostgresWire_SuspendedPortalRefusedAfterTenantSuspended(t *testing.T) {
	tenants, fe := openRowLimitedPortal(t)

	setPgTenantStatus(t, tenants, tenant.StatusSuspended)
	rows, end := executePortal(t, fe)
	if rows != 0 || (end != portalEndRefused && end != portalEndClosed) {
		t.Fatalf("resuming the portal while suspended = %d rows, %s; want no rows, refused or closed", rows, end)
	}
}

// A portal of an active tenant resumes, one row per Execute, and completes.
func TestPostgresWire_SuspendedPortalResumesWhileTenantActive(t *testing.T) {
	_, fe := openRowLimitedPortal(t)

	for i := 2; i <= portalRows; i++ {
		if rows, end := executePortal(t, fe); rows != 1 || end != portalEndSuspended {
			t.Fatalf("Execute %d = %d rows, %s; want 1 row, suspended", i, rows, end)
		}
	}
	if rows, end := executePortal(t, fe); rows != 0 || end != portalEndComplete {
		t.Fatalf("final Execute = %d rows, %s; want no rows, complete", rows, end)
	}
}
