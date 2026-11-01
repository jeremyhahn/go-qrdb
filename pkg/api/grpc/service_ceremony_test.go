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

package grpc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward/replicaforwardtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// fixedErrCeremonyService is a service.CeremonyServicer double local to this
// file (unconstrained by build tags, unlike grpc_coverage_test.go's
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

// TestQrdbServiceServer_CeremonyInitialize_ForwardsOnAdminCredentialNotLeaderError
// is the node-parity regression guard (owner ruling 2026-10-06) at the
// handler level: unlike TestForwardAdminWrite_ForwardsToLeaderAndRelaysTheAnswer,
// which calls forwardAdminWrite directly, this drives the real
// qrdbServiceServer.CeremonyInitialize handler, proving its own
// errors.As/replicaForwarder wiring -- not just forwardAdminWrite in
// isolation -- forwards on this error.
func TestQrdbServiceServer_CeremonyInitialize_ForwardsOnAdminCredentialNotLeaderError(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	leader := startTarget(t, ca, "node-1", "raft-leader")

	members := replicaforwardtest.Members{
		{NodeID: 1, Address: "raft-leader", APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolGRPC: leader.addr}},
	}
	ingressTLS := ca.TLS(t, "node-a")
	fwd := NewReplicaForwarder(replicaforward.Config{
		Locator: replicaforward.NewLocator(members, replicaforward.NewLeaderCache(), "raft-a", nil),
		RootCAs: ca.Pool,
		Timeout: 5 * time.Second,
	}, ingressTLS)
	t.Cleanup(fwd.Close)

	svc := &qrdbServiceServer{
		ceremonySvc:      fixedErrCeremonyService{err: &service.AdminCredentialNotLeaderError{LeaderAddress: "raft-leader"}},
		replicaForwarder: fwd,
	}

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice", TenantID: "acme"})
	resp, err := svc.CeremonyInitialize(ctx, &pb.CeremonyInitializeRequest{})
	if err != nil {
		t.Fatalf("CeremonyInitialize = %v; want the leader's answer", err)
	}
	if resp.GetSpkiPin() != "leader-answered" {
		t.Fatalf("SpkiPin = %q; want the leader's", resp.GetSpkiPin())
	}
}

// TestQrdbServiceServer_CeremonyInitialize_NoForwarderAnswersTheRefusal
// proves a node with no replicaForwarder configured (nil, the zero value)
// answers its own AdminCredentialNotLeaderError rather than panicking.
func TestQrdbServiceServer_CeremonyInitialize_NoForwarderAnswersTheRefusal(t *testing.T) {
	svc := &qrdbServiceServer{
		ceremonySvc: fixedErrCeremonyService{err: &service.AdminCredentialNotLeaderError{LeaderAddress: "raft-leader"}},
	}

	_, err := svc.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("CeremonyInitialize code = %s; want Unavailable (the node's own refusal)", status.Code(err))
	}
}

// TestForwardAdminWrite_RequestNotAProtoMessageDoesNotForward proves
// forwardAdminWrite refuses to forward a request it cannot marshal, rather
// than panicking on the type assertion.
func TestForwardAdminWrite_RequestNotAProtoMessageDoesNotForward(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	ingressTLS := ca.TLS(t, "node-a")
	fwd := NewReplicaForwarder(replicaforward.Config{
		Locator: replicaforward.NewLocator(replicaforwardtest.Members{}, replicaforward.NewLeaderCache(), "raft-a", nil),
		RootCAs: ca.Pool,
		Timeout: time.Second,
	}, ingressTLS)
	t.Cleanup(fwd.Close)

	if _, _, ok := fwd.forwardAdminWrite(context.Background(), pb.QRDBService_CeremonyInitialize_FullMethodName, "not a proto.Message", "raft-leader"); ok {
		t.Fatal("forwardAdminWrite forwarded a request that is not a proto.Message")
	}
}

// TestForwardAdminWrite_UnreachableLeaderDoesNotForward proves
// forwardAdminWrite falls back (ok=false) when the leader's member record
// resolves but nothing answers at its endpoint, rather than hanging or
// panicking.
func TestForwardAdminWrite_UnreachableLeaderDoesNotForward(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	members := replicaforwardtest.Members{
		{NodeID: 1, Address: "raft-leader", APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolGRPC: replicaforwardtest.ClosedAddr(t)}},
	}
	ingressTLS := ca.TLS(t, "node-a")
	fwd := NewReplicaForwarder(replicaforward.Config{
		Locator: replicaforward.NewLocator(members, replicaforward.NewLeaderCache(), "raft-a", nil),
		RootCAs: ca.Pool,
		Timeout: 200 * time.Millisecond,
	}, ingressTLS)
	t.Cleanup(fwd.Close)

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "alice", TenantID: "acme"})
	if _, _, ok := fwd.forwardAdminWrite(ctx, pb.QRDBService_CeremonyInitialize_FullMethodName, &pb.CeremonyInitializeRequest{}, "raft-leader"); ok {
		t.Fatal("forwardAdminWrite forwarded to an unreachable leader")
	}
}

// TestWithRegisterReplicaForwarding proves the option sets the field
// RegisterService threads onto qrdbServiceServer.
func TestWithRegisterReplicaForwarding(t *testing.T) {
	svc := &qrdbServiceServer{}
	WithRegisterReplicaForwarding(nil)(svc)
	if svc.replicaForwarder != nil {
		t.Fatalf("replicaForwarder = %v; want nil", svc.replicaForwarder)
	}

	ca := replicaforwardtest.NewCA(t)
	fwd := NewReplicaForwarder(replicaforward.Config{
		Locator: replicaforward.NewLocator(replicaforwardtest.Members{}, replicaforward.NewLeaderCache(), "raft-a", nil),
		RootCAs: ca.Pool,
		Timeout: time.Second,
	}, ca.TLS(t, "node-a"))
	t.Cleanup(fwd.Close)
	WithRegisterReplicaForwarding(fwd)(svc)
	if svc.replicaForwarder != fwd {
		t.Fatalf("replicaForwarder = %v; want %v", svc.replicaForwarder, fwd)
	}
}

func TestSignatureDecodeError_Error(t *testing.T) {
	e := &SignatureDecodeError{Err: errors.New("bad encoding")}
	msg := e.Error()
	if !strings.Contains(msg, "bad encoding") {
		t.Fatalf("expected error to contain 'bad encoding', got %q", msg)
	}
	if !strings.Contains(msg, "decode") {
		t.Fatalf("expected error to contain 'decode', got %q", msg)
	}
}

func TestSignatureDecodeError_Unwrap(t *testing.T) {
	inner := errors.New("inner")
	if got := (&SignatureDecodeError{Err: inner}).Unwrap(); got != inner {
		t.Fatalf("Unwrap = %v, want %v", got, inner)
	}
}
