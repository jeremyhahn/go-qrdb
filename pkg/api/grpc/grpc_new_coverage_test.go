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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd && !integration

package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// RegisterService option tests (0% coverage)
// ---------------------------------------------------------------------------

func TestWithRegisterVector(t *testing.T) {
	grpcSrv := newGRPCServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterVector(&mockVectorService{}),
	)
	grpcSrv.Stop()
}

func TestWithRegisterUserService(t *testing.T) {
	grpcSrv := newGRPCServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterUserService(&mockUserService{}),
	)
	grpcSrv.Stop()
}

func TestWithRegisterRoleService(t *testing.T) {
	grpcSrv := newGRPCServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterRoleService(&mockRoleService{}),
	)
	grpcSrv.Stop()
}

func TestWithRegisterEmbedding(t *testing.T) {
	grpcSrv := newGRPCServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterEmbedding(&mockEmbeddingService{}),
	)
	grpcSrv.Stop()
}

func TestWithRegisterCredentialService(t *testing.T) {
	grpcSrv := newGRPCServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterCredentialService(&coverageGRPCCredentialService{}),
	)
	grpcSrv.Stop()
}

// ---------------------------------------------------------------------------
// WithEmbedding server option (0% coverage)
// ---------------------------------------------------------------------------

func TestWithEmbedding_ServerOption(t *testing.T) {
	embSvc := &mockEmbeddingService{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithEmbedding(embSvc))
	if srv.embeddingSvc == nil {
		t.Error("embeddingSvc not set")
	}
}

// ---------------------------------------------------------------------------
// Credential service mock for RegisterService option test
// ---------------------------------------------------------------------------

type coverageGRPCCredentialService struct{}

func (c *coverageGRPCCredentialService) CredentialSubmit(_ context.Context, _ string, _ []byte) error {
	return nil
}
func (c *coverageGRPCCredentialService) CredentialStrategy() string { return "manual" }
func (c *coverageGRPCCredentialService) CredentialDelete(_ context.Context, _ string) error {
	return nil
}
func (c *coverageGRPCCredentialService) CredentialStore(_ context.Context, _ string, _ []byte) error {
	return nil
}
func (c *coverageGRPCCredentialService) CredentialRetrieve(_ context.Context, _ string) ([]byte, error) {
	return nil, nil
}

func (c *coverageGRPCCredentialService) CredentialList(_ context.Context) ([]string, error) {
	return nil, nil
}
func (c *coverageGRPCCredentialService) CredentialRotate(_ context.Context, _ string, _ credentials.RotateOptions) (*credentials.RotateResult, error) {
	return &credentials.RotateResult{}, nil
}
func (c *coverageGRPCCredentialService) CredentialRetrieveVersion(_ context.Context, _ string, _ uint64) ([]byte, error) {
	return nil, nil
}
func (c *coverageGRPCCredentialService) CredentialListVersions(_ context.Context, _ string) ([]credentials.CredentialVersion, error) {
	return nil, nil
}
func (c *coverageGRPCCredentialService) CredentialGCExpired(_ context.Context, _ string) (int, error) {
	return 0, nil
}

// ---------------------------------------------------------------------------
// Embedding service mock (implements service.EmbeddingServicer)
// ---------------------------------------------------------------------------

type mockEmbeddingService struct{}

func (m *mockEmbeddingService) Embed(_ context.Context, _ *service.EmbedRequest) (*service.EmbedResponse, error) {
	return &service.EmbedResponse{}, nil
}
func (m *mockEmbeddingService) BatchEmbed(_ context.Context, _ *service.BatchEmbedRequest) (*service.BatchEmbedResponse, error) {
	return &service.BatchEmbedResponse{}, nil
}
func (m *mockEmbeddingService) ListModels(_ context.Context, _ *service.ListEmbeddingModelsRequest) (*service.ListEmbeddingModelsResponse, error) {
	return &service.ListEmbeddingModelsResponse{}, nil
}
func (m *mockEmbeddingService) RegisterProvider(_ string, _ service.EmbeddingProvider) error {
	return nil
}
func (m *mockEmbeddingService) DeregisterProvider(_ string) error { return nil }
func (m *mockEmbeddingService) Close() error                      { return nil }

// ---------------------------------------------------------------------------
// newGRPCServer helper
// ---------------------------------------------------------------------------

func newGRPCServer() *grpc.Server {
	return grpc.NewServer()
}

// ---------------------------------------------------------------------------
// Barrier error-path tests
// ---------------------------------------------------------------------------

func TestGRPC_BarrierUnseal_Error(t *testing.T) {
	barrierSvc := newMockBarrierService()
	barrierSvc.unsealErr = &service.KeyNotFoundError{Key: "barrier"}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierUnseal(context.Background(), &pb.BarrierUnsealRequest{Secret: "bad"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

func TestGRPC_BarrierSeal_Error(t *testing.T) {
	barrierSvc := newMockBarrierService()
	barrierSvc.sealErr = &service.KeyNotFoundError{Key: "barrier"}
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierSeal(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_BarrierStatus_Error(t *testing.T) {
	barrierSvc := newMockBarrierService()
	barrierSvc.statusErr = errors.New("status error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierStatus(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_BarrierShamirInitialize_Error(t *testing.T) {
	barrierSvc := newMockBarrierService()
	barrierSvc.shamirInitErr = errors.New("shamir init error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierShamirInitialize(context.Background(), &pb.BarrierShamirInitializeRequest{
		Secret: "s", Threshold: 2, TotalShares: 3,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_BarrierShamirUnsealShare_Error(t *testing.T) {
	barrierSvc := newMockBarrierService()
	barrierSvc.unsealShareErr = errors.New("unseal share error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierShamirUnsealShare(context.Background(), &pb.BarrierShamirUnsealShareRequest{
		Share: "share1",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_BarrierShamirUnsealShares_Error(t *testing.T) {
	barrierSvc := newMockBarrierService()
	barrierSvc.unsealSharesErr = errors.New("unseal shares error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierShamirUnsealShares(context.Background(), &pb.BarrierShamirUnsealSharesRequest{
		Shares: []string{"s1", "s2"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_BarrierRekey_Error(t *testing.T) {
	barrierSvc := newMockBarrierService()
	barrierSvc.rekeyErr = errors.New("rekey error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierRekey(context.Background(), &pb.BarrierRekeyRequest{
		Threshold: 2, Total: 3,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_BarrierRecoveryGenerate_Error(t *testing.T) {
	barrierSvc := newMockBarrierService()
	barrierSvc.recoveryGenErr = errors.New("recovery gen error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierRecoveryGenerate(context.Background(), &pb.BarrierRecoveryGenerateRequest{
		Threshold: 2, Total: 3,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_BarrierRecoveryRecover_Error(t *testing.T) {
	barrierSvc := newMockBarrierService()
	barrierSvc.recoveryRecoverErr = errors.New("recover error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierRecoveryRecover(context.Background(), &pb.BarrierRecoveryRecoverRequest{
		Keys: []string{"key1"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_BarrierRecoveryDelete_Error(t *testing.T) {
	barrierSvc := newMockBarrierService()
	barrierSvc.recoveryDeleteErr = errors.New("recovery delete error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierRecoveryDelete(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_BarrierRecoveryStatus_Error(t *testing.T) {
	barrierSvc := newMockBarrierService()
	barrierSvc.recoveryHasErr = errors.New("recovery status error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierRecoveryStatus(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_BarrierRootToken_Error(t *testing.T) {
	barrierSvc := newMockBarrierService()
	barrierSvc.rootTokenErr = errors.New("root token error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithBarrierService(barrierSvc))
	defer cleanup()

	_, err := client.BarrierRootToken(context.Background(), &pb.BarrierRootTokenRequest{
		Shares: []string{"s1"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Ceremony error-path tests
// ---------------------------------------------------------------------------

func TestGRPC_CeremonyInitialize_Error(t *testing.T) {
	ceremonySvc := newMockCeremonyService()
	ceremonySvc.initErr = errors.New("ceremony init error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithCeremonyService(ceremonySvc), WithCeremonyCA(&stubCeremonyCA{}))
	defer cleanup()

	_, err := client.CeremonyInitialize(context.Background(), &pb.CeremonyInitializeRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_CeremonyStatus_Error(t *testing.T) {
	ceremonySvc := newMockCeremonyService()
	ceremonySvc.statusErr = errors.New("ceremony status error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithCeremonyService(ceremonySvc))
	defer cleanup()

	_, err := client.CeremonyStatus(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_CeremonyClaimCertBegin_Error(t *testing.T) {
	ceremonySvc := newMockCeremonyService()
	ceremonySvc.beginErr = errors.New("claim cert begin error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithCeremonyService(ceremonySvc))
	defer cleanup()

	_, err := client.CeremonyClaimCertBegin(context.Background(), &pb.CeremonyClaimCertBeginRequest{
		Username: "alice",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_CeremonyClaimShare_Error(t *testing.T) {
	ceremonySvc := newMockCeremonyService()
	ceremonySvc.claimShareErr = errors.New("claim share error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithCeremonyService(ceremonySvc))
	defer cleanup()

	_, err := client.CeremonyBeginClaimShare(context.Background(), &pb.CeremonyBeginClaimShareRequest{
		Username: "alice",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_CeremonySignCSR_Error(t *testing.T) {
	ceremonySvc := newMockCeremonyService()
	ceremonySvc.signCSRErr = errors.New("sign csr error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithCeremonyService(ceremonySvc))
	defer cleanup()

	_, err := client.CeremonySignCSR(context.Background(), &pb.CeremonySignCSRRequest{
		Username: "alice", SoPin: "pin", CsrPem: "csr", Role: "officer",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// mapServiceError missing branches (errors.go 90.5%)
// ---------------------------------------------------------------------------

func TestMapServiceError_NilReturnsNil(t *testing.T) {
	if mapServiceError(nil) != nil {
		t.Error("expected nil for nil input")
	}
}

func TestMapServiceError_LeaseErrors(t *testing.T) {
	tests := []struct {
		err  error
		want codes.Code
	}{
		{&service.LeaseNotFoundError{LeaseID: 1}, codes.NotFound},
		{&service.LeaseExpiredError{LeaseID: 1}, codes.NotFound},
		{&service.LeaseExistsError{LeaseID: 1}, codes.AlreadyExists},
		{&service.BatchError{Err: errors.New("err")}, codes.Internal},
		{&service.IndexNotFoundError{Name: "idx"}, codes.NotFound},
		{&service.IndexExistsError{Name: "idx"}, codes.AlreadyExists},
		{&service.TimeSeriesError{Op: "ts", Err: errors.New("err")}, codes.Internal},
		{&service.ConsistencyError{}, codes.FailedPrecondition},
		{&service.VectorNotFoundError{}, codes.NotFound},
		{&service.NamespaceNotFoundError{Namespace: "ns"}, codes.NotFound},
		{&service.NamespaceExistsError{Namespace: "ns"}, codes.AlreadyExists},
		{&service.DimensionMismatchError{}, codes.InvalidArgument},
		{&service.InvalidVectorError{}, codes.InvalidArgument},
	}
	for _, tc := range tests {
		got := mapServiceError(tc.err)
		st, ok := status.FromError(got)
		if !ok {
			t.Fatalf("mapServiceError(%T) not a gRPC status: %v", tc.err, got)
		}
		if st.Code() != tc.want {
			t.Errorf("mapServiceError(%T) code = %s, want %s", tc.err, st.Code(), tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// TimeSeries error-path tests
// ---------------------------------------------------------------------------

func TestGRPC_TSRichQuery_WithGroupedResults(t *testing.T) {
	aggVal := 42.0
	tsSvc := &richQueryTSService{
		result: &timeseries.QueryResult{
			Metric: "cpu",
			Tags:   map[string]string{"host": "a"},
			Points: []*timeseries.DataPoint{
				{Timestamp: time.Now(), Value: 1.0},
			},
			AggregatedValue: &aggVal,
			GroupedResults: []*timeseries.GroupedResult{
				{
					Tags:   map[string]string{"host": "b"},
					Points: []*timeseries.DataPoint{{Timestamp: time.Now(), Value: 2.0}},
				},
			},
		},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	resp, err := client.TSRichQuery(context.Background(), &pb.TSRichQueryRequest{
		Metric:  "cpu",
		StartNs: time.Now().Add(-time.Hour).UnixNano(),
		EndNs:   time.Now().UnixNano(),
		GroupBy: []string{"host"},
	})
	if err != nil {
		t.Fatalf("TSRichQuery failed: %v", err)
	}
	if resp.GetMetric() != "cpu" {
		t.Errorf("metric = %q, want 'cpu'", resp.GetMetric())
	}
	if !resp.GetHasAggregatedValue() {
		t.Error("expected has_aggregated_value=true")
	}
	if len(resp.GetGroupedResults()) != 1 {
		t.Errorf("expected 1 grouped result, got %d", len(resp.GetGroupedResults()))
	}
}

func TestGRPC_TSRichQuery_Error(t *testing.T) {
	tsSvc := &errorTSService{
		inner:        &mockTimeSeriesService{},
		richQueryErr: &service.TimeSeriesError{Op: "query", Err: errors.New("failed")},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSRichQuery(context.Background(), &pb.TSRichQueryRequest{
		Metric:  "cpu",
		StartNs: time.Now().Add(-time.Hour).UnixNano(),
		EndNs:   time.Now().UnixNano(),
	})
	if err == nil {
		t.Fatal("expected error from TSRichQuery")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_TSWritePoint_Error(t *testing.T) {
	tsSvc := &errorTSService{
		inner:         &mockTimeSeriesService{},
		writePointErr: &service.TimeSeriesError{Op: "write", Err: errors.New("failed")},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSWritePoint(context.Background(), &pb.TSWriteRequest{
		Point: &pb.TSDataPoint{
			Metric:      "cpu",
			Value:       1.5,
			TimestampNs: time.Now().UnixNano(),
		},
	})
	if err == nil {
		t.Fatal("expected error from TSWritePoint")
	}
}

func TestGRPC_TSDeleteSeries_WithTimestamps_Error(t *testing.T) {
	tsSvc := &errorTSService{
		inner:        &mockTimeSeriesService{},
		deleteSerErr: &service.TimeSeriesError{Op: "delete", Err: errors.New("failed")},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	now := time.Now()
	_, err := client.TSDeleteSeries(context.Background(), &pb.TSDeleteSeriesRequest{
		Metric:  "cpu",
		StartNs: now.Add(-time.Hour).UnixNano(),
		EndNs:   now.UnixNano(),
	})
	if err == nil {
		t.Fatal("expected error from TSDeleteSeries")
	}
}

func TestGRPC_TSSetRetention_Error(t *testing.T) {
	tsSvc := &errorTSService{
		inner:     &mockTimeSeriesService{},
		setRetErr: &service.TimeSeriesError{Op: "set_retention", Err: errors.New("failed")},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSSetRetention(context.Background(), &pb.TSSetRetentionRequest{
		Metric:     "cpu",
		DurationNs: int64(24 * time.Hour),
	})
	if err == nil {
		t.Fatal("expected error from TSSetRetention")
	}
}

func TestGRPC_TSListMetrics_Error(t *testing.T) {
	tsSvc := &errorTSService{
		inner:      &mockTimeSeriesService{},
		listMetErr: &service.TimeSeriesError{Op: "list", Err: errors.New("failed")},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSListMetrics(context.Background(), &pb.TSListMetricsRequest{
		Prefix: "cpu",
	})
	if err == nil {
		t.Fatal("expected error from TSListMetrics")
	}
}

// ---------------------------------------------------------------------------
// UpdateRole with permissions (service_auth.go 91.7% – missing permissions branch)
// ---------------------------------------------------------------------------

func TestGRPC_UpdateRole_WithPermissions(t *testing.T) {
	roleSvc := newMockRoleServiceForGRPC()
	userSvc := newMockUserServiceForGRPC()
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithRoleService(roleSvc),
		WithUserService(userSvc))
	defer cleanup()

	_, err := client.UpdateRole(context.Background(), &pb.UpdateRoleProtoRequest{
		Id:          "role-1",
		Name:        "updated",
		Permissions: []string{"kv.read", "kv.write"},
	})
	if err != nil {
		t.Fatalf("UpdateRole failed: %v", err)
	}
}

func TestGRPC_UpdateRole_Error(t *testing.T) {
	roleSvc := newMockRoleServiceForGRPC()
	roleSvc.updateErr = errors.New("update role error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithRoleService(roleSvc),
		WithUserService(newMockUserServiceForGRPC()))
	defer cleanup()

	_, err := client.UpdateRole(context.Background(), &pb.UpdateRoleProtoRequest{Id: "r1"})
	if err == nil {
		t.Fatal("expected error from UpdateRole")
	}
}

// ---------------------------------------------------------------------------
// AssignRole - both success and missing-user-service paths
// ---------------------------------------------------------------------------

func TestGRPC_UnassignRole_Error(t *testing.T) {
	roleSvc := newMockRoleServiceForGRPC()
	userSvc := newMockUserServiceForGRPC()
	userSvc.updateErr = errors.New("update error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithRoleService(roleSvc),
		WithUserService(userSvc))
	defer cleanup()

	// GetUser returns a user with role r1 so UpdateUser is called.
	_, err := client.UnassignRole(context.Background(), &pb.UnassignRoleRequest{
		RoleId: "r1", UserId: 1,
	})
	if err == nil {
		t.Fatal("expected error from UnassignRole")
	}
}

// ---------------------------------------------------------------------------
// Mock role/user services for gRPC auth coverage
// ---------------------------------------------------------------------------

type mockRoleServiceForGRPC struct {
	updateErr   error
	unassignErr error
}

func newMockRoleServiceForGRPC() *mockRoleServiceForGRPC {
	return &mockRoleServiceForGRPC{}
}

func (m *mockRoleServiceForGRPC) CreateRole(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
	return &role.Role{ID: "r1", Name: "test"}, nil
}
func (m *mockRoleServiceForGRPC) GetRole(_ context.Context, _ string) (*role.Role, error) {
	return &role.Role{ID: "r1", Name: "test"}, nil
}
func (m *mockRoleServiceForGRPC) GetRoleByName(_ context.Context, _, _ string) (*role.Role, error) {
	return &role.Role{ID: "r1", Name: "test"}, nil
}
func (m *mockRoleServiceForGRPC) UpdateRole(_ context.Context, _ *role.UpdateRoleRequest) (*role.Role, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	return &role.Role{ID: "r1", Name: "updated"}, nil
}
func (m *mockRoleServiceForGRPC) DeleteRole(_ context.Context, _ string) error { return nil }
func (m *mockRoleServiceForGRPC) ListRoles(_ context.Context, _ *role.ListRolesRequest) (*role.ListRolesResponse, error) {
	return &role.ListRolesResponse{Roles: []*role.Role{{ID: "r1", Name: "test"}}}, nil
}
func (m *mockRoleServiceForGRPC) ResolvePermissions(_ context.Context, _ []string) ([]permission.Permission, error) {
	return nil, nil
}

type mockUserServiceForGRPC struct {
	updateErr error
}

func newMockUserServiceForGRPC() *mockUserServiceForGRPC {
	return &mockUserServiceForGRPC{}
}

func (m *mockUserServiceForGRPC) CreateUser(_ context.Context, _ *user.CreateUserRequest) (*user.User, error) {
	return &user.User{ID: 1, Username: "alice"}, nil
}
func (m *mockUserServiceForGRPC) GetUser(_ context.Context, _ uint64) (*user.User, error) {
	return &user.User{ID: 1, Username: "alice", RoleIDs: []string{"r1"}}, nil
}
func (m *mockUserServiceForGRPC) GetUserByUsername(_ context.Context, _, _ string) (*user.User, error) {
	return &user.User{ID: 1, Username: "alice"}, nil
}
func (m *mockUserServiceForGRPC) UpdateUser(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	return &user.User{ID: 1, Username: "alice"}, nil
}
func (m *mockUserServiceForGRPC) DeleteUser(_ context.Context, _ uint64) error { return nil }
func (m *mockUserServiceForGRPC) ListUsers(_ context.Context, _ *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	return &user.ListUsersResponse{}, nil
}
func (m *mockUserServiceForGRPC) EnableUser(_ context.Context, _ uint64) error  { return nil }
func (m *mockUserServiceForGRPC) DisableUser(_ context.Context, _ uint64) error { return nil }
func (m *mockUserServiceForGRPC) UserByCertThumbprint(_ context.Context, _ string) (*user.User, error) {
	return nil, nil
}

func (m *mockUserServiceForGRPC) AddCertBinding(_ context.Context, _ uint64, _ string, _ int64) (*user.User, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Collection error-path tests
// ---------------------------------------------------------------------------

func TestGRPC_CreateCollection_Error(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.createErr = errors.New("create collection error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithCollectionService(collSvc))
	defer cleanup()

	_, err := client.CreateCollection(context.Background(), &pb.CreateCollectionRequest{
		Database: "db", Name: "col",
	})
	if err == nil {
		t.Fatal("expected error from CreateCollection")
	}
}

func TestGRPC_GetCollectionInfo_Error(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.getInfoErr = errors.New("get collection info error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithCollectionService(collSvc))
	defer cleanup()

	_, err := client.GetCollectionInfo(context.Background(), &pb.GetCollectionInfoRequest{
		Database: "db", Name: "col",
	})
	if err == nil {
		t.Fatal("expected error from GetCollectionInfo")
	}
}

func TestGRPC_DropCollection_Error(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.dropErr = errors.New("drop collection error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithCollectionService(collSvc))
	defer cleanup()

	_, err := client.DropCollection(context.Background(), &pb.DropCollectionRequest{
		Database: "db", Name: "col",
	})
	if err == nil {
		t.Fatal("expected error from DropCollection")
	}
}

func TestGRPC_GetDocument_Error(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.getDocErr = errors.New("get document error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithCollectionService(collSvc))
	defer cleanup()

	_, err := client.GetDocument(context.Background(), &pb.GetDocumentRequest{
		Database: "db", Collection: "col", Id: 1,
	})
	if err == nil {
		t.Fatal("expected error from GetDocument")
	}
}

func TestGRPC_DeleteDocument_Error(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.deleteDocErr = errors.New("delete document error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithCollectionService(collSvc))
	defer cleanup()

	_, err := client.DeleteDocument(context.Background(), &pb.DeleteDocumentRequest{
		Database: "db", Collection: "col", Id: 1,
	})
	if err == nil {
		t.Fatal("expected error from DeleteDocument")
	}
}

func TestGRPC_ListDocuments_Error(t *testing.T) {
	collSvc := newMockCollectionService()
	collSvc.listDocErr = errors.New("list documents error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithCollectionService(collSvc))
	defer cleanup()

	_, err := client.ListDocuments(context.Background(), &pb.ListDocumentsRequest{
		Database: "db", Collection: "col",
	})
	if err == nil {
		t.Fatal("expected error from ListDocuments")
	}
}

// ---------------------------------------------------------------------------
// Database error-path tests
// ---------------------------------------------------------------------------

func TestGRPC_DatabaseCreate_Error(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.createErr = errors.New("db create error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithDatabaseService(dbSvc))
	defer cleanup()

	_, err := client.DatabaseCreate(context.Background(), &pb.DatabaseCreateRequest{
		Name: "mydb", Engine: "kv", Backend: "pebble",
	})
	if err == nil {
		t.Fatal("expected error from DatabaseCreate")
	}
}

func TestGRPC_DatabaseGet_Error(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.getErr = errors.New("db get error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithDatabaseService(dbSvc))
	defer cleanup()

	_, err := client.DatabaseGet(context.Background(), &pb.DatabaseGetRequest{Name: "mydb"})
	if err == nil {
		t.Fatal("expected error from DatabaseGet")
	}
}

func TestGRPC_DatabaseDrop_Error(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.dropErr = errors.New("db drop error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithDatabaseService(dbSvc))
	defer cleanup()

	_, err := client.DatabaseDrop(context.Background(), &pb.DatabaseDropRequest{Name: "mydb"})
	if err == nil {
		t.Fatal("expected error from DatabaseDrop")
	}
}

func TestGRPC_DatabaseUpdateEtcd_Error(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.updateErr = errors.New("update etcd error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithDatabaseService(dbSvc))
	defer cleanup()

	_, err := client.DatabaseUpdateEtcd(context.Background(), &pb.DatabaseUpdateEtcdRequest{
		Name: "mydb",
	})
	if err == nil {
		t.Fatal("expected error from DatabaseUpdateEtcd")
	}
}

func TestGRPC_DatabaseUpdateRaft_Error(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.updateErr = errors.New("update raft error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithDatabaseService(dbSvc))
	defer cleanup()

	_, err := client.DatabaseUpdateRaft(context.Background(), &pb.DatabaseUpdateRaftRequest{
		Name: "mydb",
	})
	if err == nil {
		t.Fatal("expected error from DatabaseUpdateRaft")
	}
}

func TestGRPC_DatabaseUpdateBarrier_Error(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.updateErr = errors.New("update barrier error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithDatabaseService(dbSvc))
	defer cleanup()

	_, err := client.DatabaseUpdateBarrier(context.Background(), &pb.DatabaseUpdateBarrierRequest{
		Name: "mydb",
	})
	if err == nil {
		t.Fatal("expected error from DatabaseUpdateBarrier")
	}
}

func TestGRPC_DatabaseUpdateConnection_Error(t *testing.T) {
	dbSvc := newMockDatabaseService()
	dbSvc.updateErr = errors.New("update connection error")
	client, cleanup := startTestServerWithAll(t, newMockKVService(), newMockAdminService(),
		WithDatabaseService(dbSvc))
	defer cleanup()

	_, err := client.DatabaseUpdateConnection(context.Background(), &pb.DatabaseUpdateConnectionRequest{
		Name: "mydb",
	})
	if err == nil {
		t.Fatal("expected error from DatabaseUpdateConnection")
	}
}
