package quic

import (
	"context"
	"crypto/tls"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	quicserver "github.com/jeremyhahn/go-qrdb/pkg/api/quic"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/pkg/volume/volumetest"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// startRoundTripServer starts the real QUIC API server with opts over a
// loopback listener and returns a transport connected to it.
func startRoundTripServer(t *testing.T, opts ...quicserver.ServerOption) *Transport {
	t.Helper()
	serverCfg, clientCfg := e2eTLS(t)
	return startRoundTripServerTLS(t, serverCfg, clientCfg, opts...)
}

// startRoundTripServerTLS is startRoundTripServer over the given server and
// client TLS configurations.
func startRoundTripServerTLS(t *testing.T, serverCfg, clientCfg *tls.Config, opts ...quicserver.ServerOption) *Transport {
	t.Helper()
	opts = append([]quicserver.ServerOption{quicserver.WithTLS(serverCfg)}, opts...)
	srv := quicserver.NewServer(nil, roundTripAdmin{}, "127.0.0.1:0", opts...)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	tr, err := New(transport.WithAddress(srv.Addr()), transport.WithTLSConfig(clientCfg), transport.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tr.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

// requireQUICRequestError fails t unless err is a *transport.RequestError for
// method.
func requireQUICRequestError(t *testing.T, err error, method string) {
	t.Helper()
	var re *transport.RequestError
	if !errors.As(err, &re) || re.Method != method {
		t.Fatalf("%s: err = %v, want *transport.RequestError for %s", method, err, method)
	}
}

// fakeSQLSvc is a service.SQLServicer that records what it was asked to run
// and answers with a fixed result set or error.
type fakeSQLSvc struct {
	result *service.SQLQueryResult
	err    error

	gotDatabase, gotQuery string
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

// scaleDatabaseSvc answers ScaleForTenant; every other DatabaseServicer
// method is the nil embedded interface and panics if reached.
type scaleDatabaseSvc struct {
	service.DatabaseServicer
	err error
}

func (s scaleDatabaseSvc) ScaleForTenant(_ context.Context, name string, replicas int) (*database.DatabaseInfo, error) {
	if s.err != nil {
		return nil, s.err
	}
	return &database.DatabaseInfo{Name: name, Replicas: replicas}, nil
}

// importSessionSvc keeps chunked-import sessions in memory; every other
// BatchServicer method is the nil embedded interface.
type importSessionSvc struct {
	service.BatchServicer

	mu       sync.Mutex
	sessions map[string]bool
	next     int
}

func (s *importSessionSvc) ImportBegin(context.Context, service.ImportOptions) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		s.sessions = make(map[string]bool)
	}
	s.next++
	id := strconv.Itoa(s.next)
	s.sessions[id] = true
	return id, nil
}

func (s *importSessionSvc) ImportAbort(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.sessions[id] {
		return &service.ImportSessionNotFoundError{SessionID: id}
	}
	delete(s.sessions, id)
	return nil
}

func (s *importSessionSvc) live(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func TestQUICRoundTrip_SQLQuery(t *testing.T) {
	svc := &fakeSQLSvc{result: &service.SQLQueryResult{
		Columns:  []string{"id", "name"},
		Rows:     []map[string]any{{"id": "1", "name": "ada"}, {"id": "2", "name": "bob"}},
		RowCount: 2,
		Duration: "1ms",
	}}
	tr := startRoundTripServer(t, quicserver.WithSQLService(svc))

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
	if len(res.Columns) != 2 || res.Columns[1] != "name" || res.RowCount != 2 || res.Duration != "1ms" {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Rows) != 2 || res.Rows[0]["name"] != "ada" || res.Rows[1]["id"] != "2" {
		t.Fatalf("Rows = %v", res.Rows)
	}
}

func TestQUICRoundTrip_SQLQuery_Errors(t *testing.T) {
	svc := &fakeSQLSvc{err: &service.SQLExecutionError{Err: errors.New("syntax error")}}
	tr := startRoundTripServer(t, quicserver.WithSQLService(svc))
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
	if _, err := startRoundTripServer(t).Query(ctx, "app", "SELECT 1"); err == nil {
		t.Fatal("Query: a server without SQL returned no error")
	}
}

func TestQUICRoundTrip_Invitations(t *testing.T) {
	expires := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	notAfter := expires.AddDate(1, 0, 0)
	svc := &fakeInvitationSvc{
		invitation: &service.Invitation{ID: 7, Token: "tok", ExpiresAt: expires, UserID: 42, Username: "ada",
			TenantID: "acme", Created: true, KeyPinFingerprint: "pin"},
		enrolled: &service.EnrolledCertificate{UserID: 42, Username: "ada", TenantID: "acme",
			CertPEM: []byte("CERT"), Serial: "01", Thumbprint: "thumb", NotAfter: notAfter},
	}
	tr := startRoundTripServer(t, quicserver.WithInvitations(svc))
	ctx := context.Background()

	inv, err := tr.InviteUser(ctx, &transport.InviteUserRequest{
		Username: "ada", Email: "ada@example.com", Name: "Ada", KeyPinFingerprint: "pin",
	})
	if err != nil {
		t.Fatalf("InviteUser: %v", err)
	}
	if g := svc.gotInvite; g == nil || g.Username != "ada" || g.Email != "ada@example.com" || g.Name != "Ada" ||
		g.KeyPinFingerprint != "pin" {
		t.Fatalf("server received %+v", g)
	}
	if inv.ID != 7 || inv.Token != "tok" || inv.UserID != 42 || inv.Username != "ada" || inv.TenantID != "acme" ||
		!inv.Created || inv.KeyPinFingerprint != "pin" || !inv.ExpiresAt.Equal(expires) {
		t.Fatalf("InviteUser = %+v", inv)
	}

	cert, err := tr.RedeemInvitation(ctx, &transport.RedeemInvitationRequest{
		TenantID: "acme", Token: "tok", CSRPEM: "CSR", CertPEM: "OWN", ChainPEM: "CHAIN",
	})
	if err != nil {
		t.Fatalf("RedeemInvitation: %v", err)
	}
	if g := svc.gotRedeem; g == nil || g.TenantID != "acme" || g.Token != "tok" || string(g.CSRPEM) != "CSR" ||
		string(g.CertPEM) != "OWN" || string(g.ChainPEM) != "CHAIN" {
		t.Fatalf("server received %+v", g)
	}
	if cert.UserID != 42 || cert.Username != "ada" || cert.CertPEM != "CERT" || cert.Serial != "01" ||
		cert.Thumbprint != "thumb" || !cert.NotAfter.Equal(notAfter) {
		t.Fatalf("RedeemInvitation = %+v", cert)
	}

	svc.err = &service.InvitationInvalidError{}
	requireQUICRequestError(t, func() error {
		_, err := tr.InviteUser(ctx, &transport.InviteUserRequest{Username: "ada"})
		return err
	}(), "InviteUser")
	requireQUICRequestError(t, func() error {
		_, err := tr.RedeemInvitation(ctx, &transport.RedeemInvitationRequest{TenantID: "acme", Token: "bad"})
		return err
	}(), "RedeemInvitation")
}

func TestQUICRoundTrip_ScaleDatabase(t *testing.T) {
	tr := startRoundTripServer(t, quicserver.WithDatabaseService(scaleDatabaseSvc{}))
	info, err := tr.ScaleDatabase(context.Background(), transport.ScaleDatabaseRequest{Name: "db1", TargetReplicas: 5})
	if err != nil {
		t.Fatalf("ScaleDatabase: %v", err)
	}
	if info.Name != "db1" || info.Replicas != 5 {
		t.Fatalf("ScaleDatabase = %+v", info)
	}

	refusing := startRoundTripServer(t, quicserver.WithDatabaseService(scaleDatabaseSvc{err: errors.New("scale refused")}))
	if _, err := refusing.ScaleDatabase(context.Background(), transport.ScaleDatabaseRequest{Name: "db1", TargetReplicas: 3}); err == nil {
		t.Fatal("ScaleDatabase: a refusal returned no error")
	}
}

func TestQUICRoundTrip_ImportAbort(t *testing.T) {
	svc := &importSessionSvc{}
	tr := startRoundTripServer(t, quicserver.WithBatch(svc))
	ctx := context.Background()

	id, err := tr.ImportBegin(ctx, transport.ImportOptions{Format: "jsonl", Filename: "data.jsonl"})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}
	if err := tr.ImportAbort(ctx, id); err != nil {
		t.Fatalf("ImportAbort: %v", err)
	}
	if svc.live(id) {
		t.Fatalf("session %q survived ImportAbort", id)
	}
	if err := tr.ImportAbort(ctx, id); err == nil {
		t.Fatal("ImportAbort: aborting an aborted session returned no error")
	}
}

func TestQUICRoundTrip_VolumePools(t *testing.T) {
	created := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	reported := created.Add(2 * time.Hour)
	var gotCreate volume.CreatePoolRequest
	var gotUpdate volume.UpdatePoolRequest
	var gotUpdateName, gotTenantsPage string
	var deleted string
	svc := &volumetest.Service{T: t,
		ListPoolsFn: func(context.Context) ([]volume.PoolView, error) {
			return []volume.PoolView{{
				Pool:        volume.Pool{Name: "fast", Provider: "dir", CapacityBytes: 100, Overcommit: 1.5, CreatedAt: created},
				UsedBytes:   7,
				TenantCount: 2,
				NodeCounts:  volume.NodeHealthCounts{Ready: 3, Unmapped: 2},
			}}, nil
		},
		GetPoolFn: func(_ context.Context, name string) (*volume.PoolDetail, error) {
			if name != "fast" {
				return nil, &volume.PoolNotFoundError{Pool: name}
			}
			return &volume.PoolDetail{
				PoolView:    volume.PoolView{Pool: volume.Pool{Name: "fast", Provider: "dir"}},
				NodeReports: []volume.NodeReportRow{{NodeID: "n1", State: "ready", SizeBytes: 50, ReportedAt: reported}},
			}, nil
		},
		CreatePoolFn: func(_ context.Context, req volume.CreatePoolRequest) (*volume.Pool, error) {
			gotCreate = req
			return &volume.Pool{Name: req.Name, Provider: req.Provider, CapacityBytes: req.CapacityBytes}, nil
		},
		UpdatePoolFn: func(_ context.Context, name string, req volume.UpdatePoolRequest) (*volume.Pool, error) {
			gotUpdateName, gotUpdate = name, req
			return &volume.Pool{Name: name, Provider: "dir", CapacityBytes: 200}, nil
		},
		DeletePoolFn: func(_ context.Context, name string) error {
			deleted = name
			return nil
		},
		ListAssignmentsFn: func(_ context.Context, pool, pageToken string, pageSize int) (*volume.AssignmentPage, error) {
			gotTenantsPage = pool + "|" + pageToken + "|" + strconv.Itoa(pageSize)
			return &volume.AssignmentPage{
				Assignments:   []volume.Assignment{{TenantID: "acme", ReservedBytes: 5}, {TenantID: "globex", ReservedBytes: 6}},
				NextPageToken: "next",
			}, nil
		},
	}
	tr := startRoundTripServer(t, quicserver.WithVolumeService(svc))
	ctx := context.Background()

	pools, err := tr.ListPools(ctx)
	if err != nil {
		t.Fatalf("ListPools: %v", err)
	}
	if len(pools) != 1 || pools[0].Name != "fast" || pools[0].CapacityBytes != 100 || pools[0].Overcommit != 1.5 ||
		pools[0].UsedBytes != 7 || pools[0].TenantCount != 2 || !pools[0].CreatedAt.Equal(created) ||
		pools[0].Nodes != (transport.VolumePoolNodeCounts{Ready: 3, Unmapped: 2}) {
		t.Fatalf("ListPools = %+v", pools)
	}

	detail, err := tr.GetPool(ctx, "fast")
	if err != nil {
		t.Fatalf("GetPool: %v", err)
	}
	if detail.Name != "fast" || len(detail.NodeReports) != 1 || detail.NodeReports[0].NodeID != "n1" ||
		detail.NodeReports[0].SizeBytes != 50 || !detail.NodeReports[0].ReportedAt.Equal(reported) {
		t.Fatalf("GetPool = %+v", detail)
	}
	if _, err := tr.GetPool(ctx, "gone"); err == nil {
		t.Fatal("GetPool(gone): no error")
	}

	p, err := tr.CreatePool(ctx, &transport.CreateVolumePoolRequest{Name: "slow", Provider: "dir", CapacityBytes: 10})
	if err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	if gotCreate.Name != "slow" || gotCreate.Provider != "dir" || gotCreate.CapacityBytes != 10 || p.Name != "slow" {
		t.Fatalf("CreatePool: server got %+v, returned %+v", gotCreate, p)
	}

	capacity := int64(200)
	p, err = tr.UpdatePool(ctx, &transport.UpdateVolumePoolRequest{Name: "slow", CapacityBytes: &capacity})
	if err != nil {
		t.Fatalf("UpdatePool: %v", err)
	}
	if gotUpdateName != "slow" || gotUpdate.CapacityBytes == nil || *gotUpdate.CapacityBytes != 200 || p.CapacityBytes != 200 {
		t.Fatalf("UpdatePool: server got %+v, returned %+v", gotUpdate, p)
	}

	tenants, err := tr.ListPoolTenants(ctx, &transport.ListVolumePoolTenantsRequest{Pool: "slow", PageToken: "p1", PageSize: 2})
	if err != nil {
		t.Fatalf("ListPoolTenants: %v", err)
	}
	if gotTenantsPage != "slow|p1|2" || tenants.NextPageToken != "next" || len(tenants.Tenants) != 2 ||
		tenants.Tenants[0] != (transport.VolumePoolTenant{TenantID: "acme", ReservedBytes: 5}) ||
		tenants.Tenants[1].TenantID != "globex" {
		t.Fatalf("ListPoolTenants: server got %q, returned %+v", gotTenantsPage, tenants)
	}
	if _, err := tr.ListPoolTenants(ctx, nil); err != nil || gotTenantsPage != "||0" {
		t.Fatalf("ListPoolTenants(nil): err = %v, server got %q", err, gotTenantsPage)
	}
	if err := tr.DeletePool(ctx, "slow"); err != nil || deleted != "slow" {
		t.Fatalf("DeletePool: err = %v, deleted %q", err, deleted)
	}
}

func TestQUICRoundTrip_VolumePools_Unconfigured(t *testing.T) {
	tr := startRoundTripServer(t)
	ctx := context.Background()
	calls := map[string]func() error{
		"ListPools":       func() error { _, err := tr.ListPools(ctx); return err },
		"GetPool":         func() error { _, err := tr.GetPool(ctx, "fast"); return err },
		"CreatePool":      func() error { _, err := tr.CreatePool(ctx, &transport.CreateVolumePoolRequest{Name: "x"}); return err },
		"UpdatePool":      func() error { _, err := tr.UpdatePool(ctx, &transport.UpdateVolumePoolRequest{Name: "x"}); return err },
		"DeletePool":      func() error { return tr.DeletePool(ctx, "x") },
		"ListPoolTenants": func() error { _, err := tr.ListPoolTenants(ctx, &transport.ListVolumePoolTenantsRequest{Pool: "x"}); return err },
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s: a server without the volume service returned no error", name)
		}
	}
}

// TestQUICRoundTrip_ClosedTransport_Services proves each method refuses with
// *transport.ClosedError once the transport is closed, without a round trip.
func TestQUICRoundTrip_ClosedTransport_Services(t *testing.T) {
	tr := startRoundTripServer(t)
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
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
		"ImportAbort":     func() error { return tr.ImportAbort(ctx, "1") },
		"ListPools":       func() error { _, err := tr.ListPools(ctx); return err },
		"GetPool":         func() error { _, err := tr.GetPool(ctx, "fast"); return err },
		"CreatePool":      func() error { _, err := tr.CreatePool(ctx, &transport.CreateVolumePoolRequest{Name: "x"}); return err },
		"UpdatePool":      func() error { _, err := tr.UpdatePool(ctx, &transport.UpdateVolumePoolRequest{Name: "x"}); return err },
		"DeletePool":      func() error { return tr.DeletePool(ctx, "x") },
		"ListPoolTenants": func() error { _, err := tr.ListPoolTenants(ctx, &transport.ListVolumePoolTenantsRequest{Pool: "x"}); return err },
		"DatabaseBarrierOfficerRegister": func() error {
			_, err := tr.DatabaseBarrierOfficerRegister(ctx, &transport.DatabaseOfficerRegisterRequest{})
			return err
		},
		"DatabaseBarrierOfficerList": func() error { _, err := tr.DatabaseBarrierOfficerList(ctx, "db1"); return err },
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

func TestQUICErrors_HealthCheckAndConnectBound(t *testing.T) {
	if got := (&healthCheckError{}).Error(); got != "health check failed: empty status" {
		t.Fatalf("healthCheckError{} = %q", got)
	}
	cause := errors.New("status degraded")
	if got := (&healthCheckError{Err: cause}).Error(); got != "health check failed: status degraded" {
		t.Fatalf("healthCheckError = %q", got)
	}
	e := &connectBoundError{Attempts: 3, Err: errors.New("deadline"), Last: errors.New("refused")}
	want := "quic transport: no healthy connection after 3 attempt(s): deadline; last attempt: refused"
	if got := e.Error(); got != want {
		t.Fatalf("connectBoundError = %q, want %q", got, want)
	}
}
