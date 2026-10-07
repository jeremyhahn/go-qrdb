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

package embedded

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// nonReplicaRegistry resolves every database the way the registry of a node
// that holds no replica of it does: with the placement's refusal. A database
// in served resolves to its store instead, and one in missing is not found.
type nonReplicaRegistry struct {
	service.Registry
	served  map[string]*mockKVService
	missing map[string]bool
}

func (r *nonReplicaRegistry) KVService(tenantID, db string) (service.KVServicer, error) {
	if s, ok := r.served[db]; ok {
		return s, nil
	}
	if r.missing[db] {
		return nil, &service.DatabaseNotFoundError{TenantID: tenantID, Database: db}
	}
	return nil, &placement.NotAReplicaError{TenantID: tenantID, Database: db, ShardID: 9, NodeID: "node-c",
		LeaderReplicaID: 1, Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "node-a"}}}
}

// forwardCall is one frame handed to the forwarder.
type forwardCall struct {
	caller           *auth.Identity
	method, database string
	payload          []byte
	refusal          *placement.NotAReplicaError
}

// leaderForwardCall records one ForwardToLeader call.
type leaderForwardCall struct {
	caller           *auth.Identity
	method, database string
	payload          []byte
	leaderAddr       string
}

// recordingForwarder answers every frame with status and body, or forwards
// nothing when refuse is set, and records what it was handed.
type recordingForwarder struct {
	status      uint16
	body        []byte
	refuse      bool
	calls       []forwardCall
	leaderCalls []leaderForwardCall
}

func (f *recordingForwarder) Forward(_ context.Context, caller *auth.Identity, method, database string, payload []byte, refusal *placement.NotAReplicaError) (uint16, []byte, bool) {
	f.calls = append(f.calls, forwardCall{caller, method, database, payload, refusal})
	if f.refuse {
		return 0, nil, false
	}
	return f.status, f.body, true
}

func (f *recordingForwarder) ForwardToLeader(_ context.Context, caller *auth.Identity, method, database string, payload []byte, leaderAddr string) (uint16, []byte, bool) {
	f.leaderCalls = append(f.leaderCalls, leaderForwardCall{caller, method, database, payload, leaderAddr})
	if f.refuse {
		return 0, nil, false
	}
	return f.status, f.body, true
}

// fixedCaller authenticates id, or fails with err.
type fixedCaller struct {
	id  *auth.Identity
	err error
}

func (c fixedCaller) Authenticate(context.Context) (*auth.Identity, error) { return c.id, c.err }
func (fixedCaller) Name() string                                           { return "fixed" }

var alice = &auth.Identity{Subject: "alice", TenantID: "acme"}

// forwardingTransport returns the acme-scoped "orders" transport of a node
// that holds no replica of it, forwarding through fwd as caller.
func forwardingTransport(t *testing.T, reg *nonReplicaRegistry, fwd ReplicaForwarder, caller auth.Authenticator) transport.Transport {
	t.Helper()
	tr, err := New(newMockKVService(), &mockAdminService{}, WithServiceRegistry(reg), WithReplicaForwarding(fwd, caller))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	scoped, err := tr.WithDatabase("orders")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	return scoped
}

func acmeCtx() context.Context { return tenant.WithTenantID(context.Background(), "acme") }

func isRefusal(err error) bool {
	var r *placement.NotAReplicaError
	return errors.As(err, &r)
}

// A write the node refuses is sent to a replica as the request frame the
// caller would have sent there, signed for the configured caller.
func TestForwardingScope_ForwardsTheRefusedWriteAsTheConfiguredCaller(t *testing.T) {
	fwd := &recordingForwarder{status: wire.StatusOK}
	kv := forwardingTransport(t, &nonReplicaRegistry{}, fwd, fixedCaller{id: alice}).(transport.KVClient)
	if err := kv.Put(acmeCtx(), "k1", []byte("v1")); err != nil {
		t.Fatalf("Put = %v; want the replica's answer", err)
	}
	if len(fwd.calls) != 1 {
		t.Fatalf("forwarded %d frames; want 1", len(fwd.calls))
	}
	c := fwd.calls[0]
	var req pb.PutRequest
	if err := proto.Unmarshal(c.payload, &req); err != nil || req.GetKey() != "k1" || string(req.GetValue()) != "v1" {
		t.Fatalf("payload = %v (%v); want the PutRequest for k1", &req, err)
	}
	if c.method != "kv.Put" || c.database != "orders" || c.caller != alice || c.refusal == nil || c.refusal.Database != "orders" {
		t.Fatalf("forwarded %s on %q as %+v with refusal %+v; want kv.Put on orders as alice with the node's refusal", c.method, c.database, c.caller, c.refusal)
	}
}

// With an AuthMiddleware installed, the embedded transport authorizes the
// identity the call's context carries before it reaches the node, so a call
// whose context carries none is refused as unauthenticated and nothing is
// forwarded -- the configured caller is who a forward is made as, never a
// stand-in for the in-process caller. A call whose context carries an
// identity the node admits is forwarded as the configured caller.
func TestForwardingScope_AuthorizesTheContextsIdentityBeforeForwarding(t *testing.T) {
	mw := authmw.New(rbac.New(authtest.BuiltInRoles(t, "acme")))
	fwd := &recordingForwarder{status: wire.StatusOK}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithServiceRegistry(&nonReplicaRegistry{}),
		WithReplicaForwarding(fwd, fixedCaller{id: alice}), WithAuthMiddleware(mw))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	scoped, err := tr.WithDatabase("orders")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	kv := scoped.(transport.KVClient)

	if err := kv.Put(acmeCtx(), "k1", []byte("v1")); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("Put with no identity = %v; want auth.ErrUnauthenticated", err)
	}
	if len(fwd.calls) != 0 {
		t.Fatalf("forwarded %d frames for an unauthenticated call; want none", len(fwd.calls))
	}

	app := identityCtx("acme", role.SystemRoleID("acme", role.RoleNameOperator))
	if err := kv.Put(app, "k1", []byte("v1")); err != nil {
		t.Fatalf("Put as an admitted identity = %v; want the replica's answer", err)
	}
	if len(fwd.calls) != 1 || fwd.calls[0].caller != alice {
		t.Fatalf("forwarded %+v; want one frame, as the configured caller", fwd.calls)
	}
}

// A read returns the replica's value, and a replica's error status is the
// call's error.
func TestForwardingScope_ReturnsTheReplicasAnswer(t *testing.T) {
	body, err := proto.Marshal(&pb.GetResponse{Value: []byte("remote")})
	if err != nil {
		t.Fatal(err)
	}
	kv := forwardingTransport(t, &nonReplicaRegistry{}, &recordingForwarder{status: wire.StatusOK, body: body}, fixedCaller{id: alice}).(transport.KVClient)
	if v, err := kv.Get(acmeCtx(), "k1"); err != nil || string(v) != "remote" {
		t.Fatalf("Get = %q, %v; want the replica's value", v, err)
	}

	denied, err := wire.EncodeErrorPayload(wire.StatusPermissionDenied, wire.ErrorPayload{Message: "kv:write denied"})
	if err != nil {
		t.Fatal(err)
	}
	kv = forwardingTransport(t, &nonReplicaRegistry{}, &recordingForwarder{status: wire.StatusPermissionDenied, body: denied}, fixedCaller{id: alice}).(transport.KVClient)
	err = kv.Delete(acmeCtx(), "k1")
	var reqErr *transport.RequestError
	if !errors.As(err, &reqErr) || reqErr.StatusCode != int(wire.StatusPermissionDenied) {
		t.Fatalf("Delete = %v; want the replica's permission denial", err)
	}
}

// Every case a call is not forwarded in answers the node's own result: the
// refusal when there is no caller to forward as, the caller is another
// tenant's, or no replica served it; and every other answer as it is.
func TestForwardingScope_AnswersTheNodesResultWhenNotForwarded(t *testing.T) {
	for name, tc := range map[string]struct {
		reg     *nonReplicaRegistry
		fwd     *recordingForwarder
		caller  auth.Authenticator
		check   func(error) bool
		reached bool
	}{
		"the caller does not authenticate": {fwd: &recordingForwarder{}, caller: fixedCaller{err: auth.ErrUnauthenticated}, check: isRefusal},
		"the caller authenticates no one":  {fwd: &recordingForwarder{}, caller: fixedCaller{}, check: isRefusal},
		"the caller is another tenant's": {fwd: &recordingForwarder{}, caller: fixedCaller{id: &auth.Identity{Subject: "bob", TenantID: "globex"}},
			check: isRefusal},
		"no replica served it": {fwd: &recordingForwarder{refuse: true}, caller: fixedCaller{id: alice}, check: isRefusal, reached: true},
		"another refusal": {reg: &nonReplicaRegistry{missing: map[string]bool{"orders": true}}, fwd: &recordingForwarder{}, caller: fixedCaller{id: alice},
			check: func(err error) bool { return err != nil && !isRefusal(err) }},
		"served by the node": {reg: &nonReplicaRegistry{served: map[string]*mockKVService{"orders": newMockKVService()}}, fwd: &recordingForwarder{},
			caller: fixedCaller{id: alice}, check: func(err error) bool { return err == nil }},
	} {
		t.Run(name, func(t *testing.T) {
			reg := tc.reg
			if reg == nil {
				reg = &nonReplicaRegistry{}
			}
			kv := forwardingTransport(t, reg, tc.fwd, tc.caller).(transport.KVClient)
			err := kv.Put(acmeCtx(), "k1", []byte("v1"))
			if !tc.check(err) {
				t.Fatalf("Put = %v; want the node's own result", err)
			}
			if reached := len(tc.fwd.calls) > 0; reached != tc.reached {
				t.Fatalf("forwarder reached = %v; want %v", reached, tc.reached)
			}
		})
	}
}

// Without both a forwarder and a caller nothing is forwarded: the scoped
// transport is the plain one, and a refused call is the refusal.
func TestWithReplicaForwarding_NeedsAForwarderAndACaller(t *testing.T) {
	for name, opt := range map[string]Option{
		"no forwarder": WithReplicaForwarding(nil, fixedCaller{id: alice}),
		"no caller":    WithReplicaForwarding(&recordingForwarder{}, nil),
	} {
		t.Run(name, func(t *testing.T) {
			tr, err := New(newMockKVService(), &mockAdminService{}, WithServiceRegistry(&nonReplicaRegistry{}), opt)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			scoped, err := tr.WithDatabase("orders")
			if err != nil {
				t.Fatalf("WithDatabase: %v", err)
			}
			if _, ok := scoped.(*Transport); !ok {
				t.Fatalf("scoped transport is %T; want the plain *Transport", scoped)
			}
			if err := scoped.(*Transport).Put(acmeCtx(), "k1", []byte("v1")); !isRefusal(err) {
				t.Fatalf("Put = %v; want the refusal", err)
			}
		})
	}
}

// A forwarding scope re-scoped to another database still forwards, to that
// database.
func TestForwardingScope_ReScopingKeepsForwarding(t *testing.T) {
	fwd := &recordingForwarder{status: wire.StatusOK}
	scoped := forwardingTransport(t, &nonReplicaRegistry{}, fwd, fixedCaller{id: alice})
	again, err := scoped.(transport.DatabaseScoper).WithDatabase("billing")
	if err != nil {
		t.Fatalf("WithDatabase: %v", err)
	}
	if err := again.(transport.KVClient).Put(acmeCtx(), "k1", []byte("v1")); err != nil {
		t.Fatalf("Put = %v", err)
	}
	if len(fwd.calls) != 1 || fwd.calls[0].database != "billing" {
		t.Fatalf("forwarded %+v; want one frame on billing", fwd.calls)
	}
}

// TestTransport_CeremonyInitialize_ForwardsOnAdminCredentialNotLeaderError is
// the node-parity regression guard (owner ruling 2026-10-06): the admin-
// credential step of CeremonyInitialize must run on the system shard's
// leader (service.CeremonyServiceWithLeaderGate); when this embedding is
// configured with a caller and forwarder (WithReplicaForwarding), it
// forwards the identical request to the leader over QUIC -- the same way a
// database-scoped embedded call forwards a per-database refusal -- rather
// than returning the node's own refusal to the embedder.
func TestTransport_CeremonyInitialize_ForwardsOnAdminCredentialNotLeaderError(t *testing.T) {
	body, err := proto.Marshal(&pb.CeremonyInitializeResponse{SpkiPin: "leader-answered"})
	if err != nil {
		t.Fatal(err)
	}
	fwd := &recordingForwarder{status: wire.StatusOK, body: body}
	ceremonySvc := &mockCeremonyService{initFn: func(context.Context, ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
		return nil, &service.AdminCredentialNotLeaderError{LeaderAddress: "raft-leader"}
	}}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithCeremonyService(ceremonySvc), WithReplicaForwarding(fwd, fixedCaller{id: alice}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	resp, err := tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{})
	if err != nil {
		t.Fatalf("CeremonyInitialize = %v; want the leader's answer", err)
	}
	if resp.SPKIPin != "leader-answered" {
		t.Fatalf("SPKIPin = %q; want the leader's", resp.SPKIPin)
	}
	if len(fwd.leaderCalls) != 1 {
		t.Fatalf("leaderCalls = %+v; want exactly one", fwd.leaderCalls)
	}
	if c := fwd.leaderCalls[0]; c.leaderAddr != "raft-leader" || c.caller != alice {
		t.Fatalf("leaderCalls[0] = %+v; want raft-leader as alice", c)
	}
}

// TestTransport_CeremonyInitialize_NoForwardingConfiguredAnswersTheRefusal
// proves a node with no caller/forwarder configured (WithReplicaForwarding
// never called, the default) answers its own refusal rather than panicking
// or silently swallowing the leader-only error.
func TestTransport_CeremonyInitialize_NoForwardingConfiguredAnswersTheRefusal(t *testing.T) {
	ceremonySvc := &mockCeremonyService{initFn: func(context.Context, ceremony.CeremonyCA) (*service.CeremonyInitResult, error) {
		return nil, &service.AdminCredentialNotLeaderError{LeaderAddress: "raft-leader"}
	}}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithCeremonyService(ceremonySvc))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = tr.CeremonyInitialize(context.Background(), &transport.CeremonyInitializeRequest{})
	var notLeader *service.AdminCredentialNotLeaderError
	if !errors.As(err, &notLeader) {
		t.Fatalf("CeremonyInitialize = %v; want the node's own AdminCredentialNotLeaderError", err)
	}
}
