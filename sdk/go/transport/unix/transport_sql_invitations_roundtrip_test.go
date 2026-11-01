package unix

import (
	"context"
	"errors"
	"testing"
	"time"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// fakeSQLSvc is a service.SQLServicer that records the database and query it
// was asked to run and answers with a fixed result set or error.
type fakeSQLSvc struct {
	result *service.SQLQueryResult
	err    error

	gotDatabase string
	gotQuery    string
}

var _ service.SQLServicer = (*fakeSQLSvc)(nil)

func (f *fakeSQLSvc) Execute(_ context.Context, database, query string) (*service.SQLQueryResult, error) {
	f.gotDatabase, f.gotQuery = database, query
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

// fakeInvitationSvc is a service.InvitationServicer that records each request
// and answers with a fixed invitation, enrolment or error.
type fakeInvitationSvc struct {
	invitation *service.Invitation
	enrolled   *service.EnrolledCertificate
	err        error

	gotInvite *service.InviteUserRequest
	gotRedeem *service.RedeemInvitationRequest
}

var _ service.InvitationServicer = (*fakeInvitationSvc)(nil)

func (f *fakeInvitationSvc) InviteUser(_ context.Context, req *service.InviteUserRequest) (*service.Invitation, error) {
	f.gotInvite = req
	if f.err != nil {
		return nil, f.err
	}
	return f.invitation, nil
}

func (f *fakeInvitationSvc) RedeemInvitation(_ context.Context, req *service.RedeemInvitationRequest) (*service.EnrolledCertificate, error) {
	f.gotRedeem = req
	if f.err != nil {
		return nil, f.err
	}
	return f.enrolled, nil
}

func TestUnixRT_SQLQuery(t *testing.T) {
	svc := &fakeSQLSvc{result: &service.SQLQueryResult{
		Columns:  []string{"id", "name"},
		Rows:     []map[string]any{{"id": "1", "name": "ada"}, {"id": "2", "name": "bob"}},
		RowCount: 2,
		Duration: "1ms",
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithSQLService(svc))
	defer cleanup()

	if tr.SQLClient() != transport.SQLClient(tr) {
		t.Fatal("SQLClient does not return the transport itself")
	}
	res, err := tr.Query(context.Background(), "app", "SELECT id, name FROM users")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if svc.gotDatabase != "app" || svc.gotQuery != "SELECT id, name FROM users" {
		t.Fatalf("server ran %q on %q", svc.gotQuery, svc.gotDatabase)
	}
	if len(res.Columns) != 2 || res.Columns[0] != "id" || res.Columns[1] != "name" {
		t.Fatalf("Columns = %v", res.Columns)
	}
	if res.RowCount != 2 || res.Duration != "1ms" || len(res.Rows) != 2 {
		t.Fatalf("result = %+v", res)
	}
	if res.Rows[0]["name"] != "ada" || res.Rows[1]["id"] != "2" {
		t.Fatalf("Rows = %v", res.Rows)
	}
}

func TestUnixRT_SQLQuery_Errors(t *testing.T) {
	svc := &fakeSQLSvc{err: &service.SQLExecutionError{Err: errors.New("syntax error")}}
	tr, cleanup := startAuthServer(t, unixserver.WithSQLService(svc))
	defer cleanup()
	ctx := context.Background()

	if _, err := tr.Query(ctx, "app", "SELEC"); err == nil {
		t.Fatal("Query: an engine failure returned no error")
	}
	if _, err := tr.Query(ctx, "app", ""); err == nil {
		t.Fatal("Query: an empty query returned no error")
	}
	if svc.gotQuery != "SELEC" {
		t.Fatalf("an empty query reached the service: %q", svc.gotQuery)
	}

	unconfigured, stop := startAuthServer(t)
	defer stop()
	if _, err := unconfigured.Query(ctx, "app", "SELECT 1"); err == nil {
		t.Fatal("Query: a server without SQL returned no error")
	}
}

func TestUnixRT_InviteUser(t *testing.T) {
	expires := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	svc := &fakeInvitationSvc{invitation: &service.Invitation{
		ID: 7, Token: "tok", ExpiresAt: expires, UserID: 42, Username: "ada",
		TenantID: "system", Created: true, KeyPinFingerprint: "pin",
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithInvitations(svc), unixserver.WithRoleService(tenantRoleSvc("system")))
	defer cleanup()

	inv, err := tr.InviteUser(context.Background(), &transport.InviteUserRequest{
		Username: "ada", Email: "ada@example.com", Name: "Ada", KeyPinFingerprint: "pin",
	})
	if err != nil {
		t.Fatalf("InviteUser: %v", err)
	}
	got := svc.gotInvite
	if got == nil || got.Username != "ada" || got.Email != "ada@example.com" || got.Name != "Ada" ||
		got.KeyPinFingerprint != "pin" {
		t.Fatalf("server received %+v", got)
	}
	if inv.ID != 7 || inv.Token != "tok" || inv.UserID != 42 || inv.Username != "ada" ||
		inv.TenantID != "system" || !inv.Created || inv.KeyPinFingerprint != "pin" || !inv.ExpiresAt.Equal(expires) {
		t.Fatalf("InviteUser = %+v", inv)
	}
}

func TestUnixRT_RedeemInvitation(t *testing.T) {
	notAfter := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := &fakeInvitationSvc{enrolled: &service.EnrolledCertificate{
		UserID: 42, Username: "ada", TenantID: "acme", CertPEM: []byte("CERT"),
		Serial: "01", Thumbprint: "thumb", NotAfter: notAfter,
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithInvitations(svc))
	defer cleanup()

	cert, err := tr.RedeemInvitation(context.Background(), &transport.RedeemInvitationRequest{
		TenantID: "acme", Token: "tok", CSRPEM: "CSR", CertPEM: "OWN", ChainPEM: "CHAIN",
	})
	if err != nil {
		t.Fatalf("RedeemInvitation: %v", err)
	}
	got := svc.gotRedeem
	if got == nil || got.TenantID != "acme" || got.Token != "tok" || string(got.CSRPEM) != "CSR" ||
		string(got.CertPEM) != "OWN" || string(got.ChainPEM) != "CHAIN" {
		t.Fatalf("server received %+v", got)
	}
	if cert.UserID != 42 || cert.Username != "ada" || cert.TenantID != "acme" || cert.CertPEM != "CERT" ||
		cert.Serial != "01" || cert.Thumbprint != "thumb" || !cert.NotAfter.Equal(notAfter) {
		t.Fatalf("RedeemInvitation = %+v", cert)
	}
}

func TestUnixRT_Invitations_Errors(t *testing.T) {
	svc := &fakeInvitationSvc{err: &service.InvitationInvalidError{}}
	tr, cleanup := startAuthServer(t, unixserver.WithInvitations(svc), unixserver.WithRoleService(tenantRoleSvc("system")))
	defer cleanup()
	ctx := context.Background()

	if _, err := tr.InviteUser(ctx, &transport.InviteUserRequest{Username: "ada"}); err == nil {
		t.Fatal("InviteUser: a service refusal returned no error")
	}
	if _, err := tr.RedeemInvitation(ctx, &transport.RedeemInvitationRequest{TenantID: "acme", Token: "bad"}); err == nil {
		t.Fatal("RedeemInvitation: an invalid token returned no error")
	}

	var nilReq *transport.NilRequestError
	if _, err := tr.InviteUser(ctx, nil); !errors.As(err, &nilReq) {
		t.Fatalf("InviteUser(nil): err = %v, want *transport.NilRequestError", err)
	}
	requireUnixRequestError(t, func() error { _, err := tr.InviteUser(ctx, nil); return err }(), "InviteUser")
	if _, err := tr.RedeemInvitation(ctx, nil); !errors.As(err, &nilReq) {
		t.Fatalf("RedeemInvitation(nil): err = %v, want *transport.NilRequestError", err)
	}
	requireUnixRequestError(t, func() error { _, err := tr.RedeemInvitation(ctx, nil); return err }(), "RedeemInvitation")
}

func TestUnixRT_ScaleDatabase(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithDatabaseService(&mockDatabaseSvc{}))
	defer cleanup()

	info, err := tr.ScaleDatabase(context.Background(), transport.ScaleDatabaseRequest{Name: "db1", TargetReplicas: 5})
	if err != nil {
		t.Fatalf("ScaleDatabase: %v", err)
	}
	if info.Name != "db1" || info.Replicas != 5 {
		t.Fatalf("ScaleDatabase = %+v", info)
	}

	unconfigured, stop := startAuthServer(t)
	defer stop()
	if _, err := unconfigured.ScaleDatabase(context.Background(), transport.ScaleDatabaseRequest{Name: "db1", TargetReplicas: 3}); err == nil {
		t.Fatal("ScaleDatabase: a server without the database service returned no error")
	}
}

func TestUnixRT_ImportAbort(t *testing.T) {
	svc := &mockBatchSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithBatch(svc))
	defer cleanup()
	ctx := context.Background()

	id, err := tr.ImportBegin(ctx, transport.ImportOptions{Format: "jsonl", Filename: "data.jsonl"})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}
	if err := tr.ImportAbort(ctx, id); err != nil {
		t.Fatalf("ImportAbort: %v", err)
	}
	svc.mu.Lock()
	_, live := svc.sessions[id]
	svc.mu.Unlock()
	if live {
		t.Fatalf("session %q survived ImportAbort", id)
	}
	if err := tr.ImportAbort(ctx, id); err == nil {
		t.Fatal("ImportAbort: aborting an aborted session returned no error")
	}
	if err := tr.ImportAppend(ctx, id, 0, []byte("{}\n")); err == nil {
		t.Fatal("ImportAppend: appending to an aborted session returned no error")
	}
}

// TestUnixRT_ClosedTransport_SQLInvitationsScaleImport proves each method
// refuses with *transport.ClosedError once the transport is closed, without a
// round trip.
func TestUnixRT_ClosedTransport_SQLInvitationsScaleImport(t *testing.T) {
	tr, cleanup := startAuthServer(t)
	cleanup()
	ctx := context.Background()

	calls := map[string]func() error{
		"Query": func() error { _, err := tr.Query(ctx, "app", "SELECT 1"); return err },
		"InviteUser": func() error {
			_, err := tr.InviteUser(ctx, &transport.InviteUserRequest{Username: "ada"})
			return err
		},
		"RedeemInvitation": func() error {
			_, err := tr.RedeemInvitation(ctx, &transport.RedeemInvitationRequest{Token: "tok"})
			return err
		},
		"ScaleDatabase": func() error {
			_, err := tr.ScaleDatabase(ctx, transport.ScaleDatabaseRequest{Name: "db1"})
			return err
		},
		"ImportAbort": func() error { return tr.ImportAbort(ctx, "1") },
	}
	for name, call := range calls {
		var closed *transport.ClosedError
		if err := call(); !errors.As(err, &closed) {
			t.Errorf("%s: err = %v, want *transport.ClosedError", name, err)
		}
	}
}

// TestUnixRT_UpdateUser_OptionalFields proves the nil-vs-set fields of
// UpdateUserRequest cross the socket with their has_* flags, and that the
// bindings the server answers with come back on the user.
func TestUnixRT_UpdateUser_OptionalFields(t *testing.T) {
	var got *user.UpdateUserRequest
	usvc := &mockUserSvc{updateFn: func(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
		got = req
		return &user.User{ID: req.ID, Username: "alice", Lifecycle: user.LifecycleActive,
			RoleBindings: req.RoleBindings, Attributes: req.Attributes}, nil
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithUserService(usvc), unixserver.WithRoleService(tenantRoleSvc("")))
	defer cleanup()
	ctx := context.Background()

	u, err := tr.UpdateUser(ctx, &transport.UpdateUserRequest{
		ID:           1,
		AuthMethods:  []string{},
		LastAuth:     "2026-10-01T12:00:00Z",
		RoleBindings: []transport.RoleBinding{{RoleID: "reader", TenantID: "system"}},
		Attributes:   map[string]string{"team": "core"},
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if got == nil || got.AuthMethods == nil || len(got.AuthMethods) != 0 {
		t.Fatalf("server AuthMethods = %#v, want an empty non-nil set", got)
	}
	if got.LastAuth == nil || !got.LastAuth.Equal(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("server LastAuth = %v", got.LastAuth)
	}
	if len(got.RoleBindings) != 1 || got.RoleBindings[0] != (user.RoleBinding{RoleID: "reader", TenantID: "system"}) {
		t.Fatalf("server RoleBindings = %+v", got.RoleBindings)
	}
	if got.Attributes["team"] != "core" {
		t.Fatalf("server Attributes = %v", got.Attributes)
	}
	if len(u.RoleBindings) != 1 || u.RoleBindings[0] != (transport.RoleBinding{RoleID: "reader", TenantID: "system"}) {
		t.Fatalf("UpdateUser RoleBindings = %+v", u.RoleBindings)
	}

	got = nil
	if _, err := tr.UpdateUser(ctx, &transport.UpdateUserRequest{ID: 1}); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if got.AuthMethods != nil || got.RoleBindings != nil || got.Attributes != nil || got.LastAuth != nil {
		t.Fatalf("unset fields reached the server as set: %+v", got)
	}

	got = nil
	requireUnixRequestError(t, func() error {
		_, err := tr.UpdateUser(ctx, &transport.UpdateUserRequest{ID: 1, LastAuth: "yesterday"})
		return err
	}(), "UpdateUser")
	if got != nil {
		t.Fatal("a malformed LastAuth reached the server")
	}
	var nilReq *transport.NilRequestError
	if _, err := tr.UpdateUser(ctx, nil); !errors.As(err, &nilReq) {
		t.Fatalf("UpdateUser(nil): err = %v, want *transport.NilRequestError", err)
	}
}

// TestUnixRT_ClosedTransport_DatabaseBarrier proves every per-database
// barrier method refuses with *transport.ClosedError once the transport is
// closed, before any request reaches the holder ceremony client.
func TestUnixRT_ClosedTransport_DatabaseBarrier(t *testing.T) {
	tr, cleanup := startAuthServer(t)
	cleanup()
	ctx := context.Background()

	calls := map[string]func() error{
		"SealDatabase": func() error { return tr.SealDatabase(ctx, "db1") },
		"DatabaseBarrierStatus": func() error {
			_, err := tr.DatabaseBarrierStatus(ctx, "db1")
			return err
		},
		"DatabaseBarrierOfficerRegister": func() error {
			_, err := tr.DatabaseBarrierOfficerRegister(ctx, &transport.DatabaseOfficerRegisterRequest{})
			return err
		},
		"DatabaseBarrierOfficerList": func() error {
			_, err := tr.DatabaseBarrierOfficerList(ctx, "db1")
			return err
		},
		"DatabaseBarrierShamirInitialize": func() error {
			_, err := tr.DatabaseBarrierShamirInitialize(ctx, &transport.DatabaseShamirInitializeRequest{})
			return err
		},
		"DatabaseBarrierShareClaimBegin": func() error {
			_, err := tr.DatabaseBarrierShareClaimBegin(ctx, &transport.DatabaseShareClaimBeginRequest{})
			return err
		},
		"DatabaseBarrierShareClaimComplete": func() error {
			_, err := tr.DatabaseBarrierShareClaimComplete(ctx, &transport.DatabaseShareClaimCompleteRequest{})
			return err
		},
		"DatabaseBarrierQuorumBegin": func() error {
			_, err := tr.DatabaseBarrierQuorumBegin(ctx, &transport.DatabaseQuorumBeginRequest{})
			return err
		},
		"DatabaseBarrierQuorumSubmit": func() error {
			_, err := tr.DatabaseBarrierQuorumSubmit(ctx, &transport.DatabaseQuorumSubmitRequest{})
			return err
		},
		"DatabaseBarrierRekeyStart": func() error {
			_, err := tr.DatabaseBarrierRekeyStart(ctx, &transport.DatabaseRekeyStartRequest{})
			return err
		},
		"DatabaseBarrierOfficerRevoke": func() error {
			_, err := tr.DatabaseBarrierOfficerRevoke(ctx, &transport.DatabaseOfficerRevokeRequest{})
			return err
		},
		"DatabaseBarrierOfficerUnenrol": func() error {
			return tr.DatabaseBarrierOfficerUnenrol(ctx, &transport.DatabaseOfficerUnenrolRequest{})
		},
	}
	for name, call := range calls {
		var closed *transport.ClosedError
		if err := call(); !errors.As(err, &closed) {
			t.Errorf("%s: err = %v, want *transport.ClosedError", name, err)
		}
	}
}

// TestUnixRT_DatabaseSealAndStatus_Errors proves a server-side refusal of
// SealDatabase and DatabaseBarrierStatus surfaces as an error.
func TestUnixRT_DatabaseSealAndStatus_Errors(t *testing.T) {
	svc := &mockDatabaseSvc{
		sealDBFn: func(context.Context, string) error { return errors.New("seal refused") },
		barrierStatusFn: func(string) (*seal.BarrierStatus, error) {
			return nil, errors.New("status refused")
		},
	}
	tr, cleanup := startAuthServer(t, unixserver.WithDatabaseService(svc))
	defer cleanup()
	ctx := context.Background()

	if err := tr.SealDatabase(ctx, "db1"); err == nil {
		t.Fatal("SealDatabase: a refusal returned no error")
	}
	if _, err := tr.DatabaseBarrierStatus(ctx, "db1"); err == nil {
		t.Fatal("DatabaseBarrierStatus: a refusal returned no error")
	}
}
