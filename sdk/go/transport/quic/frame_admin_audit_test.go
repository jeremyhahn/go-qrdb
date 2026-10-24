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
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// recordingFrames is a FrameSender that records every frame and answers each
// with the body registered for its method.
type recordingFrames struct {
	sent    map[string][]byte
	answers map[string][]byte
}

func newRecordingFrames() *recordingFrames {
	return &recordingFrames{sent: map[string][]byte{}, answers: map[string][]byte{}}
}

func (r *recordingFrames) send(_ context.Context, method, _ string, payload []byte) (uint16, []byte, error) {
	r.sent[method] = payload
	return wire.StatusOK, r.answers[method], nil
}

func mustMarshalPB(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustUnmarshalPB(t *testing.T, b []byte, m proto.Message) {
	t.Helper()
	if err := proto.Unmarshal(b, m); err != nil {
		t.Fatalf("unmarshal %T: %v", m, err)
	}
}

// TestFrameTransport_JoinAdminCallsCarryEveryField proves each join-admin
// call frames its arguments to its admin.* method.
func TestFrameTransport_JoinAdminCallsCarryEveryField(t *testing.T) {
	ctx := context.Background()
	frames := newRecordingFrames()
	tr := NewFrameTransport("", frames.send)

	if err := tr.PromoteMember(ctx, 7, 9); err != nil {
		t.Fatalf("PromoteMember: %v", err)
	}
	var promote pb.PromoteMemberRequest
	mustUnmarshalPB(t, frames.sent["admin.PromoteMember"], &promote)
	if promote.GetShardId() != 7 || promote.GetReplicaId() != 9 {
		t.Fatalf("PromoteMember frame = %v; want shard 7 replica 9", &promote)
	}

	if err := tr.RetireJoinIdentity(ctx, "node-3", "decommissioned"); err != nil {
		t.Fatalf("RetireJoinIdentity: %v", err)
	}
	var retire pb.RetireJoinIdentityRequest
	mustUnmarshalPB(t, frames.sent["admin.RetireJoinIdentity"], &retire)
	if retire.GetNodeId() != "node-3" || retire.GetReason() != "decommissioned" {
		t.Fatalf("RetireJoinIdentity frame = %v", &retire)
	}

	if err := tr.AddObserverMember(ctx, 4, 5, "10.0.0.5:7000", "node-5"); err != nil {
		t.Fatalf("AddObserverMember: %v", err)
	}
	var observer pb.AddObserverMemberRequest
	mustUnmarshalPB(t, frames.sent["admin.AddObserverMember"], &observer)
	if observer.GetShardId() != 4 || observer.GetReplicaId() != 5 ||
		observer.GetRaftAddress() != "10.0.0.5:7000" || observer.GetMemberIdentity() != "node-5" {
		t.Fatalf("AddObserverMember frame = %v", &observer)
	}

	if err := tr.ApproveJoinRequest(ctx, "jr-1", "node-6"); err != nil {
		t.Fatalf("ApproveJoinRequest: %v", err)
	}
	var approve pb.ApproveJoinRequestRequest
	mustUnmarshalPB(t, frames.sent["admin.ApproveJoinRequest"], &approve)
	if approve.GetId() != "jr-1" || approve.GetNodeId() != "node-6" {
		t.Fatalf("ApproveJoinRequest frame = %v", &approve)
	}

	if err := tr.RejectJoinRequest(ctx, "jr-2", "unknown host"); err != nil {
		t.Fatalf("RejectJoinRequest: %v", err)
	}
	var reject pb.RejectJoinRequestRequest
	mustUnmarshalPB(t, frames.sent["admin.RejectJoinRequest"], &reject)
	if reject.GetId() != "jr-2" || reject.GetReason() != "unknown host" {
		t.Fatalf("RejectJoinRequest frame = %v", &reject)
	}
}

// TestFrameTransport_MintJoinTokenDecodesTheToken proves the token request
// is framed and the RFC 3339 expiry decoded.
func TestFrameTransport_MintJoinTokenDecodesTheToken(t *testing.T) {
	frames := newRecordingFrames()
	expires := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	frames.answers["admin.MintJoinToken"] = mustMarshalPB(t, &pb.MintJoinTokenResponse{Token: "tok", ExpiresAt: expires.Format(time.RFC3339)})
	tr := NewFrameTransport("", frames.send)

	resp, err := tr.MintJoinToken(context.Background(), &transport.MintJoinTokenRequest{NodeID: "node-8", TTLSeconds: 600})
	if err != nil {
		t.Fatalf("MintJoinToken: %v", err)
	}
	if resp.Token != "tok" || !resp.ExpiresAt.Equal(expires) {
		t.Fatalf("MintJoinToken = %+v; want tok expiring %v", resp, expires)
	}
	var req pb.MintJoinTokenRequest
	mustUnmarshalPB(t, frames.sent["admin.MintJoinToken"], &req)
	if req.GetNodeId() != "node-8" || req.GetTtlSeconds() != 600 {
		t.Fatalf("MintJoinToken frame = %v", &req)
	}
}

// TestFrameTransport_MintJoinTokenRefusesABadAnswer proves an undecodable
// answer or expiry is a RequestError naming MintJoinToken.
func TestFrameTransport_MintJoinTokenRefusesABadAnswer(t *testing.T) {
	for name, body := range map[string][]byte{
		"bad expiry": mustMarshalPB(t, &pb.MintJoinTokenResponse{Token: "tok", ExpiresAt: "tomorrow"}),
		"bad proto":  {0xff, 0xff, 0xff},
	} {
		t.Run(name, func(t *testing.T) {
			frames := newRecordingFrames()
			frames.answers["admin.MintJoinToken"] = body
			tr := NewFrameTransport("", frames.send)
			_, err := tr.MintJoinToken(context.Background(), &transport.MintJoinTokenRequest{NodeID: "n"})
			requireQUICRequestError(t, err, "MintJoinToken")
		})
	}
}

// TestFrameTransport_ListPendingJoinRequestsDecodesEachRequest proves every
// pending request is decoded with its timestamps.
func TestFrameTransport_ListPendingJoinRequestsDecodesEachRequest(t *testing.T) {
	frames := newRecordingFrames()
	submitted := time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)
	expires := submitted.Add(time.Hour)
	frames.answers["admin.ListPendingJoinRequests"] = mustMarshalPB(t, &pb.ListPendingJoinRequestsResponse{
		Requests: []*pb.PendingJoinRequestMessage{{
			Id:          "jr-1",
			NodeUri:     "qrdb://node-1",
			ReplicaId:   11,
			Features:    []string{"mvcc"},
			SubmittedAt: submitted.Format(time.RFC3339),
			ExpiresAt:   expires.Format(time.RFC3339),
		}},
	})
	tr := NewFrameTransport("", frames.send)

	got, err := tr.ListPendingJoinRequests(context.Background())
	if err != nil {
		t.Fatalf("ListPendingJoinRequests: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListPendingJoinRequests = %d requests; want 1", len(got))
	}
	r := got[0]
	if r.ID != "jr-1" || r.NodeURI != "qrdb://node-1" || r.ReplicaID != 11 ||
		len(r.Features) != 1 || r.Features[0] != "mvcc" ||
		!r.SubmittedAt.Equal(submitted) || !r.ExpiresAt.Equal(expires) {
		t.Fatalf("ListPendingJoinRequests[0] = %+v", r)
	}
}

// TestFrameTransport_ListPendingJoinRequestsRefusesABadAnswer proves an
// undecodable answer or timestamp is a RequestError.
func TestFrameTransport_ListPendingJoinRequestsRefusesABadAnswer(t *testing.T) {
	good := time.Now().UTC().Format(time.RFC3339)
	for name, body := range map[string][]byte{
		"bad submitted": mustMarshalPB(t, &pb.ListPendingJoinRequestsResponse{Requests: []*pb.PendingJoinRequestMessage{{SubmittedAt: "x", ExpiresAt: good}}}),
		"bad expiry":    mustMarshalPB(t, &pb.ListPendingJoinRequestsResponse{Requests: []*pb.PendingJoinRequestMessage{{SubmittedAt: good, ExpiresAt: "x"}}}),
		"bad proto":     {0xff, 0xff, 0xff},
	} {
		t.Run(name, func(t *testing.T) {
			frames := newRecordingFrames()
			frames.answers["admin.ListPendingJoinRequests"] = body
			tr := NewFrameTransport("", frames.send)
			_, err := tr.ListPendingJoinRequests(context.Background())
			requireQUICRequestError(t, err, "ListPendingJoinRequests")
		})
	}
}

// TestFrameTransport_JoinAdminCallsSurfaceTheSendersError proves a sender
// failure reaches the caller of every join-admin call.
func TestFrameTransport_JoinAdminCallsSurfaceTheSendersError(t *testing.T) {
	ctx := context.Background()
	tr := NewFrameTransport("", func(context.Context, string, string, []byte) (uint16, []byte, error) {
		return 0, nil, failure{}
	})
	calls := map[string]func() error{
		"PromoteMember":      func() error { return tr.PromoteMember(ctx, 1, 2) },
		"RetireJoinIdentity": func() error { return tr.RetireJoinIdentity(ctx, "n", "r") },
		"AddObserverMember":  func() error { return tr.AddObserverMember(ctx, 1, 2, "a", "i") },
		"ApproveJoinRequest": func() error { return tr.ApproveJoinRequest(ctx, "id", "n") },
		"RejectJoinRequest":  func() error { return tr.RejectJoinRequest(ctx, "id", "r") },
		"MintJoinToken": func() error {
			_, err := tr.MintJoinToken(ctx, &transport.MintJoinTokenRequest{NodeID: "n"})
			return err
		},
		"ListPendingJoinRequests": func() error {
			_, err := tr.ListPendingJoinRequests(ctx)
			return err
		},
	}
	for name, call := range calls {
		if err := call(); !errors.As(err, new(failure)) {
			t.Errorf("%s = %v; want the sender's failure", name, err)
		}
	}
}

// TestFrameTransport_JoinAdminCallsRefuseOnceClosed proves every join-admin
// call returns ClosedError without sending once the transport is closed.
func TestFrameTransport_JoinAdminCallsRefuseOnceClosed(t *testing.T) {
	ctx := context.Background()
	frames := newRecordingFrames()
	tr := NewFrameTransport("", frames.send)
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	calls := map[string]func() error{
		"PromoteMember":      func() error { return tr.PromoteMember(ctx, 1, 2) },
		"RetireJoinIdentity": func() error { return tr.RetireJoinIdentity(ctx, "n", "r") },
		"AddObserverMember":  func() error { return tr.AddObserverMember(ctx, 1, 2, "a", "i") },
		"ApproveJoinRequest": func() error { return tr.ApproveJoinRequest(ctx, "id", "n") },
		"RejectJoinRequest":  func() error { return tr.RejectJoinRequest(ctx, "id", "r") },
		"MintJoinToken": func() error {
			_, err := tr.MintJoinToken(ctx, &transport.MintJoinTokenRequest{NodeID: "n"})
			return err
		},
		"ListPendingJoinRequests": func() error {
			_, err := tr.ListPendingJoinRequests(ctx)
			return err
		},
		"EmitAudit": func() error { return tr.EmitAudit(ctx, transport.AuditEvent{}) },
		"QueryAudit": func() error {
			_, err := tr.QueryAudit(ctx, transport.AuditFilter{})
			return err
		},
		"VerifyReplicatedChain": func() error {
			_, err := tr.VerifyReplicatedChain(ctx, "t1")
			return err
		},
	}
	for name, call := range calls {
		if err := call(); !errors.As(err, new(*transport.ClosedError)) {
			t.Errorf("%s = %v; want ClosedError", name, err)
		}
	}
	if len(frames.sent) != 0 {
		t.Fatalf("a closed transport sent %d frames", len(frames.sent))
	}
}

// TestFrameTransport_AuditClientIsTheTransport proves the audit projection
// is the transport itself and that emission is refused as not implemented.
func TestFrameTransport_AuditClientIsTheTransport(t *testing.T) {
	frames := newRecordingFrames()
	tr := NewFrameTransport("", frames.send)
	if got, ok := tr.AuditClient().(*Transport); !ok || got != tr {
		t.Fatalf("AuditClient = %T; want the transport itself", tr.AuditClient())
	}
	var nie *transport.NotImplementedError
	if err := tr.EmitAudit(context.Background(), transport.AuditEvent{}); !errors.As(err, &nie) || nie.Method != "EmitAudit" {
		t.Fatalf("EmitAudit = %v; want NotImplementedError for EmitAudit", err)
	}
	if len(frames.sent) != 0 {
		t.Fatalf("EmitAudit sent %d frames; want none", len(frames.sent))
	}
}

// TestFrameTransport_QueryAuditFramesTheTenantAndReplicatedFlag proves the
// filter goes out under the server's field names and the page comes back.
func TestFrameTransport_QueryAuditFramesTheTenantAndReplicatedFlag(t *testing.T) {
	frames := newRecordingFrames()
	frames.answers["audit.Query"] = []byte(`{}`)
	tr := NewFrameTransport("", frames.send)

	page, err := tr.QueryAudit(context.Background(), transport.AuditFilter{TenantID: "acme", Replicated: true})
	if err != nil || page == nil {
		t.Fatalf("QueryAudit = %v, %v; want a page", page, err)
	}
	var sent map[string]any
	if err := json.Unmarshal(frames.sent["audit.Query"], &sent); err != nil {
		t.Fatal(err)
	}
	if sent["tenant_id"] != "acme" || sent["replicated"] != true {
		t.Fatalf("audit.Query payload = %v; want tenant_id acme and replicated true", sent)
	}
}

// TestFrameTransport_QueryAuditReportsFailures proves a sender failure and an
// undecodable page are RequestErrors naming QueryAudit.
func TestFrameTransport_QueryAuditReportsFailures(t *testing.T) {
	frames := newRecordingFrames()
	frames.answers["audit.Query"] = []byte(`{not json`)
	_, err := NewFrameTransport("", frames.send).QueryAudit(context.Background(), transport.AuditFilter{})
	requireQUICRequestError(t, err, "QueryAudit")
	if !errors.As(err, new(*UnmarshalError)) {
		t.Fatalf("QueryAudit = %v; want an UnmarshalError", err)
	}

	failing := NewFrameTransport("", func(context.Context, string, string, []byte) (uint16, []byte, error) {
		return 0, nil, failure{}
	})
	_, err = failing.QueryAudit(context.Background(), transport.AuditFilter{})
	requireQUICRequestError(t, err, "QueryAudit")
	if !errors.As(err, new(failure)) {
		t.Fatalf("QueryAudit = %v; want the sender's failure", err)
	}
}

// TestFrameTransport_VerifyReplicatedChainDecodesTheVerdict proves the tenant
// is framed and the continuity verdict decoded.
func TestFrameTransport_VerifyReplicatedChainDecodesTheVerdict(t *testing.T) {
	frames := newRecordingFrames()
	frames.answers["audit.VerifyReplicatedChain"] = []byte(`{"verified":false,"reason":"gap at 4","records":3,"head":5,"low":1,"genesis":true}`)
	tr := NewFrameTransport("", frames.send)

	got, err := tr.VerifyReplicatedChain(context.Background(), "acme")
	if err != nil {
		t.Fatalf("VerifyReplicatedChain: %v", err)
	}
	want := transport.ChainContinuityResult{Reason: "gap at 4", Records: 3, Head: 5, Low: 1, Genesis: true}
	if *got != want {
		t.Fatalf("VerifyReplicatedChain = %+v; want %+v", *got, want)
	}
	var sent map[string]string
	if err := json.Unmarshal(frames.sent["audit.VerifyReplicatedChain"], &sent); err != nil || sent["tenant_id"] != "acme" {
		t.Fatalf("payload = %v (%v); want tenant_id acme", sent, err)
	}
}

// TestFrameTransport_VerifyReplicatedChainReportsFailures proves an
// unimplemented verb is NotImplementedError, and other failures and an
// undecodable verdict are RequestErrors.
func TestFrameTransport_VerifyReplicatedChainReportsFailures(t *testing.T) {
	ctx := context.Background()
	body, err := wire.EncodeErrorPayload(wire.StatusUnimplemented, wire.ErrorPayload{Message: "no replicated chain"})
	if err != nil {
		t.Fatal(err)
	}
	unimplemented := NewFrameTransport("", func(context.Context, string, string, []byte) (uint16, []byte, error) {
		return wire.StatusUnimplemented, body, nil
	})
	var nie *transport.NotImplementedError
	if _, err := unimplemented.VerifyReplicatedChain(ctx, "acme"); !errors.As(err, &nie) || nie.Method != "VerifyReplicatedChain" {
		t.Fatalf("VerifyReplicatedChain = %v; want NotImplementedError", err)
	}

	failing := NewFrameTransport("", func(context.Context, string, string, []byte) (uint16, []byte, error) {
		return 0, nil, failure{}
	})
	_, err = failing.VerifyReplicatedChain(ctx, "acme")
	requireQUICRequestError(t, err, "VerifyReplicatedChain")

	frames := newRecordingFrames()
	frames.answers["audit.VerifyReplicatedChain"] = []byte(`[`)
	_, err = NewFrameTransport("", frames.send).VerifyReplicatedChain(ctx, "acme")
	requireQUICRequestError(t, err, "VerifyReplicatedChain")
	if !errors.As(err, new(*UnmarshalError)) {
		t.Fatalf("VerifyReplicatedChain = %v; want an UnmarshalError", err)
	}
}

// TestFrameTransport_DatabaseBarrierShareClaimCompleteDeliversTheEnvelope
// proves the claim is framed to db.BarrierShareClaimComplete and the envelope
// returned.
func TestFrameTransport_DatabaseBarrierShareClaimCompleteDeliversTheEnvelope(t *testing.T) {
	frames := newRecordingFrames()
	frames.answers["db.BarrierShareClaimComplete"] = mustMarshalPB(t, &pb.BarrierShareClaimCompleteResponse{ShareIndex: 3, Envelope: []byte("sealed")})
	tr := NewFrameTransport("", frames.send)

	resp, err := tr.DatabaseBarrierShareClaimComplete(context.Background(), &transport.DatabaseShareClaimCompleteRequest{
		Database:                  "orders",
		ShareClaimCompleteRequest: transport.ShareClaimCompleteRequest{Username: "alice", NonceHex: "beef", Signature: []byte("sig")},
	})
	if err != nil {
		t.Fatalf("DatabaseBarrierShareClaimComplete: %v", err)
	}
	if resp.ShareIndex != 3 || string(resp.Envelope) != "sealed" {
		t.Fatalf("DatabaseBarrierShareClaimComplete = %+v", resp)
	}
	var req pb.DatabaseBarrierShareClaimCompleteRequest
	mustUnmarshalPB(t, frames.sent["db.BarrierShareClaimComplete"], &req)
	c := req.GetClaim()
	if req.GetDatabase() != "orders" || c.GetUsername() != "alice" || c.GetNonceHex() != "beef" || string(c.GetSignature()) != "sig" {
		t.Fatalf("db.BarrierShareClaimComplete frame = %v", &req)
	}
}

// TestFrameTransport_DatabaseBarrierShareClaimCompleteRefusesNoDatabase
// proves a claim naming no database is refused before anything is sent.
func TestFrameTransport_DatabaseBarrierShareClaimCompleteRefusesNoDatabase(t *testing.T) {
	frames := newRecordingFrames()
	tr := NewFrameTransport("", frames.send)
	_, err := tr.DatabaseBarrierShareClaimComplete(context.Background(), &transport.DatabaseShareClaimCompleteRequest{})
	if !errors.As(err, new(*transport.DatabaseNameRequiredError)) {
		t.Fatalf("DatabaseBarrierShareClaimComplete = %v; want DatabaseNameRequiredError", err)
	}
	if len(frames.sent) != 0 {
		t.Fatalf("sent %d frames; want none", len(frames.sent))
	}
}

// TestFrameTransport_UserRoleBindingsAndFactorsCrossTheWire proves UpdateUser
// frames its role bindings and the answer's bindings and factor metadata
// decode into the user.
func TestFrameTransport_UserRoleBindingsAndFactorsCrossTheWire(t *testing.T) {
	frames := newRecordingFrames()
	frames.answers["user.Update"] = []byte(`{"id":5,"username":"bob",` +
		`"roleBindings":[{"roleID":"reader","tenantID":"acme"}],` +
		`"factors":[{"id":"f1","factorName":"fido2","createdAt":"2026-10-24T00:00:00Z"}]}`)
	tr := NewFrameTransport("", frames.send)

	user, err := tr.UpdateUser(context.Background(), &transport.UpdateUserRequest{
		ID:           5,
		RoleBindings: []transport.RoleBinding{{RoleID: "writer", TenantID: "acme"}},
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	if len(user.RoleBindings) != 1 || user.RoleBindings[0] != (transport.RoleBinding{RoleID: "reader", TenantID: "acme"}) {
		t.Fatalf("RoleBindings = %+v", user.RoleBindings)
	}
	wantFactor := transport.FactorMetadata{ID: "f1", FactorName: "fido2", CreatedAt: "2026-10-24T00:00:00Z"}
	if len(user.Factors) != 1 || user.Factors[0] != wantFactor {
		t.Fatalf("Factors = %+v; want %+v", user.Factors, wantFactor)
	}
	var sent struct {
		RoleBindings []roleBindingWire `json:"roleBindings"`
	}
	if err := json.Unmarshal(frames.sent["user.Update"], &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.RoleBindings) != 1 || sent.RoleBindings[0] != (roleBindingWire{RoleID: "writer", TenantID: "acme"}) {
		t.Fatalf("user.Update roleBindings = %+v", sent.RoleBindings)
	}
}

// TestFrameTransport_UpdateUserKeepsAbsentRoleBindingsAbsent proves an
// UpdateUser with no role bindings sends null, which leaves them alone, and a
// non-nil empty slice sends [], which clears them.
func TestFrameTransport_UpdateUserKeepsAbsentRoleBindingsAbsent(t *testing.T) {
	for name, tc := range map[string]struct {
		bindings []transport.RoleBinding
		want     string
	}{
		"absent": {nil, "null"},
		"clear":  {[]transport.RoleBinding{}, "[]"},
	} {
		t.Run(name, func(t *testing.T) {
			frames := newRecordingFrames()
			frames.answers["user.Update"] = []byte(`{"id":5}`)
			tr := NewFrameTransport("", frames.send)
			if _, err := tr.UpdateUser(context.Background(), &transport.UpdateUserRequest{ID: 5, RoleBindings: tc.bindings}); err != nil {
				t.Fatalf("UpdateUser: %v", err)
			}
			var sent map[string]json.RawMessage
			if err := json.Unmarshal(frames.sent["user.Update"], &sent); err != nil {
				t.Fatal(err)
			}
			if string(sent["roleBindings"]) != tc.want {
				t.Fatalf("roleBindings = %s; want %s", sent["roleBindings"], tc.want)
			}
		})
	}
}

// notFoundFrames answers every frame with a wire not-found.
func notFoundFrames(t *testing.T) FrameSender {
	t.Helper()
	body, err := wire.EncodeErrorPayload(wire.StatusNotFound, wire.ErrorPayload{Message: "lease not found"})
	if err != nil {
		t.Fatal(err)
	}
	return func(context.Context, string, string, []byte) (uint16, []byte, error) {
		return wire.StatusNotFound, body, nil
	}
}

// TestFrameTransport_ExecuteBatchNamesTheMissingLease proves a not-found
// batch names the lease its ops carried, and stays a plain not-found when
// they carried none.
func TestFrameTransport_ExecuteBatchNamesTheMissingLease(t *testing.T) {
	ctx := context.Background()
	tr := NewFrameTransport("", notFoundFrames(t))

	_, err := tr.ExecuteBatch(ctx, []transport.Op{{Key: []byte("a")}, {Key: []byte("b"), LeaseID: 42}})
	var lnf *transport.LeaseNotFoundError
	if !errors.As(err, &lnf) || lnf.LeaseID != 42 {
		t.Fatalf("ExecuteBatch = %v; want LeaseNotFoundError for lease 42", err)
	}

	_, err = tr.ExecuteBatch(ctx, []transport.Op{{Key: []byte("a")}})
	if errors.As(err, &lnf) || !isWireNotFound(err) {
		t.Fatalf("ExecuteBatch = %v; want the wire not-found unchanged", err)
	}

	failing := NewFrameTransport("", func(context.Context, string, string, []byte) (uint16, []byte, error) {
		return 0, nil, failure{}
	})
	if _, err := failing.ExecuteBatch(ctx, []transport.Op{{Key: []byte("a"), LeaseID: 42}}); !errors.As(err, new(failure)) || errors.As(err, &lnf) {
		t.Fatalf("ExecuteBatch = %v; want the sender's failure, not a lease error", err)
	}
}

// TestFrameTransport_ImportBeginReturnsTheSession proves the import options
// are framed and the session ID returned, and an undecodable answer refused.
func TestFrameTransport_ImportBeginReturnsTheSession(t *testing.T) {
	frames := newRecordingFrames()
	frames.answers["batch.ImportBegin"] = mustMarshalPB(t, &pb.ImportBeginResponse{SessionId: "s-1"})
	tr := NewFrameTransport("", frames.send)

	id, err := tr.ImportBegin(context.Background(), transport.ImportOptions{Format: "csv", KeyField: "id", SkipHeader: true})
	if err != nil || id != "s-1" {
		t.Fatalf("ImportBegin = %q, %v; want s-1", id, err)
	}
	var req pb.ImportBeginRequest
	mustUnmarshalPB(t, frames.sent["batch.ImportBegin"], &req)
	if req.GetFormat() != "csv" || req.GetKeyField() != "id" || !req.GetSkipHeader() {
		t.Fatalf("batch.ImportBegin frame = %v", &req)
	}

	frames.answers["batch.ImportBegin"] = []byte{0xff, 0xff, 0xff}
	if _, err := tr.ImportBegin(context.Background(), transport.ImportOptions{}); !errors.As(err, new(*unmarshalError)) {
		t.Fatalf("ImportBegin = %v; want an unmarshal error", err)
	}
}

// TestFrameTransport_BarrierEnableEncryptionDecodesTheOutcome proves the
// request is framed and the outcome decoded, and an undecodable answer
// refused.
func TestFrameTransport_BarrierEnableEncryptionDecodesTheOutcome(t *testing.T) {
	frames := newRecordingFrames()
	frames.answers["barrier.EnableEncryption"] = mustMarshalPB(t, &pb.BarrierEnableEncryptionResponse{Status: "enabled", Scope: "tenant"})
	tr := NewFrameTransport("", frames.send)

	resp, err := tr.BarrierEnableEncryption(context.Background(), &transport.BarrierEnableEncryptionRequest{TenantID: "acme", Strategy: "shamir", ShamirThreshold: 2, ShamirTotal: 3})
	if err != nil || resp.Status != "enabled" || resp.Scope != "tenant" {
		t.Fatalf("BarrierEnableEncryption = %+v, %v", resp, err)
	}
	var req pb.BarrierEnableEncryptionRequest
	mustUnmarshalPB(t, frames.sent["barrier.EnableEncryption"], &req)
	if req.GetTenantId() != "acme" || req.GetStrategy() != "shamir" || req.GetShamirThreshold() != 2 || req.GetShamirTotal() != 3 {
		t.Fatalf("barrier.EnableEncryption frame = %v", &req)
	}

	frames.answers["barrier.EnableEncryption"] = []byte{0xff, 0xff, 0xff}
	if _, err := tr.BarrierEnableEncryption(context.Background(), &transport.BarrierEnableEncryptionRequest{}); !errors.As(err, new(*unmarshalError)) {
		t.Fatalf("BarrierEnableEncryption = %v; want an unmarshal error", err)
	}
}

// TestFrameTransport_ImportSessionCallsRefuseOnceClosed proves the import
// session calls return ClosedError without sending once closed.
func TestFrameTransport_ImportSessionCallsRefuseOnceClosed(t *testing.T) {
	ctx := context.Background()
	frames := newRecordingFrames()
	tr := NewFrameTransport("", frames.send)
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	calls := map[string]func() error{
		"ImportBegin": func() error {
			_, err := tr.ImportBegin(ctx, transport.ImportOptions{})
			return err
		},
		"ImportAppend": func() error { return tr.ImportAppend(ctx, "s", 1, []byte("x")) },
		"ImportCommit": func() error {
			_, err := tr.ImportCommit(ctx, "s")
			return err
		},
		"ImportAbort": func() error { return tr.ImportAbort(ctx, "s") },
	}
	for name, call := range calls {
		if err := call(); !errors.As(err, new(*transport.ClosedError)) {
			t.Errorf("%s = %v; want ClosedError", name, err)
		}
	}
	if len(frames.sent) != 0 {
		t.Fatalf("a closed transport sent %d frames", len(frames.sent))
	}
}

// TestFrameTransport_ImportCommitReportsFailures proves a sender failure is
// surfaced and an undecodable commit answer refused.
func TestFrameTransport_ImportCommitReportsFailures(t *testing.T) {
	ctx := context.Background()
	failing := NewFrameTransport("", func(context.Context, string, string, []byte) (uint16, []byte, error) {
		return 0, nil, failure{}
	})
	if err := failing.ImportAppend(ctx, "s", 1, []byte("x")); !errors.As(err, new(failure)) {
		t.Fatalf("ImportAppend = %v; want the sender's failure", err)
	}
	if _, err := failing.ImportCommit(ctx, "s"); !errors.As(err, new(failure)) {
		t.Fatalf("ImportCommit = %v; want the sender's failure", err)
	}
	frames := newRecordingFrames()
	frames.answers["batch.ImportCommit"] = []byte{0xff, 0xff, 0xff}
	if _, err := NewFrameTransport("", frames.send).ImportCommit(ctx, "s"); !errors.As(err, new(*unmarshalError)) {
		t.Fatalf("ImportCommit = %v; want an unmarshal error", err)
	}
}
