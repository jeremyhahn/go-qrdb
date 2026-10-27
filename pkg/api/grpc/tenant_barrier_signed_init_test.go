// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package grpc

import (
	"bytes"
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	tenantpkg "github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// TestTenantBarrier_SignedInitBegin_MapsRequestAndResponse proves every
// begin field reaches the service and every result field reaches the client.
func TestTenantBarrier_SignedInitBegin_MapsRequestAndResponse(t *testing.T) {
	expires := time.Unix(1_800_000_000, 0)
	var got *tenantpkg.TenantBarrierInitBeginRequest
	svc := newMockTenantService()
	svc.signedBeginFn = func(_ context.Context, req *tenantpkg.TenantBarrierInitBeginRequest) (*tenantpkg.TenantBarrierInitBeginResult, error) {
		got = req
		return &tenantpkg.TenantBarrierInitBeginResult{
			TenantID: req.TenantID, Strategy: "shamir", Reference: []byte("ref"), Statement: []byte("stmt"),
			ExpiresAt: expires, Threshold: 2, TotalShares: 3, ShareCount: 3,
		}, nil
	}
	resp, err := pb.NewTenantBarrierServiceClient(connect(t, svc)).SignedInitBegin(context.Background(), &pb.TenantBarrierSignedInitBeginRequest{
		TenantId: "acme", Strategy: "shamir", KeyRef: "kr", Threshold: 2, Shares: 3, PublicKey: []byte("spki"),
	})
	if err != nil {
		t.Fatalf("SignedInitBegin: %v", err)
	}
	if got.TenantID != "acme" || got.Strategy != seal.StrategyID("shamir") || got.KeyRef != "kr" ||
		got.Threshold != 2 || got.Shares != 3 || !bytes.Equal(got.SignerPublicKey, []byte("spki")) || got.Roster != nil {
		t.Fatalf("service request = %+v", got)
	}
	if resp.GetTenantId() != "acme" || resp.GetStrategy() != "shamir" || string(resp.GetReference()) != "ref" ||
		string(resp.GetStatement()) != "stmt" || resp.GetExpires() != expires.Unix() ||
		resp.GetThreshold() != 2 || resp.GetTotalShares() != 3 || resp.GetShareCount() != 3 {
		t.Fatalf("response = %+v", resp)
	}
}

func TestTenantBarrier_SignedInitBegin_NilService(t *testing.T) {
	srv := &tenantBarrierServer{svc: nil}
	_, err := srv.SignedInitBegin(context.Background(), &pb.TenantBarrierSignedInitBeginRequest{TenantId: "acme"})
	requireCode(t, err, codes.Unimplemented)
}

func TestTenantBarrier_SignedInitBegin_EmptyTenantID(t *testing.T) {
	_, err := pb.NewTenantBarrierServiceClient(connect(t, newMockTenantService())).SignedInitBegin(context.Background(), &pb.TenantBarrierSignedInitBeginRequest{})
	requireCode(t, err, codes.InvalidArgument)
}

// TestTenantBarrier_SignedInitBegin_ServiceErrors pins the codes the
// signed-init refusals reach a gRPC client with.
func TestTenantBarrier_SignedInitBegin_ServiceErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want codes.Code
	}{
		{"field required", &tenantpkg.SignedInitFieldRequiredError{Field: "signer public key"}, codes.InvalidArgument},
		{"too many", &tenantpkg.TooManySignedInitsError{Limit: 1}, codes.ResourceExhausted},
		{"unsupported", &tenantpkg.SignedInitUnsupportedError{TenantID: "acme"}, codes.FailedPrecondition},
		{"not found", tenantpkg.ErrTenantNotFound, codes.NotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newMockTenantService()
			svc.signedBeginFn = func(context.Context, *tenantpkg.TenantBarrierInitBeginRequest) (*tenantpkg.TenantBarrierInitBeginResult, error) {
				return nil, tc.err
			}
			_, err := pb.NewTenantBarrierServiceClient(connect(t, svc)).SignedInitBegin(context.Background(), &pb.TenantBarrierSignedInitBeginRequest{TenantId: "acme"})
			requireCode(t, err, tc.want)
		})
	}
}

// TestTenantBarrier_SignedInitComplete_MapsRequest proves the reference,
// signature and passphrase reach the service and the result reaches the
// client.
func TestTenantBarrier_SignedInitComplete_MapsRequest(t *testing.T) {
	var got tenantpkg.TenantBarrierInitCompleteRequest
	var passphrase []byte
	svc := newMockTenantService()
	svc.signedCompleteFn = func(_ context.Context, req *tenantpkg.TenantBarrierInitCompleteRequest) (*tenantpkg.TenantBarrierInitResult, error) {
		got = tenantpkg.TenantBarrierInitCompleteRequest{
			TenantID: req.TenantID, Reference: bytes.Clone(req.Reference),
			Signature: bytes.Clone(req.Signature), Passphrase: req.Passphrase,
		}
		_ = req.Passphrase.WithBytes(func(p []byte) error { passphrase = bytes.Clone(p); return nil })
		return &tenantpkg.TenantBarrierInitResult{TenantID: req.TenantID, Strategy: "passphrase"}, nil
	}
	resp, err := pb.NewTenantBarrierServiceClient(connect(t, svc)).SignedInitComplete(context.Background(), &pb.TenantBarrierSignedInitCompleteRequest{
		TenantId: "acme", Reference: []byte("ref"), Signature: []byte("sig"), Passphrase: []byte("pw"),
	})
	if err != nil {
		t.Fatalf("SignedInitComplete: %v", err)
	}
	if got.TenantID != "acme" || string(got.Reference) != "ref" || string(got.Signature) != "sig" || string(passphrase) != "pw" || got.Passphrase.IsAlive() {
		t.Fatalf("service request = %+v", got)
	}
	if resp.GetTenantId() != "acme" || resp.GetStrategy() != "passphrase" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestTenantBarrier_SignedInitComplete_NilService(t *testing.T) {
	srv := &tenantBarrierServer{svc: nil}
	_, err := srv.SignedInitComplete(context.Background(), &pb.TenantBarrierSignedInitCompleteRequest{TenantId: "acme"})
	requireCode(t, err, codes.Unimplemented)
}

func TestTenantBarrier_SignedInitComplete_EmptyTenantID(t *testing.T) {
	_, err := pb.NewTenantBarrierServiceClient(connect(t, newMockTenantService())).SignedInitComplete(context.Background(), &pb.TenantBarrierSignedInitCompleteRequest{})
	requireCode(t, err, codes.InvalidArgument)
}

// TestTenantBarrier_SignedInitComplete_Refused pins that a refused
// signature, an unknown or replayed reference and an expired one reach a gRPC
// client as PermissionDenied.
func TestTenantBarrier_SignedInitComplete_Refused(t *testing.T) {
	for _, reason := range []tenantpkg.SignedInitReason{tenantpkg.SignedInitUnknown, tenantpkg.SignedInitExpired} {
		svc := newMockTenantService()
		svc.signedCompleteFn = func(context.Context, *tenantpkg.TenantBarrierInitCompleteRequest) (*tenantpkg.TenantBarrierInitResult, error) {
			return nil, &tenantpkg.SignedInitError{TenantID: "acme", Reason: reason}
		}
		_, err := pb.NewTenantBarrierServiceClient(connect(t, svc)).SignedInitComplete(context.Background(), &pb.TenantBarrierSignedInitCompleteRequest{
			TenantId: "acme", Reference: []byte("ref"), Signature: []byte("sig"),
		})
		requireCode(t, err, codes.PermissionDenied)
	}
}
