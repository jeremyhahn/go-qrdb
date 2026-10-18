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

package unix

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	grpccredentials "google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	grpcpkg "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward/replicaforwardtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// forwardAllowAuthorizer admits every call.
type forwardAllowAuthorizer struct{}

func (forwardAllowAuthorizer) Authorize(context.Context, *auth.Identity, string, string) error {
	return nil
}

// countingKV is a KV double that counts the writes it serves, and refuses
// them with refusal when set.
type countingKV struct {
	*mockKVService
	puts    atomic.Int32
	refusal error
}

func (k *countingKV) Put(ctx context.Context, key string, value []byte) error {
	if k.refusal != nil {
		return k.refusal
	}
	k.puts.Add(1)
	return k.mockKVService.Put(ctx, key, value)
}

// TestReplicaForwardUnix_TheSocketForwardsOverGRPC: a write the socket's
// node refuses as a non-replica is forwarded to the replica's gRPC listener
// as the caller, and the answer says which replica served it.
func TestReplicaForwardUnix_TheSocketForwardsOverGRPC(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)

	replicaKV := &countingKV{mockKVService: newMockKVService()}
	target := grpcpkg.NewServer(replicaKV, &mockAdminService{}, "127.0.0.1:0", grpcpkg.WithTLS(ca.TLS(t, "node-1")))
	if err := target.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(target.Stop)

	refusal := &placement.NotAReplicaError{TenantID: "acme", Database: "orders", ShardID: 42, NodeID: "node-a", LeaderReplicaID: 1,
		Replicas: []placement.Replica{{ReplicaID: 1, NodeID: "node-1", RaftAddr: "raft-b"}}}
	members := replicaforwardtest.Members{{NodeID: 1, Address: "raft-b", APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolGRPC: target.Addr()}}}
	cache := replicaforward.NewLeaderCache()
	ingressKV := &countingKV{mockKVService: newMockKVService(), refusal: refusal}
	mw := authmw.New(forwardAllowAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(authtest.NewFixedAuthenticator(&auth.Identity{Subject: "alice", TenantID: "acme"})))
	socket := tempSocketPath(t)
	srv := NewServer(ingressKV, &mockAdminService{}, socket, WithTLS(ca.TLS(t, "node-a")), WithAuthMiddleware(mw),
		WithReplicaForwarding(replicaforward.Config{Locator: replicaforward.NewLocator(members, cache, "raft-a", nil), RootCAs: ca.Pool, Timeout: 5 * time.Second}))
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)

	clientTLS := ca.TLS(t, "")
	clientTLS.ServerName = "127.0.0.1"
	conn, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(grpccredentials.NewTLS(clientTLS)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var header metadata.MD
	if _, err := pb.NewQRDBServiceClient(conn).Put(ctx, &pb.PutRequest{Key: "k", Value: []byte("v")}, grpc.Header(&header)); err != nil {
		t.Fatalf("Put through the socket = %v (%v); want it forwarded", err, status.Convert(err).Details())
	}
	if replicaKV.puts.Load() != 1 || header.Get(replicaforward.MetadataForwardedTo)[0] != "1" {
		t.Fatalf("replica writes %d, header %v; want the write served by replica 1", replicaKV.puts.Load(), header)
	}
	if _, ok := cache.Get(42); !ok {
		t.Fatal("the replica that served was not cached")
	}
}

// fixedCeremonyService is a service.CeremonyServicer double local to this
// file that answers every call with a fixed result or error.
type fixedCeremonyService struct {
	result *service.CeremonyInitResult
	err    error
}

func (f fixedCeremonyService) CeremonyInitialize(context.Context, ceremony.CeremonyCA, *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	return f.result, f.err
}

func (f fixedCeremonyService) CeremonySignCSR(context.Context, string, string, string, string) (*service.CeremonySignCSRResult, error) {
	return nil, f.err
}

func (f fixedCeremonyService) CeremonyStatus(context.Context) (*service.CeremonyStatusInfo, error) {
	return nil, f.err
}

// TestReplicaForwardUnix_CeremonyInitializeForwardsOnAdminCredentialNotLeaderError
// is the node-parity regression guard (owner ruling 2026-10-06): the Unix
// socket shares qrdbServiceServer's CeremonyInitialize handler with the TCP
// gRPC server, and registers its OWN ReplicaForwarder onto it
// (WithRegisterReplicaForwarding) exactly as the TCP listener does, so a
// follower reached over the socket also forwards the admin-credential step
// to the system shard's leader instead of answering its own refusal.
func TestReplicaForwardUnix_CeremonyInitializeForwardsOnAdminCredentialNotLeaderError(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)

	leader := grpcpkg.NewServer(newMockKVService(), &mockAdminService{}, "127.0.0.1:0", grpcpkg.WithTLS(ca.TLS(t, "node-1")),
		grpcpkg.WithCeremonyService(fixedCeremonyService{result: &service.CeremonyInitResult{SPKIPin: "leader-answered"}}))
	if err := leader.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(leader.Stop)

	members := replicaforwardtest.Members{{NodeID: 1, Address: "raft-leader", APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolGRPC: leader.Addr()}}}
	cache := replicaforward.NewLeaderCache()
	mw := authmw.New(forwardAllowAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)), authmw.WithAuthenticator(authtest.NewFixedAuthenticator(&auth.Identity{Subject: "alice", TenantID: "acme"})))
	socket := tempSocketPath(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socket, WithTLS(ca.TLS(t, "node-a")), WithAuthMiddleware(mw),
		WithCeremonyService(fixedCeremonyService{err: &service.AdminCredentialNotLeaderError{LeaderAddress: "raft-leader"}}),
		WithReplicaForwarding(replicaforward.Config{Locator: replicaforward.NewLocator(members, cache, "raft-a", nil), RootCAs: ca.Pool, Timeout: 5 * time.Second}))
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Stop)

	clientTLS := ca.TLS(t, "")
	clientTLS.ServerName = "127.0.0.1"
	conn, err := grpc.NewClient("unix://"+socket, grpc.WithTransportCredentials(grpccredentials.NewTLS(clientTLS)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := pb.NewQRDBServiceClient(conn).CeremonyInitialize(ctx, &pb.CeremonyInitializeRequest{})
	if err != nil {
		t.Fatalf("CeremonyInitialize through the socket = %v (%v); want the leader's answer", err, status.Convert(err).Details())
	}
	if resp.GetSpkiPin() != "leader-answered" {
		t.Fatalf("SpkiPin = %q; want the leader's", resp.GetSpkiPin())
	}
}
