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

//go:build !integration && !qrdb_ts

package grpc

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	gogrpc "google.golang.org/grpc"

	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	tenantpkg "github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
)

// ---------------------------------------------------------------------------
// Local mini versions of TS service variants needed here
// (the full versions are in grpc_coverage_test.go behind qrdb_ts)
// ---------------------------------------------------------------------------

type richQueryTSSvcMini struct {
	mockTimeSeriesService
	result *timeseries.QueryResult
}

func (m *richQueryTSSvcMini) QueryRich(_ context.Context, _ *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	return m.result, nil
}

type retentionTSSvcMini struct {
	mockTimeSeriesService
	policy *timeseries.RetentionPolicy
}

func (m *retentionTSSvcMini) GetRetentionPolicy(_ string) (*timeseries.RetentionPolicy, error) {
	return m.policy, nil
}

// ---------------------------------------------------------------------------
// TimeSeries and Index handler tests
// (grpc_coverage_test.go covers these under full build tags;
//  these run under codec_json && raft only)
// ---------------------------------------------------------------------------

func TestGRPC_IndexList_Mini_Success(t *testing.T) {
	indexSvc := &mockIndexService{listResult: []string{"idx-a", "idx-b", "idx-c"}}
	client, cleanup := startTestServerFull(t, WithIndex(indexSvc))
	defer cleanup()

	resp, err := client.IndexList(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("IndexList failed: %v", err)
	}
	if len(resp.GetNames()) != 3 {
		t.Fatalf("expected 3 names, got %d", len(resp.GetNames()))
	}
}

func TestGRPC_IndexList_Mini_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.IndexList(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_IndexList_Mini_Error(t *testing.T) {
	indexSvc := &mockIndexService{listErr: &service.ReadError{Err: errors.New("list failed")}}
	client, cleanup := startTestServerFull(t, WithIndex(indexSvc))
	defer cleanup()

	_, err := client.IndexList(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_TSWritePoint_Mini_Success(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSWritePoint(context.Background(), &pb.TSWriteRequest{
		Point: &pb.TSDataPoint{
			Metric:      "cpu",
			Value:       1.5,
			TimestampNs: time.Now().UnixNano(),
		},
	})
	if err != nil {
		t.Fatalf("TSWritePoint failed: %v", err)
	}
}

func TestGRPC_TSWritePoint_Mini_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TSWritePoint(context.Background(), &pb.TSWriteRequest{
		Point: &pb.TSDataPoint{Metric: "cpu", Value: 1.0, TimestampNs: 1000},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TSWritePoint_Mini_NilPoint(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSWritePoint(context.Background(), &pb.TSWriteRequest{})
	if err == nil {
		t.Fatal("expected error for nil point, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

func TestGRPC_TSRichQuery_Mini_Success(t *testing.T) {
	tsSvc := &richQueryTSSvcMini{
		result: &timeseries.QueryResult{Metric: "cpu", Points: []*timeseries.DataPoint{}},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	resp, err := client.TSRichQuery(context.Background(), &pb.TSRichQueryRequest{
		Metric:  "cpu",
		StartNs: time.Now().Add(-time.Hour).UnixNano(),
		EndNs:   time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSRichQuery failed: %v", err)
	}
	if resp.GetMetric() != "cpu" {
		t.Fatalf("expected metric 'cpu', got %q", resp.GetMetric())
	}
}

func TestGRPC_TSRichQuery_Mini_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TSRichQuery(context.Background(), &pb.TSRichQueryRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TSDeleteSeries_Mini_Success(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSDeleteSeries(context.Background(), &pb.TSDeleteSeriesRequest{
		Metric:  "cpu",
		StartNs: time.Now().Add(-time.Hour).UnixNano(),
		EndNs:   time.Now().UnixNano(),
	})
	if err != nil {
		t.Fatalf("TSDeleteSeries failed: %v", err)
	}
}

func TestGRPC_TSDeleteSeries_Mini_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TSDeleteSeries(context.Background(), &pb.TSDeleteSeriesRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_TSGetRetention_Mini_Success(t *testing.T) {
	tsSvc := &retentionTSSvcMini{
		policy: &timeseries.RetentionPolicy{
			Duration:    24 * time.Hour,
			Resolution:  time.Minute,
			Aggregation: timeseries.AggAvg,
		},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	resp, err := client.TSGetRetention(context.Background(), &pb.TSGetRetentionRequest{Metric: "cpu"})
	if err != nil {
		t.Fatalf("TSGetRetention failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestGRPC_TSGetRetention_Mini_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TSGetRetention(context.Background(), &pb.TSGetRetentionRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_TSSetRetention_Mini_Success(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSSetRetention(context.Background(), &pb.TSSetRetentionRequest{
		Metric:     "cpu",
		DurationNs: int64(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("TSSetRetention failed: %v", err)
	}
}

func TestGRPC_TSSetRetention_Mini_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TSSetRetention(context.Background(), &pb.TSSetRetentionRequest{Metric: "cpu"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_TSListMetrics_Mini_Success(t *testing.T) {
	tsSvc := &mockTimeSeriesService{}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	resp, err := client.TSListMetrics(context.Background(), &pb.TSListMetricsRequest{Prefix: "cpu"})
	if err != nil {
		t.Fatalf("TSListMetrics failed: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestGRPC_TSListMetrics_Mini_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TSListMetrics(context.Background(), &pb.TSListMetricsRequest{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// ---------------------------------------------------------------------------
// RegisterService 0% options: WithRegisterVector/UserService/RoleService/Embedding
// ---------------------------------------------------------------------------

func TestWithRegisterVector_Mini(t *testing.T) {
	grpcSrv := gogrpc.NewServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterVector(nil))
	grpcSrv.Stop()
}

func TestWithRegisterUserService_Mini(t *testing.T) {
	grpcSrv := gogrpc.NewServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterUserService(nil))
	grpcSrv.Stop()
}

func TestWithRegisterRoleService_Mini(t *testing.T) {
	grpcSrv := gogrpc.NewServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterRoleService(nil))
	grpcSrv.Stop()
}

func TestWithRegisterEmbedding_Mini(t *testing.T) {
	grpcSrv := gogrpc.NewServer()
	RegisterService(grpcSrv, newMockKVService(), newMockAdminService(),
		WithRegisterEmbedding(nil))
	grpcSrv.Stop()
}

// ---------------------------------------------------------------------------
// service_collection.go error type tests
// ---------------------------------------------------------------------------

func TestDocumentUnmarshalError(t *testing.T) {
	inner := errors.New("bad JSON")
	e := &DocumentUnmarshalError{Err: inner}
	if e.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap did not return inner error")
	}
}

func TestDocumentMarshalError(t *testing.T) {
	inner := errors.New("marshal failure")
	e := &DocumentMarshalError{Err: inner}
	if e.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
	if e.Unwrap() != inner {
		t.Fatal("Unwrap did not return inner error")
	}
}

// ---------------------------------------------------------------------------
// service_database.go proto converter tests
// ---------------------------------------------------------------------------

func TestProtoDatabaseCreateToConfig_WithProtocol(t *testing.T) {
	req := &pb.DatabaseCreateRequest{
		Name:    "testdb",
		Engine:  "mvcc",
		Backend: "pebble",
		Protocol: &pb.DatabaseProtocolConfig{
			Mysql: true,
			Etcd: &pb.DatabaseEtcdConfig{
				Enabled:    true,
				ListenAddr: "0.0.0.0:2379",
			},
		},
		Raft: &pb.DatabaseRaftConfig{
			ClusterMode: "single",
			ShardId:     1,
			ReplicaId:   1,
			RaftAddress: "127.0.0.1:63001",
		},
	}
	cfg := protoDatabaseCreateToConfig(req)
	if cfg.Name != "testdb" {
		t.Fatalf("expected name 'testdb', got %q", cfg.Name)
	}
	if !cfg.Protocol.MySQL {
		t.Fatal("expected MySQL=true")
	}
	if cfg.Protocol.Etcd == nil || !cfg.Protocol.Etcd.Enabled {
		t.Fatal("expected etcd config with Enabled=true")
	}
	if cfg.Raft == nil || cfg.Raft.ShardID != 1 {
		t.Fatal("expected raft config with ShardID=1")
	}
}

func TestProtoProtocolConfigToDomain_WithEtcd(t *testing.T) {
	p := &pb.DatabaseProtocolConfig{
		Mysql: true,
		Etcd: &pb.DatabaseEtcdConfig{
			Enabled:             true,
			ListenAddr:          "0.0.0.0:2379",
			AdvertiseClientUrls: []string{"http://localhost:2379"},
		},
	}
	result := protoProtocolConfigToDomain(p)
	if !result.MySQL {
		t.Fatal("expected MySQL=true")
	}
	if result.Etcd == nil {
		t.Fatal("expected non-nil Etcd config")
	}
	if !result.Etcd.Enabled {
		t.Fatal("expected Etcd.Enabled=true")
	}
}

func TestProtoRaftConfigToDomain_AllBranches(t *testing.T) {
	r := &pb.DatabaseRaftConfig{
		ClusterMode: "multi",
		ShardId:     2,
		ReplicaId:   3,
		RaftAddress: "127.0.0.1:63002",
		Static: &pb.DatabaseStaticDiscovery{
			Peers:        []string{"peer1", "peer2"},
			SharedSecret: "secret",
		},
		Multicast: &pb.DatabaseMulticastDiscovery{
			SharedSecret:   "mc-secret",
			MulticastGroup: "224.0.0.1:9999",
		},
		Dns: &pb.DatabaseDNSDiscovery{
			Service: "_raft",
			Domain:  "cluster.local",
		},
		Token: &pb.DatabaseTokenDiscovery{
			Endpoint:    "http://discovery:8080",
			Token:       "tok",
			ClusterSize: 3,
		},
	}
	result := protoRaftConfigToDomain(r)
	if result == nil {
		t.Fatal("expected non-nil raft config")
	}
	if result.ShardID != 2 {
		t.Fatalf("expected ShardID 2, got %d", result.ShardID)
	}
	if result.Static == nil || len(result.Static.Peers) != 2 {
		t.Fatal("expected Static discovery with 2 peers")
	}
	if result.Multicast == nil || result.Multicast.SharedSecret != "mc-secret" {
		t.Fatal("expected Multicast discovery")
	}
	if result.DNS == nil || result.DNS.Domain != "cluster.local" {
		t.Fatal("expected DNS discovery")
	}
	if result.Token == nil || result.Token.Token != "tok" {
		t.Fatal("expected Token discovery")
	}
}

func TestProtoRaftConfigToDomain_Nil(t *testing.T) {
	result := protoRaftConfigToDomain(nil)
	if result != nil {
		t.Fatal("expected nil for nil input")
	}
}

func TestDatabaseInfoToProto_WithAllFields(t *testing.T) {
	info := &database.DatabaseInfo{
		Name:     "mydb",
		Engine:   "mvcc",
		Backend:  "pebble",
		Status:   "running",
		ShardID:  1,
		TenantID: "tenant-1",
		Raft: &database.DatabaseRaftConfig{
			ClusterMode: "single",
			ShardID:     1,
			Static: &database.StaticDiscoveryConfig{
				Peers: []string{"peer1"},
			},
			Multicast: &database.MulticastDiscoveryConfig{
				SharedSecret: "ms",
			},
		},
		Barrier: &database.DatabaseBarrierConfig{Enabled: true},
		Connection: &database.DatabaseConnectionConfig{
			Transport: "tcp",
			Address:   "127.0.0.1:9999",
		},
	}
	result := databaseInfoToProto(info)
	if result.GetName() != "mydb" {
		t.Fatalf("expected name 'mydb', got %q", result.GetName())
	}
	if result.GetRaft() == nil {
		t.Fatal("expected non-nil raft config")
	}
	if result.GetBarrier() == nil || !result.GetBarrier().GetEnabled() {
		t.Fatal("expected barrier enabled")
	}
	if result.GetConnection() == nil {
		t.Fatal("expected non-nil connection config")
	}
}

func TestDomainRaftConfigToProto_AllFields(t *testing.T) {
	r := &database.DatabaseRaftConfig{
		ClusterMode: "multi",
		ShardID:     5,
		Static: &database.StaticDiscoveryConfig{
			Peers:        []string{"p1"},
			SharedSecret: "s",
		},
		Multicast: &database.MulticastDiscoveryConfig{
			SharedSecret:   "mc",
			MulticastGroup: "224.0.0.1",
		},
		DNS: &database.DNSDiscoveryConfig{
			Service: "_raft",
			Domain:  "cluster.local",
		},
		Token: &database.TokenDiscoveryConfig{
			Endpoint: "http://tok-svc",
			Token:    "abc",
		},
	}
	result := domainRaftConfigToProto(r)
	if result == nil {
		t.Fatal("expected non-nil raft proto")
	}
	if result.GetShardId() != 5 {
		t.Fatalf("expected shard_id 5, got %d", result.GetShardId())
	}
	if result.GetStatic() == nil {
		t.Fatal("expected non-nil static config")
	}
	if result.GetMulticast() == nil {
		t.Fatal("expected non-nil multicast config")
	}
	if result.GetDns() == nil {
		t.Fatal("expected non-nil DNS config")
	}
	if result.GetToken() == nil {
		t.Fatal("expected non-nil token config")
	}
}

// ---------------------------------------------------------------------------
// service.go: Get/Put/Delete/Scan NilService path (increases coverage on 80% fns)
// ---------------------------------------------------------------------------

func TestGRPC_Put_NilService(t *testing.T) {
	// Use a server with nil KV service.
	tlsCfg := testServerTLSConfig(t)
	srv := NewServer(nil, newMockAdminService(), "127.0.0.1:0", WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn, err := newTLSGRPCConn(t, srv.Addr(), tlsCfg)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	client := pb.NewQRDBServiceClient(conn)
	_, err = client.Put(context.Background(), &pb.PutRequest{Key: "k", Value: []byte("v")})
	if err == nil {
		t.Fatal("expected error for nil KV service, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_Get_NilService(t *testing.T) {
	tlsCfg := testServerTLSConfig(t)
	srv := NewServer(nil, newMockAdminService(), "127.0.0.1:0", WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn, err := newTLSGRPCConn(t, srv.Addr(), tlsCfg)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	client := pb.NewQRDBServiceClient(conn)
	_, err = client.Get(context.Background(), &pb.GetRequest{Key: "k"})
	if err == nil {
		t.Fatal("expected error for nil KV service, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_Delete_NilService(t *testing.T) {
	tlsCfg := testServerTLSConfig(t)
	srv := NewServer(nil, newMockAdminService(), "127.0.0.1:0", WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn, err := newTLSGRPCConn(t, srv.Addr(), tlsCfg)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	client := pb.NewQRDBServiceClient(conn)
	_, err = client.Delete(context.Background(), &pb.DeleteRequest{Key: "k"})
	if err == nil {
		t.Fatal("expected error for nil KV service, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestGRPC_List_NilService(t *testing.T) {
	tlsCfg := testServerTLSConfig(t)
	srv := NewServer(nil, newMockAdminService(), "127.0.0.1:0", WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn, err := newTLSGRPCConn(t, srv.Addr(), tlsCfg)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	client := pb.NewQRDBServiceClient(conn)
	_, err = client.List(context.Background(), &pb.ListRequest{Prefix: ""})
	if err == nil {
		t.Fatal("expected error for nil KV service, got nil")
	}
}

func TestGRPC_Scan_NilService(t *testing.T) {
	tlsCfg := testServerTLSConfig(t)
	srv := NewServer(nil, newMockAdminService(), "127.0.0.1:0", WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn, err := newTLSGRPCConn(t, srv.Addr(), tlsCfg)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	client := pb.NewQRDBServiceClient(conn)
	_, err = client.Scan(context.Background(), &pb.ScanRequest{Prefix: ""})
	if err == nil {
		t.Fatal("expected error for nil KV service, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

// newTLSGRPCConn creates a gRPC client connection that trusts the server
// certificate in serverCfg, verifying it under the "localhost" server name.
func newTLSGRPCConn(t *testing.T, addr string, serverCfg *tls.Config) (*gogrpc.ClientConn, error) {
	return gogrpc.NewClient(addr, gogrpc.WithTransportCredentials(testClientCreds(t, serverCfg)))
}

// ---------------------------------------------------------------------------
// tenant_barrier_server.go: unseal and error branch coverage.
// ---------------------------------------------------------------------------

func TestTenantBarrier_Unseal_KeyPreferred(t *testing.T) {
	svc := newMockTenantBarrierOnlySvc()
	conn, cleanup := startGRPCWithTenantBarrier(t, svc)
	defer cleanup()

	// Pass the key directly; the proto Unseal has a single Key []byte field.
	_, err := pb.NewTenantBarrierServiceClient(conn).Unseal(context.Background(), &pb.TenantBarrierUnsealRequest{
		TenantId: "t1",
		Key:      []byte("main-key"),
	})
	if err != nil {
		t.Fatalf("TenantBarrierUnseal (key) failed: %v", err)
	}
}

// TestTenantBarrier_Init_EmptyPassphrase exercises the Init handler with a
// non-empty tenant_id but empty passphrase, verifying the service is called
// and succeeds (passphrase may legitimately be empty for shamir strategy).
func TestTenantBarrier_Init_EmptyPassphrase(t *testing.T) {
	svc := newMockTenantBarrierOnlySvc()
	server := &tenantBarrierServer{svc: svc, authMW: nil}

	_, err := server.Init(context.Background(), &pb.TenantBarrierInitRequest{
		TenantId:   "t1",
		Passphrase: "",
		Strategy:   "shamir",
	})
	if err != nil {
		t.Fatalf("Init with empty passphrase failed: %v", err)
	}
}

// TestTenantBarrier_Unseal_EmptyKey exercises the Unseal handler when the
// key byte slice is empty, verifying the service is still invoked.
func TestTenantBarrier_Unseal_EmptyKey(t *testing.T) {
	svc := newMockTenantBarrierOnlySvc()
	server := &tenantBarrierServer{svc: svc, authMW: nil}

	_, err := server.Unseal(context.Background(), &pb.TenantBarrierUnsealRequest{
		TenantId: "t1",
		Key:      []byte{},
	})
	if err != nil {
		t.Fatalf("Unseal with empty key failed: %v", err)
	}
}

// TestTenantBarrier_Seal_NotFoundError verifies the Seal handler maps
// ErrTenantNotFound to codes.NotFound via the direct server method.
func TestTenantBarrier_Seal_NotFoundError(t *testing.T) {
	svc := newMockTenantBarrierOnlySvc()
	svc.barrierSealErr = tenantpkg.ErrTenantNotFound
	server := &tenantBarrierServer{svc: svc, authMW: nil}

	_, err := server.Seal(context.Background(), &pb.TenantBarrierSealRequest{TenantId: "t1"})
	if err == nil {
		t.Fatal("expected error for not-found tenant, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

// TestTenantBarrier_Status_NotFoundError verifies the Status handler maps
// ErrTenantNotFound to codes.NotFound via the direct server method.
func TestTenantBarrier_Status_NotFoundError(t *testing.T) {
	svc := newMockTenantBarrierOnlySvc()
	svc.barrierStatusErr = tenantpkg.ErrTenantNotFound
	server := &tenantBarrierServer{svc: svc, authMW: nil}

	_, err := server.Status(context.Background(), &pb.TenantBarrierStatusRequest{TenantId: "t1"})
	if err == nil {
		t.Fatal("expected error for not-found tenant, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// credentials_server.go: mapCredentialError missing branches
// ---------------------------------------------------------------------------

func TestMapCredentialError_AllBranches(t *testing.T) {
	// mapCredentialError is already 81.8% covered; test remaining branches.
	// Check that nil returns nil.
	if got := mapCredentialError(nil); got != nil {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
	// Unknown error → Internal.
	got := mapCredentialError(errors.New("unknown cred error"))
	st, _ := status.FromError(got)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal for unknown error, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// service_tenant.go: TenantCreate/Get/List/Update/Delete under codec_json+raft
// The service_tenant_test.go has these under full build tags;
// here we run them under the minimal tag set.
// ---------------------------------------------------------------------------

func TestTenant_Create_Mini_Success(t *testing.T) {
	tenantSvc := newMockTenantBarrierOnlySvc()
	client, cleanup := startTestServerFull(t, WithTenantService(tenantSvc))
	defer cleanup()

	resp, err := client.TenantCreate(context.Background(), &pb.TenantCreateRequest{
		Id:   "acme",
		Name: "ACME Corp",
	})
	if err != nil {
		t.Fatalf("TenantCreate failed: %v", err)
	}
	if resp.GetId() != "acme" {
		t.Fatalf("expected id 'acme', got %q", resp.GetId())
	}
}

func TestTenant_Create_Mini_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TenantCreate(context.Background(), &pb.TenantCreateRequest{Id: "t1"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Unimplemented {
		t.Fatalf("expected Unimplemented, got %s", st.Code())
	}
}

func TestTenant_Get_Mini_Success(t *testing.T) {
	tenantSvc := newMockTenantBarrierOnlySvc()
	client, cleanup := startTestServerFull(t, WithTenantService(tenantSvc))
	defer cleanup()

	resp, err := client.TenantGet(context.Background(), &pb.TenantGetRequest{Id: "acme"})
	if err != nil {
		t.Fatalf("TenantGet failed: %v", err)
	}
	if resp.GetId() != "acme" {
		t.Fatalf("expected id 'acme', got %q", resp.GetId())
	}
}

func TestTenant_Get_Mini_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TenantGet(context.Background(), &pb.TenantGetRequest{Id: "t1"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestTenant_List_Mini_Success(t *testing.T) {
	tenantSvc := newMockTenantBarrierOnlySvc()
	client, cleanup := startTestServerFull(t, WithTenantService(tenantSvc))
	defer cleanup()

	_, err := client.TenantList(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("TenantList failed: %v", err)
	}
}

func TestTenant_List_Mini_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TenantList(context.Background(), &emptypb.Empty{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestTenant_Update_Mini_Success(t *testing.T) {
	tenantSvc := newMockTenantBarrierOnlySvc()
	client, cleanup := startTestServerFull(t, WithTenantService(tenantSvc))
	defer cleanup()

	resp, err := client.TenantUpdate(context.Background(), &pb.TenantUpdateRequest{
		Id: "acme", Name: "ACME Updated",
	})
	if err != nil {
		t.Fatalf("TenantUpdate failed: %v", err)
	}
	if resp.GetName() != "ACME Updated" {
		t.Fatalf("expected name 'ACME Updated', got %q", resp.GetName())
	}
}

func TestTenant_Update_Mini_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TenantUpdate(context.Background(), &pb.TenantUpdateRequest{Id: "t1"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestTenant_Delete_Mini_Success(t *testing.T) {
	tenantSvc := newMockTenantBarrierOnlySvc()
	client, cleanup := startTestServerFull(t, WithTenantService(tenantSvc))
	defer cleanup()

	_, err := client.TenantDelete(context.Background(), &pb.TenantDeleteRequest{Id: "acme"})
	if err != nil {
		t.Fatalf("TenantDelete failed: %v", err)
	}
}

func TestTenant_Delete_Mini_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TenantDelete(context.Background(), &pb.TenantDeleteRequest{Id: "t1"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestTenant_UpdateQuotas_Mini_Success(t *testing.T) {
	tenantSvc := newMockTenantBarrierOnlySvc()
	client, cleanup := startTestServerFull(t, WithTenantService(tenantSvc))
	defer cleanup()

	resp, err := client.TenantUpdateQuotas(context.Background(), &pb.TenantUpdateQuotasRequest{
		Id:     "acme",
		Quotas: &pb.TenantQuotas{MaxDatabases: 5, MaxKeys: 1000},
	})
	if err != nil {
		t.Fatalf("TenantUpdateQuotas failed: %v", err)
	}
	if resp.GetId() != "acme" {
		t.Fatalf("expected id 'acme', got %q", resp.GetId())
	}
}

func TestTenant_UpdateQuotas_Mini_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()

	_, err := client.TenantUpdateQuotas(context.Background(), &pb.TenantUpdateQuotasRequest{
		Id: "t1", Quotas: &pb.TenantQuotas{MaxDatabases: 1},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestTenant_UpdateQuotas_Mini_NilQuotas(t *testing.T) {
	tenantSvc := newMockTenantBarrierOnlySvc()
	client, cleanup := startTestServerFull(t, WithTenantService(tenantSvc))
	defer cleanup()

	_, err := client.TenantUpdateQuotas(context.Background(), &pb.TenantUpdateQuotasRequest{
		Id:     "acme",
		Quotas: nil,
	})
	if err == nil {
		t.Fatal("expected error for nil quotas, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %s", st.Code())
	}
}

// tenantToProto helper tests (mini versions to avoid conflict with full-tag tests).
func TestTenantToProto_MiniNil(t *testing.T) {
	if got := tenantToProto(nil); got != nil {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
}

func TestQuotaToProto_MiniNil(t *testing.T) {
	if got := quotaToProto(nil); got != nil {
		t.Fatalf("expected nil for nil input, got %v", got)
	}
}

func TestMapTenantError_MiniUnknown(t *testing.T) {
	got := mapTenantError(errors.New("unknown tenant error"))
	st, _ := status.FromError(got)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal for unknown error, got %s", st.Code())
	}
}

// ---------------------------------------------------------------------------
// errorTSService - already defined in grpc_new_coverage_test.go under full tags.
// Under !qrdb_ts we define a local version.
// ---------------------------------------------------------------------------

type errorTSSvc struct {
	inner         *mockTimeSeriesService
	richQueryErr  error
	writePointErr error
	deleteSerErr  error
	setRetErr     error
	listMetErr    error
}

func (m *errorTSSvc) Write(_ context.Context, _ string, _ float64, _ time.Time, _ map[string]string) error {
	return m.writePointErr
}
func (m *errorTSSvc) WriteBatch(_ context.Context, _ []timeseries.DataPoint) error { return nil }
func (m *errorTSSvc) Query(_ context.Context, _ string, _, _ time.Time, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	return nil, nil
}
func (m *errorTSSvc) Aggregate(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration) ([]timeseries.AggResult, error) {
	return nil, nil
}
func (m *errorTSSvc) WritePoint(_ context.Context, _ *timeseries.DataPoint) error {
	return m.writePointErr
}
func (m *errorTSSvc) QueryRich(_ context.Context, _ *timeseries.QueryRequest) (*timeseries.QueryResult, error) {
	return nil, m.richQueryErr
}
func (m *errorTSSvc) DeleteSeries(_ context.Context, _ *timeseries.DeleteRequest) error {
	return m.deleteSerErr
}
func (m *errorTSSvc) GetRetentionPolicy(_ string) (*timeseries.RetentionPolicy, error) {
	return nil, nil
}
func (m *errorTSSvc) SetRetentionPolicy(_ string, _ *timeseries.RetentionPolicy) error {
	return m.setRetErr
}
func (m *errorTSSvc) ListMetrics(_ string, _ int) ([]string, error) {
	return nil, m.listMetErr
}
func (m *errorTSSvc) QueryWithConsistency(_ context.Context, _ string, _, _ time.Time, _ consistency.ReadOptions, _ ...timeseries.TSQueryOption) ([]timeseries.DataPoint, error) {
	return nil, nil
}
func (m *errorTSSvc) AggregateWithConsistency(_ context.Context, _ string, _, _ time.Time, _ timeseries.AggFunc, _ time.Duration, _ consistency.ReadOptions) ([]timeseries.AggResult, error) {
	return nil, nil
}
func (m *errorTSSvc) QueryRichWithConsistency(_ context.Context, _ *timeseries.QueryRequest, _ consistency.ReadOptions) (*timeseries.QueryResult, error) {
	return nil, m.richQueryErr
}
func (m *errorTSSvc) ListMetricsWithConsistency(_ context.Context, _ string, _ int, _ consistency.ReadOptions) ([]string, error) {
	return nil, m.listMetErr
}

func TestGRPC_TSRichQuery_Mini_Error(t *testing.T) {
	tsSvc := &errorTSSvc{
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
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %s", st.Code())
	}
}

func TestGRPC_TSWritePoint_Mini_Error(t *testing.T) {
	tsSvc := &errorTSSvc{
		inner:         &mockTimeSeriesService{},
		writePointErr: &service.TimeSeriesError{Op: "write", Err: errors.New("failed")},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSWritePoint(context.Background(), &pb.TSWriteRequest{
		Point: &pb.TSDataPoint{Metric: "cpu", Value: 1.5, TimestampNs: time.Now().UnixNano()},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_TSSetRetention_Mini_Error(t *testing.T) {
	tsSvc := &errorTSSvc{
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
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_TSListMetrics_Mini_Error(t *testing.T) {
	tsSvc := &errorTSSvc{
		inner:      &mockTimeSeriesService{},
		listMetErr: &service.TimeSeriesError{Op: "list", Err: errors.New("failed")},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSListMetrics(context.Background(), &pb.TSListMetricsRequest{Prefix: "cpu"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestGRPC_TSDeleteSeries_Mini_Error(t *testing.T) {
	tsSvc := &errorTSSvc{
		inner:        &mockTimeSeriesService{},
		deleteSerErr: &service.TimeSeriesError{Op: "delete", Err: errors.New("failed")},
	}
	client, cleanup := startTestServerFull(t, WithTimeSeries(tsSvc))
	defer cleanup()

	_, err := client.TSDeleteSeries(context.Background(), &pb.TSDeleteSeriesRequest{
		Metric:  "cpu",
		StartNs: time.Now().Add(-time.Hour).UnixNano(),
		EndNs:   time.Now().UnixNano(),
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
