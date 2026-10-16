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

// The conditional factor add over gRPC, driven against the real service on
// a real loopback connection. The Unix-socket transport registers this same
// servicer, so it is covered by these too.
package grpc

import (
	"context"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
)

// assertConditionRefusalRPC requires err to be AlreadyExists carrying the
// qrdb ErrorInfo refusal, and returns the refusal its metadata decodes to.
func assertConditionRefusalRPC(t *testing.T, err error) *auth.FactorConditionFailedError {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.AlreadyExists {
		t.Fatalf("err = %v, want AlreadyExists", err)
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == replicaforward.ErrorDomain {
			if info.GetReason() != auth.ReasonFactorConditionFailed {
				t.Fatalf("reason = %q, want %q", info.GetReason(), auth.ReasonFactorConditionFailed)
			}
			return auth.FactorConditionFailedFromMetadata(info.GetMetadata())
		}
	}
	t.Fatalf("status carries no %s ErrorInfo: %v", replicaforward.ErrorDomain, st.Details())
	return nil
}

func TestGRPC_AddConditional_noActiveType(t *testing.T) {
	conn, _ := startRealFactorServer(t)
	client := pb.NewUserAuthFactorServiceClient(conn)
	ctx := context.Background()

	first, err := client.Add(ctx, &pb.UserAuthFactorAddRequest{
		UserId: 42, Type: "mtls", Credential: []byte("cred-1"), IfNoActiveType: true,
	})
	if err != nil {
		t.Fatalf("conditional Add: %v", err)
	}
	if first.GetFactorSetVersion() != 1 {
		t.Fatalf("FactorSetVersion = %d, want 1", first.GetFactorSetVersion())
	}

	_, err = client.Add(ctx, &pb.UserAuthFactorAddRequest{
		UserId: 42, Type: "mtls", Credential: []byte("cred-2"), IfNoActiveType: true,
	})
	got := assertConditionRefusalRPC(t, err)
	if got.Condition != auth.FactorConditionNoActiveOfType || got.ExistingFactorID != first.GetFactor().GetId() ||
		got.UserID != 42 || got.CurrentVersion != 1 {
		t.Fatalf("refusal = %+v", got)
	}

	list, err := client.List(ctx, &pb.UserAuthFactorListRequest{UserId: 42})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if list.GetTotal() != 1 || list.GetFactorSetVersion() != 1 {
		t.Fatalf("List = %d factors at version %d, want 1 at 1", list.GetTotal(), list.GetFactorSetVersion())
	}
}

func TestGRPC_AddConditional_expectedVersion(t *testing.T) {
	conn, _ := startRealFactorServer(t)
	client := pb.NewUserAuthFactorServiceClient(conn)
	ctx := context.Background()

	zero := uint64(0)
	if _, err := client.Add(ctx, &pb.UserAuthFactorAddRequest{
		UserId: 42, Type: "mtls", Credential: []byte("cred-1"), ExpectedFactorSetVersion: &zero,
	}); err != nil {
		t.Fatalf("Add at version 0: %v", err)
	}
	_, err := client.Add(ctx, &pb.UserAuthFactorAddRequest{
		UserId: 42, Type: "fido2", Credential: []byte("cred-2"), ExpectedFactorSetVersion: &zero,
	})
	got := assertConditionRefusalRPC(t, err)
	if got.Condition != auth.FactorConditionExpectedVersion || got.ExpectedVersion != 0 || got.CurrentVersion != 1 {
		t.Fatalf("refusal = %+v", got)
	}
}

func TestGRPC_AddUnconditional_reportsNoVersion(t *testing.T) {
	conn, _ := startRealFactorServer(t)
	client := pb.NewUserAuthFactorServiceClient(conn)

	for _, cred := range []string{"cred-1", "cred-2"} {
		resp, err := client.Add(context.Background(), &pb.UserAuthFactorAddRequest{
			UserId: 42, Type: "mtls", Credential: []byte(cred),
		})
		if err != nil {
			t.Fatalf("Add(%s): %v", cred, err)
		}
		if resp.GetFactorSetVersion() != 0 {
			t.Fatalf("unconditional Add FactorSetVersion = %d, want 0", resp.GetFactorSetVersion())
		}
	}
}
