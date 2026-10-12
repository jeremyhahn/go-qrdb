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
	"crypto/tls"
	"errors"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward/replicaforwardtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// quicSeen is what a fake target saw of one forwarded request.
type quicSeen struct {
	method, database, subject string
	payload                   []byte
	verifyErr                 error
}

// quicTarget is a replica's QUIC listener. It reads the forwarded envelope
// and verifies its assertion as the auth middleware binds it -- the inner
// method and payload, presented by a cluster peer -- then answers with
// answer: a frame, or nothing at all (the connection is dropped).
type quicTarget struct {
	addr     string
	raftAddr string
	calls    atomic.Int32
	answer   func(ctx context.Context) (status uint16, body []byte, drop bool)
	// stop ends the listener and its socket, as the process dying does.
	stop func()

	mu   sync.Mutex
	seen []quicSeen
}

func startQUICTarget(t *testing.T, ca *replicaforwardtest.CA, nodeID, raftAddr string) *quicTarget {
	t.Helper()
	return startQUICTargetTLS(t, ca.TLS(t, nodeID), raftAddr)
}

// startQUICTargetTLS starts a target serving cfg.
func startQUICTargetTLS(t *testing.T, cfg *tls.Config, raftAddr string) *quicTarget {
	t.Helper()
	cfg.NextProtos = []string{wire.ProtocolV3}
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	tr := &quicgo.Transport{Conn: udp}
	ln, err := tr.Listen(cfg, &quicgo.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Closing the socket first, as a process dying does, sends the
	// connections nothing: a peer learns of it only from the kernel.
	stop := func() { cancel(); _ = udp.Close(); _ = tr.Close() }
	t.Cleanup(stop)
	qt := &quicTarget{addr: ln.Addr().String(), raftAddr: raftAddr, stop: stop}
	go func() {
		for {
			conn, err := ln.Accept(ctx)
			if err != nil {
				return
			}
			go qt.serve(ctx, conn)
		}
	}()
	return qt
}

func (qt *quicTarget) serve(ctx context.Context, conn *quicgo.Conn) {
	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			return
		}
		qt.calls.Add(1)
		method, database, rid, payload, err := wire.ReadRequest(stream)
		if err != nil {
			return
		}
		seen := quicSeen{method: method, database: database}
		env, derr := wire.DecodeOnBehalf(payload)
		seen.verifyErr = derr
		if derr == nil {
			seen.payload = env.Payload
			a, verr := forward.VerifyBinding(env.Assertion, env.Signature, conn.ConnectionState().TLS.PeerCertificates[0], "", time.Now(),
				forward.RPCBinding("quic", env.Method, env.Payload))
			seen.verifyErr = verr
			if a != nil {
				seen.method, seen.subject = env.Method, a.Subject
			}
		}
		qt.mu.Lock()
		qt.seen = append(qt.seen, seen)
		qt.mu.Unlock()
		st, body := wire.StatusOK, []byte("served by "+qt.raftAddr)
		if seen.verifyErr != nil {
			st, body = errorFrame(wire.StatusUnauthenticated, seen.verifyErr)
		} else if qt.answer != nil {
			var drop bool
			if st, body, drop = qt.answer(ctx); drop {
				_ = conn.CloseWithError(1, "dropped")
				return
			}
		}
		_ = wire.WriteResponse(stream, rid, st, body)
		_ = stream.Close()
	}
}

func (qt *quicTarget) lastSeen(t *testing.T) quicSeen {
	t.Helper()
	qt.mu.Lock()
	defer qt.mu.Unlock()
	if len(qt.seen) == 0 {
		t.Fatal("the target saw no request")
	}
	return qt.seen[len(qt.seen)-1]
}

// quicFwdCluster is an ingress node holding no replica -- a real listener
// whose KV refuses every write as a non-replica -- in front of the replicas.
type quicFwdCluster struct {
	cache  *replicaforward.LeaderCache
	srv    *Server
	client *tls.Config
}

func newQUICFwdCluster(t *testing.T, ca *replicaforwardtest.CA, timeout time.Duration, targets ...*quicTarget) *quicFwdCluster {
	t.Helper()
	c := &quicFwdCluster{cache: replicaforward.NewLeaderCache()}
	refusal := &placement.NotAReplicaError{TenantID: "acme", Database: "orders", ShardID: 42, NodeID: "node-a", LeaderReplicaID: 1}
	var members replicaforwardtest.Members
	for i, qt := range targets {
		refusal.Replicas = append(refusal.Replicas, placement.Replica{ReplicaID: uint64(i + 1), NodeID: "node-" + strconv.Itoa(i+1), RaftAddr: qt.raftAddr})
		members = append(members, cluster.Member{NodeID: uint64(i + 1), Address: qt.raftAddr,
			APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolQUIC: qt.addr}})
	}
	caller := &auth.Identity{Subject: "alice", TenantID: "acme"}
	mw := authmw.New(&noopAuthorizer{}, authmw.WithAuthenticator(authtest.NewFixedAuthenticator(caller)))
	kv := &mockKVService{putFn: func(context.Context, string, []byte) error { return refusal }}
	c.srv = NewServer(kv, &mockAdminService{}, "127.0.0.1:0", WithTLS(ca.TLS(t, "node-a")), WithAuthMiddleware(mw),
		WithReplicaForwarding(replicaforward.Config{Locator: replicaforward.NewLocator(members, c.cache, "raft-a", nil), RootCAs: ca.Pool, Timeout: timeout}))
	if err := c.srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.srv.Stop)
	c.client = ca.TLS(t, "")
	c.client.NextProtos = []string{wire.ProtocolV3}
	return c
}

// put sends a kv.Put for key=value on database orders through the ingress
// node, as method (kv.Put, or an envelope to look forwarded).
func (c *quicFwdCluster) put(t *testing.T, method string, payload []byte) (uint16, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := quicgo.DialAddr(ctx, c.srv.Addr(), c.client, &quicgo.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseWithError(0, "") }()
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.WriteRequest(stream, method, "orders", 7, payload); err != nil {
		t.Fatal(err)
	}
	rid, st, body, err := wire.ReadResponse(stream)
	_ = stream.Close()
	if err != nil || rid != 7 {
		t.Fatalf("response %d, %v; want request 7's", rid, err)
	}
	return st, body
}

func putPayload(t *testing.T) []byte {
	t.Helper()
	b, err := proto.Marshal(&pb.PutRequest{Key: "k1", Value: []byte("v1")})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestQUIC_CeremonyInitialize_ForwardsOnAdminCredentialNotLeaderError is the
// node-parity regression guard (owner ruling 2026-10-06): the admin-
// credential step of ceremony.Initialize must run on the system shard's
// leader (service.CeremonyServiceWithLeaderGate); a follower forwards the
// identical request to the leader through the SAME FrameForwarder the
// per-database NotAReplicaError path uses, rather than answering its own
// refusal.
// fixedErrCeremonyService is a service.CeremonyServicer double local to this
// file (unconstrained by build tags, unlike mocks_svc_test.go's
// mockCeremonyService) that fails every call with a fixed error.
type fixedErrCeremonyService struct{ err error }

func (f fixedErrCeremonyService) CeremonyInitialize(context.Context, ceremony.CeremonyCA, *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	return nil, f.err
}

func (f fixedErrCeremonyService) CeremonySignCSR(context.Context, string, string, string, string) (*service.CeremonySignCSRResult, error) {
	return nil, f.err
}

func (f fixedErrCeremonyService) CeremonyStatus(context.Context) (*service.CeremonyStatusInfo, error) {
	return nil, f.err
}

func TestQUIC_CeremonyInitialize_ForwardsOnAdminCredentialNotLeaderError(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	leader := startQUICTarget(t, ca, "node-1", "raft-leader")
	leader.answer = func(context.Context) (uint16, []byte, bool) {
		body, err := proto.Marshal(&pb.CeremonyInitializeResponse{SpkiPin: "leader-answered"})
		if err != nil {
			t.Fatal(err)
		}
		return wire.StatusOK, body, false
	}

	cache := replicaforward.NewLeaderCache()
	members := replicaforwardtest.Members{
		{NodeID: 1, Address: "raft-leader", APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolQUIC: leader.addr}},
	}
	caller := &auth.Identity{Subject: "alice", TenantID: "acme"}
	mw := authmw.New(&noopAuthorizer{}, authmw.WithAuthenticator(authtest.NewFixedAuthenticator(caller)))
	ceremonySvc := fixedErrCeremonyService{err: &service.AdminCredentialNotLeaderError{LeaderAddress: "raft-leader"}}
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithTLS(ca.TLS(t, "node-a")), WithAuthMiddleware(mw), WithCeremonyService(ceremonySvc),
		WithReplicaForwarding(replicaforward.Config{Locator: replicaforward.NewLocator(members, cache, "raft-a", nil), RootCAs: ca.Pool, Timeout: 5 * time.Second}))
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)

	client := ca.TLS(t, "")
	client.NextProtos = []string{wire.ProtocolV3}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := quicgo.DialAddr(ctx, srv.Addr(), client, &quicgo.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseWithError(0, "") }()
	stream, err := conn.OpenStreamSync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reqPayload, err := proto.Marshal(&pb.CeremonyInitializeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if err := wire.WriteRequest(stream, "ceremony.Initialize", "", 7, reqPayload); err != nil {
		t.Fatal(err)
	}
	rid, st, body, err := wire.ReadResponse(stream)
	_ = stream.Close()
	if err != nil || rid != 7 {
		t.Fatalf("response %d, %v; want request 7's", rid, err)
	}
	if st != wire.StatusOK {
		t.Fatalf("status = %d, body %s; want %d (the leader's answer)", st, body, wire.StatusOK)
	}
	var resp pb.CeremonyInitializeResponse
	if err := proto.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.GetSpkiPin() != "leader-answered" {
		t.Fatalf("SpkiPin = %q; want the leader's answer", resp.GetSpkiPin())
	}
	seen := leader.lastSeen(t)
	if seen.verifyErr != nil || seen.subject != "alice" {
		t.Fatalf("leader saw %+v; want a verified assertion for alice", seen)
	}
}

// refusalOf decodes an answer frame as the NOT_A_REPLICA refusal.
func refusalOf(t *testing.T, st uint16, body []byte) *placement.NotAReplicaError {
	t.Helper()
	p, err := wire.DecodeErrorPayload(body)
	if st != wire.StatusUnavailable || err != nil || p.Info == nil || p.Info.Reason != replicaforward.ReasonNotAReplica {
		t.Fatalf("answer %s %+v (%v); want the NOT_A_REPLICA refusal", wire.StatusName(st), p, err)
	}
	refusal, err := replicaforward.ParseNotAReplica(p.Info.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	return refusal
}

func TestReplicaForwardQUIC_ForwardsAsTheCallerAndRelaysTheAnswer(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startQUICTarget(t, ca, "node-1", "raft-b")
	c := newQUICFwdCluster(t, ca, 5*time.Second, b)

	payload := putPayload(t)
	st, body := c.put(t, "kv.Put", payload)
	if st != wire.StatusOK || string(body) != "served by raft-b" {
		t.Fatalf("answer %s %q; want the replica's", wire.StatusName(st), body)
	}
	seen := b.lastSeen(t)
	if seen.verifyErr != nil || seen.subject != "alice" || seen.method != "kv.Put" || seen.database != "orders" || !proto.Equal(mustPut(t, seen.payload), mustPut(t, payload)) {
		t.Fatalf("target saw %+v; want alice's kv.Put on orders under a verified assertion", seen)
	}
	if l, ok := c.cache.Get(42); !ok || l.RaftAddr != "raft-b" {
		t.Fatalf("cache = %+v, %v; want the replica that served", l, ok)
	}
}

func mustPut(t *testing.T, b []byte) *pb.PutRequest {
	t.Helper()
	var req pb.PutRequest
	if err := proto.Unmarshal(b, &req); err != nil {
		t.Fatal(err)
	}
	return &req
}

// TestReplicaForwardQUIC_AForwardedRequestIsNeverForwardedAgain: an
// envelope is served as its inner method and never forwarded; with no
// verifiable assertion it is not even served.
func TestReplicaForwardQUIC_AForwardedRequestIsNeverForwardedAgain(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startQUICTarget(t, ca, "node-1", "raft-b")
	c := newQUICFwdCluster(t, ca, 5*time.Second, b)

	env, err := wire.EncodeOnBehalf(wire.OnBehalf{Method: "kv.Put", Assertion: "a", Signature: "s", Payload: putPayload(t)})
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := c.put(t, wire.MethodOnBehalf, env); st != wire.StatusUnauthenticated || b.calls.Load() != 0 {
		t.Fatalf("a forged envelope = %s (%d target calls); want unauthenticated and nothing forwarded", wire.StatusName(st), b.calls.Load())
	}
	if st, _ := c.put(t, wire.MethodOnBehalf, []byte{0xff}); st != wire.StatusInvalidArgument {
		t.Fatalf("a malformed envelope = %s; want invalid argument", wire.StatusName(st))
	}
}

func TestReplicaForwardQUIC_NotLeaderRetriesTheNamedLeader(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startQUICTarget(t, ca, "node-1", "raft-b")
	cc := startQUICTarget(t, ca, "node-2", "raft-c")
	b.answer = func(context.Context) (uint16, []byte, bool) {
		st, body := mapServiceError(&service.NotLeaderError{ShardID: 42, LeaderID: 2, LeaderAddress: "raft-c"})
		return st, body, false
	}
	c := newQUICFwdCluster(t, ca, 5*time.Second, b, cc)

	if st, body := c.put(t, "kv.Put", putPayload(t)); st != wire.StatusOK || string(body) != "served by raft-c" {
		t.Fatalf("answer %s %q; want the named leader's", wire.StatusName(st), body)
	}
	if b.calls.Load() != 1 || cc.calls.Load() != 1 {
		t.Fatalf("calls b=%d c=%d; want one each", b.calls.Load(), cc.calls.Load())
	}
}

func TestReplicaForwardQUIC_UnreachableRetriesTheNextReplica(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	dead := &quicTarget{raftAddr: "raft-b", addr: replicaforwardtest.ClosedAddr(t)}
	cc := startQUICTarget(t, ca, "node-2", "raft-c")
	c := newQUICFwdCluster(t, ca, 2*time.Second, dead, cc)

	if st, body := c.put(t, "kv.Put", putPayload(t)); st != wire.StatusOK || string(body) != "served by raft-c" {
		t.Fatalf("answer %s %q; want the next replica's", wire.StatusName(st), body)
	}
}

// TestReplicaForwardQUIC_ADeadTargetsConnectionIsNotReused: once the target
// a cached connection leads to is gone, the connection closes on its own
// within a keep-alive, and the next forward finds the target unreachable --
// the request never left this node -- and goes to the next replica.
func TestReplicaForwardQUIC_ADeadTargetsConnectionIsNotReused(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startQUICTarget(t, ca, "node-1", "raft-b")
	cc := startQUICTarget(t, ca, "node-2", "raft-c")
	c := newQUICFwdCluster(t, ca, 5*time.Second, b, cc)
	if st, _ := c.put(t, "kv.Put", putPayload(t)); st != wire.StatusOK {
		t.Fatalf("first forward = %s", wire.StatusName(st))
	}
	if _, ok := c.srv.forwarder.cached(b.addr); !ok {
		t.Fatal("the connection to the target that served was not kept")
	}

	b.stop()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok := c.srv.forwarder.cached(b.addr); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the connection to a dead target stayed open")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st, body := c.put(t, "kv.Put", putPayload(t)); st != wire.StatusOK || string(body) != "served by raft-c" {
		t.Fatalf("forward after the target died = %s %q; want the next replica's", wire.StatusName(st), body)
	}
}

func TestReplicaForwardQUIC_AFailureAfterSendingIsNotRetried(t *testing.T) {
	for name, answer := range map[string]func(ctx context.Context) (uint16, []byte, bool){
		"connection dropped": func(context.Context) (uint16, []byte, bool) { return 0, nil, true },
		"timeout": func(ctx context.Context) (uint16, []byte, bool) {
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
			}
			return wire.StatusOK, nil, false
		},
	} {
		t.Run(name, func(t *testing.T) {
			ca := replicaforwardtest.NewCA(t)
			b := startQUICTarget(t, ca, "node-1", "raft-b")
			cc := startQUICTarget(t, ca, "node-2", "raft-c")
			b.answer = answer
			c := newQUICFwdCluster(t, ca, 400*time.Millisecond, b, cc)

			st, body := c.put(t, "kv.Put", putPayload(t))
			if p, _ := wire.DecodeErrorPayload(body); st != wire.StatusUnavailable || (p.Info != nil && p.Info.Reason == replicaforward.ReasonNotAReplica) {
				t.Fatalf("answer %s %+v; want the failure, not the refusal", wire.StatusName(st), p)
			}
			if cc.calls.Load() != 0 {
				t.Fatalf("a request that may have applied was sent again (%d)", cc.calls.Load())
			}
		})
	}
}

func TestReplicaForwardQUIC_TheTargetsErrorIsTheAnswer(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startQUICTarget(t, ca, "node-1", "raft-b")
	b.answer = func(context.Context) (uint16, []byte, bool) {
		st, body := mapServiceError(&service.KeyNotFoundError{Key: "k1"})
		return st, body, false
	}
	c := newQUICFwdCluster(t, ca, 5*time.Second, b)
	if st, _ := c.put(t, "kv.Put", putPayload(t)); st != wire.StatusNotFound {
		t.Fatalf("answer %s; want the replica's not found", wire.StatusName(st))
	}
}

// A replica whose first handshakes fail, as one restarting or loaded does,
// is reconnected to until one passes: its answer is the answer, not this
// node's refusal.
func TestReplicaForwardQUIC_AReconnectingReplicaIsWaitedOn(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	refusing, offered := replicaforwardtest.RefuseHandshakes(ca.TLS(t, "node-1"), 2)
	b := startQUICTargetTLS(t, refusing, "raft-b")
	b.answer = func(context.Context) (uint16, []byte, bool) {
		st, body := mapServiceError(&service.KeyNotFoundError{Key: "k1"})
		return st, body, false
	}
	c := newQUICFwdCluster(t, ca, 5*time.Second, b)
	if st, _ := c.put(t, "kv.Put", putPayload(t)); st != wire.StatusNotFound {
		t.Fatalf("answer %s; want the reconnecting replica's not found", wire.StatusName(st))
	}
	if got := offered.Load(); got != 3 {
		t.Fatalf("handshakes offered = %d; want two refused, then the one that passed", got)
	}
	if got := b.calls.Load(); got != 1 {
		t.Fatalf("replica served %d requests; want one", got)
	}
}

func TestReplicaForwardQUIC_EveryReplicaRefusingIsTheRefusal(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	refuse := func(context.Context) (uint16, []byte, bool) {
		st, body := mapServiceError(&placement.NotAReplicaError{TenantID: "acme", Database: "orders", NodeID: "node-x"})
		return st, body, false
	}
	b := startQUICTarget(t, ca, "node-1", "raft-b")
	cc := startQUICTarget(t, ca, "node-2", "raft-c")
	b.answer, cc.answer = refuse, refuse
	c := newQUICFwdCluster(t, ca, 5*time.Second, b, cc)
	st, body := c.put(t, "kv.Put", putPayload(t))
	if got := refusalOf(t, st, body); got.NodeID != "node-a" || len(got.Replicas) != 2 {
		t.Fatalf("refusal = %+v; want the ingress node's, with its replica set", got)
	}
}

// TestFrameForwarder_NoCallerAndAClosedForwarderForwardNothing.
func TestFrameForwarder_NoCallerAndAClosedForwarderForwardNothing(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startQUICTarget(t, ca, "node-1", "raft-b")
	members := replicaforwardtest.Members{{NodeID: 1, Address: "raft-b", APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolQUIC: b.addr}}}
	f := NewFrameForwarder(replicaforward.Config{Locator: replicaforward.NewLocator(members, replicaforward.NewLeaderCache(), "raft-a", nil), RootCAs: ca.Pool}, ca.TLS(t, "node-a"))
	refusal := &placement.NotAReplicaError{TenantID: "acme", Database: "orders", ShardID: 1, LeaderReplicaID: 1, Replicas: []placement.Replica{{ReplicaID: 1, RaftAddr: "raft-b"}}}

	if _, _, ok := f.Forward(context.Background(), nil, "kv.Put", "orders", nil, refusal); ok {
		t.Fatal("a request with no authenticated caller was forwarded")
	}
	f.Close()
	if _, _, ok := f.Forward(context.Background(), &auth.Identity{Subject: "alice", TenantID: "acme"}, "kv.Put", "orders", nil, refusal); ok || b.calls.Load() != 0 {
		t.Fatalf("a closed forwarder forwarded (%d target calls)", b.calls.Load())
	}
}

func TestClassifyQUICAnswer(t *testing.T) {
	for name, tc := range map[string]struct {
		status  uint16
		body    []byte
		refused bool
		outcome replicaforward.Outcome
	}{
		"ok":                 {status: wire.StatusOK},
		"not a replica":      {status: wire.StatusUnavailable, body: frameOf(&placement.NotAReplicaError{}), refused: true, outcome: replicaforward.NotAReplica},
		"not leader":         {status: wire.StatusUnavailable, body: frameOf(&service.NotLeaderError{LeaderAddress: "x"}), refused: true, outcome: replicaforward.NotLeader},
		"another 503":        {status: wire.StatusUnavailable, body: frameOf(errors.New("sealed"))},
		"an undecodable 503": {status: wire.StatusUnavailable, body: []byte{0xff}},
	} {
		res, refused := classifyQUICAnswer(tc.status, tc.body)
		if refused != tc.refused || (refused && res.Outcome != tc.outcome) {
			t.Errorf("%s: classify = %+v, %v", name, res, refused)
		}
	}
}

func frameOf(err error) []byte {
	_, body := mapServiceError(err)
	return body
}

// TestForwardToLeader_EmptyLeaderAddressDoesNotForward proves ForwardToLeader
// refuses to forward when the refusal named no leader, rather than treating
// an empty address as "resolve this node's own address".
func TestForwardToLeader_EmptyLeaderAddressDoesNotForward(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	f := NewFrameForwarder(replicaforward.Config{Locator: replicaforward.NewLocator(replicaforwardtest.Members{}, replicaforward.NewLeaderCache(), "raft-a", nil), RootCAs: ca.Pool}, ca.TLS(t, "node-a"))
	t.Cleanup(f.Close)

	caller := &auth.Identity{Subject: "alice", TenantID: "acme"}
	if _, _, ok := f.ForwardToLeader(context.Background(), caller, "ceremony.Initialize", "", nil, ""); ok {
		t.Fatal("ForwardToLeader forwarded with no leader address")
	}
}

// TestForwardToLeader_UnresolvableLeaderDoesNotForward proves ForwardToLeader
// falls back when the named leader has no member record, rather than
// panicking on a nil member.
func TestForwardToLeader_UnresolvableLeaderDoesNotForward(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	f := NewFrameForwarder(replicaforward.Config{Locator: replicaforward.NewLocator(replicaforwardtest.Members{}, replicaforward.NewLeaderCache(), "raft-a", nil), RootCAs: ca.Pool}, ca.TLS(t, "node-a"))
	t.Cleanup(f.Close)

	caller := &auth.Identity{Subject: "alice", TenantID: "acme"}
	if _, _, ok := f.ForwardToLeader(context.Background(), caller, "ceremony.Initialize", "", nil, "raft-leader"); ok {
		t.Fatal("ForwardToLeader forwarded to an unresolvable leader")
	}
}

// TestForwardToLeader_NoQUICEndpointDoesNotForward proves ForwardToLeader
// falls back when the leader's member record advertises no QUIC endpoint,
// rather than dialing an empty address.
func TestForwardToLeader_NoQUICEndpointDoesNotForward(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	members := replicaforwardtest.Members{{NodeID: 1, Address: "raft-leader", APIEndpoints: map[cluster.APIProtocol]string{}}}
	f := NewFrameForwarder(replicaforward.Config{Locator: replicaforward.NewLocator(members, replicaforward.NewLeaderCache(), "raft-a", nil), RootCAs: ca.Pool}, ca.TLS(t, "node-a"))
	t.Cleanup(f.Close)

	caller := &auth.Identity{Subject: "alice", TenantID: "acme"}
	if _, _, ok := f.ForwardToLeader(context.Background(), caller, "ceremony.Initialize", "", nil, "raft-leader"); ok {
		t.Fatal("ForwardToLeader forwarded to a leader with no QUIC endpoint")
	}
}

// TestForwardToLeader_UnreachableLeaderDoesNotForward proves ForwardToLeader
// falls back (ok=false) when the leader's member record resolves but
// nothing answers at its endpoint, rather than hanging or panicking.
func TestForwardToLeader_UnreachableLeaderDoesNotForward(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	members := replicaforwardtest.Members{{NodeID: 1, Address: "raft-leader", APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolQUIC: replicaforwardtest.ClosedAddr(t)}}}
	f := NewFrameForwarder(replicaforward.Config{Locator: replicaforward.NewLocator(members, replicaforward.NewLeaderCache(), "raft-a", nil), RootCAs: ca.Pool, Timeout: 200 * time.Millisecond}, ca.TLS(t, "node-a"))
	t.Cleanup(f.Close)

	caller := &auth.Identity{Subject: "alice", TenantID: "acme"}
	if _, _, ok := f.ForwardToLeader(context.Background(), caller, "ceremony.Initialize", "", nil, "raft-leader"); ok {
		t.Fatal("ForwardToLeader forwarded to an unreachable leader")
	}
}
