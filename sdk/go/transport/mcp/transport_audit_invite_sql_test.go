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

package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// recordingInvitations is an InvitationServicer that records the request it
// is handed and answers from it, so a test sees exactly what crossed the wire.
type recordingInvitations struct {
	invite *service.InviteUserRequest
	redeem *service.RedeemInvitationRequest
	err    error
}

var _ service.InvitationServicer = (*recordingInvitations)(nil)

func (r *recordingInvitations) InviteUser(_ context.Context, req *service.InviteUserRequest) (*service.Invitation, error) {
	r.invite = req
	if r.err != nil {
		return nil, r.err
	}
	return &service.Invitation{
		ID: 7, Token: "tok", ExpiresAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), UserID: 9,
		Username: req.Username, TenantID: req.TenantID, Created: true, KeyPinFingerprint: req.KeyPinFingerprint,
	}, nil
}

func (r *recordingInvitations) RedeemInvitation(_ context.Context, req *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error) {
	r.redeem = req
	if r.err != nil {
		return nil, r.err
	}
	return &service.EnrolledCertificate{
		UserID: 9, Username: "ada", TenantID: req.TenantID, CertPEM: []byte("CERT"),
		Serial: "01", Thumbprint: "thumb", NotAfter: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
	}, nil
}

// recordingSQL is a SQLServicer that records the database and query it is
// handed and answers with one row naming them.
type recordingSQL struct {
	database, query string
}

var _ service.SQLServicer = (*recordingSQL)(nil)

func (r *recordingSQL) Execute(_ context.Context, database, query string) (*service.SQLQueryResult, error) {
	r.database, r.query = database, query
	if query == "" {
		return nil, &service.SQLEmptyQueryError{}
	}
	return &service.SQLQueryResult{
		Columns:  []string{"db"},
		Rows:     []map[string]any{{"db": database}},
		RowCount: 1,
		Duration: "1ms",
	}, nil
}

func TestMCP_Audit_QueryCarriesEveryFilterField(t *testing.T) {
	ring := audit.NewRingBuffer(16)
	ctx := context.Background()
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	for _, ev := range []audit.AuditEvent{
		{Timestamp: at, Actor: "alice", Resource: "barrier", Action: "unseal", Outcome: "success", ClientIP: "10.0.0.1", RequestID: "r1"},
		{Timestamp: at.Add(time.Minute), Actor: "bob", Resource: "barrier", Action: "seal", Outcome: "failure:denied"},
	} {
		if err := ring.Emit(ctx, ev); err != nil {
			t.Fatalf("Emit: %v", err)
		}
	}
	tr := connectServerInProcess(t, mcpapi.WithAuditLogger(ring))

	page, err := tr.QueryAudit(ctx, transport.AuditFilter{
		From: at.Add(-time.Hour), To: at.Add(time.Hour), Actor: "alice", Resource: "barrier",
		Action: "unseal", Outcome: "success", Limit: 10,
	})
	if err != nil {
		t.Fatalf("QueryAudit: %v", err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("QueryAudit = %+v, want alice's one event", page)
	}
	if ev := page.Events[0]; ev.Actor != "alice" || ev.ClientIP != "10.0.0.1" || ev.RequestID != "r1" {
		t.Fatalf("event = %+v, want the client IP and request id kept", ev)
	}

	all, err := tr.QueryAudit(ctx, transport.AuditFilter{})
	if err != nil {
		t.Fatalf("QueryAudit(all): %v", err)
	}
	if len(all.Events) != 2 {
		t.Fatalf("QueryAudit(all) = %d events, want 2", len(all.Events))
	}

	// A cursor, tenant and replicated read reach the server: with no
	// replicated trail configured the query is refused, not answered locally.
	if _, err := tr.QueryAudit(ctx, transport.AuditFilter{TenantID: "t1", Cursor: "c", Replicated: true}); err == nil {
		t.Fatal("replicated QueryAudit succeeded on a node with no replicated trail")
	}
}

func TestMCP_Audit_VerifyUnsupportedAndClosed(t *testing.T) {
	tr := connectServerInProcess(t, mcpapi.WithAuditLogger(audit.NewRingBuffer(4)))
	ctx := context.Background()

	// The ring buffer keeps no hash chain, so the node cannot verify one:
	// the answer is the NotImplementedError every transport reports.
	_, err := tr.VerifyChain(ctx)
	var ni *transport.NotImplementedError
	if !errors.As(err, &ni) || ni.Method != "VerifyChain" {
		t.Fatalf("VerifyChain err = %T %v, want NotImplementedError{VerifyChain}", err, err)
	}
	if _, err := tr.VerifyReplicatedChain(ctx, "t1"); err == nil {
		t.Fatal("VerifyReplicatedChain succeeded on a node with no replicated trail")
	}
	if !errors.As(tr.EmitAudit(ctx, transport.AuditEvent{}), &ni) || ni.Method != "EmitAudit" {
		t.Fatal("EmitAudit did not report NotImplementedError")
	}
	if tr.AuditClient() == nil {
		t.Fatal("AuditClient() = nil")
	}

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	requireClosed(t, tr.EmitAudit(ctx, transport.AuditEvent{}))
	_, err = tr.QueryAudit(ctx, transport.AuditFilter{})
	requireClosed(t, err)
	_, err = tr.VerifyChain(ctx)
	requireClosed(t, err)
	_, err = tr.VerifyReplicatedChain(ctx, "")
	requireClosed(t, err)
}

func TestMCP_Invitations_InviteSendsEveryField(t *testing.T) {
	inv := &recordingInvitations{}
	tr := connectServerInProcess(t, mcpapi.WithInvitations(inv))
	ctx := context.Background()

	got, err := tr.InviteUser(ctx, &transport.InviteUserRequest{
		Username: "ada", Email: "ada@example.com", Name: "Ada", KeyPinFingerprint: "pin",
	})
	if err != nil {
		t.Fatalf("InviteUser: %v", err)
	}
	want := service.InviteUserRequest{Username: "ada", Email: "ada@example.com", Name: "Ada", KeyPinFingerprint: "pin"}
	if inv.invite == nil || inv.invite.Username != want.Username || inv.invite.Email != want.Email ||
		inv.invite.Name != want.Name || inv.invite.KeyPinFingerprint != want.KeyPinFingerprint || len(inv.invite.RoleIDs) != 0 {
		t.Fatalf("service got %+v, want %+v", inv.invite, want)
	}
	if got.ID != 7 || got.Token != "tok" || got.UserID != 9 || got.Username != "ada" || !got.Created ||
		got.KeyPinFingerprint != "pin" || !got.ExpiresAt.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("InviteUser = %+v", got)
	}

	if _, err := tr.InviteUser(ctx, &transport.InviteUserRequest{Username: "min"}); err != nil {
		t.Fatalf("InviteUser(min): %v", err)
	}
	if inv.invite.Email != "" || inv.invite.Name != "" || inv.invite.KeyPinFingerprint != "" {
		t.Fatalf("unset fields reached the service: %+v", inv.invite)
	}

	inv.err = &service.InvitationInvalidError{}
	if _, err := tr.InviteUser(ctx, &transport.InviteUserRequest{Username: "ada"}); err == nil {
		t.Fatal("InviteUser succeeded when the service refused")
	}
}

func TestMCP_Invitations_RedeemReachesTheEnrolServer(t *testing.T) {
	inv := &recordingInvitations{}
	tr := connectServerInProcess(t, mcpapi.WithInvitations(inv))
	ctx := context.Background()

	// In process there is one MCPServer, the authenticated /mcp one, and the
	// public qrdb_invitation_redeem is served only on the enrolment endpoint:
	// the dial succeeds and the call is refused before any service sees it.
	_, err := tr.RedeemInvitation(ctx, &transport.RedeemInvitationRequest{
		TenantID: "t1", Token: "tok", CSRPEM: "CSR", CertPEM: "CERT", ChainPEM: "CHAIN",
	})
	requireRequestError(t, err, "qrdb_invitation_redeem")
	if inv.redeem != nil {
		t.Fatalf("/mcp served the public redeem tool: service got %+v", inv.redeem)
	}

	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err = tr.InviteUser(ctx, &transport.InviteUserRequest{Username: "ada"})
	requireClosed(t, err)
	_, err = tr.RedeemInvitation(ctx, &transport.RedeemInvitationRequest{Token: "tok"})
	requireClosed(t, err)
}

func TestMCP_SQL_QueryRoundTrip(t *testing.T) {
	sql := &recordingSQL{}
	tr := connectServerInProcess(t, mcpapi.WithSQLService(sql))
	ctx := context.Background()

	res, err := tr.SQLClient().Query(ctx, "app", "SELECT 1")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if sql.database != "app" || sql.query != "SELECT 1" {
		t.Fatalf("service got (%q, %q)", sql.database, sql.query)
	}
	if res.RowCount != 1 || len(res.Columns) != 1 || res.Columns[0] != "db" || res.Rows[0]["db"] != "app" || res.Duration != "1ms" {
		t.Fatalf("Query = %+v", res)
	}

	_, err = tr.Query(ctx, "app", "")
	requireRequestError(t, err, "Query")
}
