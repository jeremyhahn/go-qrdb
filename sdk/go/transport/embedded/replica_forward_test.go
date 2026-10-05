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
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
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

// recordingForwarder answers every frame with status and body, or forwards
// nothing when refuse is set, and records what it was handed.
type recordingForwarder struct {
	status uint16
	body   []byte
	refuse bool
	calls  []forwardCall
}

func (f *recordingForwarder) Forward(_ context.Context, caller *auth.Identity, method, database string, payload []byte, refusal *placement.NotAReplicaError) (uint16, []byte, bool) {
	f.calls = append(f.calls, forwardCall{caller, method, database, payload, refusal})
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
