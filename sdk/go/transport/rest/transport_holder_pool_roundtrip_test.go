package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// recordedCall is one request the wire recorder saw: its verb, its path with
// any query, the database header and the decoded JSON body.
type recordedCall struct {
	Method   string
	Target   string
	Database string
	Body     map[string]any
}

// wireRecorder answers every request with the reply registered for its
// "VERB path" (404 when none is) and records what each request carried, so a
// test asserts the route and the payload a call produced rather than only that
// it returned. The handler runs on the server's goroutines, so the record is
// locked.
type wireRecorder struct {
	mu      sync.Mutex
	calls   []recordedCall
	replies map[string]any
	status  int
}

func newWireRecorder(t *testing.T) (*Transport, *wireRecorder) {
	t.Helper()
	wr := &wireRecorder{replies: map[string]any{}, status: http.StatusOK}
	srv := httptest.NewServer(wr)
	t.Cleanup(srv.Close)
	return newWiredTransport(srv), wr
}

func (wr *wireRecorder) reply(verb, path string, body any) {
	wr.mu.Lock()
	defer wr.mu.Unlock()
	wr.replies[verb+" "+path] = body
}

func (wr *wireRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	wr.mu.Lock()
	wr.calls = append(wr.calls, recordedCall{
		Method:   r.Method,
		Target:   r.URL.RequestURI(),
		Database: r.Header.Get(transport.DatabaseHeader),
		Body:     body,
	})
	out, ok := wr.replies[r.Method+" "+r.URL.Path]
	status := wr.status
	wr.mu.Unlock()
	if !ok {
		http.Error(w, `{"error":"no route"}`, http.StatusNotFound)
		return
	}
	if out == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(out)
}

// last returns the most recent call, failing the test when there was none.
func (wr *wireRecorder) last(t *testing.T) recordedCall {
	t.Helper()
	wr.mu.Lock()
	defer wr.mu.Unlock()
	if len(wr.calls) == 0 {
		t.Fatal("no request reached the server")
	}
	return wr.calls[len(wr.calls)-1]
}

func (wr *wireRecorder) count() int {
	wr.mu.Lock()
	defer wr.mu.Unlock()
	return len(wr.calls)
}

func assertCall(t *testing.T, got recordedCall, method, target string) {
	t.Helper()
	if got.Method != method || got.Target != target {
		t.Fatalf("request = %s %s, want %s %s", got.Method, got.Target, method, target)
	}
}

func TestRESTVolumePools_EveryVerbHitsItsRoute(t *testing.T) {
	rt, wr := newWireRecorder(t)
	pool := transport.VolumePool{Name: "fast pool", Provider: "local", CapacityBytes: 1 << 30}
	wr.reply(http.MethodGet, "/v1/volume-pools/list", map[string]any{"pools": []transport.VolumePool{pool}})
	wr.reply(http.MethodGet, "/v1/volume-pools/fast pool", &transport.VolumePoolDetail{VolumePool: pool})
	wr.reply(http.MethodPost, "/v1/volume-pools", &pool)
	wr.reply(http.MethodPut, "/v1/volume-pools/fast pool", &pool)
	wr.reply(http.MethodDelete, "/v1/volume-pools/fast pool", nil)
	wr.reply(http.MethodGet, "/v1/volume-pools/fast pool/tenants", &transport.ListVolumePoolTenantsResponse{
		Tenants:       []transport.VolumePoolTenant{{TenantID: "acme", ReservedBytes: 7}},
		NextPageToken: "n2",
	})
	ctx := context.Background()

	pools, err := rt.ListPools(ctx)
	if err != nil || len(pools) != 1 || pools[0].Name != pool.Name {
		t.Fatalf("ListPools = %+v, %v", pools, err)
	}
	assertCall(t, wr.last(t), http.MethodGet, "/v1/volume-pools/list")

	detail, err := rt.GetPool(ctx, pool.Name)
	if err != nil || detail.CapacityBytes != pool.CapacityBytes {
		t.Fatalf("GetPool = %+v, %v", detail, err)
	}
	assertCall(t, wr.last(t), http.MethodGet, "/v1/volume-pools/fast%20pool")

	created, err := rt.CreatePool(ctx, &transport.CreateVolumePoolRequest{Name: pool.Name})
	if err != nil || created.Provider != pool.Provider {
		t.Fatalf("CreatePool = %+v, %v", created, err)
	}
	assertCall(t, wr.last(t), http.MethodPost, "/v1/volume-pools")

	description := "d"
	updated, err := rt.UpdatePool(ctx, &transport.UpdateVolumePoolRequest{Name: pool.Name, Description: &description})
	if err != nil || updated.Name != pool.Name {
		t.Fatalf("UpdatePool = %+v, %v", updated, err)
	}
	assertCall(t, wr.last(t), http.MethodPut, "/v1/volume-pools/fast%20pool")

	if err := rt.DeletePool(ctx, pool.Name); err != nil {
		t.Fatalf("DeletePool: %v", err)
	}
	assertCall(t, wr.last(t), http.MethodDelete, "/v1/volume-pools/fast%20pool")

	tenants, err := rt.ListPoolTenants(ctx, &transport.ListVolumePoolTenantsRequest{Pool: pool.Name, PageSize: 5, PageToken: "n1"})
	if err != nil || len(tenants.Tenants) != 1 || tenants.Tenants[0].TenantID != "acme" || tenants.NextPageToken != "n2" {
		t.Fatalf("ListPoolTenants = %+v, %v", tenants, err)
	}
	assertCall(t, wr.last(t), http.MethodGet, "/v1/volume-pools/fast%20pool/tenants?page_size=5&page_token=n1")
}

func TestRESTVolumePools_RefusalsAndClosed(t *testing.T) {
	rt, wr := newWireRecorder(t)
	ctx := context.Background()

	// No route is registered, so every read is refused by the server and the
	// refusal is returned rather than an empty result.
	if _, err := rt.ListPools(ctx); err == nil {
		t.Error("ListPools: want the server's refusal")
	}
	if _, err := rt.GetPool(ctx, "p"); err == nil {
		t.Error("GetPool: want the server's refusal")
	}
	// A nil request lists the empty pool name with no query string.
	if _, err := rt.ListPoolTenants(ctx, nil); err == nil {
		t.Error("ListPoolTenants: want the server's refusal")
	}
	assertCall(t, wr.last(t), http.MethodGet, "/v1/volume-pools//tenants")

	// A reply that is not JSON is a decode error, not a zero value.
	wr.reply(http.MethodGet, "/v1/volume-pools/list", "not-an-object")
	wr.reply(http.MethodGet, "/v1/volume-pools/p", "not-an-object")
	wr.reply(http.MethodGet, "/v1/volume-pools/p/tenants", "not-an-object")
	if _, err := rt.ListPools(ctx); err == nil {
		t.Error("ListPools: want a decode error")
	}
	if _, err := rt.GetPool(ctx, "p"); err == nil {
		t.Error("GetPool: want a decode error")
	}
	if _, err := rt.ListPoolTenants(ctx, &transport.ListVolumePoolTenantsRequest{Pool: "p"}); err == nil {
		t.Error("ListPoolTenants: want a decode error")
	}

	before := wr.count()
	rt.closed.Store(true)
	closedCalls := map[string]func() error{
		"ListPools":       func() error { _, err := rt.ListPools(ctx); return err },
		"GetPool":         func() error { _, err := rt.GetPool(ctx, "p"); return err },
		"CreatePool":      func() error { _, err := rt.CreatePool(ctx, &transport.CreateVolumePoolRequest{}); return err },
		"UpdatePool":      func() error { _, err := rt.UpdatePool(ctx, &transport.UpdateVolumePoolRequest{}); return err },
		"DeletePool":      func() error { return rt.DeletePool(ctx, "p") },
		"ListPoolTenants": func() error { _, err := rt.ListPoolTenants(ctx, nil); return err },
	}
	assertAllClosed(t, closedCalls)
	if wr.count() != before {
		t.Errorf("a closed transport sent %d requests", wr.count()-before)
	}
}

// assertAllClosed runs every call and requires a ClosedError from each.
func assertAllClosed(t *testing.T, calls map[string]func() error) {
	t.Helper()
	for name, call := range calls {
		var closed *transport.ClosedError
		if err := call(); !errors.As(err, &closed) {
			t.Errorf("%s after close = %v, want ClosedError", name, err)
		}
	}
}

func TestRESTBarrierHolders_EveryCallHitsItsRoute(t *testing.T) {
	rt, wr := newWireRecorder(t)
	register := &transport.OfficerRegisterResponse{CertPEM: "cert", Role: transport.RoleSecurityOfficer}
	begin := &transport.ShareClaimBeginResponse{NonceHex: "abcd"}
	complete := &transport.ShareClaimCompleteResponse{ShareIndex: 3}
	wr.reply(http.MethodPost, "/v1/barrier/officers", register)
	wr.reply(http.MethodPost, "/v1/barrier/recovery/officers", register)
	wr.reply(http.MethodGet, "/v1/barrier/officers", map[string]any{"officers": []*transport.OfficerStatus{{Username: "alice", ShareIndex: 1}}})
	wr.reply(http.MethodPost, "/v1/barrier/officers/al ice/claim/begin", begin)
	wr.reply(http.MethodPost, "/v1/barrier/officers/al ice/claim/complete", complete)
	wr.reply(http.MethodPost, "/v1/barrier/recovery/officers/al ice/claim/begin", begin)
	wr.reply(http.MethodPost, "/v1/barrier/recovery/officers/al ice/claim/complete", complete)
	wr.reply(http.MethodPost, "/v1/barrier/quorum/begin", &transport.QuorumChallenge{ChallengeID: "ch-1"})
	wr.reply(http.MethodPost, "/v1/barrier/cluster-unseal", &transport.ClusterSeal{Active: true, Node: "n1"})
	ctx := context.Background()

	if r, err := rt.OfficerRegister(ctx, &transport.OfficerRegisterRequest{Username: "alice"}); err != nil || r.CertPEM != "cert" {
		t.Fatalf("OfficerRegister = %+v, %v", r, err)
	}
	assertCall(t, wr.last(t), http.MethodPost, "/v1/barrier/officers")
	if wr.last(t).Body["username"] != "alice" {
		t.Errorf("OfficerRegister body = %v", wr.last(t).Body)
	}

	if r, err := rt.RecoveryOfficerRegister(ctx, &transport.OfficerRegisterRequest{Username: "bob"}); err != nil || r.Role != transport.RoleSecurityOfficer {
		t.Fatalf("RecoveryOfficerRegister = %+v, %v", r, err)
	}
	assertCall(t, wr.last(t), http.MethodPost, "/v1/barrier/recovery/officers")

	officers, err := rt.OfficerList(ctx)
	if err != nil || len(officers) != 1 || officers[0].Username != "alice" || officers[0].ShareIndex != 1 {
		t.Fatalf("OfficerList = %+v, %v", officers, err)
	}

	claimBegin := &transport.ShareClaimBeginRequest{Username: "al ice"}
	claimComplete := &transport.ShareClaimCompleteRequest{Username: "al ice", NonceHex: "abcd"}
	if r, err := rt.ShareClaimBegin(ctx, claimBegin); err != nil || r.NonceHex != "abcd" {
		t.Fatalf("ShareClaimBegin = %+v, %v", r, err)
	}
	assertCall(t, wr.last(t), http.MethodPost, "/v1/barrier/officers/al%20ice/claim/begin")
	if r, err := rt.ShareClaimComplete(ctx, claimComplete); err != nil || r.ShareIndex != 3 {
		t.Fatalf("ShareClaimComplete = %+v, %v", r, err)
	}
	assertCall(t, wr.last(t), http.MethodPost, "/v1/barrier/officers/al%20ice/claim/complete")
	if r, err := rt.RecoveryShareClaimBegin(ctx, claimBegin); err != nil || r.NonceHex != "abcd" {
		t.Fatalf("RecoveryShareClaimBegin = %+v, %v", r, err)
	}
	assertCall(t, wr.last(t), http.MethodPost, "/v1/barrier/recovery/officers/al%20ice/claim/begin")
	if r, err := rt.RecoveryShareClaimComplete(ctx, claimComplete); err != nil || r.ShareIndex != 3 {
		t.Fatalf("RecoveryShareClaimComplete = %+v, %v", r, err)
	}
	assertCall(t, wr.last(t), http.MethodPost, "/v1/barrier/recovery/officers/al%20ice/claim/complete")

	if c, err := rt.BarrierQuorumBegin(ctx, &transport.QuorumBeginRequest{}); err != nil || c.ChallengeID != "ch-1" {
		t.Fatalf("BarrierQuorumBegin = %+v, %v", c, err)
	}
	assertCall(t, wr.last(t), http.MethodPost, "/v1/barrier/quorum/begin")

	if s, err := rt.BarrierClusterUnseal(ctx); err != nil || !s.Active || s.Node != "n1" {
		t.Fatalf("BarrierClusterUnseal = %+v, %v", s, err)
	}
	assertCall(t, wr.last(t), http.MethodPost, "/v1/barrier/cluster-unseal")
}

func TestRESTBarrierHolders_RefusalsAndClosed(t *testing.T) {
	rt, wr := newWireRecorder(t)
	ctx := context.Background()
	refused := map[string]func() error{
		"OfficerRegister": func() error { _, err := rt.OfficerRegister(ctx, &transport.OfficerRegisterRequest{}); return err },
		"RecoveryOfficerRegister": func() error {
			_, err := rt.RecoveryOfficerRegister(ctx, &transport.OfficerRegisterRequest{})
			return err
		},
		"OfficerList": func() error { _, err := rt.OfficerList(ctx); return err },
		"ShareClaimBegin": func() error {
			_, err := rt.ShareClaimBegin(ctx, &transport.ShareClaimBeginRequest{Username: "u"})
			return err
		},
		"ShareClaimComplete": func() error {
			_, err := rt.ShareClaimComplete(ctx, &transport.ShareClaimCompleteRequest{Username: "u"})
			return err
		},
		"RecoveryShareClaimBegin": func() error {
			_, err := rt.RecoveryShareClaimBegin(ctx, &transport.ShareClaimBeginRequest{Username: "u"})
			return err
		},
		"RecoveryShareClaimComplete": func() error {
			_, err := rt.RecoveryShareClaimComplete(ctx, &transport.ShareClaimCompleteRequest{Username: "u"})
			return err
		},
		"BarrierQuorumBegin":   func() error { _, err := rt.BarrierQuorumBegin(ctx, &transport.QuorumBeginRequest{}); return err },
		"BarrierClusterUnseal": func() error { _, err := rt.BarrierClusterUnseal(ctx); return err },
	}
	for name, call := range refused {
		if err := call(); err == nil {
			t.Errorf("%s: want the server's refusal", name)
		}
	}

	before := wr.count()
	rt.closed.Store(true)
	assertAllClosed(t, refused)
	if wr.count() != before {
		t.Errorf("a closed transport sent %d requests", wr.count()-before)
	}
}

func TestRESTTenantBarrierHolders_EveryCallHitsItsRoute(t *testing.T) {
	rt, wr := newWireRecorder(t)
	round := &transport.QuorumRound{Op: transport.OpRekey, Required: 2}
	wr.reply(http.MethodPost, "/v1/tenants/acme/barrier/officers", &transport.OfficerRegisterResponse{CertPEM: "cert"})
	wr.reply(http.MethodGet, "/v1/tenants/acme/barrier/officers", map[string]any{"officers": []*transport.OfficerStatus{{Username: "carol"}}})
	wr.reply(http.MethodPost, "/v1/tenants/acme/barrier/officers/carol/claim/begin", &transport.ShareClaimBeginResponse{NonceHex: "ff"})
	wr.reply(http.MethodPost, "/v1/tenants/acme/barrier/officers/carol/claim/complete", &transport.ShareClaimCompleteResponse{ShareIndex: 2})
	wr.reply(http.MethodPost, "/v1/tenants/acme/barrier/quorum/begin", &transport.QuorumChallenge{ChallengeID: "ch-2"})
	wr.reply(http.MethodPost, "/v1/tenants/acme/barrier/rekey/start", round)
	wr.reply(http.MethodPost, "/v1/tenants/acme/barrier/officers/revoke", round)
	ctx := context.Background()

	if r, err := rt.TenantBarrierOfficerRegister(ctx, &transport.TenantOfficerRegisterRequest{TenantID: "acme", Username: "carol"}); err != nil || r.CertPEM != "cert" {
		t.Fatalf("TenantBarrierOfficerRegister = %+v, %v", r, err)
	}
	assertCall(t, wr.last(t), http.MethodPost, "/v1/tenants/acme/barrier/officers")

	officers, err := rt.TenantBarrierOfficerList(ctx, "acme")
	if err != nil || len(officers) != 1 || officers[0].Username != "carol" {
		t.Fatalf("TenantBarrierOfficerList = %+v, %v", officers, err)
	}

	if r, err := rt.TenantBarrierShareClaimBegin(ctx, &transport.TenantShareClaimBeginRequest{TenantID: "acme", Username: "carol"}); err != nil || r.NonceHex != "ff" {
		t.Fatalf("TenantBarrierShareClaimBegin = %+v, %v", r, err)
	}
	if r, err := rt.TenantBarrierShareClaimComplete(ctx, &transport.TenantShareClaimCompleteRequest{TenantID: "acme", Username: "carol"}); err != nil || r.ShareIndex != 2 {
		t.Fatalf("TenantBarrierShareClaimComplete = %+v, %v", r, err)
	}
	if c, err := rt.TenantBarrierQuorumBegin(ctx, &transport.TenantQuorumBeginRequest{TenantID: "acme", Op: transport.OpUnseal}); err != nil || c.ChallengeID != "ch-2" {
		t.Fatalf("TenantBarrierQuorumBegin = %+v, %v", c, err)
	}
	if wr.last(t).Body["op"] != transport.OpUnseal {
		t.Errorf("TenantBarrierQuorumBegin body = %v", wr.last(t).Body)
	}
	if r, err := rt.TenantBarrierRekeyStart(ctx, &transport.TenantRekeyStartRequest{TenantID: "acme", Threshold: 2}); err != nil || r.Required != 2 {
		t.Fatalf("TenantBarrierRekeyStart = %+v, %v", r, err)
	}
	assertCall(t, wr.last(t), http.MethodPost, "/v1/tenants/acme/barrier/rekey/start")
	if r, err := rt.TenantBarrierOfficerRevoke(ctx, &transport.TenantOfficerRevokeRequest{TenantID: "acme", Username: "carol"}); err != nil || r.Op != transport.OpRekey {
		t.Fatalf("TenantBarrierOfficerRevoke = %+v, %v", r, err)
	}
	assertCall(t, wr.last(t), http.MethodPost, "/v1/tenants/acme/barrier/officers/revoke")
}

func TestRESTTenantBarrierHolders_RefuseWithoutTenant(t *testing.T) {
	rt, wr := newWireRecorder(t)
	ctx := context.Background()
	// A nil request and an empty tenant ID are both refused before any
	// request leaves: neither names a barrier to address.
	noTenant := map[string]func() error{
		"Register nil": func() error { _, err := rt.TenantBarrierOfficerRegister(ctx, nil); return err },
		"Register empty": func() error {
			_, err := rt.TenantBarrierOfficerRegister(ctx, &transport.TenantOfficerRegisterRequest{})
			return err
		},
		"List empty":      func() error { _, err := rt.TenantBarrierOfficerList(ctx, ""); return err },
		"ClaimBegin nil":  func() error { _, err := rt.TenantBarrierShareClaimBegin(ctx, nil); return err },
		"ClaimComplete":   func() error { _, err := rt.TenantBarrierShareClaimComplete(ctx, nil); return err },
		"QuorumBegin nil": func() error { _, err := rt.TenantBarrierQuorumBegin(ctx, nil); return err },
		"Rekey nil":       func() error { _, err := rt.TenantBarrierRekeyStart(ctx, nil); return err },
		"Rekey empty": func() error {
			_, err := rt.TenantBarrierRekeyStart(ctx, &transport.TenantRekeyStartRequest{})
			return err
		},
		"Revoke nil": func() error { _, err := rt.TenantBarrierOfficerRevoke(ctx, nil); return err },
	}
	for name, call := range noTenant {
		var empty *emptyTenantIDError
		if err := call(); !errors.As(err, &empty) {
			t.Errorf("%s = %v, want emptyTenantIDError", name, err)
		}
	}
	if wr.count() != 0 {
		t.Errorf("%d requests left without a tenant", wr.count())
	}

	if _, err := rt.TenantBarrierOfficerList(ctx, "acme"); err == nil {
		t.Error("TenantBarrierOfficerList: want the server's refusal")
	}
	rt.closed.Store(true)
	assertAllClosed(t, map[string]func() error{
		"Register": func() error {
			_, err := rt.TenantBarrierOfficerRegister(ctx, &transport.TenantOfficerRegisterRequest{TenantID: "acme"})
			return err
		},
		"List": func() error { _, err := rt.TenantBarrierOfficerList(ctx, "acme"); return err },
	})
}

func TestRESTClusterJoin_EveryCallHitsItsRoute(t *testing.T) {
	rt, wr := newWireRecorder(t)
	for _, path := range []string{
		"/membership/promote", "/membership/add-observer", "/v1/cluster/join-identities/retire",
		"/v1/cluster/join-requests/approve", "/v1/cluster/join-requests/reject",
	} {
		wr.reply(http.MethodPost, path, map[string]any{})
	}
	wr.reply(http.MethodGet, "/v1/cluster/join-requests", map[string]any{"requests": []map[string]any{
		{"id": "jr-1", "node_uri": "spiffe://example/node/2", "replica_id": 2, "features": []string{"voter"}},
	}})
	ctx := context.Background()

	if err := rt.PromoteMember(ctx, 1, 2); err != nil {
		t.Fatalf("PromoteMember: %v", err)
	}
	assertCall(t, wr.last(t), http.MethodPost, "/membership/promote")
	if b := wr.last(t).Body; b["shard_id"] != float64(1) || b["replica_id"] != float64(2) {
		t.Errorf("PromoteMember body = %v", b)
	}
	if err := rt.AddObserverMember(ctx, 1, 3, "db.example.com:7000", "node-3"); err != nil {
		t.Fatalf("AddObserverMember: %v", err)
	}
	assertCall(t, wr.last(t), http.MethodPost, "/membership/add-observer")
	if err := rt.RetireJoinIdentity(ctx, "node-3", "decommissioned"); err != nil {
		t.Fatalf("RetireJoinIdentity: %v", err)
	}
	assertCall(t, wr.last(t), http.MethodPost, "/v1/cluster/join-identities/retire")

	pending, err := rt.ListPendingJoinRequests(ctx)
	if err != nil || len(pending) != 1 || pending[0].ID != "jr-1" || pending[0].ReplicaID != 2 || len(pending[0].Features) != 1 {
		t.Fatalf("ListPendingJoinRequests = %+v, %v", pending, err)
	}
	if err := rt.ApproveJoinRequest(ctx, "jr-1", "node-2"); err != nil {
		t.Fatalf("ApproveJoinRequest: %v", err)
	}
	assertCall(t, wr.last(t), http.MethodPost, "/v1/cluster/join-requests/approve")
	if err := rt.RejectJoinRequest(ctx, "jr-1", "unknown"); err != nil {
		t.Fatalf("RejectJoinRequest: %v", err)
	}
	assertCall(t, wr.last(t), http.MethodPost, "/v1/cluster/join-requests/reject")
}

func TestRESTClusterJoin_RefusalsAndClosed(t *testing.T) {
	rt, wr := newWireRecorder(t)
	ctx := context.Background()
	calls := map[string]func() error{
		"PromoteMember":           func() error { return rt.PromoteMember(ctx, 1, 2) },
		"AddObserverMember":       func() error { return rt.AddObserverMember(ctx, 1, 2, "a", "i") },
		"RetireJoinIdentity":      func() error { return rt.RetireJoinIdentity(ctx, "n", "r") },
		"ListPendingJoinRequests": func() error { _, err := rt.ListPendingJoinRequests(ctx); return err },
		"ApproveJoinRequest":      func() error { return rt.ApproveJoinRequest(ctx, "id", "n") },
		"RejectJoinRequest":       func() error { return rt.RejectJoinRequest(ctx, "id", "r") },
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s: want the server's refusal", name)
		}
	}
	before := wr.count()
	rt.closed.Store(true)
	assertAllClosed(t, calls)
	if wr.count() != before {
		t.Errorf("a closed transport sent %d requests", wr.count()-before)
	}
}

func TestRESTTenantAndAccess_EveryCallHitsItsRoute(t *testing.T) {
	rt, wr := newWireRecorder(t)
	wr.reply(http.MethodGet, "/v1/tenants/deleted/list", &transport.ListDeletedTenantsResponse{
		Tenants:       []transport.DeletedTenant{{ID: "gone", Name: "old"}},
		NextPageToken: "p2",
	})
	wr.reply(http.MethodPost, "/users/invite", &transport.Invitation{ID: 41, Username: "dave"})
	wr.reply(http.MethodPost, "/invitations/redeem", &transport.EnrolledCert{Username: "dave", Serial: "01"})
	wr.reply(http.MethodPost, "/v1/sql/query", &transport.SQLQueryResult{Columns: []string{"n"}, RowCount: 1})
	wr.reply(http.MethodGet, "/v1/audit/replicated/verify", &transport.ChainContinuityResult{Verified: true, Records: 4})
	wr.reply(http.MethodPost, "/v1/credentials/store", map[string]any{})
	wr.reply(http.MethodPost, "/batch/import/s 1/abort", map[string]any{})
	wr.reply(http.MethodPatch, "/v1/databases/orders/scale", &transport.DatabaseInfo{Name: "orders"})
	ctx := context.Background()

	deleted, err := rt.ListDeletedTenants(ctx, &transport.ListDeletedTenantsRequest{PageSize: 10, PageToken: "p1"})
	if err != nil || len(deleted.Tenants) != 1 || deleted.Tenants[0].ID != "gone" || deleted.NextPageToken != "p2" {
		t.Fatalf("ListDeletedTenants = %+v, %v", deleted, err)
	}
	assertCall(t, wr.last(t), http.MethodGet, "/v1/tenants/deleted/list?page_size=10&page_token=p1")

	inv, err := rt.InviteUser(ctx, &transport.InviteUserRequest{})
	if err != nil || inv.ID != 41 || inv.Username != "dave" {
		t.Fatalf("InviteUser = %+v, %v", inv, err)
	}
	enrolled, err := rt.RedeemInvitation(ctx, &transport.RedeemInvitationRequest{})
	if err != nil || enrolled.Serial != "01" {
		t.Fatalf("RedeemInvitation = %+v, %v", enrolled, err)
	}

	if rt.SQLClient() != transport.SQLClient(rt) {
		t.Error("SQLClient is not the transport itself")
	}
	res, err := rt.SQLClient().Query(ctx, "orders", "SELECT 1")
	if err != nil || res.RowCount != 1 || len(res.Columns) != 1 {
		t.Fatalf("Query = %+v, %v", res, err)
	}
	if b := wr.last(t).Body; b["query"] != "SELECT 1" || b["database"] != "orders" {
		t.Errorf("Query body = %v", b)
	}

	if rt.AuditClient() != transport.AuditClient(rt) {
		t.Error("AuditClient is not the transport itself")
	}
	chain, err := rt.VerifyReplicatedChain(ctx, "acme")
	if err != nil || !chain.Verified || chain.Records != 4 {
		t.Fatalf("VerifyReplicatedChain = %+v, %v", chain, err)
	}
	assertCall(t, wr.last(t), http.MethodGet, "/v1/audit/replicated/verify?tenant_id=acme")
	if _, err := rt.VerifyReplicatedChain(ctx, ""); err != nil {
		t.Fatalf("VerifyReplicatedChain(all): %v", err)
	}
	assertCall(t, wr.last(t), http.MethodGet, "/v1/audit/replicated/verify")

	if err := rt.CredentialStore(ctx, "db-pass", []byte("s3cret")); err != nil {
		t.Fatalf("CredentialStore: %v", err)
	}
	if b := wr.last(t).Body; b["name"] != "db-pass" || b["value"] != "czNjcmV0" {
		t.Errorf("CredentialStore body = %v", b)
	}
	if err := rt.ImportAbort(ctx, "s 1"); err != nil {
		t.Fatalf("ImportAbort: %v", err)
	}
	assertCall(t, wr.last(t), http.MethodPost, "/batch/import/s%201/abort")

	info, err := rt.ScaleDatabase(ctx, transport.ScaleDatabaseRequest{Name: "orders", TargetReplicas: 5})
	if err != nil || info.Name != "orders" {
		t.Fatalf("ScaleDatabase = %+v, %v", info, err)
	}
	if b := wr.last(t).Body; b["target_replicas"] != float64(5) || b["Name"] != nil {
		t.Errorf("ScaleDatabase body = %v", b)
	}
}

func TestRESTTenantAndAccess_RefusalsAndClosed(t *testing.T) {
	rt, wr := newWireRecorder(t)
	ctx := context.Background()

	// A nil request lists the first page with no query string.
	if _, err := rt.ListDeletedTenants(ctx, nil); err == nil {
		t.Error("ListDeletedTenants: want the server's refusal")
	}
	assertCall(t, wr.last(t), http.MethodGet, "/v1/tenants/deleted/list")

	// An empty tombstone list decodes as an empty, non-nil slice.
	wr.reply(http.MethodGet, "/v1/tenants/deleted/list", map[string]any{})
	deleted, err := rt.ListDeletedTenants(ctx, nil)
	if err != nil || deleted.Tenants == nil || len(deleted.Tenants) != 0 {
		t.Fatalf("ListDeletedTenants(empty) = %+v, %v", deleted, err)
	}
	wr.reply(http.MethodGet, "/v1/tenants/deleted/list", "not-an-object")
	if _, err := rt.ListDeletedTenants(ctx, nil); err == nil {
		t.Error("ListDeletedTenants: want a decode error")
	}

	// 501 is the server saying it has no replicated chain to verify; any other
	// status is an unexpected one carrying the body.
	wr.reply(http.MethodGet, "/v1/audit/replicated/verify", map[string]any{})
	wr.mu.Lock()
	wr.status = http.StatusNotImplemented
	wr.mu.Unlock()
	var notImpl *transport.NotImplementedError
	if _, err := rt.VerifyReplicatedChain(ctx, ""); !errors.As(err, &notImpl) {
		t.Errorf("VerifyReplicatedChain(501) = %v, want NotImplementedError", err)
	}
	wr.mu.Lock()
	wr.status = http.StatusTeapot
	wr.mu.Unlock()
	var unexpected *transport.UnexpectedStatusError
	if _, err := rt.VerifyReplicatedChain(ctx, ""); !errors.As(err, &unexpected) || unexpected.StatusCode != http.StatusTeapot {
		t.Errorf("VerifyReplicatedChain(418) = %v, want UnexpectedStatusError", err)
	}
	wr.mu.Lock()
	wr.status = http.StatusOK
	wr.mu.Unlock()
	wr.reply(http.MethodGet, "/v1/audit/replicated/verify", "not-an-object")
	if _, err := rt.VerifyReplicatedChain(ctx, ""); err == nil {
		t.Error("VerifyReplicatedChain: want a decode error")
	}

	// The REST surface has no audit-emit route: the call says so.
	if err := rt.EmitAudit(ctx, transport.AuditEvent{}); !errors.As(err, &notImpl) {
		t.Errorf("EmitAudit = %v, want NotImplementedError", err)
	}

	refused := map[string]func() error{
		"InviteUser":       func() error { _, err := rt.InviteUser(ctx, &transport.InviteUserRequest{}); return err },
		"RedeemInvitation": func() error { _, err := rt.RedeemInvitation(ctx, &transport.RedeemInvitationRequest{}); return err },
		"Query":            func() error { _, err := rt.Query(ctx, "d", "q"); return err },
		"CredentialStore":  func() error { return rt.CredentialStore(ctx, "n", nil) },
		"ImportAbort":      func() error { return rt.ImportAbort(ctx, "s") },
		"ScaleDatabase":    func() error { _, err := rt.ScaleDatabase(ctx, transport.ScaleDatabaseRequest{Name: "d"}); return err },
	}
	for name, call := range refused {
		if err := call(); err == nil {
			t.Errorf("%s: want the server's refusal", name)
		}
	}

	before := wr.count()
	rt.closed.Store(true)
	refused["ListDeletedTenants"] = func() error { _, err := rt.ListDeletedTenants(ctx, nil); return err }
	refused["VerifyReplicatedChain"] = func() error { _, err := rt.VerifyReplicatedChain(ctx, ""); return err }
	refused["EmitAudit"] = func() error { return rt.EmitAudit(ctx, transport.AuditEvent{}) }
	assertAllClosed(t, refused)
	if wr.count() != before {
		t.Errorf("a closed transport sent %d requests", wr.count()-before)
	}
}

func TestRESTUpdateTenantEntitlements_PatchesTheEntitlementsAlone(t *testing.T) {
	rt, wr := newWireRecorder(t)
	wr.reply(http.MethodPatch, "/v1/tenants/acme/entitlements", &restTenant{ID: "acme", Name: "Acme"})
	req := &transport.UpdateTenantEntitlementsRequest{
		TenantID:     "acme",
		Entitlements: transport.TenantEntitlements{AllowDedicatedRaft: true, AllowedVolumePools: []string{"fast"}},
	}
	resp, err := rt.UpdateTenantEntitlements(context.Background(), req)
	if err != nil || resp.Tenant.ID != "acme" {
		t.Fatalf("UpdateTenantEntitlements = %+v, %v", resp, err)
	}
	got := wr.last(t)
	assertCall(t, got, http.MethodPatch, "/v1/tenants/acme/entitlements")
	// The body is the entitlements document itself, not the request wrapper.
	raw, _ := json.Marshal(&req.Entitlements)
	var want map[string]any
	_ = json.Unmarshal(raw, &want)
	if !reflect.DeepEqual(got.Body, want) {
		t.Errorf("body = %v, want %v", got.Body, want)
	}
}

func TestRESTUpdateTenantEntitlements_Refusals(t *testing.T) {
	rt, wr := newWireRecorder(t)
	ctx := context.Background()
	req := &transport.UpdateTenantEntitlementsRequest{TenantID: "acme"}
	if _, err := rt.UpdateTenantEntitlements(ctx, req); err == nil {
		t.Error("want the server's refusal")
	}
	wr.reply(http.MethodPatch, "/v1/tenants/acme/entitlements", "not-an-object")
	if _, err := rt.UpdateTenantEntitlements(ctx, req); err == nil {
		t.Error("want a decode error")
	}
	before := wr.count()
	rt.closed.Store(true)
	assertAllClosed(t, map[string]func() error{
		"UpdateTenantEntitlements": func() error { _, err := rt.UpdateTenantEntitlements(ctx, req); return err },
	})
	if wr.count() != before {
		t.Errorf("a closed transport sent %d requests", wr.count()-before)
	}
}

func TestRESTDatabaseBarrierShareClaimComplete_RoundTrip(t *testing.T) {
	rt, wr := newWireRecorder(t)
	wr.reply(http.MethodPost, "/v1/databases/vault/barrier/officers/al ice/claim/complete", &transport.ShareClaimCompleteResponse{ShareIndex: 4})
	ctx := context.Background()
	req := &transport.DatabaseShareClaimCompleteRequest{
		Database:                  "vault",
		ShareClaimCompleteRequest: transport.ShareClaimCompleteRequest{Username: "al ice", NonceHex: "aa"},
	}
	resp, err := rt.DatabaseBarrierShareClaimComplete(ctx, req)
	if err != nil || resp.ShareIndex != 4 {
		t.Fatalf("DatabaseBarrierShareClaimComplete = %+v, %v", resp, err)
	}
	assertCall(t, wr.last(t), http.MethodPost, "/v1/databases/vault/barrier/officers/al%20ice/claim/complete")

	// Neither a nil request nor an empty name addresses a database barrier.
	before := wr.count()
	var nameRequired *transport.DatabaseNameRequiredError
	if _, err := rt.DatabaseBarrierShareClaimComplete(ctx, nil); !errors.As(err, &nameRequired) {
		t.Errorf("nil request = %v, want DatabaseNameRequiredError", err)
	}
	if _, err := rt.DatabaseBarrierShareClaimComplete(ctx, &transport.DatabaseShareClaimCompleteRequest{}); !errors.As(err, &nameRequired) {
		t.Errorf("empty name = %v, want DatabaseNameRequiredError", err)
	}
	if wr.count() != before {
		t.Errorf("%d requests left without a database", wr.count()-before)
	}
	req.Username = "bob"
	if _, err := rt.DatabaseBarrierShareClaimComplete(ctx, req); err == nil {
		t.Error("unrouted holder: want the server's refusal")
	}
}

func TestRESTWithDatabase_ScopesDataPlaneOnly(t *testing.T) {
	rt, wr := newWireRecorder(t)
	wr.reply(http.MethodPost, "/batch/import/s1/abort", map[string]any{})
	wr.reply(http.MethodGet, "/v1/volume-pools/list", map[string]any{"pools": []transport.VolumePool{}})
	scoped, err := rt.WithDatabase("orders")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	ctx := context.Background()

	// A data-plane request through the scoped transport names its database.
	if err := scoped.(*Transport).ImportAbort(ctx, "s1"); err != nil {
		t.Fatalf("scoped ImportAbort: %v", err)
	}
	if got := wr.last(t).Database; got != "orders" {
		t.Errorf("scoped data-plane database = %q, want orders", got)
	}
	// A control-plane request is cluster-wide and carries no database.
	if _, err := scoped.(*Transport).ListPools(ctx); err != nil {
		t.Fatalf("scoped ListPools: %v", err)
	}
	if got := wr.last(t).Database; got != "" {
		t.Errorf("scoped control-plane database = %q, want none", got)
	}
	// The transport it was derived from stays unscoped.
	if err := rt.ImportAbort(ctx, "s1"); err != nil {
		t.Fatalf("base ImportAbort: %v", err)
	}
	if got := wr.last(t).Database; got != "" {
		t.Errorf("base data-plane database = %q, want unscoped", got)
	}
}
